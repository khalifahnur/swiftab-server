package handler

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"swiftab/server/internal/middleware"
	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type AdminHandler struct {
	DB        *mongo.Database
	SecretKey string
	Image     *util.CloudinaryService
}

func (h *AdminHandler) getAdminID(r *http.Request) (bson.ObjectID, error) {
	tid, ok := r.Context().Value(middleware.AdminIDKey).(bson.ObjectID)
	if !ok {
		return bson.NilObjectID, errors.New("missing admin ID")
	}
	return tid, nil
}

func (h *AdminHandler) getRestaurantID(r *http.Request) (bson.ObjectID, error) {
	rid, ok := r.Context().Value(middleware.RestaurantIDKey).(bson.ObjectID)
	if !ok {
		return bson.NilObjectID, errors.New("missing restaurant ID")
	}
	return rid, nil
}

func (h *AdminHandler) SignUpAdmin(w http.ResponseWriter, r *http.Request) {
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

	var existingAdmin models.Admin
	err := h.DB.Collection("admins").FindOne(r.Context(), bson.M{"email": reqBody.Email}).Decode(&existingAdmin)
	if err != mongo.ErrNoDocuments {
		util.JsonError(w, http.StatusUnauthorized, "User already exists")
		return
	}

	hashedPassword, err := util.HashPassword(reqBody.Password)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error hashing password")
		return
	}

	newAdmin := models.Admin{
		ID:          bson.NewObjectID(),
		Name:        reqBody.Name,
		Email:       reqBody.Email,
		Password:    hashedPassword,
		PhoneNumber: reqBody.PhoneNumber,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	_, err = h.DB.Collection("admins").InsertOne(r.Context(), newAdmin)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error creating user")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{"message": "User created successfully"})
}

func (h *AdminHandler) LoginAdmin(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	defer r.Body.Close()

	var admin models.Admin
	err := h.DB.Collection("admins").FindOne(r.Context(), bson.M{"email": reqBody.Email}).Decode(&admin)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			util.JsonError(w, http.StatusUnauthorized, "Incorrect Email/Password")
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "Database error")
		return
	}

	if !util.VerifyPassword(admin.Password, reqBody.Password) {
		util.JsonError(w, http.StatusUnauthorized, "Incorrect Email/Password")
		return
	}

	var restID string
	if admin.RestaurantID != nil {
		restID = admin.RestaurantID.Hex()
	}

	tokenString, err := util.GenerateToken(admin.ID.Hex(), restID, admin.Email, "admin", 24*time.Hour)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Failed to generate token")
		return
	}

	isProd := false

	cookie := &http.Cookie{
		Name:     "access_token",
		Value:    tokenString,
		Path:     "/",
		MaxAge:   24 * 60 * 60,
		HttpOnly: true,
		Secure:   isProd,
		SameSite: http.SameSiteLaxMode, // Change to SameSiteNoneMode if cross-domain in prod
	}

	if isProd {
		cookie.SameSite = http.SameSiteNoneMode
	}

	http.SetCookie(w, cookie)

	util.JsonResponse(w, http.StatusOK, map[string]string{
		"message": "Login successful",
	})
}

func (h *AdminHandler) LogoutAdmin(w http.ResponseWriter, r *http.Request) {
	// The cookie name MUST match the name used in your AuthMiddleware and Login handler.
	// Based on your middleware code, it should be "access_token", not "admin_auth".
	cookieName := "access_token"

	_, err := r.Cookie(cookieName)
	if err != nil {
		util.JsonError(w, http.StatusUnauthorized, "No token provided")
		return
	}

	// Ideally, pull this from an environment variable: os.Getenv("ENV") == "production"
	isProd := false

	clearCookie := &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Now().Add(-1 * time.Hour),
		HttpOnly: true,
		Secure:   isProd,
		SameSite: http.SameSiteLaxMode,
	}

	if isProd {
		clearCookie.SameSite = http.SameSiteNoneMode
	}

	http.SetCookie(w, clearCookie)

	util.JsonResponse(w, http.StatusOK, map[string]string{
		"message": "Logged out successfully",
	})
}

func (h *AdminHandler) GetAdminInfo(w http.ResponseWriter, r *http.Request) {
	adminID, exists := h.getAdminID(r)
	if exists != nil {
		util.JsonError(w, http.StatusUnauthorized, "Admin not authenticated")
		return
	}

	var admin models.Admin
	err := h.DB.Collection("admins").FindOne(r.Context(), bson.M{"_id": adminID}).Decode(&admin)
	if err != nil {
		util.JsonError(w, http.StatusNotFound, "Admin info not found")
		return
	}

	var restaurant models.Restaurant
	if admin.RestaurantID != nil {
		_ = h.DB.Collection("restaurants").FindOne(r.Context(), bson.M{"_id": *admin.RestaurantID}).Decode(&restaurant)
	}

	var restData models.RestaurantData
	var about models.About
	if len(restaurant.Data) > 0 {
		restData = restaurant.Data[0]
		if len(restData.About) > 0 {
			about = restData.About[0]
		}
	}

	responseData := map[string]interface{}{
		"name":           admin.Name,
		"createdAt":      admin.CreatedAt,
		"email":          admin.Email,
		"phoneNumber":    admin.PhoneNumber,
		"restaurantName": restData.RestaurantName,
		"restaurantId":   restaurant.ID,
		"location":       restData.Location,
		"image":          restData.Image,
		"hrsOfOperation": about.HrsOfOperation,
		"description":    about.Description,
	}

	util.JsonResponse(w, http.StatusOK, responseData)
}

func generateValidationCode() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(900000))
	return fmt.Sprintf("%d", n.Int64()+100000)
}

func sendWaiterValidationCode(email string, code string) {
	fmt.Printf("Sending code %s to %s\n", code, email)
}

func sendWaiterSigninEmail(email string) {
	fmt.Printf("Sending signin email to %s\n", email)
}

func (h *AdminHandler) WaiterSignUp(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.getRestaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusUnauthorized, "Restaurant not authenticated")
		return
	}

	var reqBody struct {
		FirstName   string `json:"firstname"`
		LastName    string `json:"lastname"`
		Email       string `json:"email"`
		PhoneNumber string `json:"phoneNumber"`
	}

	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	defer r.Body.Close()

	if reqBody.FirstName == "" || reqBody.LastName == "" || reqBody.Email == "" || reqBody.PhoneNumber == "" {
		util.JsonError(w, http.StatusBadRequest, "All fields are required")
		return
	}

	// Check if Waiter already exists
	var existingWaiter models.Waiter
	err = h.DB.Collection("waiters").FindOne(r.Context(), bson.M{"email": reqBody.Email}).Decode(&existingWaiter)
	if err != mongo.ErrNoDocuments {
		util.JsonError(w, http.StatusUnauthorized, "Waiter already exists")
		return
	}

	validationCode := generateValidationCode()
	expiration := time.Now().Add(10 * time.Minute)

	newWaiter := models.Waiter{
		ID:                       bson.NewObjectID(),
		FirstName:                reqBody.FirstName,
		LastName:                 reqBody.LastName,
		Email:                    reqBody.Email,
		PhoneNumber:              reqBody.PhoneNumber,
		RestaurantID:             &restaurantID,
		ValidationCode:           validationCode,
		ValidationCodeExpiration: &expiration,
		CreatedAt:                time.Now(),
		UpdatedAt:                time.Now(),
	}

	_, err = h.DB.Collection("waiters").InsertOne(r.Context(), newWaiter)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error creating waiter")
		return
	}

	// Send email asynchronously
	go sendWaiterValidationCode(reqBody.Email, validationCode)

	util.JsonResponse(w, http.StatusOK, map[string]string{
		"message": "Waiter created successfully",
	})
}

func (h *AdminHandler) DeleteWaiter(w http.ResponseWriter, r *http.Request) {
	adminID, exists := h.getAdminID(r)
	if exists != nil {
		util.JsonError(w, http.StatusUnauthorized, "Admin not authenticated")
		return
	}

	waiterIDStr := r.PathValue("id")
	waiterID, err := bson.ObjectIDFromHex(waiterIDStr)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid Waiter ID")
		return
	}

	var admin models.Admin
	err = h.DB.Collection("admins").FindOne(r.Context(), bson.M{"_id": adminID}).Decode(&admin)
	if err != nil {
		util.JsonError(w, http.StatusNotFound, "Admin info not found")
		return
	}

	_, err = h.DB.Collection("waiters").DeleteOne(r.Context(), bson.M{"_id": waiterID})
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Server error")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Waiter deleted successfully"})
}

func (h *AdminHandler) FetchWaiter(w http.ResponseWriter, r *http.Request) {
	adminID, exists := h.getAdminID(r)
	if exists != nil {
		util.JsonError(w, http.StatusUnauthorized, "User not authenticated")
		return
	}

	var admin models.Admin
	err := h.DB.Collection("admins").FindOne(r.Context(), bson.M{"_id": adminID}).Decode(&admin)
	if err != nil {
		util.JsonError(w, http.StatusNotFound, "Admin info not found")
		return
	}

	if admin.RestaurantID == nil {
		util.JsonResponse(w, http.StatusOK, map[string]interface{}{
			"message": "fetched waiter",
			"waiter":  []models.Waiter{},
		})
		return
	}

	var waiters []models.Waiter
	cursor, err := h.DB.Collection("waiters").Find(r.Context(), bson.M{"restaurantId": *admin.RestaurantID})
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Server error")
		return
	}
	defer cursor.Close(r.Context())
	if err := cursor.All(r.Context(), &waiters); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error decoding waiters")
		return
	}

	if waiters == nil {
		waiters = []models.Waiter{}
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message": "fetched waiter",
		"waiter":  waiters,
	})
}

func (h *AdminHandler) AddRestaurantData(w http.ResponseWriter, r *http.Request) {
	adminID, exists := h.getAdminID(r)
	if exists != nil {
		util.JsonError(w, http.StatusUnauthorized, "Admin not authenticated")
		return
	}

	err := r.ParseMultipartForm(10 << 20)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Error parsing form data")
		return
	}

	dataString := r.FormValue("data")
	if dataString == "" {
		util.JsonError(w, http.StatusBadRequest, "Data is invalid or empty")
		return
	}

	var parsedData struct {
		Title string                  `json:"title"`
		Data  []models.RestaurantData `json:"data"`
	}

	if err := json.Unmarshal([]byte(dataString), &parsedData); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid JSON in data field")
		return
	}

	if parsedData.Title == "" {
		util.JsonError(w, http.StatusBadRequest, "Title is required")
		return
	}
	if len(parsedData.Data) == 0 {
		util.JsonError(w, http.StatusBadRequest, "Data is invalid or empty")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Image file is required")
		return
	}
	defer file.Close()

	uploadedImageURL, err := h.Image.UploadRestaurantImage(r.Context(), file)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Failed to upload image to Cloudinary")
		return
	}

	parsedData.Data[0].Image = uploadedImageURL

	newRestaurant := models.Restaurant{
		ID:        bson.NewObjectID(),
		Title:     parsedData.Title,
		Data:      parsedData.Data,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	_, err = h.DB.Collection("restaurants").InsertOne(r.Context(), newRestaurant)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error saving restaurant data")
		return
	}

	update := bson.M{"$set": bson.M{"restaurantId": newRestaurant.ID}}
	_, err = h.DB.Collection("admins").UpdateByID(r.Context(), adminID, update)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Restaurant created, but failed to link to Admin")
		return
	}

	util.JsonResponse(w, http.StatusCreated, map[string]interface{}{
		"message":    "Restaurant data added successfully",
		"restaurant": newRestaurant,
	})
}
