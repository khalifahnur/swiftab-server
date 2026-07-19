package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type UserHandler struct {
	DB *mongo.Database
}

func sendSigninEmail(email string) {
	fmt.Println("user received the email")
}

func (h *UserHandler) SignUpUser(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		Name        string `json:"name"`
		Email       string `json:"email"`
		Password    string `json:"password"`
		PhoneNumber string `json:"phoneNumber"`
	}

	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	defer r.Body.Close()

	if reqBody.Name == "" || reqBody.Email == "" || reqBody.Password == "" || reqBody.PhoneNumber == "" {
		util.JsonError(w, http.StatusBadRequest, "All fields are required")
		return
	}

	var existingUser models.User
	err := h.DB.Collection("users").FindOne(r.Context(), bson.M{"email": reqBody.Email}).Decode(&existingUser)
	if err != mongo.ErrNoDocuments {
		util.JsonError(w, http.StatusUnauthorized, "User already exists")
		return
	}

	hashedPassword, err := util.HashPassword(reqBody.Password)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error hashing password")
		return
	}

	newUser := models.User{
		ID:          bson.NewObjectID(),
		Name:        reqBody.Name,
		Email:       reqBody.Email,
		Password:    hashedPassword,
		PhoneNumber: reqBody.PhoneNumber,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}

	_, err = h.DB.Collection("users").InsertOne(r.Context(), newUser)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error creating user")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{
		"message": "User created successfully",
	})
}

func (h *UserHandler) LoginUser(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	defer r.Body.Close()

	var user models.User
	err := h.DB.Collection("users").FindOne(r.Context(), bson.M{"email": reqBody.Email}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			util.JsonError(w, http.StatusUnauthorized, "Incorrect Email/Password")
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "Database error")
		return
	}

	if !util.VerifyPassword(user.Password, reqBody.Password) {
		util.JsonError(w, http.StatusUnauthorized, "Incorrect Email/Password")
		return
	}

	// Generate JWT using the newly added utility function
	tokenString, err := util.GenerateUserToken(user.ID.Hex(), user.Email, 24*time.Hour)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error occurred during login")
		return
	}

	// Send Email Asynchronously
	go sendSigninEmail(user.Email)

	// Return response exactly matching your Express payload
	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"token": tokenString,
		"user": map[string]string{
			"userId":      user.ID.Hex(),
			"name":        user.Name,
			"email":       user.Email,
			"phoneNumber": user.PhoneNumber,
		},
	})
}
