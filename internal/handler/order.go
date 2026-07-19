package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"swiftab/server/internal/middleware"
	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type OrderHandler struct {
	DB *mongo.Database
}

func (h *OrderHandler) UserCompleteOrder(w http.ResponseWriter, r *http.Request) {
	orderIDStr := r.PathValue("orderId")
	orderID, err := bson.ObjectIDFromHex(orderIDStr)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var reqBody struct {
		Status        string `json:"status"`
		PaymentMethod string `json:"paymentMethod"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	defer r.Body.Close()

	update := bson.M{
		"$set": bson.M{
			"orderStatus":   reqBody.Status,
			"paymentMethod": reqBody.PaymentMethod,
		},
	}

	res, err := h.DB.Collection("orders").UpdateByID(r.Context(), orderID, update)
	if err != nil || res.MatchedCount == 0 {
		util.JsonError(w, http.StatusNotFound, "Order not found")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{
		"message": "Wait for the assigned waiter to complete payment",
	})
}

func (h *OrderHandler) CreateOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Menu           []models.MenuItem `json:"menu"`
		UserID         string            `json:"userId"`
		RestaurantID   string            `json:"restaurantId"`
		ReservationID  string            `json:"reservationId"`
		TableNumber    string            `json:"tableNumber"`
		RestaurantName string            `json:"restaurantName"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	defer r.Body.Close()

	if req.UserID == "" || req.RestaurantID == "" || req.ReservationID == "" || req.TableNumber == "" {
		util.JsonError(w, http.StatusBadRequest, "Missing required identity fields")
		return
	}
	if len(req.Menu) == 0 {
		util.JsonError(w, http.StatusBadRequest, "Order must contain at least one menu item")
		return
	}

	resID, err1 := bson.ObjectIDFromHex(req.ReservationID)
	restID, err2 := bson.ObjectIDFromHex(req.RestaurantID)
	userID, err3 := bson.ObjectIDFromHex(req.UserID)

	if err1 != nil || err2 != nil || err3 != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid ID formats")
		return
	}

	var reservation models.Reservation
	err := h.DB.Collection("reservations").FindOne(r.Context(), bson.M{"reservationInfo.reservationID": req.ReservationID}).Decode(&reservation)
	if err != nil {
		util.JsonError(w, http.StatusNotFound, "Reservation not found")
		return
	}

	var newItemsTotal float64
	for _, item := range req.Menu {
		newItemsTotal += item.Cost * float64(item.Quantity)
	}

	var existingOrder *models.Order
	if reservation.UserOrder != nil {
		filter := bson.M{
			"_id":           *reservation.UserOrder,
			"paymentStatus": "unpaid",
			"orderStatus":   bson.M{"$ne": "cancelled"},
		}
		var order models.Order
		if err := h.DB.Collection("orders").FindOne(r.Context(), filter).Decode(&order); err == nil {
			existingOrder = &order
		}
	}

	if existingOrder != nil {
		update := bson.M{
			"$push": bson.M{"items": bson.M{"$each": req.Menu}},
			"$inc":  bson.M{"totalAmount": newItemsTotal},
		}
		_, err = h.DB.Collection("orders").UpdateByID(r.Context(), existingOrder.ID, update)
		if err != nil {
			util.JsonError(w, http.StatusInternalServerError, "Failed to update order")
			return
		}
		util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Order updated successfully"})
		return
	}

	newOrder := models.Order{
		ID:             bson.NewObjectID(),
		RestaurantName: req.RestaurantName,
		UserID:         userID,
		RestaurantID:   restID,
		ReservationID:  resID,
		TableNumber:    req.TableNumber,
		Items:          req.Menu,
		TotalAmount:    newItemsTotal,
		OrderStatus:    "placed",
		PaymentStatus:  "unpaid",
	}

	_, err = h.DB.Collection("orders").InsertOne(r.Context(), newOrder)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}

	updateRes := bson.M{"$set": bson.M{"userOrder": newOrder.ID, "status": "active"}}
	h.DB.Collection("reservations").UpdateByID(r.Context(), reservation.ID, updateRes)

	util.JsonResponse(w, http.StatusCreated, map[string]interface{}{
		"message": "Order created successfully",
		"order":   newOrder,
	})
}

type AggUserOrder struct {
	ID            bson.ObjectID        `bson:"_id"`
	OrderID       string               `bson:"orderId"`
	TableNumber   string               `bson:"tableNumber"`
	Items         []models.MenuItem    `bson:"items"`
	OrderStatus   string               `bson:"orderStatus"`
	PaymentStatus string               `bson:"paymentStatus"`
	TotalAmount   float64              `bson:"totalAmount"`
	CreatedAt     time.Time            `bson:"createdAt"`
	Restaurant    []models.Restaurant  `bson:"restaurant"`
	Reservation   []models.Reservation `bson:"reservation"`
}

func (h *OrderHandler) GetUserOrders(w http.ResponseWriter, r *http.Request) {
	userIDStr := r.PathValue("userId")
	if userIDStr == "" {
		util.JsonError(w, http.StatusBadRequest, "User ID is required")
		return
	}

	userID, err := bson.ObjectIDFromHex(userIDStr)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid User ID format")
		return
	}

	pipeline := mongo.Pipeline{
		bson.D{{"$match", bson.M{"userId": userID}}},
		bson.D{{"$sort", bson.M{"createdAt": 1}}},
		bson.D{{"$lookup", bson.M{
			"from":         "restaurants",
			"localField":   "restaurantId",
			"foreignField": "_id",
			"as":           "restaurant",
		}}},
		bson.D{{"$lookup", bson.M{
			"from":         "reservations",
			"localField":   "reservationId",
			"foreignField": "_id",
			"as":           "reservation",
		}}},
	}

	cursor, err := h.DB.Collection("orders").Aggregate(r.Context(), pipeline)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	defer cursor.Close(r.Context())

	var aggOrders []AggUserOrder
	if err := cursor.All(r.Context(), &aggOrders); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error decoding orders")
		return
	}

	if len(aggOrders) == 0 {
		util.JsonResponse(w, http.StatusOK, []interface{}{})
		return
	}

	var formattedOrders []map[string]interface{}
	for _, order := range aggOrders {
		reservationID := "N/A"
		restaurantName := "Unknown Restaurant"
		var restaurantImage interface{} = nil
		var restaurantLocation interface{} = nil
		var restaurantID interface{} = nil

		if len(order.Reservation) > 0 {
			reservationID = order.Reservation[0].ReservationInfo.ReservationID
		}

		if len(order.Restaurant) > 0 {
			rest := order.Restaurant[0]
			restaurantID = rest.ID

			if len(rest.Data) > 0 {
				restaurantName = rest.Data[0].RestaurantName
				restaurantImage = rest.Data[0].Image
				restaurantLocation = rest.Data[0].Location
			} else if rest.Title != "" {
				restaurantName = rest.Title
			}
		}

		items := order.Items
		if items == nil {
			items = []models.MenuItem{}
		}
		status := order.OrderStatus
		if status == "" {
			status = "placed"
		}
		paid := order.PaymentStatus
		if paid == "" {
			paid = "unpaid"
		}

		formattedOrders = append(formattedOrders, map[string]interface{}{
			"_id":                order.ID.Hex(),
			"orderId":            order.OrderID,
			"reservationId":      reservationID,
			"tableNumber":        order.TableNumber,
			"menu":               items,
			"status":             status,
			"paid":               paid,
			"totalAmount":        order.TotalAmount,
			"createdAt":          order.CreatedAt,
			"restaurantName":     restaurantName,
			"restaurantImage":    restaurantImage,
			"restaurantLocation": restaurantLocation,
			"restaurantId":       restaurantID,
		})
	}

	util.JsonResponse(w, http.StatusOK, formattedOrders)
}

func (h *OrderHandler) GetWaiterOrders(w http.ResponseWriter, r *http.Request) {
	restaurantIDStr := r.PathValue("restaurantId")
	restID, err := bson.ObjectIDFromHex(restaurantIDStr)
	if err != nil || restaurantIDStr == "undefined" || restaurantIDStr == "null" {
		util.JsonError(w, http.StatusBadRequest, "Valid Restaurant ID is required")
		return
	}

	tab := r.URL.Query().Get("tab")

	statusMap := map[string]string{
		"not-taken": "placed",
		"served":    "served",
		"payment":   "ready_to_pay",
		"completed": "completed",
	}

	filter := bson.M{"restaurantId": restID}

	if mappedStatus, exists := statusMap[tab]; exists {
		filter["orderStatus"] = mappedStatus
	} else {
		filter["orderStatus"] = bson.M{"$ne": "cancelled"}
	}

	now := time.Now().UTC()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	endOfDay := startOfDay.Add(24 * time.Hour).Add(-time.Nanosecond)

	filter["createdAt"] = bson.M{
		"$gte": startOfDay,
		"$lte": endOfDay,
	}

	var orders []models.Order
	cursor, err := h.DB.Collection("orders").Find(r.Context(), filter)
	if err == nil {
		err = cursor.All(r.Context(), &orders)
	}

	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Server Error fetching orders")
		return
	}

	var formattedData []map[string]interface{}
	for _, o := range orders {
		formattedData = append(formattedData, map[string]interface{}{
			"_id":           o.ID.Hex(),
			"tableNumber":   o.TableNumber,
			"status":        o.OrderStatus,
			"items":         o.Items,
			"totalAmount":   o.TotalAmount,
			"userId":        o.UserID.Hex(),
			"paymentMethod": o.PaymentMethod,
			"paymentStatus": o.PaymentStatus,
		})
	}

	if formattedData == nil {
		formattedData = []map[string]interface{}{}
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"count":   len(formattedData),
		"data":    formattedData,
	})
}

func (h *OrderHandler) UpdateOrderStatus(w http.ResponseWriter, r *http.Request) {
	orderIDStr := r.PathValue("orderId")
	orderID, err := bson.ObjectIDFromHex(orderIDStr)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var reqBody struct {
		OrderStatus string `json:"orderStatus"`
		ServedBy    string `json:"servedBy"` // Waiter ID
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	defer r.Body.Close()

	waiterID, _ := bson.ObjectIDFromHex(reqBody.ServedBy)

	filter := bson.M{"_id": orderID}

	if reqBody.OrderStatus == "served" {
		filter["$or"] = []bson.M{
			{"servedBy": nil},
			{"servedBy": bson.M{"$exists": false}},
		}
	}

	update := bson.M{"$set": bson.M{
		"orderStatus": reqBody.OrderStatus,
		"servedBy":    waiterID,
	}}

	res, err := h.DB.Collection("orders").UpdateOne(r.Context(), filter, update)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	if res.MatchedCount == 0 {
		var checkOrder models.Order
		err := h.DB.Collection("orders").FindOne(r.Context(), bson.M{"_id": orderID}).Decode(&checkOrder)
		if err == nil && checkOrder.ServedBy != nil {
			util.JsonResponse(w, http.StatusConflict, map[string]interface{}{
				"success": false,
				"message": "Too late! Another waiter has already claimed this order.",
			})
			return
		}

		util.JsonError(w, http.StatusNotFound, "Order not found")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Order status updated",
	})
}

func (h *OrderHandler) WaiterCompleteOrder(w http.ResponseWriter, r *http.Request) {
	orderIDStr := r.PathValue("orderId")
	orderID, err := bson.ObjectIDFromHex(orderIDStr)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var reqBody struct {
		Status        string `json:"status"`
		PaymentStatus string `json:"paymentStatus"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	defer r.Body.Close()

	update := bson.M{"$set": bson.M{
		"orderStatus":   reqBody.Status,
		"paymentStatus": reqBody.PaymentStatus,
	}}

	var updatedOrder models.Order
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	err = h.DB.Collection("orders").FindOneAndUpdate(r.Context(), bson.M{"_id": orderID}, update, opts).Decode(&updatedOrder)

	if err != nil {
		if err == mongo.ErrNoDocuments {
			util.JsonError(w, http.StatusNotFound, "Order not found")
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Update the attached reservation to completed
	h.DB.Collection("reservations").UpdateByID(r.Context(), updatedOrder.ReservationID, bson.M{
		"$set": bson.M{"status": "completed"},
	})

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Table closed successfully.",
	})
}

type AggAdminOrder struct {
	models.Order `bson:",inline"`
	User         []models.User   `bson:"user"`
	Waiter       []models.Waiter `bson:"waiter"`
}

func (h *OrderHandler) FetchAllOrder(w http.ResponseWriter, r *http.Request) {
	restaurantID, ok := r.Context().Value(middleware.RestaurantIDKey).(bson.ObjectID)
	if !ok {
		util.JsonError(w, http.StatusBadRequest, "Restaurant ID is required")
		return
	}
	pipeline := mongo.Pipeline{
		bson.D{{"$match", bson.M{"restaurantId": restaurantID}}},
		bson.D{{"$sort", bson.M{"createdAt": 1}}},
		bson.D{{"$limit", 500}},
		bson.D{{"$lookup", bson.M{
			"from":         "users",
			"localField":   "userId",
			"foreignField": "_id",
			"as":           "user",
		}}},
		bson.D{{"$lookup", bson.M{
			"from":         "waiters",
			"localField":   "servedBy",
			"foreignField": "_id",
			"as":           "waiter",
		}}},
	}

	cursor, err := h.DB.Collection("orders").Aggregate(r.Context(), pipeline)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "An error occurred while retrieving orders")
		return
	}
	defer cursor.Close(r.Context())

	var aggOrders []AggAdminOrder
	if err := cursor.All(r.Context(), &aggOrders); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error decoding orders")
		return
	}

	if len(aggOrders) == 0 {
		util.JsonResponse(w, http.StatusOK, map[string]interface{}{
			"message": "No orders found",
			"orders":  []interface{}{},
		})
		return
	}

	var formattedOrders []map[string]interface{}

	for _, order := range aggOrders {
		var userObj interface{} = nil
		if len(order.User) > 0 {
			u := order.User[0]
			userObj = map[string]interface{}{
				"_id":         u.ID.Hex(),
				"name":        u.Name,
				"phoneNumber": u.PhoneNumber,
			}
		}

		var waiterObj interface{} = nil
		if len(order.Waiter) > 0 {
			w := order.Waiter[0]
			waiterObj = map[string]interface{}{
				"_id":       w.ID.Hex(),
				"firstname": w.FirstName,
			}
		}

		formattedOrders = append(formattedOrders, map[string]interface{}{
			"_id":           order.ID.Hex(),
			"tableNumber":   order.TableNumber,
			"items":         order.Items,
			"totalAmount":   order.TotalAmount,
			"orderStatus":   order.OrderStatus,
			"paymentStatus": order.PaymentStatus,
			"paymentMethod": order.PaymentMethod,
			"userId":        userObj,
			"servedBy":      waiterObj,
			"createdAt":     order.CreatedAt,
		})
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message": "Successfully fetched orders",
		"orders":  formattedOrders,
	})
}
