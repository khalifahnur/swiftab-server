package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"swiftab/server/internal/events"
	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type ReservationHandler struct {
	*Base
}

var (
	errUserConflict  = errors.New("USER_CONFLICT")
	errTableConflict = errors.New("TABLE_CONFLICT")
)

// holdingStatuses are the reservation states that keep a table taken.
var holdingStatuses = bson.A{"active", "seated"}

// overlapFilter matches reservations whose time range intersects [from, to).
func overlapFilter(from, to time.Time) bson.M {
	return bson.M{
		"reservationInfo.bookingFor": bson.M{"$lt": to},
		"reservationInfo.endTime":    bson.M{"$gt": from},
	}
}

// --- Dashboard -------------------------------------------------------------

func (h *ReservationHandler) FetchReservations(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Restaurant ID is required")
		return
	}

	opts := options.Find().
		SetSort(bson.M{"reservationInfo.bookingFor": -1}).
		SetLimit(500)

	var reservations []models.Reservation
	cursor, err := h.DB.Collection("reservations").Find(r.Context(), bson.M{"restaurantId": restaurantID}, opts)
	if err == nil {
		err = cursor.All(r.Context(), &reservations)
	}
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "An error occurred while retrieving reservations")
		return
	}

	formattedReservations := make([]map[string]interface{}, 0, len(reservations))
	for _, res := range reservations {
		info := res.ReservationInfo
		formattedReservations = append(formattedReservations, map[string]interface{}{
			"_id":           res.ID.Hex(),
			"reservationID": info.ReservationID,
			"name":          info.Name,
			"email":         info.Email,
			"phoneNumber":   info.PhoneNumber,
			"start":         info.BookingFor,
			"end":           info.EndTime,
			"bookedAt":      info.BookingDate,
			"guests":        info.Guests,
			"table":         info.TableNumber,
			"floor":         info.DiningArea,
			"notes":         info.Notes,
			"status":        res.Status,
			"hasOrder":      res.UserOrder != nil,
		})
	}

	util.JsonResponse(w, http.StatusOK, formattedReservations)
}

// AdminUpdateReservation lets the host stand move a booking along: seat the
// party, mark a no-show, cancel, or complete it.
func (h *ReservationHandler) AdminUpdateReservation(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Restaurant ID is required")
		return
	}
	reservationID, err := bson.ObjectIDFromHex(r.PathValue("id"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid reservation ID")
		return
	}

	var reqBody struct {
		Status string `json:"status"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if !models.ValidReservationStatus(reqBody.Status) {
		util.JsonError(w, http.StatusBadRequest, "Unsupported reservation status")
		return
	}

	var updated models.Reservation
	err = h.DB.Collection("reservations").FindOneAndUpdate(r.Context(),
		bson.M{"_id": reservationID, "restaurantId": restaurantID},
		bson.M{"$set": bson.M{"status": reqBody.Status, "updatedAt": time.Now().UTC()}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&updated)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			util.JsonError(w, http.StatusNotFound, "Reservation not found")
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "Could not update the reservation")
		return
	}

	h.publish(restaurantID, events.Event{Type: "reservation", Action: "status", ID: reservationID.Hex(), Table: updated.ReservationInfo.TableNumber, Status: reqBody.Status})

	info := updated.ReservationInfo
	switch models.ReservationStatus(reqBody.Status) {
	case models.ReservationCancelled:
		h.notifyUser(updated.UserID, util.PushMessage{
			Title:   "Reservation cancelled",
			Body:    info.RestaurantName + " cancelled your booking. Open the app to choose another time.",
			Data:    map[string]string{"type": "reservation", "reservationId": info.ReservationID},
			Channel: "reservations",
		})
	case models.ReservationSeated:
		h.notifyUser(updated.UserID, util.PushMessage{
			Title:   "Your table is ready",
			Body:    "Welcome to " + info.RestaurantName + ". You can order from the app at table " + info.TableNumber + ".",
			Data:    map[string]string{"type": "reservation", "reservationId": info.ReservationID},
			Channel: "reservations",
		})
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Reservation updated"})
}

// --- Diners ----------------------------------------------------------------

type aggReservation struct {
	models.Reservation `bson:",inline"`
	Restaurant         []models.Restaurant `bson:"restaurant"`
}

// userReservations lists a diner's reservations in the given states. The
// legacy fields (date, time, table...) are kept; bookingFor and endTime are
// the exact instants for clients that format times themselves.
func (h *ReservationHandler) userReservations(w http.ResponseWriter, r *http.Request, statuses bson.A, upcomingFirst bool) {
	userID, err := bson.ObjectIDFromHex(r.PathValue("userId"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid User ID")
		return
	}
	if !ownsUser(r, userID) {
		util.JsonError(w, http.StatusForbidden, "You can only view your own reservations")
		return
	}

	sortOrder := -1
	if upcomingFirst {
		sortOrder = 1
	}

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"userId": userID, "status": bson.M{"$in": statuses}}}},
		{{Key: "$sort", Value: bson.M{"reservationInfo.bookingFor": sortOrder}}},
		{{Key: "$limit", Value: 100}},
		{{Key: "$lookup", Value: bson.M{
			"from":         "restaurants",
			"localField":   "restaurantId",
			"foreignField": "_id",
			"as":           "restaurant",
		}}},
		{{Key: "$project", Value: bson.M{"restaurant.data.menu": 0, "restaurant.data.review": 0}}},
	}

	cursor, err := h.DB.Collection("reservations").Aggregate(r.Context(), pipeline)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}

	var aggReservations []aggReservation
	if err := cursor.All(r.Context(), &aggReservations); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error decoding reservations")
		return
	}

	responseData := make([]map[string]interface{}, 0, len(aggReservations))
	for _, agg := range aggReservations {
		info := agg.ReservationInfo

		restName := info.RestaurantName
		restImage := ""
		restLocation := ""
		var restRate, lat, lng float64

		if len(agg.Restaurant) > 0 && len(agg.Restaurant[0].Data) > 0 {
			data := agg.Restaurant[0].Data[0]
			restName = data.RestaurantName
			restImage = data.Image
			restLocation = data.Location
			restRate = data.Rate
			lat, lng = data.Latitude, data.Longitude
		}
		if restName == "" {
			restName = "Unknown Restaurant"
		}

		// Day and clock time are rendered in the restaurant's timezone, which
		// is where the booking takes place.
		local := info.BookingFor.In(h.location())

		responseData = append(responseData, map[string]interface{}{
			"id":             agg.ID.Hex(),
			"reservationId":  info.ReservationID,
			"status":         agg.Status,
			"date":           local.Format("2006-01-02"),
			"time":           info.BookingFor.UnixMilli(),
			"timeLabel":      local.Format("15:04"),
			"bookingFor":     info.BookingFor,
			"endTime":        info.EndTime,
			"guests":         info.Guests,
			"table":          info.TableNumber,
			"diningArea":     info.DiningArea,
			"restaurantName": restName,
			"image":          restImage,
			"location":       restLocation,
			"latitude":       lat,
			"longitude":      lng,
			"rate":           restRate,
			"restaurantId":   agg.RestaurantID.Hex(),
			"hasOrder":       agg.UserOrder != nil,
			"sharing":        agg.Tab.IsOpen(),
			"tabGuests":      tabGuestCount(agg.Tab),
		})
	}

	util.JsonResponse(w, http.StatusOK, responseData)
}

// UserActiveReservation lists upcoming and in-progress bookings. A seated
// booking stays here so the diner can keep ordering against it.
func (h *ReservationHandler) UserActiveReservation(w http.ResponseWriter, r *http.Request) {
	h.userReservations(w, r, holdingStatuses, true)
}

func (h *ReservationHandler) UserCompletedReservation(w http.ResponseWriter, r *http.Request) {
	h.userReservations(w, r, bson.A{"completed"}, false)
}

func (h *ReservationHandler) UserCancelledReservation(w http.ResponseWriter, r *http.Request) {
	h.userReservations(w, r, bson.A{"cancelled", "no_show"}, false)
}

func GenerateReservationID(restaurantID string, bookingFor time.Time) string {
	suffix := "XXXX"
	if len(restaurantID) >= 4 {
		suffix = strings.ToUpper(restaurantID[len(restaurantID)-4:])
	}
	// The random part keeps two tables booked for the same minute distinct.
	return fmt.Sprintf("RES-%s-%d-%s", suffix, bookingFor.Unix(), util.GenerateCode()[:3])
}

func (h *ReservationHandler) CreateReservation(w http.ResponseWriter, r *http.Request) {
	userID, err1 := bson.ObjectIDFromHex(r.PathValue("userId"))
	restID, err2 := bson.ObjectIDFromHex(r.PathValue("restaurantId"))
	if err1 != nil || err2 != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid ID formats")
		return
	}
	if !ownsUser(r, userID) {
		util.JsonError(w, http.StatusForbidden, "You can only book for your own account")
		return
	}

	var reqBody struct {
		Data struct {
			ReservationInfo struct {
				Name           string    `json:"name"`
				Email          string    `json:"email"`
				PhoneNumber    string    `json:"phoneNumber"`
				BookingFor     time.Time `json:"bookingFor"`
				EndTime        time.Time `json:"endTime"`
				Guests         int       `json:"guests"`
				TableNumber    string    `json:"tableNumber"`
				DiningArea     string    `json:"diningArea"`
				RestaurantName string    `json:"restaurantName"`
				Notes          string    `json:"notes"`
			} `json:"reservationInfo"`
		} `json:"data"`
	}

	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid reservation data")
		return
	}

	info := reqBody.Data.ReservationInfo
	info.TableNumber = strings.TrimSpace(info.TableNumber)
	now := time.Now().UTC()

	switch {
	case info.BookingFor.IsZero() || info.EndTime.IsZero():
		util.JsonError(w, http.StatusBadRequest, "Choose a date and time for your reservation")
		return
	case !info.EndTime.After(info.BookingFor):
		util.JsonError(w, http.StatusBadRequest, "The end time must be after the start time")
		return
	case info.EndTime.Sub(info.BookingFor) > 12*time.Hour:
		util.JsonError(w, http.StatusBadRequest, "A reservation can last at most 12 hours")
		return
	case info.BookingFor.Before(now.Add(-5 * time.Minute)):
		util.JsonError(w, http.StatusBadRequest, "That time has already passed. Choose a later time.")
		return
	case info.TableNumber == "":
		util.JsonError(w, http.StatusBadRequest, "Choose a table")
		return
	}
	if info.Guests < 1 {
		info.Guests = 1
	}

	restaurant, err := h.loadRestaurant(r.Context(), restID)
	if err != nil || len(restaurant.Data) == 0 {
		util.JsonError(w, http.StatusNotFound, "Restaurant not found")
		return
	}
	if !restaurant.Data[0].IsAcceptingReservations() {
		util.JsonError(w, http.StatusConflict, "This restaurant is not taking reservations right now")
		return
	}

	layout, err := h.loadLayout(r.Context(), restID)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not check the floor plan")
		return
	}
	var table *models.Table
	for i := range layout.TablePosition {
		if layout.TablePosition[i].Name == info.TableNumber {
			table = &layout.TablePosition[i]
			break
		}
	}
	if table == nil {
		util.JsonError(w, http.StatusBadRequest, "That table is no longer available. Please choose another.")
		return
	}
	if seats := len(table.Chairs); seats > 0 && info.Guests > seats {
		util.JsonError(w, http.StatusBadRequest, fmt.Sprintf("Table %s seats %d. Choose a larger table for %d guests.", table.Name, seats, info.Guests))
		return
	}

	diningArea := layout.FloorName(table.FloorID)
	if diningArea == "" {
		diningArea = info.DiningArea
	}

	newRes := models.Reservation{
		ID:           bson.NewObjectID(),
		RestaurantID: restID,
		UserID:       userID,
		Status:       models.ReservationActive,
		ReservationInfo: models.ReservationInfo{
			ReservationID:  GenerateReservationID(restID.Hex(), info.BookingFor),
			Name:           strings.TrimSpace(info.Name),
			Email:          strings.TrimSpace(info.Email),
			PhoneNumber:    strings.TrimSpace(info.PhoneNumber),
			BookingDate:    now,
			BookingFor:     info.BookingFor.UTC(),
			EndTime:        info.EndTime.UTC(),
			Guests:         info.Guests,
			TableNumber:    info.TableNumber,
			DiningArea:     diningArea,
			RestaurantName: restaurant.Data[0].RestaurantName,
			Notes:          strings.TrimSpace(info.Notes),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	// book checks for clashes and inserts. Inside a transaction the check
	// and the insert are atomic, so two diners cannot take the same table.
	book := func(ctx context.Context) (interface{}, error) {
		reservations := h.DB.Collection("reservations")

		userConflict := overlapFilter(info.BookingFor, info.EndTime)
		userConflict["userId"] = userID
		userConflict["status"] = bson.M{"$in": holdingStatuses}
		if err := reservations.FindOne(ctx, userConflict).Err(); err == nil {
			return nil, errUserConflict
		} else if !errors.Is(err, mongo.ErrNoDocuments) {
			return nil, err
		}

		tableConflict := overlapFilter(info.BookingFor, info.EndTime)
		tableConflict["restaurantId"] = restID
		tableConflict["reservationInfo.tableNumber"] = info.TableNumber
		tableConflict["status"] = bson.M{"$in": holdingStatuses}
		if err := reservations.FindOne(ctx, tableConflict).Err(); err == nil {
			return nil, errTableConflict
		} else if !errors.Is(err, mongo.ErrNoDocuments) {
			return nil, err
		}

		_, err := reservations.InsertOne(ctx, newRes)
		return nil, err
	}

	session, err := h.DB.Client().StartSession()
	if err == nil {
		_, err = session.WithTransaction(r.Context(), book)
		session.EndSession(r.Context())
	}
	if isTransactionUnsupported(err) {
		// A standalone MongoDB (local development) has no transactions.
		_, err = book(r.Context())
	}

	if err != nil {
		switch {
		case errors.Is(err, errUserConflict):
			util.JsonResponse(w, http.StatusConflict, map[string]interface{}{
				"message":         "You already have a reservation at this time.",
				"error":           "You already have a reservation at this time.",
				"conflictDetails": map[string]string{"type": "user_conflict"},
			})
		case errors.Is(err, errTableConflict):
			util.JsonResponse(w, http.StatusConflict, map[string]interface{}{
				"message":         "This table was just booked by another user.",
				"error":           "This table was just booked by another user.",
				"conflictDetails": map[string]string{"tableNumber": info.TableNumber},
			})
		default:
			util.JsonError(w, http.StatusInternalServerError, "An error occurred while creating the reservation")
		}
		return
	}

	h.publish(restID, events.Event{Type: "reservation", Action: "created", ID: newRes.ID.Hex(), Table: info.TableNumber, Status: string(models.ReservationActive)})

	saved := newRes.ReservationInfo
	local := saved.BookingFor.In(h.location())
	responseData := map[string]interface{}{
		"id":            newRes.ID.Hex(),
		"reservationId": saved.ReservationID,
		"name":          saved.Name,
		"date":          local.Format("2006-01-02"),
		"time":          local.Format("15:04"),
		"bookingFor":    saved.BookingFor,
		"endTime":       saved.EndTime,
		"duration":      fmt.Sprintf("%.2f", saved.EndTime.Sub(saved.BookingFor).Hours()),
		"guests":        saved.Guests,
		"tableNumber":   saved.TableNumber,
		"floor":         saved.DiningArea,
		"restaurantId":  restID.Hex(),
	}

	util.JsonResponse(w, http.StatusCreated, map[string]interface{}{
		"message":      "Successfully reserved",
		"responseData": responseData,
	})
}

// CheckAvailability reports which tables are free for a time range.
func (h *ReservationHandler) CheckAvailability(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		RestaurantID string    `json:"restaurantId"`
		BookingFor   time.Time `json:"bookingFor"`
		EndTime      time.Time `json:"endTime"`
	}

	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if reqBody.RestaurantID == "" || reqBody.BookingFor.IsZero() || reqBody.EndTime.IsZero() {
		util.JsonError(w, http.StatusBadRequest, "restaurantId, bookingFor, and endTime are required.")
		return
	}
	if !reqBody.EndTime.After(reqBody.BookingFor) {
		util.JsonError(w, http.StatusBadRequest, "endTime must be after bookingFor.")
		return
	}

	restID, err := bson.ObjectIDFromHex(reqBody.RestaurantID)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid restaurant ID")
		return
	}

	filter := overlapFilter(reqBody.BookingFor, reqBody.EndTime)
	filter["restaurantId"] = restID
	filter["status"] = bson.M{"$in": holdingStatuses}

	var overlapping []models.Reservation
	cursor, err := h.DB.Collection("reservations").Find(r.Context(), filter)
	if err == nil {
		err = cursor.All(r.Context(), &overlapping)
	}
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Database error retrieving reservations")
		return
	}

	reserved := make(map[string]bool, len(overlapping))
	for _, res := range overlapping {
		reserved[res.ReservationInfo.TableNumber] = true
	}

	// The floor plan is keyed by the restaurant's id, stored as a string.
	layout, err := h.loadLayout(r.Context(), restID)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not fetch restaurant layout")
		return
	}

	tables := make([]string, 0, len(layout.TablePosition))
	availability := make([]map[string]interface{}, 0, len(layout.TablePosition))
	for _, table := range layout.TablePosition {
		tables = append(tables, table.Name)
		availability = append(availability, map[string]interface{}{
			"tableNumber": table.Name,
			"isAvailable": !reserved[table.Name],
			"seats":       len(table.Chairs),
			"floorId":     table.FloorID,
		})
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"availability": availability,
		"tables":       tables,
	})
}

// UpdateStatusByUser cancels one of the diner's own upcoming reservations.
func (h *ReservationHandler) UpdateStatusByUser(w http.ResponseWriter, r *http.Request) {
	userID, err := bson.ObjectIDFromHex(r.PathValue("userId"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid User ID")
		return
	}
	if !ownsUser(r, userID) {
		util.JsonError(w, http.StatusForbidden, "You can only cancel your own reservations")
		return
	}

	var reqBody struct {
		ID            string `json:"id"`
		RestaurantID  string `json:"restaurantId"`
		ReservationID string `json:"reservationId"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// The booking is identified by its document id or its public code;
	// either is enough because the match is always scoped to this diner.
	filter := bson.M{"userId": userID}
	switch {
	case reqBody.ID != "":
		objID, err := bson.ObjectIDFromHex(reqBody.ID)
		if err != nil {
			util.JsonError(w, http.StatusBadRequest, "Invalid reservation ID format")
			return
		}
		filter["_id"] = objID
	case reqBody.ReservationID != "":
		filter["reservationInfo.reservationID"] = reqBody.ReservationID
	default:
		util.JsonError(w, http.StatusBadRequest, "Missing required fields: id or reservationId")
		return
	}

	var existing models.Reservation
	if err := h.DB.Collection("reservations").FindOne(r.Context(), filter).Decode(&existing); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			util.JsonError(w, http.StatusNotFound, "Reservation not found or details do not match")
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "An error occurred while updating the reservation")
		return
	}

	switch existing.Status {
	case models.ReservationCancelled:
		util.JsonResponse(w, http.StatusOK, map[string]interface{}{"message": "Reservation already cancelled", "status": existing.Status})
		return
	case models.ReservationActive:
	default:
		util.JsonError(w, http.StatusConflict, "This reservation can no longer be cancelled")
		return
	}

	filter["status"] = models.ReservationActive
	res, err := h.DB.Collection("reservations").UpdateOne(r.Context(), filter, bson.M{"$set": bson.M{
		"status":    models.ReservationCancelled,
		"updatedAt": time.Now().UTC(),
	}})
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "An error occurred while updating the reservation")
		return
	}
	if res.MatchedCount == 0 {
		util.JsonError(w, http.StatusConflict, "This reservation can no longer be cancelled")
		return
	}

	h.publish(existing.RestaurantID, events.Event{Type: "reservation", Action: "status", ID: existing.ID.Hex(), Table: existing.ReservationInfo.TableNumber, Status: string(models.ReservationCancelled)})

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message":       "Reservation cancelled",
		"id":            existing.ID.Hex(),
		"reservationId": existing.ReservationInfo.ReservationID,
		"status":        models.ReservationCancelled,
	})
}

// tabGuestCount is how many friends joined a booking's shared tab.
func tabGuestCount(tab *models.Tab) int {
	if !tab.IsOpen() {
		return 0
	}
	return len(tab.Guests)
}
