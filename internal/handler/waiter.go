package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"swiftab/server/internal/middleware"
	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type WaiterHandler struct {
	*Base
}

// passwordWindow is how long after entering a valid invite code a waiter may
// set their password.
const passwordWindow = 15 * time.Minute

func (h *WaiterHandler) findWaiterByEmail(r *http.Request, raw string) (*models.Waiter, error) {
	email, _ := util.NormalizeEmail(raw)
	var waiter models.Waiter
	err := h.DB.Collection("waiters").FindOne(r.Context(), bson.M{
		"email": bson.M{"$in": bson.A{email, strings.TrimSpace(raw)}},
	}).Decode(&waiter)
	if err != nil {
		return nil, err
	}
	return &waiter, nil
}

func inviteCodeValid(waiter *models.Waiter, code string) bool {
	code = strings.TrimSpace(code)
	if code == "" || waiter.ValidationCode == "" || waiter.ValidationCode != code {
		return false
	}
	return waiter.ValidationCodeExpiration == nil || time.Now().Before(*waiter.ValidationCodeExpiration)
}

func (h *WaiterHandler) waiterView(r *http.Request, waiter models.Waiter) map[string]interface{} {
	view := map[string]interface{}{
		"_id":           waiter.ID.Hex(),
		"restaurantId":  waiter.RestaurantID,
		"firstname":     waiter.FirstName,
		"lastname":      waiter.LastName,
		"phoneNumber":   waiter.PhoneNumber,
		"email":         waiter.Email,
		"onDuty":        waiter.OnDuty,
		"dutyStartedAt": waiter.DutyStartedAt,
	}
	if waiter.RestaurantID != nil {
		if restaurant, err := h.loadRestaurant(r.Context(), *waiter.RestaurantID); err == nil && len(restaurant.Data) > 0 {
			view["restaurantName"] = restaurant.Data[0].RestaurantName
			view["restaurantImage"] = restaurant.Data[0].Image
		}
	}
	return view
}

// WaiterAuthentication is step one of sign-up: the waiter proves they hold
// the invite code their manager gave them.
func (h *WaiterHandler) WaiterAuthentication(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		Email          string `json:"email"`
		ValidationCode string `json:"validationcode"`
	}

	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if reqBody.Email == "" || reqBody.ValidationCode == "" {
		util.JsonError(w, http.StatusBadRequest, "Email and Verification Code are required!")
		return
	}

	waiter, err := h.findWaiterByEmail(r, reqBody.Email)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			util.JsonError(w, http.StatusNotFound, "No invite found for this email. Ask your manager to add you.")
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if waiter.IsActivated() {
		util.JsonError(w, http.StatusConflict, "This account is already set up. Sign in instead.")
		return
	}
	if !inviteCodeValid(waiter, reqBody.ValidationCode) {
		util.JsonError(w, http.StatusUnauthorized, "Verification code is incorrect or expired!")
		return
	}

	now := time.Now().UTC()
	h.DB.Collection("waiters").UpdateByID(r.Context(), waiter.ID, bson.M{"$set": bson.M{"validatedAt": now}})

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message":    "Successfully signed up",
		"waiterData": h.waiterView(r, *waiter),
	})
}

// WaiterPassword is step two of sign-up: choosing a password. It only works
// for an account that has just passed the invite-code check (or that sends
// the code again), so an email address alone cannot claim an account.
func (h *WaiterHandler) WaiterPassword(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		Email          string `json:"email"`
		Password       string `json:"password"`
		ValidationCode string `json:"validationcode"`
	}

	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if reqBody.Email == "" || reqBody.Password == "" {
		util.JsonError(w, http.StatusBadRequest, "Email and password are required")
		return
	}
	if len(reqBody.Password) < minPasswordLen {
		util.JsonError(w, http.StatusBadRequest, "Password must be at least 6 characters")
		return
	}

	waiter, err := h.findWaiterByEmail(r, reqBody.Email)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			util.JsonError(w, http.StatusNotFound, "Waiter not found")
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if waiter.IsActivated() {
		util.JsonError(w, http.StatusConflict, "This account already has a password. Sign in instead.")
		return
	}

	recentlyVerified := waiter.ValidatedAt != nil && time.Since(*waiter.ValidatedAt) <= passwordWindow
	if !recentlyVerified && !inviteCodeValid(waiter, reqBody.ValidationCode) {
		util.JsonError(w, http.StatusUnauthorized, "Enter your invite code again to continue")
		return
	}

	hashedPassword, err := util.HashPassword(reqBody.Password)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error hashing password")
		return
	}

	// The password filter makes sure two simultaneous requests cannot both
	// claim the account.
	res, err := h.DB.Collection("waiters").UpdateOne(r.Context(),
		bson.M{"_id": waiter.ID, "password": bson.M{"$in": bson.A{nil, ""}}},
		bson.M{
			"$set": bson.M{"password": hashedPassword, "updatedAt": time.Now().UTC()},
			"$unset": bson.M{
				"validationcode":           "",
				"validationcodeExpiration": "",
				"validatedAt":              "",
			},
		},
	)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if res.MatchedCount == 0 {
		util.JsonError(w, http.StatusConflict, "This account already has a password. Sign in instead.")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{
		"message": "Waiter password created successfully",
	})
}

func (h *WaiterHandler) LoginWaiter(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	waiter, err := h.findWaiterByEmail(r, reqBody.Email)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			util.JsonError(w, http.StatusUnauthorized, "Incorrect Email/Password")
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "Error occurred during login")
		return
	}

	if !waiter.IsActivated() {
		util.JsonError(w, http.StatusUnauthorized, "Finish signing up with your invite code first")
		return
	}
	if !util.VerifyPassword(waiter.Password, reqBody.Password) {
		util.JsonError(w, http.StatusUnauthorized, "Incorrect Email/Password")
		return
	}

	restID := ""
	if waiter.RestaurantID != nil {
		restID = waiter.RestaurantID.Hex()
	}

	tokenString, err := util.GenerateWaiterToken(waiter.ID.Hex(), restID, waiter.Email, mobileSessionTTL)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Failed to generate token")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"token":  tokenString,
		"waiter": h.waiterView(r, *waiter),
	})
}

// pathWaiter parses {waiterId} and checks the caller may act as that waiter.
func (h *WaiterHandler) pathWaiter(w http.ResponseWriter, r *http.Request) (bson.ObjectID, bool) {
	waiterID, err := bson.ObjectIDFromHex(r.PathValue("waiterId"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid Waiter ID")
		return bson.NilObjectID, false
	}
	if authed, ok := middleware.AuthedWaiter(r); ok {
		if authed != waiterID {
			util.JsonError(w, http.StatusForbidden, "You can only manage your own account")
			return bson.NilObjectID, false
		}
	} else if middleware.Role(r) != "" {
		util.JsonError(w, http.StatusForbidden, "You can only manage your own account")
		return bson.NilObjectID, false
	}
	return waiterID, true
}

func (h *WaiterHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	waiterID, ok := h.pathWaiter(w, r)
	if !ok {
		return
	}

	var reqBody struct {
		FirstName   *string `json:"firstname"`
		LastName    *string `json:"lastname"`
		PhoneNumber *string `json:"phoneNumber"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	set := bson.M{}
	if reqBody.FirstName != nil {
		v := strings.TrimSpace(*reqBody.FirstName)
		if v == "" {
			util.JsonError(w, http.StatusBadRequest, "First name is required")
			return
		}
		set["firstname"] = v
	}
	if reqBody.LastName != nil {
		set["lastname"] = strings.TrimSpace(*reqBody.LastName)
	}
	if reqBody.PhoneNumber != nil {
		v := strings.TrimSpace(*reqBody.PhoneNumber)
		if v == "" {
			util.JsonError(w, http.StatusBadRequest, "Phone number is required")
			return
		}
		set["phoneNumber"] = v
	}
	if len(set) == 0 {
		util.JsonError(w, http.StatusBadRequest, "Nothing to update")
		return
	}
	set["updatedAt"] = time.Now().UTC()

	var waiter models.Waiter
	err := h.DB.Collection("waiters").FindOneAndUpdate(r.Context(),
		bson.M{"_id": waiterID},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&waiter)
	if err != nil {
		util.JsonError(w, http.StatusNotFound, "Waiter not found")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message": "Profile updated",
		"waiter":  h.waiterView(r, waiter),
	})
}

// SetDuty clocks a waiter in or out. Waiters who are off duty are not sent
// new-order notifications.
func (h *WaiterHandler) SetDuty(w http.ResponseWriter, r *http.Request) {
	waiterID, ok := h.pathWaiter(w, r)
	if !ok {
		return
	}

	var reqBody struct {
		OnDuty bool `json:"onDuty"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	now := time.Now().UTC()
	// dutyStartedAt keeps the time of the last clock-in, so it also records
	// that this waiter uses the duty switch at all.
	set := bson.M{"onDuty": reqBody.OnDuty, "updatedAt": now}
	if reqBody.OnDuty {
		set["dutyStartedAt"] = now
	}

	var waiter models.Waiter
	err := h.DB.Collection("waiters").FindOneAndUpdate(r.Context(),
		bson.M{"_id": waiterID},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&waiter)
	if err != nil {
		util.JsonError(w, http.StatusNotFound, "Waiter not found")
		return
	}
	// A waiter clocking out for the first time has no clock-in recorded;
	// store one so the off-duty state is honoured.
	if !reqBody.OnDuty && waiter.DutyStartedAt == nil {
		h.DB.Collection("waiters").UpdateByID(r.Context(), waiterID, bson.M{"$set": bson.M{"dutyStartedAt": now}})
		waiter.DutyStartedAt = &now
	}

	message := "You are off duty"
	if reqBody.OnDuty {
		message = "You are on duty"
	}
	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message":       message,
		"onDuty":        waiter.OnDuty,
		"dutyStartedAt": waiter.DutyStartedAt,
	})
}

func (h *WaiterHandler) RegisterPushToken(w http.ResponseWriter, r *http.Request) {
	if waiterID, ok := h.pathWaiter(w, r); ok {
		h.registerPushToken(w, r, "waiters", waiterID)
	}
}

func (h *WaiterHandler) RemovePushToken(w http.ResponseWriter, r *http.Request) {
	if waiterID, ok := h.pathWaiter(w, r); ok {
		h.removePushToken(w, r, "waiters", waiterID)
	}
}

// Summary returns the waiter's shift figures for today and how many orders
// are waiting in each queue of their restaurant.
func (h *WaiterHandler) Summary(w http.ResponseWriter, r *http.Request) {
	waiterID, ok := h.pathWaiter(w, r)
	if !ok {
		return
	}

	var waiter models.Waiter
	if err := h.DB.Collection("waiters").FindOne(r.Context(), bson.M{"_id": waiterID}).Decode(&waiter); err != nil {
		util.JsonError(w, http.StatusNotFound, "Waiter not found")
		return
	}
	if waiter.RestaurantID == nil {
		util.JsonError(w, http.StatusBadRequest, "This account is not linked to a restaurant")
		return
	}

	now := time.Now().UTC()
	today := h.startOfDay(now)
	orders := h.DB.Collection("orders")

	type statusCount struct {
		Status string  `bson:"_id"`
		Count  int     `bson:"count"`
		Amount float64 `bson:"amount"`
	}
	countBy := func(match bson.M) (map[string]statusCount, error) {
		cursor, err := orders.Aggregate(r.Context(), bson.A{
			bson.M{"$match": match},
			bson.M{"$group": bson.M{"_id": "$orderStatus", "count": bson.M{"$sum": 1}, "amount": bson.M{"$sum": "$totalAmount"}}},
		})
		if err != nil {
			return nil, err
		}
		var rows []statusCount
		if err := cursor.All(r.Context(), &rows); err != nil {
			return nil, err
		}
		out := make(map[string]statusCount, len(rows))
		for _, row := range rows {
			out[row.Status] = row
		}
		return out, nil
	}

	// What is waiting across the whole floor.
	queues, err := countBy(bson.M{
		"restaurantId": *waiter.RestaurantID,
		"orderStatus":  bson.M{"$in": bson.A{"placed", "served", "ready_to_pay"}},
		"createdAt":    bson.M{"$gte": now.Add(-openOrderWindow)},
	})
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not load your summary")
		return
	}

	// What this waiter has handled since the business day began.
	mine, err := countBy(bson.M{
		"restaurantId": *waiter.RestaurantID,
		"servedBy":     waiterID,
		"orderStatus":  bson.M{"$ne": "cancelled"},
		"updatedAt":    bson.M{"$gte": today},
	})
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not load your summary")
		return
	}

	var tablesToday int
	for _, row := range mine {
		tablesToday += row.Count
	}
	closed := mine[string(models.OrderCompleted)]

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"onDuty":        waiter.OnDuty,
		"dutyStartedAt": waiter.DutyStartedAt,
		"queues": map[string]int{
			"not-taken": queues[string(models.OrderPlaced)].Count,
			"served":    queues[string(models.OrderServed)].Count,
			"payment":   queues[string(models.OrderReadyToPay)].Count,
		},
		"today": map[string]interface{}{
			"tablesServed": tablesToday,
			"tablesClosed": closed.Count,
			"salesClosed":  closed.Amount,
		},
	})
}
