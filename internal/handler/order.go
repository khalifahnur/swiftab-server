package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"swiftab/server/internal/events"
	"swiftab/server/internal/middleware"
	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type OrderHandler struct {
	*Base
}

const (
	// openOrderWindow bounds how far back an unfinished order is still
	// treated as live. It spans midnight so a late table is not dropped at
	// the day boundary.
	openOrderWindow = 36 * time.Hour
	maxItemQuantity = 50
	maxOrderLines   = 60
)

var paymentLabels = map[string]string{
	"mpesa":        "M-Pesa",
	"cash":         "Cash",
	"card":         "Card",
	"airtel_money": "Airtel Money",
}

func paymentLabel(method string) string {
	if label, ok := paymentLabels[method]; ok {
		return label
	}
	return method
}

// menuIndex maps every dish on a restaurant's menu by id.
func menuIndex(data models.RestaurantData) map[bson.ObjectID]models.MenuItem {
	index := map[bson.ObjectID]models.MenuItem{}
	for _, items := range data.Menu {
		for _, item := range items {
			index[item.ID] = item
		}
	}
	return index
}

// priceItems turns the lines a diner sent into order lines priced from the
// restaurant's own menu. The client's name and price are ignored so a
// tampered request cannot change what a dish costs.
func priceItems(requested []models.MenuItem, menu map[bson.ObjectID]models.MenuItem) ([]models.MenuItem, float64, error) {
	if len(requested) == 0 {
		return nil, 0, errors.New("Order must contain at least one menu item")
	}
	if len(requested) > maxOrderLines {
		return nil, 0, errors.New("This order has too many lines")
	}

	lines := make([]models.MenuItem, 0, len(requested))
	position := map[bson.ObjectID]int{}
	total := 0.0

	for _, req := range requested {
		dish, ok := menu[req.ID]
		if !ok {
			name := strings.TrimSpace(req.Name)
			if name == "" {
				name = "An item"
			}
			return nil, 0, fmt.Errorf("%s is no longer on the menu", name)
		}
		if !dish.IsAvailable() {
			return nil, 0, fmt.Errorf("%s is currently unavailable", dish.Name)
		}
		qty := req.Quantity
		if qty < 1 {
			qty = 1
		}
		if qty > maxItemQuantity {
			return nil, 0, fmt.Errorf("You can order at most %d of %s at a time", maxItemQuantity, dish.Name)
		}

		note := strings.TrimSpace(req.Note)
		if len(note) > 200 {
			note = note[:200]
		}

		total += dish.Cost * float64(qty)

		// The same dish listed twice becomes one line, unless a kitchen note
		// makes the lines different.
		if at, seen := position[dish.ID]; seen && lines[at].Note == note {
			lines[at].Quantity += qty
			continue
		}
		position[dish.ID] = len(lines)
		lines = append(lines, models.MenuItem{
			ID:          dish.ID,
			Name:        dish.Name,
			Description: dish.Description,
			Image:       dish.Image,
			Cost:        dish.Cost,
			Quantity:    qty,
			Note:        note,
		})
	}
	return lines, total, nil
}

// mergeItems adds new lines to an order's existing lines, combining a dish
// that is already on the order for the same person with the same note.
func mergeItems(existing, added []models.MenuItem) []models.MenuItem {
	out := append([]models.MenuItem{}, existing...)
	for _, line := range added {
		merged := false
		for i := range out {
			if out[i].ID == line.ID && out[i].Note == line.Note && out[i].AddedByID == line.AddedByID {
				out[i].Quantity += line.Quantity
				merged = true
				break
			}
		}
		if !merged {
			out = append(out, line)
		}
	}
	return out
}

func orderTotal(items []models.MenuItem) float64 {
	total := 0.0
	for _, item := range items {
		qty := item.Quantity
		if qty < 1 {
			qty = 1
		}
		total += item.Cost * float64(qty)
	}
	return total
}

// CreateOrder places a diner's order against their reservation. A second
// order on the same unpaid reservation is added to the open bill.
func (h *OrderHandler) CreateOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Menu           []models.MenuItem `json:"menu"`
		UserID         string            `json:"userId"`
		RestaurantID   string            `json:"restaurantId"`
		ReservationID  string            `json:"reservationId"`
		TableNumber    string            `json:"tableNumber"`
		RestaurantName string            `json:"restaurantName"`
	}

	if err := util.DecodeJSON(w, r, &req); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.UserID == "" || req.RestaurantID == "" || req.ReservationID == "" {
		util.JsonError(w, http.StatusBadRequest, "Missing required identity fields")
		return
	}

	restID, err2 := bson.ObjectIDFromHex(req.RestaurantID)
	userID, err3 := bson.ObjectIDFromHex(req.UserID)
	if err2 != nil || err3 != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid ID formats")
		return
	}
	if !ownsUser(r, userID) {
		util.JsonError(w, http.StatusForbidden, "You can only order for your own account")
		return
	}

	var reservation models.Reservation
	err := h.DB.Collection("reservations").FindOne(r.Context(), bson.M{
		"reservationInfo.reservationID": req.ReservationID,
		"restaurantId":                  restID,
		"userId":                        userID,
	}).Decode(&reservation)
	if err != nil {
		util.JsonError(w, http.StatusNotFound, "Reservation not found")
		return
	}
	if reservation.Status != models.ReservationActive && reservation.Status != models.ReservationSeated {
		util.JsonError(w, http.StatusConflict, "This reservation is no longer active")
		return
	}

	h.placeOrder(w, r, &reservation, req.Menu, req.TableNumber, nil)
}

// placeOrder prices the requested dishes from the restaurant's own menu and
// adds them to the booking's open order, or starts a new one. It is shared by
// the diner app and by guests who checked in with a table's QR code.
// fallbackTable is used only for old bookings that have no table recorded.
//
// friend is the guest who is ordering on a host's shared tab, or nil when the
// host orders. Their lines are marked with their name so everyone can see
// who ordered what, and the host is told.
func (h *OrderHandler) placeOrder(w http.ResponseWriter, r *http.Request, reservation *models.Reservation, menu []models.MenuItem, fallbackTable string, friend *models.TabGuest) {
	restID := reservation.RestaurantID
	restaurant, err := h.loadRestaurant(r.Context(), restID)
	if err != nil || len(restaurant.Data) == 0 {
		util.JsonError(w, http.StatusNotFound, "Restaurant not found")
		return
	}

	lines, linesTotal, err := priceItems(menu, menuIndex(restaurant.Data[0]))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if friend != nil {
		for i := range lines {
			lines[i].AddedBy = friend.Name
			lines[i].AddedByID = friend.ID
		}
	}

	// The table and restaurant name come from the booking, not the request.
	tableNumber := reservation.ReservationInfo.TableNumber
	if tableNumber == "" {
		tableNumber = strings.TrimSpace(fallbackTable)
	}
	if tableNumber == "" {
		util.JsonError(w, http.StatusBadRequest, "This reservation has no table assigned")
		return
	}
	restaurantName := restaurant.Data[0].RestaurantName

	now := time.Now().UTC()
	orders := h.DB.Collection("orders")

	if reservation.UserOrder != nil {
		var open models.Order
		err := orders.FindOne(r.Context(), bson.M{
			"_id":           *reservation.UserOrder,
			"paymentStatus": "unpaid",
			"orderStatus":   bson.M{"$in": bson.A{"placed", "served", "ready_to_pay"}},
		}).Decode(&open)
		if err == nil {
			items := mergeItems(open.Items, lines)
			// New items need serving again, so the order goes back to the
			// waiters' queue.
			_, err = orders.UpdateByID(r.Context(), open.ID, bson.M{"$set": bson.M{
				"items":       items,
				"totalAmount": orderTotal(items),
				"orderStatus": models.OrderPlaced,
				"updatedAt":   now,
			}})
			if err != nil {
				util.JsonError(w, http.StatusInternalServerError, "Failed to update order")
				return
			}

			h.publish(restID, events.Event{Type: "order", Action: "updated", ID: open.ID.Hex(), Table: tableNumber, Status: string(models.OrderPlaced)})
			h.notifyWaiters(restID, util.PushMessage{
				Title:   "More items for table " + tableNumber,
				Body:    fmt.Sprintf("%d item(s) added to the order.", len(lines)),
				Data:    map[string]string{"type": "order", "orderId": open.ID.Hex()},
				Channel: "orders",
			})

			if friend != nil {
				go h.tellHost(*reservation, *friend, lines, linesTotal)
			}
			open.Items = items
			open.TotalAmount = orderTotal(items)
			open.OrderStatus = models.OrderPlaced
			open.UpdatedAt = now
			util.JsonResponse(w, http.StatusOK, map[string]interface{}{
				"message": "Order updated successfully",
				"order":   open,
			})
			return
		}
	}

	newOrder := models.Order{
		ID:             bson.NewObjectID(),
		OrderNumber:    util.GenerateOrderID(restID.Hex()),
		RestaurantName: restaurantName,
		UserID:         reservation.UserID,
		RestaurantID:   restID,
		ReservationID:  reservation.ReservationInfo.ReservationID,
		TableNumber:    tableNumber,
		Guests:         reservation.ReservationInfo.Guests,
		Items:          lines,
		TotalAmount:    linesTotal,
		OrderStatus:    models.OrderPlaced,
		PaymentStatus:  models.PaymentUnpaid,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if _, err = orders.InsertOne(r.Context(), newOrder); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}

	// Ordering means the party has arrived: mark the booking seated.
	h.DB.Collection("reservations").UpdateByID(r.Context(), reservation.ID, bson.M{"$set": bson.M{
		"userOrder": newOrder.ID,
		"status":    models.ReservationSeated,
		"updatedAt": now,
	}})

	h.publish(restID, events.Event{Type: "order", Action: "created", ID: newOrder.ID.Hex(), Table: tableNumber, Status: string(models.OrderPlaced)})
	h.notifyWaiters(restID, util.PushMessage{
		Title:   "New order · Table " + tableNumber,
		Body:    fmt.Sprintf("%d item(s) · KES %s", len(lines), strconv.FormatFloat(linesTotal, 'f', 0, 64)),
		Data:    map[string]string{"type": "order", "orderId": newOrder.ID.Hex()},
		Channel: "orders",
	})

	if friend != nil {
		go h.tellHost(*reservation, *friend, lines, linesTotal)
	}
	util.JsonResponse(w, http.StatusCreated, map[string]interface{}{
		"message": "Order created successfully",
		"order":   newOrder,
	})
}

// loadUserOrder fetches an order and checks the caller may act on it as its diner.
func (h *OrderHandler) loadUserOrder(w http.ResponseWriter, r *http.Request) *models.Order {
	orderID, err := bson.ObjectIDFromHex(r.PathValue("orderId"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid order ID")
		return nil
	}
	var order models.Order
	if err := h.DB.Collection("orders").FindOne(r.Context(), bson.M{"_id": orderID}).Decode(&order); err != nil {
		util.JsonError(w, http.StatusNotFound, "Order not found")
		return nil
	}
	if !ownsUser(r, order.UserID) {
		util.JsonError(w, http.StatusForbidden, "This is not your order")
		return nil
	}
	return &order
}

// UserCompleteOrder is the diner asking for the bill and saying how they
// will pay. The waiter confirms the payment afterwards.
func (h *OrderHandler) UserCompleteOrder(w http.ResponseWriter, r *http.Request) {
	if order := h.loadUserOrder(w, r); order != nil {
		h.requestBill(w, r, order)
	}
}

// requestBill moves an order the caller may act on to "ready to pay" with the
// payment method they chose, and tells the waiters.
func (h *OrderHandler) requestBill(w http.ResponseWriter, r *http.Request, order *models.Order) {
	var reqBody struct {
		Status        string `json:"status"`
		PaymentMethod string `json:"paymentMethod"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	method := util.NormalizePaymentMethod(reqBody.PaymentMethod)
	if method == "" {
		util.JsonError(w, http.StatusBadRequest, "Choose how you would like to pay")
		return
	}

	switch order.OrderStatus {
	case models.OrderCompleted:
		util.JsonError(w, http.StatusConflict, "This order has already been paid")
		return
	case models.OrderCancelled:
		util.JsonError(w, http.StatusConflict, "This order was cancelled")
		return
	}

	now := time.Now().UTC()
	_, err := h.DB.Collection("orders").UpdateByID(r.Context(), order.ID, bson.M{"$set": bson.M{
		"orderStatus":   models.OrderReadyToPay,
		"paymentMethod": method,
		"updatedAt":     now,
	}})
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not request the bill")
		return
	}

	h.publish(order.RestaurantID, events.Event{Type: "order", Action: "status", ID: order.ID.Hex(), Table: order.TableNumber, Status: string(models.OrderReadyToPay)})
	h.notifyWaiters(order.RestaurantID, util.PushMessage{
		Title:   "Bill requested · Table " + order.TableNumber,
		Body:    "Paying by " + paymentLabel(method) + " · KES " + strconv.FormatFloat(order.TotalAmount, 'f', 0, 64),
		Data:    map[string]string{"type": "order", "orderId": order.ID.Hex()},
		Channel: "orders",
	})

	util.JsonResponse(w, http.StatusOK, map[string]string{
		"message":       "Wait for the assigned waiter to complete payment",
		"paymentMethod": method,
	})
}

// UserCancelOrder lets a diner cancel an order nobody has started serving.
func (h *OrderHandler) UserCancelOrder(w http.ResponseWriter, r *http.Request) {
	if order := h.loadUserOrder(w, r); order != nil {
		h.cancelPlacedOrder(w, r, order)
	}
}

// cancelPlacedOrder cancels an order the caller may act on, as long as
// nobody has started serving it.
func (h *OrderHandler) cancelPlacedOrder(w http.ResponseWriter, r *http.Request, order *models.Order) {
	if order.OrderStatus != models.OrderPlaced {
		util.JsonError(w, http.StatusConflict, "This order is already being served. Ask your waiter for help.")
		return
	}

	now := time.Now().UTC()
	res, err := h.DB.Collection("orders").UpdateOne(r.Context(),
		bson.M{"_id": order.ID, "orderStatus": models.OrderPlaced},
		bson.M{"$set": bson.M{"orderStatus": models.OrderCancelled, "updatedAt": now}},
	)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not cancel the order")
		return
	}
	if res.MatchedCount == 0 {
		util.JsonError(w, http.StatusConflict, "This order is already being served. Ask your waiter for help.")
		return
	}

	h.publish(order.RestaurantID, events.Event{Type: "order", Action: "status", ID: order.ID.Hex(), Table: order.TableNumber, Status: string(models.OrderCancelled)})
	util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Order cancelled"})
}

type aggUserOrder struct {
	models.Order `bson:",inline"`
	Restaurant   []models.Restaurant `bson:"restaurant"`
	Waiter       []models.Waiter     `bson:"waiter"`
}

// paidLabel is the "Paid"/"Unpaid" string older clients read.
func paidLabel(status models.PaymentStatus) string {
	if status == models.PaymentPaid {
		return "Paid"
	}
	return "Unpaid"
}

func (h *OrderHandler) GetUserOrders(w http.ResponseWriter, r *http.Request) {
	userID, err := bson.ObjectIDFromHex(r.PathValue("userId"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid User ID format")
		return
	}
	if !ownsUser(r, userID) {
		util.JsonError(w, http.StatusForbidden, "You can only view your own orders")
		return
	}

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"userId": userID}}},
		{{Key: "$sort", Value: bson.M{"createdAt": -1}}},
		{{Key: "$limit", Value: 100}},
		{{Key: "$lookup", Value: bson.M{
			"from":         "restaurants",
			"localField":   "restaurantId",
			"foreignField": "_id",
			"as":           "restaurant",
		}}},
		{{Key: "$lookup", Value: bson.M{
			"from":         "waiters",
			"localField":   "servedBy",
			"foreignField": "_id",
			"as":           "waiter",
		}}},
		// The joined restaurant only supplies a name, photo and address.
		{{Key: "$project", Value: bson.M{"restaurant.data.menu": 0, "restaurant.data.review": 0}}},
	}

	cursor, err := h.DB.Collection("orders").Aggregate(r.Context(), pipeline)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}

	var aggOrders []aggUserOrder
	if err := cursor.All(r.Context(), &aggOrders); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error decoding orders")
		return
	}

	formattedOrders := make([]map[string]interface{}, 0, len(aggOrders))
	for _, order := range aggOrders {
		restaurantName := order.RestaurantName
		var restaurantImage, restaurantLocation interface{}

		if len(order.Restaurant) > 0 {
			rest := order.Restaurant[0]
			if len(rest.Data) > 0 {
				restaurantName = rest.Data[0].RestaurantName
				restaurantImage = rest.Data[0].Image
				restaurantLocation = rest.Data[0].Location
			} else if rest.Title != "" {
				restaurantName = rest.Title
			}
		}
		if restaurantName == "" {
			restaurantName = "Unknown Restaurant"
		}

		items := order.Items
		if items == nil {
			items = []models.MenuItem{}
		}
		status := order.OrderStatus
		if status == "" {
			status = models.OrderPlaced
		}
		paymentStatus := order.PaymentStatus
		if paymentStatus == "" {
			paymentStatus = models.PaymentUnpaid
		}

		waiterName := ""
		if len(order.Waiter) > 0 {
			waiterName = strings.TrimSpace(order.Waiter[0].FirstName + " " + order.Waiter[0].LastName)
		}

		formattedOrders = append(formattedOrders, map[string]interface{}{
			"_id":                order.ID.Hex(),
			"orderId":            order.OrderNumber,
			"reservationId":      order.ReservationID,
			"tableNumber":        order.TableNumber,
			"menu":               items,
			"status":             status,
			"paid":               paidLabel(paymentStatus),
			"paymentStatus":      paymentStatus,
			"paymentMethod":      order.PaymentMethod,
			"totalAmount":        order.TotalAmount,
			"createdAt":          order.CreatedAt,
			"updatedAt":          order.UpdatedAt,
			"servedAt":           order.ServedAt,
			"paidAt":             order.PaidAt,
			"takenBy":            waiterName,
			"restaurantName":     restaurantName,
			"restaurantImage":    restaurantImage,
			"restaurantLocation": restaurantLocation,
			"restaurantId":       order.RestaurantID.Hex(),
		})
	}

	util.JsonResponse(w, http.StatusOK, formattedOrders)
}

type aggFloorOrder struct {
	models.Order `bson:",inline"`
	User         []models.User        `bson:"user"`
	Waiter       []models.Waiter      `bson:"waiter"`
	Reservation  []models.Reservation `bson:"reservation"`
}

// guests returns the party size, falling back to the reservation for orders
// placed before the size was copied onto the order.
func (o aggFloorOrder) guests() int {
	if o.Guests > 0 {
		return o.Guests
	}
	if len(o.Reservation) > 0 {
		return o.Reservation[0].ReservationInfo.Guests
	}
	return 0
}

func (o aggFloorOrder) customer() interface{} {
	if len(o.User) > 0 {
		u := o.User[0]
		return map[string]interface{}{
			"_id":         u.ID.Hex(),
			"name":        u.Name,
			"phoneNumber": u.PhoneNumber,
		}
	}
	// A booking made by the restaurant has a guest name but no account.
	if len(o.Reservation) > 0 && o.Reservation[0].ReservationInfo.Name != "" {
		info := o.Reservation[0].ReservationInfo
		return map[string]interface{}{
			"_id":         "",
			"name":        info.Name,
			"phoneNumber": info.PhoneNumber,
		}
	}
	return nil
}

func (o aggFloorOrder) waiter() interface{} {
	if len(o.Waiter) > 0 {
		wt := o.Waiter[0]
		return map[string]interface{}{
			"_id":       wt.ID.Hex(),
			"firstname": wt.FirstName,
			"lastname":  wt.LastName,
		}
	}
	return nil
}

// floorOrderLookups joins the diner, the waiter and the reservation onto an order.
func floorOrderLookups() []bson.D {
	return []bson.D{
		{{Key: "$lookup", Value: bson.M{
			"from":         "users",
			"localField":   "userId",
			"foreignField": "_id",
			"as":           "user",
		}}},
		{{Key: "$lookup", Value: bson.M{
			"from":         "waiters",
			"localField":   "servedBy",
			"foreignField": "_id",
			"as":           "waiter",
		}}},
		{{Key: "$lookup", Value: bson.M{
			"from":         "reservations",
			"localField":   "reservationId",
			"foreignField": "reservationInfo.reservationID",
			"as":           "reservation",
		}}},
	}
}

// GetWaiterOrders lists a restaurant's orders for one tab of the waiter app.
// Unfinished orders stay visible across midnight; the completed tab shows
// the current business day.
func (h *OrderHandler) GetWaiterOrders(w http.ResponseWriter, r *http.Request) {
	restaurantIDStr := r.PathValue("restaurantId")
	restID, err := bson.ObjectIDFromHex(restaurantIDStr)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Valid Restaurant ID is required")
		return
	}
	if !waiterMayAccess(r, restID) {
		util.JsonError(w, http.StatusForbidden, "You do not work at this restaurant")
		return
	}

	statusMap := map[string]models.OrderStatus{
		"not-taken": models.OrderPlaced,
		"served":    models.OrderServed,
		"payment":   models.OrderReadyToPay,
		"completed": models.OrderCompleted,
	}

	now := time.Now().UTC()
	filter := bson.M{"restaurantId": restID}
	sort := bson.M{"createdAt": 1} // oldest first: serve in the order received

	status, known := statusMap[r.URL.Query().Get("tab")]
	switch {
	case known && status == models.OrderCompleted:
		filter["orderStatus"] = status
		filter["updatedAt"] = bson.M{"$gte": h.startOfDay(now)}
		sort = bson.M{"updatedAt": -1}
	case known:
		filter["orderStatus"] = status
		filter["createdAt"] = bson.M{"$gte": now.Add(-openOrderWindow)}
	default:
		filter["orderStatus"] = bson.M{"$ne": models.OrderCancelled}
		filter["createdAt"] = bson.M{"$gte": now.Add(-openOrderWindow)}
	}

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: filter}},
		{{Key: "$sort", Value: sort}},
		{{Key: "$limit", Value: 200}},
	}
	pipeline = append(pipeline, floorOrderLookups()...)

	cursor, err := h.DB.Collection("orders").Aggregate(r.Context(), pipeline)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Server Error fetching orders")
		return
	}
	var orders []aggFloorOrder
	if err := cursor.All(r.Context(), &orders); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Server Error fetching orders")
		return
	}

	formattedData := make([]map[string]interface{}, 0, len(orders))
	for _, o := range orders {
		items := o.Items
		if items == nil {
			items = []models.MenuItem{}
		}
		servedBy := ""
		if o.ServedBy != nil {
			servedBy = o.ServedBy.Hex()
		}
		formattedData = append(formattedData, map[string]interface{}{
			"_id":           o.ID.Hex(),
			"orderId":       o.OrderNumber,
			"tableNumber":   o.TableNumber,
			"status":        o.OrderStatus,
			"items":         items,
			"totalAmount":   o.TotalAmount,
			"userId":        o.customer(),
			"guests":        o.guests(),
			"time":          o.CreatedAt,
			"updatedAt":     o.UpdatedAt,
			"paymentMethod": o.PaymentMethod,
			"paymentStatus": o.PaymentStatus,
			"servedBy":      servedBy,
			"waiter":        o.waiter(),
		})
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"count":   len(formattedData),
		"data":    formattedData,
	})
}

// UpdateOrderStatus is a waiter claiming and serving an order.
func (h *OrderHandler) UpdateOrderStatus(w http.ResponseWriter, r *http.Request) {
	orderID, err := bson.ObjectIDFromHex(r.PathValue("orderId"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var reqBody struct {
		OrderStatus string `json:"orderStatus"`
		ServedBy    string `json:"servedBy"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	newStatus := models.OrderStatus(reqBody.OrderStatus)
	if newStatus != models.OrderServed && newStatus != models.OrderCancelled {
		util.JsonError(w, http.StatusBadRequest, "Unsupported order status")
		return
	}

	// A signed-in waiter always acts as themselves.
	waiterID, authed := middleware.AuthedWaiter(r)
	if !authed {
		waiterID, err = bson.ObjectIDFromHex(reqBody.ServedBy)
		if err != nil {
			util.JsonError(w, http.StatusBadRequest, "Could not identify the waiter. Please sign in again.")
			return
		}
	}

	var order models.Order
	if err := h.DB.Collection("orders").FindOne(r.Context(), bson.M{"_id": orderID}).Decode(&order); err != nil {
		util.JsonError(w, http.StatusNotFound, "Order not found")
		return
	}
	if !waiterMayAccess(r, order.RestaurantID) {
		util.JsonError(w, http.StatusForbidden, "You do not work at this restaurant")
		return
	}
	if !order.OrderStatus.IsOpen() {
		util.JsonError(w, http.StatusConflict, "This order is already closed")
		return
	}

	now := time.Now().UTC()
	filter := bson.M{"_id": orderID, "orderStatus": bson.M{"$in": bson.A{"placed", "served", "ready_to_pay"}}}
	set := bson.M{"orderStatus": newStatus, "updatedAt": now}

	if newStatus == models.OrderServed {
		// Only one waiter can claim an order. The waiter who already has it
		// can serve it again when the table adds items.
		filter["$or"] = bson.A{
			bson.M{"servedBy": nil},
			bson.M{"servedBy": waiterID},
		}
		set["servedBy"] = waiterID
		set["servedAt"] = now
	}

	res, err := h.DB.Collection("orders").UpdateOne(r.Context(), filter, bson.M{"$set": set})
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if res.MatchedCount == 0 {
		util.JsonResponse(w, http.StatusConflict, map[string]interface{}{
			"success": false,
			"message": "Too late! Another waiter has already claimed this order.",
			"error":   "Too late! Another waiter has already claimed this order.",
		})
		return
	}

	h.publish(order.RestaurantID, events.Event{Type: "order", Action: "status", ID: orderID.Hex(), Table: order.TableNumber, Status: string(newStatus)})

	if newStatus == models.OrderServed {
		h.notifyUser(order.UserID, util.PushMessage{
			Title:   "Your order is served",
			Body:    "Enjoy your meal at " + order.RestaurantName + ". Request the bill in the app when you are ready.",
			Data:    map[string]string{"type": "order", "orderId": orderID.Hex(), "status": string(newStatus)},
			Channel: "orders",
		})
	} else {
		h.notifyUser(order.UserID, util.PushMessage{
			Title:   "Order cancelled",
			Body:    "Your order at " + order.RestaurantName + " was cancelled by the restaurant.",
			Data:    map[string]string{"type": "order", "orderId": orderID.Hex(), "status": string(newStatus)},
			Channel: "orders",
		})
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Order status updated",
	})
}

// closeOrder marks an order paid and completed and finishes its reservation.
func (h *OrderHandler) closeOrder(r *http.Request, order *models.Order, method, reference string) error {
	now := time.Now().UTC()
	set := bson.M{
		"orderStatus":   models.OrderCompleted,
		"paymentStatus": models.PaymentPaid,
		"paidAt":        now,
		"updatedAt":     now,
	}
	if method != "" {
		set["paymentMethod"] = method
		order.PaymentMethod = method
	}
	if reference != "" {
		set["paymentReference"] = reference
	}

	if _, err := h.DB.Collection("orders").UpdateByID(r.Context(), order.ID, bson.M{"$set": set}); err != nil {
		return err
	}

	// The order stores the booking's public code, not its document id.
	if order.ReservationID != "" {
		h.DB.Collection("reservations").UpdateOne(r.Context(), bson.M{
			"restaurantId":                  order.RestaurantID,
			"reservationInfo.reservationID": order.ReservationID,
			"status":                        bson.M{"$in": bson.A{"active", "seated"}},
		}, bson.M{"$set": bson.M{"status": models.ReservationCompleted, "updatedAt": now}})
	}

	h.publish(order.RestaurantID, events.Event{Type: "order", Action: "status", ID: order.ID.Hex(), Table: order.TableNumber, Status: string(models.OrderCompleted)})
	h.notifyUser(order.UserID, util.PushMessage{
		Title:   "Payment received",
		Body:    "Thanks for dining at " + order.RestaurantName + ". Tap to rate your visit.",
		Data:    map[string]string{"type": "order", "orderId": order.ID.Hex(), "status": string(models.OrderCompleted)},
		Channel: "orders",
	})
	return nil
}

// WaiterCompleteOrder is the waiter confirming payment and closing the table.
func (h *OrderHandler) WaiterCompleteOrder(w http.ResponseWriter, r *http.Request) {
	orderID, err := bson.ObjectIDFromHex(r.PathValue("orderId"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid order ID")
		return
	}

	// status and paymentStatus are accepted for older app builds but the
	// outcome is always "completed and paid".
	var reqBody struct {
		Status           string `json:"status"`
		PaymentStatus    string `json:"paymentStatus"`
		PaymentMethod    string `json:"paymentMethod"`
		PaymentReference string `json:"paymentReference"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	var order models.Order
	if err := h.DB.Collection("orders").FindOne(r.Context(), bson.M{"_id": orderID}).Decode(&order); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			util.JsonError(w, http.StatusNotFound, "Order not found")
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !waiterMayAccess(r, order.RestaurantID) {
		util.JsonError(w, http.StatusForbidden, "You do not work at this restaurant")
		return
	}
	switch order.OrderStatus {
	case models.OrderCompleted:
		util.JsonError(w, http.StatusConflict, "This table is already closed")
		return
	case models.OrderCancelled:
		util.JsonError(w, http.StatusConflict, "This order was cancelled")
		return
	}

	method := util.NormalizePaymentMethod(reqBody.PaymentMethod)
	if method == "" && order.PaymentMethod == "" {
		// Nobody said how the bill was settled; cash is what a waiter takes
		// at the table without the app being involved.
		method = "cash"
	}
	reference := strings.TrimSpace(reqBody.PaymentReference)
	if len(reference) > 40 {
		reference = reference[:40]
	}

	if err := h.closeOrder(r, &order, method, reference); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Table closed successfully.",
	})
}

// --- Dashboard -------------------------------------------------------------

// FetchAllOrder lists the restaurant's orders, newest first. Optional query
// parameters: status, from and to (YYYY-MM-DD in the business timezone).
func (h *OrderHandler) FetchAllOrder(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Restaurant ID is required")
		return
	}

	filter := bson.M{"restaurantId": restaurantID}
	q := r.URL.Query()
	if status := q.Get("status"); models.ValidOrderStatus(status) {
		filter["orderStatus"] = status
	}
	created := bson.M{}
	if from, err := time.ParseInLocation("2006-01-02", q.Get("from"), h.location()); err == nil {
		created["$gte"] = from
	}
	if to, err := time.ParseInLocation("2006-01-02", q.Get("to"), h.location()); err == nil {
		created["$lt"] = to.AddDate(0, 0, 1)
	}
	if len(created) > 0 {
		filter["createdAt"] = created
	}

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: filter}},
		{{Key: "$sort", Value: bson.M{"createdAt": -1}}},
		{{Key: "$limit", Value: 500}},
	}
	pipeline = append(pipeline, floorOrderLookups()...)

	cursor, err := h.DB.Collection("orders").Aggregate(r.Context(), pipeline)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "An error occurred while retrieving orders")
		return
	}

	var aggOrders []aggFloorOrder
	if err := cursor.All(r.Context(), &aggOrders); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error decoding orders")
		return
	}

	formattedOrders := make([]map[string]interface{}, 0, len(aggOrders))
	for _, order := range aggOrders {
		items := order.Items
		if items == nil {
			items = []models.MenuItem{}
		}
		paymentStatus := order.PaymentStatus
		if paymentStatus == "" {
			paymentStatus = models.PaymentUnpaid
		}
		formattedOrders = append(formattedOrders, map[string]interface{}{
			"_id":              order.ID.Hex(),
			"orderId":          order.OrderNumber,
			"reservationId":    order.ReservationID,
			"tableNumber":      order.TableNumber,
			"guests":           order.guests(),
			"items":            items,
			"totalAmount":      order.TotalAmount,
			"orderStatus":      order.OrderStatus,
			"paymentStatus":    paymentStatus,
			"paymentMethod":    order.PaymentMethod,
			"paymentReference": order.PaymentReference,
			"paid":             paidLabel(paymentStatus),
			"userId":           order.customer(),
			"servedBy":         order.waiter(),
			"createdAt":        order.CreatedAt,
			"updatedAt":        order.UpdatedAt,
			"servedAt":         order.ServedAt,
			"paidAt":           order.PaidAt,
		})
	}

	message := "Successfully fetched orders"
	if len(formattedOrders) == 0 {
		message = "No orders found"
	}
	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message": message,
		"orders":  formattedOrders,
	})
}

// AdminUpdateOrder lets a manager correct an order: cancel it, record how it
// was paid, or close it when the waiter app was not used.
func (h *OrderHandler) AdminUpdateOrder(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Restaurant ID is required")
		return
	}
	orderID, err := bson.ObjectIDFromHex(r.PathValue("orderId"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var reqBody struct {
		OrderStatus      string `json:"orderStatus"`
		PaymentMethod    string `json:"paymentMethod"`
		PaymentReference string `json:"paymentReference"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	var order models.Order
	if err := h.DB.Collection("orders").FindOne(r.Context(), bson.M{"_id": orderID, "restaurantId": restaurantID}).Decode(&order); err != nil {
		util.JsonError(w, http.StatusNotFound, "Order not found")
		return
	}

	method := util.NormalizePaymentMethod(reqBody.PaymentMethod)
	reference := strings.TrimSpace(reqBody.PaymentReference)
	if len(reference) > 40 {
		reference = reference[:40]
	}

	switch models.OrderStatus(reqBody.OrderStatus) {
	case models.OrderCompleted:
		if order.OrderStatus == models.OrderCancelled {
			util.JsonError(w, http.StatusConflict, "A cancelled order cannot be completed")
			return
		}
		if method == "" && order.PaymentMethod == "" {
			util.JsonError(w, http.StatusBadRequest, "Choose how this order was paid")
			return
		}
		if err := h.closeOrder(r, &order, method, reference); err != nil {
			util.JsonError(w, http.StatusInternalServerError, "Could not close the order")
			return
		}
		util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Order marked as paid"})
		return

	case models.OrderCancelled:
		if order.PaymentStatus == models.PaymentPaid {
			util.JsonError(w, http.StatusConflict, "A paid order cannot be cancelled")
			return
		}
		now := time.Now().UTC()
		if _, err := h.DB.Collection("orders").UpdateByID(r.Context(), orderID, bson.M{"$set": bson.M{
			"orderStatus": models.OrderCancelled,
			"updatedAt":   now,
		}}); err != nil {
			util.JsonError(w, http.StatusInternalServerError, "Could not cancel the order")
			return
		}
		h.publish(restaurantID, events.Event{Type: "order", Action: "status", ID: orderID.Hex(), Table: order.TableNumber, Status: string(models.OrderCancelled)})
		h.notifyUser(order.UserID, util.PushMessage{
			Title:   "Order cancelled",
			Body:    "Your order at " + order.RestaurantName + " was cancelled by the restaurant.",
			Data:    map[string]string{"type": "order", "orderId": orderID.Hex(), "status": string(models.OrderCancelled)},
			Channel: "orders",
		})
		util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Order cancelled"})
		return

	case "":
		// Only the payment details are being corrected.
		set := bson.M{"updatedAt": time.Now().UTC()}
		if method != "" {
			set["paymentMethod"] = method
		}
		if reference != "" {
			set["paymentReference"] = reference
		}
		if len(set) == 1 {
			util.JsonError(w, http.StatusBadRequest, "Nothing to update")
			return
		}
		if _, err := h.DB.Collection("orders").UpdateByID(r.Context(), orderID, bson.M{"$set": set}); err != nil {
			util.JsonError(w, http.StatusInternalServerError, "Could not update the order")
			return
		}
		h.publish(restaurantID, events.Event{Type: "order", Action: "updated", ID: orderID.Hex(), Table: order.TableNumber, Status: string(order.OrderStatus)})
		util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Payment details updated"})
		return
	}

	util.JsonError(w, http.StatusBadRequest, "Unsupported order status")
}
