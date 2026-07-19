package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"swiftab/server/internal/middleware"
	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type ReservationHandler struct {
	DB *mongo.Database
}

func (h *ReservationHandler) getRestaurantID(r *http.Request) (bson.ObjectID, error) {
	rid, ok := r.Context().Value(middleware.RestaurantIDKey).(bson.ObjectID)
	if !ok {
		return bson.NilObjectID, errors.New("missing restaurant ID")
	}
	return rid, nil
}

func (h *ReservationHandler) FetchReservations(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.getRestaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Restaurant ID is required")
		return
	}

	filter := bson.M{"restaurantId": restaurantID}

	opts := options.Find().
		SetSort(bson.M{"reservationInfo.bookingFor": -1}).
		SetLimit(500)

	var reservations []models.Reservation
	cursor, err := h.DB.Collection("reservations").Find(r.Context(), filter, opts)
	if err == nil {
		err = cursor.All(r.Context(), &reservations)
	}

	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "An error occurred while retrieving reservations")
		return
	}

	var formattedReservations []map[string]interface{}
	for _, res := range reservations {
		info := res.ReservationInfo
		formattedReservations = append(formattedReservations, map[string]interface{}{
			"_id":         res.ID.Hex(),
			"name":        info.Name,
			"email":       info.Email,
			"phoneNumber": info.PhoneNumber,
			"start":       info.BookingFor,
			"end":         info.EndTime,
			"guests":      info.Guests,
			"table":       info.TableNumber,
			"floor":       info.DiningArea,
			"status":      res.Status,
		})
	}

	if formattedReservations == nil {
		formattedReservations = []map[string]interface{}{}
	}

	util.JsonResponse(w, http.StatusOK, formattedReservations)
}

type AggReservation struct {
	models.Reservation `bson:",inline"`
	Restaurant         []models.Restaurant `bson:"restaurant"`
}

func (h *ReservationHandler) getUserReservationsByStatus(w http.ResponseWriter, r *http.Request, status string) {
	userIDStr := r.PathValue("userId")
	if userIDStr == "" {
		util.JsonError(w, http.StatusBadRequest, "User ID is required")
		return
	}

	userID, err := bson.ObjectIDFromHex(userIDStr)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid User ID")
		return
	}

	pipeline := mongo.Pipeline{
		bson.D{{"$match", bson.M{
			"userId": userID,
			"status": status,
		}}},
		bson.D{{"$lookup", bson.M{
			"from":         "restaurants",
			"localField":   "restaurantId",
			"foreignField": "_id",
			"as":           "restaurant",
		}}},
	}

	cursor, err := h.DB.Collection("reservations").Aggregate(r.Context(), pipeline)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	defer cursor.Close(r.Context())

	var aggReservations []AggReservation
	if err := cursor.All(r.Context(), &aggReservations); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error decoding reservations")
		return
	}

	if len(aggReservations) == 0 {
		util.JsonResponse(w, http.StatusOK, []interface{}{})
		return
	}

	var responseData []map[string]interface{}

	for _, agg := range aggReservations {
		info := agg.ReservationInfo

		restName := "Unknown Restaurant"
		restImage := ""
		restLocation := ""
		var restRate float64 = 0

		if len(agg.Restaurant) > 0 {
			rest := agg.Restaurant[0]
			if len(rest.Data) > 0 {
				restName = rest.Data[0].RestaurantName
				restImage = rest.Data[0].Image
				restLocation = rest.Data[0].Location
				restRate = rest.Data[0].Rate
			}
		}

		dateStr := info.BookingFor.Format("2006-01-02")
		timeStr := info.BookingFor.Format("15:04")

		if status == "active" {
			responseData = append(responseData, map[string]interface{}{
				"id":             agg.ID.Hex(),
				"reservationId":  info.ReservationID,
				"date":           dateStr,
				"time":           timeStr,
				"table":          info.TableNumber,
				"restaurantName": restName,
				"image":          restImage,
				"location":       restLocation,
				"rate":           restRate,
				"restaurantId":   agg.RestaurantID,
			})
		} else {
			responseData = append(responseData, map[string]interface{}{
				"date":           dateStr,
				"time":           info.BookingFor.UnixNano() / int64(time.Millisecond),
				"location":       restLocation,
				"rate":           restRate,
				"image":          restImage,
				"restaurantName": restName,
			})
		}
	}

	util.JsonResponse(w, http.StatusOK, responseData)
}

func (h *ReservationHandler) UserActiveReservation(w http.ResponseWriter, r *http.Request) {
	h.getUserReservationsByStatus(w, r, "active")
}

func (h *ReservationHandler) UserCompletedReservation(w http.ResponseWriter, r *http.Request) {
	h.getUserReservationsByStatus(w, r, "completed")
}

func GenerateReservationID(restaurantID string, bookingFor time.Time) string {
	if len(restaurantID) < 4 {
		return fmt.Sprintf("RES-XXXX-%d", bookingFor.Unix())
	}
	return fmt.Sprintf("RES-%s-%d", restaurantID[len(restaurantID)-4:], bookingFor.Unix())
}

func (h *ReservationHandler) CreateReservation(w http.ResponseWriter, r *http.Request) {
	userIDStr := r.PathValue("userId")
	restIDStr := r.PathValue("restaurantId")

	if userIDStr == "" || restIDStr == "" {
		util.JsonError(w, http.StatusBadRequest, "Missing restaurantId or userId parameters")
		return
	}

	userID, err1 := bson.ObjectIDFromHex(userIDStr)
	restID, err2 := bson.ObjectIDFromHex(restIDStr)
	if err1 != nil || err2 != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid ID formats")
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
			} `json:"reservationInfo"`
		} `json:"data"`
	}

	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid reservation data")
		return
	}

	info := reqBody.Data.ReservationInfo

	session, err := h.DB.Client().StartSession()
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not start database session")
		return
	}
	defer session.EndSession(r.Context())

	timeOverlapQuery := bson.M{
		"$or": []bson.M{
			{
				"reservationInfo.bookingFor": bson.M{"$lt": info.EndTime},
				"reservationInfo.endTime":    bson.M{"$gt": info.BookingFor},
			},
		},
	}

	var savedReservation *models.Reservation

	callback := func(sessCtx context.Context) (interface{}, error) {
		userConflictFilter := bson.M{
			"restaurantId": restID,
			"userId":       userID,
			"status":       bson.M{"$in": []string{"active", "seated"}},
		}
		for k, v := range timeOverlapQuery {
			userConflictFilter[k] = v
		}

		var checkUser models.Reservation
		err := h.DB.Collection("reservations").FindOne(sessCtx, userConflictFilter).Decode(&checkUser)
		if err != mongo.ErrNoDocuments {
			return nil, fmt.Errorf("USER_CONFLICT")
		}

		tableConflictFilter := bson.M{
			"restaurantId":                restID,
			"reservationInfo.tableNumber": info.TableNumber,
			"status":                      bson.M{"$in": []string{"active", "seated"}},
		}
		for k, v := range timeOverlapQuery {
			tableConflictFilter[k] = v
		}

		var checkTable models.Reservation
		err = h.DB.Collection("reservations").FindOne(sessCtx, tableConflictFilter).Decode(&checkTable)
		if err != mongo.ErrNoDocuments {
			return nil, fmt.Errorf("TABLE_CONFLICT")
		}

		resID := GenerateReservationID(restIDStr, info.BookingFor)

		newRes := models.Reservation{
			ID:           bson.NewObjectID(),
			RestaurantID: restID,
			UserID:       userID,
			Status:       models.ReservationActive,
			ReservationInfo: models.ReservationInfo{
				ReservationID:  resID,
				Name:           info.Name,
				Email:          info.Email,
				PhoneNumber:    info.PhoneNumber,
				BookingDate:    time.Now().UTC(),
				BookingFor:     info.BookingFor,
				EndTime:        info.EndTime,
				Guests:         info.Guests,
				TableNumber:    info.TableNumber,
				DiningArea:     info.DiningArea,
				RestaurantName: info.RestaurantName,
			},
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}

		_, err = h.DB.Collection("reservations").InsertOne(sessCtx, newRes)
		if err != nil {
			return nil, err
		}

		savedReservation = &newRes
		return nil, nil
	}

	_, err = session.WithTransaction(r.Context(), callback)

	if err != nil {
		if err.Error() == "USER_CONFLICT" {
			util.JsonResponse(w, http.StatusConflict, map[string]interface{}{
				"message":         "You already have a reservation at this time.",
				"conflictDetails": map[string]string{"type": "user_conflict"},
			})
			return
		}
		if err.Error() == "TABLE_CONFLICT" {
			util.JsonResponse(w, http.StatusConflict, map[string]interface{}{
				"message":         "This table was just booked by another user.",
				"conflictDetails": map[string]string{"tableNumber": info.TableNumber},
			})
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "An error occurred while creating the reservation")
		return
	}

	durationHours := info.EndTime.Sub(info.BookingFor).Hours()

	responseData := map[string]interface{}{
		"reservationId": savedReservation.ReservationInfo.ReservationID,
		"name":          savedReservation.ReservationInfo.Name,
		"date":          info.BookingFor.Format("2006-01-02"),
		"time":          info.BookingFor.Format("15:04"),
		"duration":      fmt.Sprintf("%.2f", durationHours),
		"guests":        savedReservation.ReservationInfo.Guests,
		"tableNumber":   savedReservation.ReservationInfo.TableNumber,
		"floor":         savedReservation.ReservationInfo.DiningArea,
	}

	util.JsonResponse(w, http.StatusCreated, map[string]interface{}{
		"message":      "Successfully reserved",
		"responseData": responseData,
	})
}

func (h *ReservationHandler) CheckAvailability(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		RestaurantID string    `json:"restaurantId"`
		BookingFor   time.Time `json:"bookingFor"`
		EndTime      time.Time `json:"endTime"`
	}

	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
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

	filter := bson.M{
		"restaurantId": restID,
		"status":       "active",
		"$or": []bson.M{
			{
				"reservationInfo.bookingFor": bson.M{"$lt": reqBody.EndTime},
				"reservationInfo.endTime":    bson.M{"$gt": reqBody.BookingFor},
			},
		},
	}

	var overlappingReservations []models.Reservation
	cursor, err := h.DB.Collection("reservations").Find(r.Context(), filter)
	if err == nil {
		err = cursor.All(r.Context(), &overlappingReservations)
	}
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Database error retrieving reservations")
		return
	}

	reservedTables := make(map[string]bool)
	for _, res := range overlappingReservations {
		reservedTables[res.ReservationInfo.TableNumber] = true
	}

	var layout models.RestaurantLayout
	err = h.DB.Collection("restaurantlayouts").FindOne(r.Context(), bson.M{"restaurantId": restID}).Decode(&layout)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not fetch restaurant layout")
		return
	}

	var tables []string
	var availability []map[string]interface{}

	for _, table := range layout.TablePosition {
		tables = append(tables, table.Name)
		isAvailable := !reservedTables[table.Name]

		availability = append(availability, map[string]interface{}{
			"tableNumber": table.Name,
			"isAvailable": isAvailable,
		})
	}

	if tables == nil {
		tables = []string{}
	}
	if availability == nil {
		availability = []map[string]interface{}{}
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"availability": availability,
		"tables":       tables,
	})
}
