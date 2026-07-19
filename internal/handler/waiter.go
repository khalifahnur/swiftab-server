package handler

import (
	"encoding/json"
	"net/http"
	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type WaiterHandler struct {
	DB *mongo.Database
}

func (h *WaiterHandler) WaiterAuthentication(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		Email          string `json:"email"`
		ValidationCode string `json:"validationcode"`
	}

	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	defer r.Body.Close()

	if reqBody.Email == "" || reqBody.ValidationCode == "" {
		util.JsonError(w, http.StatusBadRequest, "Email and Verification Code are required!")
		return
	}

	var waiter models.Waiter
	err := h.DB.Collection("waiters").FindOne(r.Context(), bson.M{"email": reqBody.Email}).Decode(&waiter)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			util.JsonError(w, http.StatusNotFound, "Waiter not found")
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	isExpired := waiter.ValidationCodeExpiration != nil && time.Now().After(*waiter.ValidationCodeExpiration)

	if waiter.ValidationCode != reqBody.ValidationCode || isExpired {
		util.JsonError(w, http.StatusUnauthorized, "Verification code is incorrect or expired!")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message":    "Successfully signed up",
		"waiterData": waiter,
	})
}

func (h *WaiterHandler) WaiterPassword(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	defer r.Body.Close()

	if reqBody.Email == "" || reqBody.Password == "" {
		util.JsonError(w, http.StatusBadRequest, "Email and password are required")
		return
	}

	hashedPassword, err := util.HashPassword(reqBody.Password)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error hashing password")
		return
	}

	filter := bson.M{"email": reqBody.Email}

	update := bson.M{
		"$set": bson.M{
			"password":       hashedPassword,
			"validationcode": "",
		},
		"$unset": bson.M{
			"validationcodeExpiration": "",
		},
	}

	res, err := h.DB.Collection("waiters").UpdateOne(r.Context(), filter, update)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	if res.MatchedCount == 0 {
		util.JsonError(w, http.StatusNotFound, "Waiter not found")
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

	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	defer r.Body.Close()

	var waiter models.Waiter
	err := h.DB.Collection("waiters").FindOne(r.Context(), bson.M{"email": reqBody.Email}).Decode(&waiter)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			util.JsonError(w, http.StatusUnauthorized, "Incorrect Email/Password")
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "Error occurred during login")
		return
	}

	if waiter.Password == "" || !util.VerifyPassword(waiter.Password, reqBody.Password) {
		util.JsonError(w, http.StatusUnauthorized, "Incorrect Email/Password")
		return
	}

	var restID string
	if waiter.RestaurantID != nil {
		restID = waiter.RestaurantID.Hex()
	}

	tokenString, err := util.GenerateToken("", restID, waiter.Email, "waiter", 24*time.Hour)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Failed to generate token")
		return
	}

	go sendWaiterSigninEmail(waiter.Email)

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"token": tokenString,
		"waiter": map[string]interface{}{
			"_id":          waiter.ID.Hex(),
			"restaurantId": waiter.RestaurantID,
			"firstname":    waiter.FirstName,
			"lastname":     waiter.LastName,
			"phoneNumber":  waiter.PhoneNumber,
			"email":        waiter.Email,
		},
	})
}
