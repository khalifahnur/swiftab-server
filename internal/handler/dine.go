package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"swiftab/server/internal/events"
	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// DineHandler serves guests who scan the QR code on a table: they say who
// they are, are seated at that table, and can then order and ask for the bill
// from their phone's browser without the app or an account.
type DineHandler struct {
	*Base
	Orders *OrderHandler
}

const (
	// walkInStay is how long a table is held for a guest who checks in.
	walkInStay = 2 * time.Hour
	// walkInMinStay is the shortest stay worth seating someone for before
	// the next booking at the table.
	walkInMinStay = 30 * time.Minute
	// bookingGrace treats a booking that starts this soon as already at the table.
	bookingGrace = 15 * time.Minute
	// turnaround is kept free between a walk-in and the next booking.
	turnaround = 5 * time.Minute
)

var (
	errTableBusy       = errors.New("table busy")
	errTableBookedSoon = errors.New("table booked soon")
)

// findTable looks a table up by its id, or by its name for tables saved
// before every table had an id.
func findTable(layout *models.RestaurantLayout, tableID string) *models.Table {
	for i := range layout.TablePosition {
		t := &layout.TablePosition[i]
		if t.ID == tableID || (t.ID == "" && t.Name == tableID) {
			return t
		}
	}
	return nil
}

func tableKey(t *models.Table) string {
	if t.ID != "" {
		return t.ID
	}
	return t.Name
}

// sameGuest reports whether a booking belongs to the person checking in.
// The phone number decides when both have one; a name alone only matches an
// earlier QR check-in, never a booking made in the app.
func sameGuest(res models.Reservation, phone, name string) bool {
	info := res.ReservationInfo
	if phone != "" {
		return util.PhoneDigits(info.PhoneNumber) == phone
	}
	return res.Source == "qr" && name != "" && strings.EqualFold(strings.TrimSpace(info.Name), name)
}

type walkInPlan struct {
	// Reuse is the guest's own booking at this table, when they already have one.
	Reuse *models.Reservation
	// End is when a new walk-in must leave the table.
	End time.Time
	// NextBooking is the start of the booking that cut the stay short, if any.
	NextBooking time.Time
}

// planWalkIn decides what checking in at a table means right now. holds are
// the table's bookings that are current or start within walkInStay; those in
// eating have an open order and count as at the table whatever their times say.
//
// A guest who already has a booking here (made in the app, or an earlier
// scan) gets it back. Anyone else is turned away while the table is taken,
// and otherwise seated until the next booking, or for walkInStay.
func planWalkIn(now time.Time, holds []models.Reservation, eating map[string]bool, phone, name string) (walkInPlan, error) {
	var next time.Time
	busy := false
	for i := range holds {
		res := holds[i]
		info := res.ReservationInfo
		current := eating[info.ReservationID] || !info.BookingFor.After(now.Add(bookingGrace))
		if !current {
			if next.IsZero() || info.BookingFor.Before(next) {
				next = info.BookingFor
			}
			continue
		}
		if sameGuest(res, phone, name) {
			return walkInPlan{Reuse: &holds[i]}, nil
		}
		busy = true
	}
	if busy {
		return walkInPlan{}, errTableBusy
	}

	end := now.Add(walkInStay)
	if !next.IsZero() && next.Add(-turnaround).Before(end) {
		end = next.Add(-turnaround)
	}
	if end.Sub(now) < walkInMinStay {
		return walkInPlan{NextBooking: next}, errTableBookedSoon
	}
	return walkInPlan{End: end, NextBooking: next}, nil
}

// --- Dashboard ---------------------------------------------------------------

// TableCodes lists every table with the code its QR link carries, for the
// dashboard to draw and print.
func (h *DineHandler) TableCodes(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Set up your restaurant first")
		return
	}
	restaurant, err := h.loadRestaurant(r.Context(), restaurantID)
	if err != nil || len(restaurant.Data) == 0 {
		util.JsonError(w, http.StatusNotFound, "Restaurant not found")
		return
	}
	layout, err := h.loadLayout(r.Context(), restaurantID)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not load your floor plan")
		return
	}

	tables := make([]map[string]interface{}, 0, len(layout.TablePosition))
	for i := range layout.TablePosition {
		t := &layout.TablePosition[i]
		key := tableKey(t)
		tables = append(tables, map[string]interface{}{
			"id":        key,
			"name":      t.Name,
			"floorId":   t.FloorID,
			"floorName": layout.FloorName(t.FloorID),
			"seats":     len(t.Chairs),
			"code":      util.TableCode(restaurantID.Hex(), key),
		})
	}
	data := restaurant.Data[0]
	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"restaurantId":   restaurantID.Hex(),
		"restaurantName": data.RestaurantName,
		"logo":           data.Logo,
		"image":          data.Image,
		"tables":         tables,
	})
}

// --- Public: the page a QR code opens ------------------------------------------

// dineTable resolves and checks the restaurant, table and code in the path.
func (h *DineHandler) dineTable(w http.ResponseWriter, r *http.Request, code string) (*models.Restaurant, *models.RestaurantLayout, *models.Table, bool) {
	restaurantID, err := bson.ObjectIDFromHex(r.PathValue("restaurantId"))
	tableID := strings.TrimSpace(r.PathValue("tableId"))
	if err != nil || tableID == "" || !util.ValidTableCode(restaurantID.Hex(), tableID, code) {
		util.JsonError(w, http.StatusNotFound, "This QR code is not valid. Ask a member of staff for help.")
		return nil, nil, nil, false
	}
	restaurant, err := h.loadRestaurant(r.Context(), restaurantID)
	if err != nil || len(restaurant.Data) == 0 {
		util.JsonError(w, http.StatusNotFound, "This restaurant is no longer on Swiftab.")
		return nil, nil, nil, false
	}
	layout, err := h.loadLayout(r.Context(), restaurantID)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not load the table")
		return nil, nil, nil, false
	}
	table := findTable(layout, tableID)
	if table == nil {
		util.JsonError(w, http.StatusNotFound, "This table is no longer in use. Ask a member of staff to seat you.")
		return nil, nil, nil, false
	}
	return restaurant, layout, table, true
}

func restaurantSummary(rest *models.Restaurant) map[string]interface{} {
	data := rest.Data[0]
	methods := data.PaymentMethods
	if methods == nil {
		methods = []string{}
	}
	return map[string]interface{}{
		"id":             rest.ID.Hex(),
		"name":           data.RestaurantName,
		"image":          data.Image,
		"logo":           data.Logo,
		"location":       data.Location,
		"paymentMethods": methods,
	}
}

func tableSummary(layout *models.RestaurantLayout, t *models.Table) map[string]interface{} {
	return map[string]interface{}{
		"id":    tableKey(t),
		"name":  t.Name,
		"floor": layout.FloorName(t.FloorID),
		"seats": len(t.Chairs),
	}
}

// tableHolds loads the bookings planWalkIn needs: the table's current and
// upcoming ones, plus any whose order is still open at the table.
func (h *DineHandler) tableHolds(ctx context.Context, restaurantID bson.ObjectID, tableName string, now time.Time) ([]models.Reservation, map[string]bool, error) {
	eating := map[string]bool{}
	cursor, err := h.DB.Collection("orders").Find(ctx, bson.M{
		"restaurantId": restaurantID,
		"tableNumber":  tableName,
		"orderStatus":  bson.M{"$in": bson.A{models.OrderPlaced, models.OrderServed, models.OrderReadyToPay}},
	}, options.Find().SetProjection(bson.M{"reservationId": 1}))
	if err != nil {
		return nil, nil, err
	}
	var open []models.Order
	if err := cursor.All(ctx, &open); err != nil {
		return nil, nil, err
	}
	ids := bson.A{}
	for _, o := range open {
		if o.ReservationID != "" {
			eating[o.ReservationID] = true
			ids = append(ids, o.ReservationID)
		}
	}

	filter := bson.M{
		"restaurantId":                restaurantID,
		"reservationInfo.tableNumber": tableName,
		"status":                      bson.M{"$in": holdingStatuses},
		"$or": bson.A{
			bson.M{
				"reservationInfo.endTime":    bson.M{"$gt": now},
				"reservationInfo.bookingFor": bson.M{"$lt": now.Add(walkInStay)},
			},
			bson.M{"reservationInfo.reservationID": bson.M{"$in": ids}},
		},
	}
	var holds []models.Reservation
	cursor, err = h.DB.Collection("reservations").Find(ctx, filter)
	if err == nil {
		err = cursor.All(ctx, &holds)
	}
	return holds, eating, err
}

// Table shows a guest which restaurant and table they scanned, and whether
// the table is free right now.
func (h *DineHandler) Table(w http.ResponseWriter, r *http.Request) {
	restaurant, layout, table, ok := h.dineTable(w, r, r.URL.Query().Get("code"))
	if !ok {
		return
	}
	status := "free"
	holds, eating, err := h.tableHolds(r.Context(), restaurant.ID, table.Name, time.Now().UTC())
	if err == nil {
		if _, err := planWalkIn(time.Now().UTC(), holds, eating, "", ""); errors.Is(err, errTableBusy) {
			status = "busy"
		}
	}
	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"restaurant": restaurantSummary(restaurant),
		"table":      tableSummary(layout, table),
		"status":     status,
	})
}

// CheckIn seats a guest at the table they scanned. They give a name or a
// phone number; scanning again with the same details returns the same visit,
// which is how a guest finds their order again on another phone.
func (h *DineHandler) CheckIn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code        string `json:"code"`
		Name        string `json:"name"`
		PhoneNumber string `json:"phoneNumber"`
		Guests      int    `json:"guests"`
	}
	if err := util.DecodeJSON(w, r, &req); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	restaurant, layout, table, ok := h.dineTable(w, r, req.Code)
	if !ok {
		return
	}

	name := strings.Join(strings.Fields(req.Name), " ")
	if len([]rune(name)) > 60 {
		name = string([]rune(name)[:60])
	}
	phone := util.PhoneDigits(req.PhoneNumber)
	if req.PhoneNumber != "" && (len(phone) < 9 || len(phone) > 12) {
		util.JsonError(w, http.StatusBadRequest, "Enter a valid phone number")
		return
	}
	if name == "" && phone == "" {
		util.JsonError(w, http.StatusBadRequest, "Enter your name or phone number")
		return
	}
	guests := req.Guests
	if guests < 1 {
		guests = 1
	}
	if guests > 30 {
		guests = 30
	}

	ctx := r.Context()
	now := time.Now().UTC()
	holds, eating, err := h.tableHolds(ctx, restaurant.ID, table.Name, now)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not check the table")
		return
	}
	plan, err := planWalkIn(now, holds, eating, phone, name)
	switch {
	case errors.Is(err, errTableBusy):
		util.JsonError(w, http.StatusConflict, "Someone is already seated at table "+table.Name+". Ask a member of staff to seat you, or scan the code on your own table.")
		return
	case errors.Is(err, errTableBookedSoon):
		util.JsonError(w, http.StatusConflict, "Table "+table.Name+" is booked from "+plan.NextBooking.In(h.location()).Format("15:04")+". Ask a member of staff for another table.")
		return
	case err != nil:
		util.JsonError(w, http.StatusInternalServerError, "Could not check the table")
		return
	}

	var visit models.Reservation
	if plan.Reuse != nil {
		visit = *plan.Reuse
		set := bson.M{"status": models.ReservationSeated, "updatedAt": now}
		if visit.ReservationInfo.Name == "" && name != "" {
			set["reservationInfo.name"] = name
			visit.ReservationInfo.Name = name
		}
		h.DB.Collection("reservations").UpdateByID(ctx, visit.ID, bson.M{"$set": set})
		if visit.Status != models.ReservationSeated {
			h.publish(restaurant.ID, events.Event{Type: "reservation", Action: "status", ID: visit.ID.Hex(), Table: table.Name, Status: string(models.ReservationSeated)})
		}
		visit.Status = models.ReservationSeated
	} else {
		data := restaurant.Data[0]
		visit = models.Reservation{
			ID:           bson.NewObjectID(),
			RestaurantID: restaurant.ID,
			ReservationInfo: models.ReservationInfo{
				ReservationID:  GenerateReservationID(restaurant.ID.Hex(), now),
				Name:           name,
				PhoneNumber:    req.PhoneNumber,
				BookingDate:    now,
				BookingFor:     now,
				EndTime:        plan.End,
				Guests:         guests,
				TableNumber:    table.Name,
				DiningArea:     layout.FloorName(table.FloorID),
				RestaurantName: data.RestaurantName,
				Notes:          "Checked in with the table's QR code",
			},
			Status:    models.ReservationSeated,
			Source:    "qr",
			CreatedAt: now,
			UpdatedAt: now,
		}
		if _, err := h.DB.Collection("reservations").InsertOne(ctx, visit); err != nil {
			util.JsonError(w, http.StatusInternalServerError, "Could not seat you. Please try again.")
			return
		}
		who := name
		if who == "" {
			who = "A guest"
		}
		h.publish(restaurant.ID, events.Event{Type: "reservation", Action: "created", ID: visit.ID.Hex(), Table: table.Name, Status: string(models.ReservationSeated)})
		h.notifyWaiters(restaurant.ID, util.PushMessage{
			Title:   "Guest seated · Table " + table.Name,
			Body:    who + " checked in with the table's QR code.",
			Data:    map[string]string{"type": "reservation", "reservationId": visit.ReservationInfo.ReservationID},
			Channel: "orders",
		})
	}

	token, err := util.GenerateGuestToken(restaurant.ID.Hex(), visit.ReservationInfo.ReservationID, visitTokenExpiry(&visit))
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not start your visit")
		return
	}

	body, err := h.visitBody(ctx, restaurant, layout, &visit, nil)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not load your visit")
		return
	}
	body["token"] = token
	body["returning"] = plan.Reuse != nil
	util.JsonResponse(w, http.StatusOK, body)
}

// --- Guest session -------------------------------------------------------------

// visitTokenExpiry outlives the stay a little, so a guest can still see
// their receipt after paying.
func visitTokenExpiry(visit *models.Reservation) time.Duration {
	expiry := time.Until(visit.ReservationInfo.EndTime) + 4*time.Hour
	if expiry < 6*time.Hour {
		expiry = 6 * time.Hour
	}
	return expiry
}

const visitEnded = "Your visit has ended. Scan the code on your table to start again."

// guestVisit loads the visit a guest token is for. friend is set when the
// token belongs to someone who joined the host's shared tab; their token
// only works while the host keeps sharing.
func (h *DineHandler) guestVisit(w http.ResponseWriter, r *http.Request) (visit *models.Reservation, friend *models.TabGuest, ok bool) {
	claims, err := util.ParseToken(bearer(r))
	if err != nil || claims.Role != util.RoleGuest || claims.ReservationID == "" {
		util.JsonError(w, http.StatusUnauthorized, visitEnded)
		return nil, nil, false
	}
	restaurantID, err := bson.ObjectIDFromHex(claims.RestaurantID)
	if err != nil {
		util.JsonError(w, http.StatusUnauthorized, visitEnded)
		return nil, nil, false
	}
	var res models.Reservation
	if err := h.DB.Collection("reservations").FindOne(r.Context(), bson.M{
		"restaurantId":                  restaurantID,
		"reservationInfo.reservationID": claims.ReservationID,
	}).Decode(&res); err != nil {
		util.JsonError(w, http.StatusUnauthorized, visitEnded)
		return nil, nil, false
	}
	if claims.TabCode != "" {
		friend = res.Tab.Guest(claims.GuestID)
		if !res.Tab.IsOpen() || res.Tab.Code != claims.TabCode || friend == nil {
			util.JsonError(w, http.StatusUnauthorized, "The host stopped sharing this tab. Ask them for a new link.")
			return nil, nil, false
		}
	}
	return &res, friend, true
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// hostName is how friends on a shared tab see the host: their first name.
func hostName(visit *models.Reservation) string {
	if parts := strings.Fields(visit.ReservationInfo.Name); len(parts) > 0 {
		return parts[0]
	}
	return "Your host"
}

// friendLines keeps the lines one friend added to a shared tab.
func friendLines(items []models.MenuItem, guestID string) []models.MenuItem {
	out := []models.MenuItem{}
	for _, item := range items {
		if item.AddedByID == guestID {
			out = append(out, item)
		}
	}
	return out
}

// visitOrders loads every order on a visit, newest first, with the waiter
// who served it.
func (h *DineHandler) visitOrders(ctx context.Context, restaurantID bson.ObjectID, reservationID string) ([]aggFloorOrder, error) {
	cursor, err := h.DB.Collection("orders").Aggregate(ctx, bson.A{
		bson.M{"$match": bson.M{"restaurantId": restaurantID, "reservationId": reservationID}},
		bson.M{"$sort": bson.M{"createdAt": -1}},
		bson.M{"$lookup": bson.M{"from": "waiters", "localField": "servedBy", "foreignField": "_id", "as": "waiter"}},
	})
	if err != nil {
		return nil, err
	}
	var rows []aggFloorOrder
	err = cursor.All(ctx, &rows)
	return rows, err
}

// visitBody is what the guest page shows: where they are, their visit and
// its orders. A friend on a shared tab sees only the dishes they added; the
// host also gets the tab's share link and who has joined.
func (h *DineHandler) visitBody(ctx context.Context, restaurant *models.Restaurant, layout *models.RestaurantLayout, visit *models.Reservation, friend *models.TabGuest) (map[string]interface{}, error) {
	info := visit.ReservationInfo
	table := map[string]interface{}{"id": "", "name": info.TableNumber, "floor": info.DiningArea, "seats": 0}
	if layout != nil {
		for i := range layout.TablePosition {
			if t := &layout.TablePosition[i]; t.Name == info.TableNumber {
				table = tableSummary(layout, t)
				break
			}
		}
	}

	rows, err := h.visitOrders(ctx, restaurant.ID, info.ReservationID)
	if err != nil {
		return nil, err
	}
	orders := make([]map[string]interface{}, 0, len(rows))
	for _, o := range rows {
		items := o.Items
		if items == nil {
			items = []models.MenuItem{}
		}
		total := o.TotalAmount
		if friend != nil {
			if items = friendLines(items, friend.ID); len(items) == 0 {
				continue
			}
			total = orderTotal(items)
		}
		waiter := ""
		if len(o.Waiter) > 0 {
			waiter = strings.TrimSpace(o.Waiter[0].FirstName + " " + o.Waiter[0].LastName)
		}
		paymentStatus := o.PaymentStatus
		if paymentStatus == "" {
			paymentStatus = models.PaymentUnpaid
		}
		orders = append(orders, map[string]interface{}{
			"_id":           o.ID.Hex(),
			"orderId":       o.OrderNumber,
			"status":        o.OrderStatus,
			"items":         items,
			"totalAmount":   total,
			"paymentMethod": o.PaymentMethod,
			"paymentStatus": paymentStatus,
			"createdAt":     o.CreatedAt,
			"updatedAt":     o.UpdatedAt,
			"servedAt":      o.ServedAt,
			"paidAt":        o.PaidAt,
			"waiter":        waiter,
		})
	}

	name := info.Name
	you := map[string]interface{}{"role": "host", "name": info.Name}
	if friend != nil {
		name = hostName(visit)
		you = map[string]interface{}{"role": "friend", "id": friend.ID, "name": friend.Name, "host": name}
	}
	body := map[string]interface{}{
		"restaurant": restaurantSummary(restaurant),
		"table":      table,
		"visit": map[string]interface{}{
			"reservationId": info.ReservationID,
			"name":          name,
			"guests":        info.Guests,
			"status":        visit.Status,
			"since":         info.BookingFor,
			"until":         info.EndTime,
			"open":          visit.Status == models.ReservationSeated || visit.Status == models.ReservationActive,
		},
		"you":    you,
		"orders": orders,
	}
	if friend == nil {
		body["tab"] = h.tabSummary(visit, rows)
	}
	return body, nil
}

// Visit returns the guest's visit and its orders. The guest page polls it to
// follow an order from the kitchen to the bill.
func (h *DineHandler) Visit(w http.ResponseWriter, r *http.Request) {
	visit, friend, ok := h.guestVisit(w, r)
	if !ok {
		return
	}
	h.respondVisit(w, r, visit, friend, http.StatusOK, nil)
}

// respondVisit writes the visit body, with any extra fields.
func (h *DineHandler) respondVisit(w http.ResponseWriter, r *http.Request, visit *models.Reservation, friend *models.TabGuest, status int, extra map[string]interface{}) {
	restaurant, err := h.loadRestaurant(r.Context(), visit.RestaurantID)
	if err != nil || len(restaurant.Data) == 0 {
		util.JsonError(w, http.StatusNotFound, "Restaurant not found")
		return
	}
	layout, _ := h.loadLayout(r.Context(), visit.RestaurantID)
	body, err := h.visitBody(r.Context(), restaurant, layout, visit, friend)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not load your visit")
		return
	}
	for k, v := range extra {
		body[k] = v
	}
	util.JsonResponse(w, status, body)
}

// PlaceOrder sends a guest's dishes to the kitchen for their table. A friend
// on a shared tab adds them to the host's bill.
func (h *DineHandler) PlaceOrder(w http.ResponseWriter, r *http.Request) {
	visit, friend, ok := h.guestVisit(w, r)
	if !ok {
		return
	}
	if visit.Status != models.ReservationSeated && visit.Status != models.ReservationActive {
		if friend != nil {
			util.JsonError(w, http.StatusConflict, "This booking has ended, so nothing more can be added to the tab.")
			return
		}
		util.JsonError(w, http.StatusConflict, "Your visit has ended. Scan the code on your table to order again.")
		return
	}
	var req struct {
		Menu []models.MenuItem `json:"menu"`
	}
	if err := util.DecodeJSON(w, r, &req); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	h.Orders.placeOrder(w, r, visit, req.Menu, "", friend)
}

// guestOrder loads one of the visit's orders for the host. Friends on a
// shared tab cannot ask for the bill or cancel: the host pays.
func (h *DineHandler) guestOrder(w http.ResponseWriter, r *http.Request) *models.Order {
	visit, friend, ok := h.guestVisit(w, r)
	if !ok {
		return nil
	}
	if friend != nil {
		util.JsonError(w, http.StatusForbidden, "Only "+hostName(visit)+" can cancel the order or ask for the bill.")
		return nil
	}
	orderID, err := bson.ObjectIDFromHex(r.PathValue("orderId"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid order ID")
		return nil
	}
	var order models.Order
	if err := h.DB.Collection("orders").FindOne(r.Context(), bson.M{
		"_id":           orderID,
		"restaurantId":  visit.RestaurantID,
		"reservationId": visit.ReservationInfo.ReservationID,
	}).Decode(&order); err != nil {
		util.JsonError(w, http.StatusNotFound, "Order not found")
		return nil
	}
	return &order
}

// RequestBill tells the waiters the guest is ready to pay, and how.
func (h *DineHandler) RequestBill(w http.ResponseWriter, r *http.Request) {
	if order := h.guestOrder(w, r); order != nil {
		h.Orders.requestBill(w, r, order)
	}
}

// CancelOrder cancels a guest's order before anyone starts serving it.
func (h *DineHandler) CancelOrder(w http.ResponseWriter, r *http.Request) {
	if order := h.guestOrder(w, r); order != nil {
		h.Orders.cancelPlacedOrder(w, r, order)
	}
}
