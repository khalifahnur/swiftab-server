package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// A shared tab lets the person who booked a table send friends a link to the
// restaurant's menu. Each friend says their name and can then add dishes,
// which go on the host's bill marked with who ordered them. The host sees who
// joined and what they ordered, and can stop sharing at any time; that
// changes the link's code, so every friend's access ends with it.

// newTabCode returns the secret part of a share link: 72 random bits.
func newTabCode() (string, error) {
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// tabOpenable reports whether friends may still be invited to a booking.
func tabOpenable(res *models.Reservation, now time.Time) bool {
	if res.Status != models.ReservationActive && res.Status != models.ReservationSeated {
		return false
	}
	end := res.ReservationInfo.EndTime
	return end.IsZero() || end.After(now)
}

// cleanGuestName tidies the name a friend typed.
func cleanGuestName(raw string) string {
	name := strings.Join(strings.Fields(raw), " ")
	if r := []rune(name); len(r) > 40 {
		name = strings.TrimSpace(string(r[:40]))
	}
	return name
}

// tabSummary is what the host sees about their tab: whether it is shared,
// the link, and each friend with what they have ordered so far.
func (h *DineHandler) tabSummary(res *models.Reservation, orders []aggFloorOrder) map[string]interface{} {
	type tally struct {
		items int
		total float64
	}
	byGuest := map[string]*tally{}
	for _, o := range orders {
		if o.OrderStatus == models.OrderCancelled {
			continue
		}
		for _, line := range o.Items {
			if line.AddedByID == "" {
				continue
			}
			t := byGuest[line.AddedByID]
			if t == nil {
				t = &tally{}
				byGuest[line.AddedByID] = t
			}
			qty := line.Quantity
			if qty < 1 {
				qty = 1
			}
			t.items += qty
			t.total += line.Cost * float64(qty)
		}
	}

	guests := []map[string]interface{}{}
	open := res.Tab.IsOpen()
	if res.Tab != nil {
		for _, g := range res.Tab.Guests {
			t := byGuest[g.ID]
			if t == nil {
				t = &tally{}
			}
			guests = append(guests, map[string]interface{}{
				"id":       g.ID,
				"name":     g.Name,
				"joinedAt": g.JoinedAt,
				"items":    t.items,
				"total":    t.total,
			})
		}
	}
	body := map[string]interface{}{
		"open":      open,
		"canShare":  tabOpenable(res, time.Now().UTC()),
		"guests":    guests,
		"maxGuests": models.MaxTabGuests,
	}
	if open {
		body["code"] = res.Tab.Code
		body["url"] = h.Cfg.WebURL + "/tab/" + res.Tab.Code
		body["openedAt"] = res.Tab.OpenedAt
	}
	return body
}

// startTab shares a booking, keeping the current link if it is already shared.
func (h *DineHandler) startTab(ctx context.Context, res *models.Reservation) error {
	if res.Tab.IsOpen() {
		return nil
	}
	code, err := newTabCode()
	if err != nil {
		return err
	}
	tab := &models.Tab{Code: code, Open: true, OpenedAt: time.Now().UTC(), Guests: []models.TabGuest{}}
	if _, err := h.DB.Collection("reservations").UpdateByID(ctx, res.ID, bson.M{"$set": bson.M{"tab": tab}}); err != nil {
		return err
	}
	res.Tab = tab
	return nil
}

// stopTab ends sharing. Dishes friends already ordered stay on the bill.
func (h *DineHandler) stopTab(ctx context.Context, res *models.Reservation) error {
	tab := &models.Tab{Open: false}
	if _, err := h.DB.Collection("reservations").UpdateByID(ctx, res.ID, bson.M{"$set": bson.M{"tab": tab}}); err != nil {
		return err
	}
	res.Tab = tab
	return nil
}

func (h *DineHandler) writeTab(w http.ResponseWriter, r *http.Request, res *models.Reservation) {
	orders, err := h.visitOrders(r.Context(), res.RestaurantID, res.ReservationInfo.ReservationID)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not load the tab")
		return
	}
	util.JsonResponse(w, http.StatusOK, h.tabSummary(res, orders))
}

// changeTab opens or closes a host's tab and returns its summary.
func (h *DineHandler) changeTab(w http.ResponseWriter, r *http.Request, res *models.Reservation, open bool) {
	if open {
		if !tabOpenable(res, time.Now().UTC()) {
			util.JsonError(w, http.StatusConflict, "This booking has ended, so its menu can no longer be shared.")
			return
		}
		if err := h.startTab(r.Context(), res); err != nil {
			util.JsonError(w, http.StatusInternalServerError, "Could not create a share link")
			return
		}
	} else if err := h.stopTab(r.Context(), res); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not stop sharing")
		return
	}
	h.writeTab(w, r, res)
}

// --- Host in the app ----------------------------------------------------------

// userBooking loads one of a diner's own bookings by its RES code.
func (h *DineHandler) userBooking(w http.ResponseWriter, r *http.Request) (*models.Reservation, bool) {
	userID, err := bson.ObjectIDFromHex(r.PathValue("userId"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid user ID")
		return nil, false
	}
	if !ownsUser(r, userID) {
		util.JsonError(w, http.StatusForbidden, "You can only share your own bookings")
		return nil, false
	}
	var res models.Reservation
	if err := h.DB.Collection("reservations").FindOne(r.Context(), bson.M{
		"userId":                        userID,
		"reservationInfo.reservationID": r.PathValue("reservationId"),
	}).Decode(&res); err != nil {
		util.JsonError(w, http.StatusNotFound, "Booking not found")
		return nil, false
	}
	return &res, true
}

// UserTab returns a booking's shared tab.
func (h *DineHandler) UserTab(w http.ResponseWriter, r *http.Request) {
	if res, ok := h.userBooking(w, r); ok {
		h.writeTab(w, r, res)
	}
}

// UserShareTab creates (or returns) the booking's share link.
func (h *DineHandler) UserShareTab(w http.ResponseWriter, r *http.Request) {
	if res, ok := h.userBooking(w, r); ok {
		h.changeTab(w, r, res, true)
	}
}

// UserStopTab stops sharing the booking.
func (h *DineHandler) UserStopTab(w http.ResponseWriter, r *http.Request) {
	if res, ok := h.userBooking(w, r); ok {
		h.changeTab(w, r, res, false)
	}
}

// --- Host who checked in with a table's QR code --------------------------------

func (h *DineHandler) hostVisit(w http.ResponseWriter, r *http.Request) (*models.Reservation, bool) {
	visit, friend, ok := h.guestVisit(w, r)
	if !ok {
		return nil, false
	}
	if friend != nil {
		util.JsonError(w, http.StatusForbidden, "Only "+hostName(visit)+" can change who the tab is shared with.")
		return nil, false
	}
	return visit, true
}

// VisitShareTab creates (or returns) the share link for a QR visit.
func (h *DineHandler) VisitShareTab(w http.ResponseWriter, r *http.Request) {
	if visit, ok := h.hostVisit(w, r); ok {
		h.changeTab(w, r, visit, true)
	}
}

// VisitStopTab stops sharing a QR visit.
func (h *DineHandler) VisitStopTab(w http.ResponseWriter, r *http.Request) {
	if visit, ok := h.hostVisit(w, r); ok {
		h.changeTab(w, r, visit, false)
	}
}

// --- Friends who open the link -------------------------------------------------

const tabGone = "This link no longer works. Ask whoever sent it for a new one."

// sharedTab finds the booking behind a share link.
func (h *DineHandler) sharedTab(w http.ResponseWriter, r *http.Request) (*models.Reservation, *models.Restaurant, bool) {
	code := strings.TrimSpace(r.PathValue("code"))
	if len(code) < 8 || len(code) > 32 {
		util.JsonError(w, http.StatusNotFound, tabGone)
		return nil, nil, false
	}
	var res models.Reservation
	if err := h.DB.Collection("reservations").FindOne(r.Context(), bson.M{"tab.code": code, "tab.open": true}).Decode(&res); err != nil {
		util.JsonError(w, http.StatusNotFound, tabGone)
		return nil, nil, false
	}
	restaurant, err := h.loadRestaurant(r.Context(), res.RestaurantID)
	if err != nil || len(restaurant.Data) == 0 {
		util.JsonError(w, http.StatusNotFound, "This restaurant is no longer on Swiftab.")
		return nil, nil, false
	}
	return &res, restaurant, true
}

// TabPreview tells someone who opened a share link whose tab it is and where.
func (h *DineHandler) TabPreview(w http.ResponseWriter, r *http.Request) {
	res, restaurant, ok := h.sharedTab(w, r)
	if !ok {
		return
	}
	info := res.ReservationInfo
	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"restaurant": restaurantSummary(restaurant),
		"host":       hostName(res),
		"table":      info.TableNumber,
		"floor":      info.DiningArea,
		"since":      info.BookingFor,
		"until":      info.EndTime,
		"status":     res.Status,
		"canJoin":    tabOpenable(res, time.Now().UTC()),
		"guests":     len(res.Tab.Guests),
		"full":       len(res.Tab.Guests) >= models.MaxTabGuests,
	})
}

// JoinTab adds a friend to a shared tab. Joining again with the same name
// returns the same place on the tab, so a friend who switches phone still
// sees what they ordered.
func (h *DineHandler) JoinTab(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := util.DecodeJSON(w, r, &req); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	name := cleanGuestName(req.Name)
	if len([]rune(name)) < 2 {
		util.JsonError(w, http.StatusBadRequest, "Enter your name so the host knows who ordered what")
		return
	}
	res, _, ok := h.sharedTab(w, r)
	if !ok {
		return
	}
	if !tabOpenable(res, time.Now().UTC()) {
		util.JsonError(w, http.StatusConflict, "This booking has ended, so nothing more can be added to the tab.")
		return
	}

	var guest *models.TabGuest
	for i := range res.Tab.Guests {
		if strings.EqualFold(res.Tab.Guests[i].Name, name) {
			guest = &res.Tab.Guests[i]
			break
		}
	}
	if guest == nil {
		g := models.TabGuest{ID: bson.NewObjectID().Hex(), Name: name, JoinedAt: time.Now().UTC()}
		// Matching the code and the free slot in the filter keeps two friends
		// joining at once from overfilling the tab or joining a closed one.
		result, err := h.DB.Collection("reservations").UpdateOne(r.Context(), bson.M{
			"_id":      res.ID,
			"tab.code": res.Tab.Code,
			"tab.open": true,
			fmt.Sprintf("tab.guests.%d", models.MaxTabGuests-1): bson.M{"$exists": false},
		}, bson.M{"$push": bson.M{"tab.guests": g}})
		if err != nil {
			util.JsonError(w, http.StatusInternalServerError, "Could not join the tab. Please try again.")
			return
		}
		if result.MatchedCount == 0 {
			util.JsonError(w, http.StatusConflict, fmt.Sprintf("This tab already has %d guests. Ask %s to add your order.", models.MaxTabGuests, hostName(res)))
			return
		}
		res.Tab.Guests = append(res.Tab.Guests, g)
		guest = &res.Tab.Guests[len(res.Tab.Guests)-1]
	}

	token, err := util.GenerateTabGuestToken(res.RestaurantID.Hex(), res.ReservationInfo.ReservationID, res.Tab.Code, guest.ID, visitTokenExpiry(res))
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not join the tab")
		return
	}
	h.respondVisit(w, r, res, guest, http.StatusOK, map[string]interface{}{"token": token})
}

// --- Telling the host ---------------------------------------------------------

// tellHost lets the host know a friend put dishes on their tab. It runs after
// the order is saved and must not use the request's context.
func (h *OrderHandler) tellHost(res models.Reservation, friend models.TabGuest, lines []models.MenuItem, total float64) {
	count := 0
	for _, line := range lines {
		count += max(line.Quantity, 1)
	}
	what := fmt.Sprintf("%d items", count)
	if count == 1 && len(lines) == 1 {
		what = lines[0].Name
	}
	h.notifyUser(res.UserID, util.PushMessage{
		Title:   friend.Name + " added to your tab",
		Body:    fmt.Sprintf("%s · KES %.0f at %s.", what, total, res.ReservationInfo.RestaurantName),
		Data:    map[string]string{"type": "tab", "reservationId": res.ReservationInfo.ReservationID},
		Channel: "orders",
	})
}
