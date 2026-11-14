package handler

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"swiftab/server/internal/middleware"
	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type AdminHandler struct {
	*Base
}

const (
	adminSessionTTL = 7 * 24 * time.Hour
	waiterInviteTTL = 48 * time.Hour
	minPasswordLen  = 6
)

// sessionCookie builds the admin session cookie. Clearing a cookie only works
// when name, domain and path match the cookie that was set, so sign-in and
// sign-out both go through here.
func (h *AdminHandler) sessionCookie(value string, maxAge int) *http.Cookie {
	cookie := &http.Cookie{
		Name:     middleware.AccessCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
	}
	if h.Cfg != nil && h.Cfg.IsProd() {
		cookie.Domain = h.Cfg.CookieDomain
		cookie.Secure = true
		cookie.SameSite = http.SameSiteNoneMode
	} else {
		cookie.SameSite = http.SameSiteLaxMode
	}
	if maxAge < 0 {
		cookie.Expires = time.Unix(0, 0)
	}
	return cookie
}

func (h *AdminHandler) startSession(w http.ResponseWriter, admin *models.Admin) error {
	restaurantID := ""
	if admin.RestaurantID != nil {
		restaurantID = admin.RestaurantID.Hex()
	}
	token, err := util.GenerateToken(admin.ID.Hex(), restaurantID, admin.Email, util.RoleAdmin, adminSessionTTL)
	if err != nil {
		return err
	}
	http.SetCookie(w, h.sessionCookie(token, int(adminSessionTTL.Seconds())))
	return nil
}

func (h *AdminHandler) SignUpAdmin(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		Name        string `json:"name"`
		Email       string `json:"email"`
		Password    string `json:"password"`
		PhoneNumber string `json:"phoneNumber"`
	}

	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	reqBody.Name = strings.TrimSpace(reqBody.Name)
	reqBody.PhoneNumber = strings.TrimSpace(reqBody.PhoneNumber)
	email, emailOK := util.NormalizeEmail(reqBody.Email)

	if reqBody.Name == "" || reqBody.Email == "" || reqBody.Password == "" || reqBody.PhoneNumber == "" {
		util.JsonError(w, http.StatusBadRequest, "All fields are required")
		return
	}
	if !emailOK {
		util.JsonError(w, http.StatusBadRequest, "Enter a valid email address")
		return
	}
	if len(reqBody.Password) < minPasswordLen {
		util.JsonError(w, http.StatusBadRequest, "Password must be at least 6 characters")
		return
	}

	err := h.DB.Collection("admins").FindOne(r.Context(), bson.M{"email": email}).Err()
	if err == nil {
		util.JsonError(w, http.StatusConflict, "An account with this email already exists")
		return
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		util.JsonError(w, http.StatusInternalServerError, "Database error")
		return
	}

	hashedPassword, err := util.HashPassword(reqBody.Password)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error hashing password")
		return
	}

	now := time.Now().UTC()
	newAdmin := models.Admin{
		ID:          bson.NewObjectID(),
		Name:        reqBody.Name,
		Email:       email,
		Password:    hashedPassword,
		PhoneNumber: reqBody.PhoneNumber,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if _, err = h.DB.Collection("admins").InsertOne(r.Context(), newAdmin); err != nil {
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

	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	email, _ := util.NormalizeEmail(reqBody.Email)

	var admin models.Admin
	// Accounts created before emails were normalised may be stored with other
	// casing, so fall back to the address exactly as typed.
	err := h.DB.Collection("admins").FindOne(r.Context(), bson.M{
		"email": bson.M{"$in": bson.A{email, strings.TrimSpace(reqBody.Email)}},
	}).Decode(&admin)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
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

	if err := h.startSession(w, &admin); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Failed to generate token")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message": "Login successful",
		"user": map[string]interface{}{
			"name":         admin.Name,
			"email":        admin.Email,
			"phoneNumber":  admin.PhoneNumber,
			"restaurantId": admin.RestaurantID,
		},
	})
}

// LogoutAdmin clears the session cookie. It succeeds even when no session
// exists so a stale browser tab can always sign out.
func (h *AdminHandler) LogoutAdmin(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, h.sessionCookie("", -1))
	util.JsonResponse(w, http.StatusOK, map[string]string{
		"message": "Logged out successfully",
	})
}

func (h *AdminHandler) loadAdmin(r *http.Request) (*models.Admin, error) {
	adminID, err := h.adminID(r)
	if err != nil {
		return nil, err
	}
	var admin models.Admin
	if err := h.DB.Collection("admins").FindOne(r.Context(), bson.M{"_id": adminID}).Decode(&admin); err != nil {
		return nil, err
	}
	return &admin, nil
}

func (h *AdminHandler) GetAdminInfo(w http.ResponseWriter, r *http.Request) {
	admin, err := h.loadAdmin(r)
	if err != nil {
		util.JsonError(w, http.StatusUnauthorized, "Admin not authenticated")
		return
	}

	responseData := map[string]interface{}{
		"name":         admin.Name,
		"createdAt":    admin.CreatedAt,
		"email":        admin.Email,
		"phoneNumber":  admin.PhoneNumber,
		"restaurantId": nil,
	}

	if admin.RestaurantID != nil {
		restaurant, err := h.loadRestaurant(r.Context(), *admin.RestaurantID)
		if err == nil && len(restaurant.Data) > 0 {
			data := restaurant.Data[0]
			var about models.About
			if len(data.About) > 0 {
				about = data.About[0]
			}

			responseData["restaurantId"] = restaurant.ID
			responseData["restaurantName"] = data.RestaurantName
			responseData["location"] = data.Location
			responseData["latitude"] = data.Latitude
			responseData["longitude"] = data.Longitude
			responseData["image"] = data.Image
			responseData["logo"] = data.Logo
			responseData["rate"] = data.Rate
			responseData["reviewCount"] = len(data.Review)
			responseData["hrsOfOperation"] = about.HrsOfOperation
			responseData["description"] = about.Description
			responseData["averagePrice"] = about.AveragePrice
			responseData["phone"] = about.Phone
			responseData["restaurantEmail"] = about.Email
			responseData["cuisines"] = nonNilStrings(data.Cuisines)
			responseData["amenities"] = nonNilStrings(data.Amenities)
			responseData["paymentMethods"] = nonNilStrings(data.PaymentMethods)
			responseData["taxRate"] = data.TaxRate
			responseData["serviceCharge"] = data.ServiceCharge
			responseData["openingHours"] = normalizeOpeningHours(data.OpeningHours)
			responseData["acceptingReservations"] = data.IsAcceptingReservations()
			responseData["subscriptionStatus"] = restaurant.SubscriptionStatus
			responseData["subscriptionPlan"] = restaurant.SubscriptionPlan
			responseData["trialEndsAt"] = restaurant.TrialEndsAt
		}
	}

	util.JsonResponse(w, http.StatusOK, responseData)
}

func (h *AdminHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	adminID, err := h.adminID(r)
	if err != nil {
		util.JsonError(w, http.StatusUnauthorized, "Admin not authenticated")
		return
	}

	var reqBody struct {
		Name        string `json:"name"`
		PhoneNumber string `json:"phoneNumber"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	name := strings.TrimSpace(reqBody.Name)
	phone := strings.TrimSpace(reqBody.PhoneNumber)
	if name == "" || phone == "" {
		util.JsonError(w, http.StatusBadRequest, "Name and phone number are required")
		return
	}

	res, err := h.DB.Collection("admins").UpdateByID(r.Context(), adminID, bson.M{"$set": bson.M{
		"name":        name,
		"phoneNumber": phone,
		"updatedAt":   time.Now().UTC(),
	}})
	if err != nil || res.MatchedCount == 0 {
		util.JsonError(w, http.StatusInternalServerError, "Could not update profile")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Profile updated"})
}

func (h *AdminHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	admin, err := h.loadAdmin(r)
	if err != nil {
		util.JsonError(w, http.StatusUnauthorized, "Admin not authenticated")
		return
	}

	var reqBody struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if !util.VerifyPassword(admin.Password, reqBody.CurrentPassword) {
		util.JsonError(w, http.StatusBadRequest, "Current password is incorrect")
		return
	}
	if len(reqBody.NewPassword) < minPasswordLen {
		util.JsonError(w, http.StatusBadRequest, "New password must be at least 6 characters")
		return
	}

	hashed, err := util.HashPassword(reqBody.NewPassword)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error hashing password")
		return
	}

	if _, err := h.DB.Collection("admins").UpdateByID(r.Context(), admin.ID, bson.M{"$set": bson.M{
		"password":  hashed,
		"updatedAt": time.Now().UTC(),
	}}); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not update password")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Password updated"})
}

// --- Waiters ---------------------------------------------------------------

// waiterView is what the dashboard sees for a member of the floor team. The
// invite code is included only until the waiter has claimed the account, so
// the manager can pass it on in person when email is not set up.
func waiterView(w models.Waiter) map[string]interface{} {
	view := map[string]interface{}{
		"id":          w.ID.Hex(),
		"firstname":   w.FirstName,
		"lastname":    w.LastName,
		"email":       w.Email,
		"phoneNumber": w.PhoneNumber,
		"createdAt":   w.CreatedAt,
		"onDuty":      w.OnDuty,
		"status":      "active",
	}
	if w.RestaurantID != nil {
		view["restaurantId"] = w.RestaurantID.Hex()
	}
	if !w.IsActivated() {
		view["status"] = "pending"
		view["inviteCode"] = w.ValidationCode
		view["inviteExpiresAt"] = w.ValidationCodeExpiration
		view["inviteExpired"] = w.ValidationCodeExpiration != nil && time.Now().After(*w.ValidationCodeExpiration)
	}
	return view
}

// sendWaiterInvite emails the invite code when email is configured and
// reports whether an email was attempted.
func (h *AdminHandler) sendWaiterInvite(r *http.Request, waiter models.Waiter, restaurantID bson.ObjectID) bool {
	if !h.Mailer.Enabled() {
		return false
	}
	restaurantName := ""
	if restaurant, err := h.loadRestaurant(r.Context(), restaurantID); err == nil && len(restaurant.Data) > 0 {
		restaurantName = restaurant.Data[0].RestaurantName
	}
	subject, body := util.WaiterInviteEmail(waiter.FirstName, restaurantName, waiter.ValidationCode)
	h.Mailer.SendAsync(waiter.Email, subject, body)
	return true
}

func inviteMessage(emailed bool) string {
	if emailed {
		return "Waiter added. Their invite code has been emailed and is shown below."
	}
	return "Waiter added. Share the invite code below with them to finish sign-up in the app."
}

func (h *AdminHandler) WaiterSignUp(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Set up your restaurant before adding staff")
		return
	}

	var reqBody struct {
		FirstName   string `json:"firstname"`
		LastName    string `json:"lastname"`
		Email       string `json:"email"`
		PhoneNumber string `json:"phoneNumber"`
	}

	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	firstName := strings.TrimSpace(reqBody.FirstName)
	lastName := strings.TrimSpace(reqBody.LastName)
	phone := strings.TrimSpace(reqBody.PhoneNumber)
	email, emailOK := util.NormalizeEmail(reqBody.Email)

	if firstName == "" || lastName == "" || reqBody.Email == "" || phone == "" {
		util.JsonError(w, http.StatusBadRequest, "All fields are required")
		return
	}
	if !emailOK {
		util.JsonError(w, http.StatusBadRequest, "Enter a valid email address")
		return
	}

	err = h.DB.Collection("waiters").FindOne(r.Context(), bson.M{"email": email}).Err()
	if err == nil {
		util.JsonError(w, http.StatusConflict, "A waiter with this email already exists")
		return
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		util.JsonError(w, http.StatusInternalServerError, "Database error")
		return
	}

	now := time.Now().UTC()
	expiration := now.Add(waiterInviteTTL)

	newWaiter := models.Waiter{
		ID:                       bson.NewObjectID(),
		FirstName:                firstName,
		LastName:                 lastName,
		Email:                    email,
		PhoneNumber:              phone,
		RestaurantID:             &restaurantID,
		ValidationCode:           util.GenerateCode(),
		ValidationCodeExpiration: &expiration,
		CreatedAt:                now,
		UpdatedAt:                now,
	}

	if _, err = h.DB.Collection("waiters").InsertOne(r.Context(), newWaiter); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error creating waiter")
		return
	}

	emailed := h.sendWaiterInvite(r, newWaiter, restaurantID)

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message": inviteMessage(emailed),
		"emailed": emailed,
		"waiter":  waiterView(newWaiter),
	})
}

// ResendWaiterInvite issues a fresh invite code for a waiter who has not
// finished signing up. With ?reset=true it also works for an active waiter:
// their password is cleared so they can set a new one with the new code,
// which is how a manager helps someone who forgot their password.
func (h *AdminHandler) ResendWaiterInvite(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Restaurant ID is required")
		return
	}

	waiterID, err := bson.ObjectIDFromHex(r.PathValue("id"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid Waiter ID")
		return
	}

	var waiter models.Waiter
	filter := bson.M{"_id": waiterID, "restaurantId": restaurantID}
	if err := h.DB.Collection("waiters").FindOne(r.Context(), filter).Decode(&waiter); err != nil {
		util.JsonError(w, http.StatusNotFound, "Waiter not found")
		return
	}

	reset := r.URL.Query().Get("reset") == "true"
	if waiter.IsActivated() && !reset {
		util.JsonError(w, http.StatusConflict, "This waiter has already finished signing up")
		return
	}

	expiration := time.Now().UTC().Add(waiterInviteTTL)
	waiter.ValidationCode = util.GenerateCode()
	waiter.ValidationCodeExpiration = &expiration

	unset := bson.M{"validatedAt": ""}
	if waiter.IsActivated() {
		// Signing in again requires the new code; drop their devices too.
		unset["password"] = ""
		unset["pushTokens"] = ""
		waiter.Password = ""
	}

	if _, err := h.DB.Collection("waiters").UpdateOne(r.Context(), filter, bson.M{
		"$set": bson.M{
			"validationcode":           waiter.ValidationCode,
			"validationcodeExpiration": expiration,
			"updatedAt":                time.Now().UTC(),
		},
		"$unset": unset,
	}); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not refresh the invite")
		return
	}

	emailed := h.sendWaiterInvite(r, waiter, restaurantID)
	message := "New invite code created. Share it with the waiter."
	if emailed {
		message = "New invite code created and emailed."
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message": message,
		"emailed": emailed,
		"waiter":  waiterView(waiter),
	})
}

func (h *AdminHandler) DeleteWaiter(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Restaurant ID is required")
		return
	}

	waiterID, err := bson.ObjectIDFromHex(r.PathValue("id"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid Waiter ID")
		return
	}

	// Scoped to the admin's own restaurant so one restaurant cannot remove
	// another's staff by guessing ids.
	res, err := h.DB.Collection("waiters").DeleteOne(r.Context(), bson.M{"_id": waiterID, "restaurantId": restaurantID})
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Server error")
		return
	}
	if res.DeletedCount == 0 {
		util.JsonError(w, http.StatusNotFound, "Waiter not found")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Waiter deleted successfully"})
}

func (h *AdminHandler) FetchWaiter(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonResponse(w, http.StatusOK, map[string]interface{}{
			"message":         "fetched waiter",
			"waiter":          []interface{}{},
			"emailConfigured": h.Mailer.Enabled(),
		})
		return
	}

	var waiters []models.Waiter
	cursor, err := h.DB.Collection("waiters").Find(r.Context(), bson.M{"restaurantId": restaurantID})
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Server error")
		return
	}
	if err := cursor.All(r.Context(), &waiters); err != nil {
		log.Printf("decode waiters: %v", err)
		util.JsonError(w, http.StatusInternalServerError, "Error decoding waiters")
		return
	}

	views := make([]map[string]interface{}, 0, len(waiters))
	for _, waiter := range waiters {
		views = append(views, waiterView(waiter))
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message":         "fetched waiter",
		"waiter":          views,
		"emailConfigured": h.Mailer.Enabled(),
	})
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
