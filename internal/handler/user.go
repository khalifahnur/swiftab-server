package handler

import (
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type UserHandler struct {
	*Base
}

const (
	mobileSessionTTL = 30 * 24 * time.Hour
	resetCodeTTL     = 10 * time.Minute
	resetTokenTTL    = 15 * time.Minute
	maxPushTokens    = 5
	maxRecentViewed  = 10
)

func userView(u models.User) map[string]string {
	return map[string]string{
		"userId":      u.ID.Hex(),
		"name":        u.Name,
		"email":       u.Email,
		"phoneNumber": u.PhoneNumber,
	}
}

func (h *UserHandler) SignUpUser(w http.ResponseWriter, r *http.Request) {
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

	name := strings.TrimSpace(reqBody.Name)
	phone := strings.TrimSpace(reqBody.PhoneNumber)
	email, emailOK := util.NormalizeEmail(reqBody.Email)

	if name == "" || reqBody.Email == "" || reqBody.Password == "" || phone == "" {
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

	err := h.DB.Collection("users").FindOne(r.Context(), bson.M{"email": email}).Err()
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
	newUser := models.User{
		ID:          bson.NewObjectID(),
		Name:        name,
		Email:       email,
		Password:    hashedPassword,
		PhoneNumber: phone,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if _, err = h.DB.Collection("users").InsertOne(r.Context(), newUser); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error creating user")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{
		"message": "User created successfully",
	})
}

// findUserByEmail looks a diner up by the normalised address, falling back
// to the address exactly as typed for accounts created before normalisation.
func (h *UserHandler) findUserByEmail(r *http.Request, raw string) (*models.User, error) {
	email, _ := util.NormalizeEmail(raw)
	var user models.User
	err := h.DB.Collection("users").FindOne(r.Context(), bson.M{
		"email": bson.M{"$in": bson.A{email, strings.TrimSpace(raw)}},
	}).Decode(&user)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (h *UserHandler) LoginUser(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	user, err := h.findUserByEmail(r, reqBody.Email)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
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

	tokenString, err := util.GenerateUserToken(user.ID.Hex(), user.Email, mobileSessionTTL)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error occurred during login")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"token": tokenString,
		"user":  userView(*user),
	})
}

// --- Password reset --------------------------------------------------------

// ForgotPassword emails a reset code. It answers the same way whether or not
// the address has an account, so it cannot be used to discover who is
// registered.
func (h *UserHandler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		Email string `json:"email"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	email, ok := util.NormalizeEmail(reqBody.Email)
	if !ok {
		util.JsonError(w, http.StatusBadRequest, "Enter a valid email address")
		return
	}
	if !h.Mailer.Enabled() {
		util.JsonError(w, http.StatusServiceUnavailable, "Password reset by email is not available yet. Please contact support.")
		return
	}

	const sent = "If an account exists for that email, a reset code is on its way."

	user, err := h.findUserByEmail(r, reqBody.Email)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			util.JsonResponse(w, http.StatusOK, map[string]string{"message": sent, "email": email})
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "Database error")
		return
	}

	code := util.GenerateCode()
	expires := time.Now().UTC().Add(resetCodeTTL)
	if _, err := h.DB.Collection("users").UpdateByID(r.Context(), user.ID, bson.M{"$set": bson.M{
		"verificationCode":           code,
		"verificationCodeExpiration": expires,
	}}); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not start the password reset")
		return
	}

	subject, body := util.PasswordResetEmail(user.Name, code)
	if err := h.Mailer.Send(user.Email, subject, body); err != nil {
		util.JsonError(w, http.StatusBadGateway, "We could not send the reset email. Please try again shortly.")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{"message": sent, "email": email})
}

// VerifyResetCode checks the emailed code and returns a short-lived token
// that authorises setting a new password.
func (h *UserHandler) VerifyResetCode(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		Email            string `json:"email"`
		VerificationCode string `json:"verificationCode"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	user, err := h.findUserByEmail(r, reqBody.Email)
	code := strings.TrimSpace(reqBody.VerificationCode)
	if err != nil || code == "" || user.VerificationCode != code ||
		user.VerificationCodeExpiration == nil || time.Now().After(*user.VerificationCodeExpiration) {
		util.JsonError(w, http.StatusBadRequest, "That code is incorrect or has expired")
		return
	}

	token, err := util.GenerateResetToken(user.Email, resetTokenTTL)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not verify the code")
		return
	}

	// A code works once.
	h.DB.Collection("users").UpdateByID(r.Context(), user.ID, bson.M{"$unset": bson.M{
		"verificationCode":           "",
		"verificationCodeExpiration": "",
	}})

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message":    "Code verified",
		"email":      user.Email,
		"resetToken": token,
		"status":     200,
	})
}

// ResetPassword sets a new password. It requires the token from
// VerifyResetCode: knowing an email address alone is not enough.
func (h *UserHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		Email       string `json:"email"`
		NewPassword string `json:"newPassword"`
		ResetToken  string `json:"resetToken"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	claims, err := util.ParseToken(strings.TrimSpace(reqBody.ResetToken))
	if err != nil || claims.Role != util.RoleReset || claims.Email == "" {
		util.JsonError(w, http.StatusUnauthorized, "Your reset session has expired. Request a new code.")
		return
	}
	if len(reqBody.NewPassword) < minPasswordLen {
		util.JsonError(w, http.StatusBadRequest, "Password must be at least 6 characters")
		return
	}

	hashed, err := util.HashPassword(reqBody.NewPassword)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error hashing password")
		return
	}

	res, err := h.DB.Collection("users").UpdateOne(r.Context(), bson.M{"email": claims.Email}, bson.M{"$set": bson.M{
		"password":  hashed,
		"updatedAt": time.Now().UTC(),
	}})
	if err != nil || res.MatchedCount == 0 {
		util.JsonError(w, http.StatusInternalServerError, "Could not update the password")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message": "Password updated. You can now sign in.",
		"email":   claims.Email,
		"status":  200,
	})
}

// --- Profile ---------------------------------------------------------------

// pathUser parses {userId} and checks the caller may act as that diner.
func (h *UserHandler) pathUser(w http.ResponseWriter, r *http.Request) (bson.ObjectID, bool) {
	userID, err := bson.ObjectIDFromHex(r.PathValue("userId"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid User ID")
		return bson.NilObjectID, false
	}
	if !ownsUser(r, userID) {
		util.JsonError(w, http.StatusForbidden, "You can only manage your own account")
		return bson.NilObjectID, false
	}
	return userID, true
}

func (h *UserHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.pathUser(w, r)
	if !ok {
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

	var user models.User
	err := h.DB.Collection("users").FindOneAndUpdate(r.Context(),
		bson.M{"_id": userID},
		bson.M{"$set": bson.M{"name": name, "phoneNumber": phone, "updatedAt": time.Now().UTC()}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&user)
	if err != nil {
		util.JsonError(w, http.StatusNotFound, "Account not found")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message": "Profile updated",
		"user":    userView(user),
	})
}

// registerPushToken stores a device's Expo push token on an account,
// keeping only the most recent few devices.
func (b *Base) registerPushToken(w http.ResponseWriter, r *http.Request, collection string, id bson.ObjectID) {
	var reqBody struct {
		Token string `json:"token"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	token := strings.TrimSpace(reqBody.Token)
	if !util.IsExpoPushToken(token) {
		util.JsonError(w, http.StatusBadRequest, "Invalid push token")
		return
	}

	coll := b.DB.Collection(collection)
	// A device belongs to whoever signed in on it last.
	coll.UpdateMany(r.Context(), bson.M{"pushTokens": token, "_id": bson.M{"$ne": id}}, bson.M{"$pull": bson.M{"pushTokens": token}})
	coll.UpdateByID(r.Context(), id, bson.M{"$pull": bson.M{"pushTokens": token}})
	res, err := coll.UpdateByID(r.Context(), id, bson.M{"$push": bson.M{"pushTokens": bson.M{
		"$each":  bson.A{token},
		"$slice": -maxPushTokens,
	}}})
	if err != nil || res.MatchedCount == 0 {
		util.JsonError(w, http.StatusNotFound, "Account not found")
		return
	}
	util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Notifications enabled on this device"})
}

// removePushToken forgets a device, used when signing out.
func (b *Base) removePushToken(w http.ResponseWriter, r *http.Request, collection string, id bson.ObjectID) {
	var reqBody struct {
		Token string `json:"token"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil || strings.TrimSpace(reqBody.Token) == "" {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	b.DB.Collection(collection).UpdateByID(r.Context(), id, bson.M{"$pull": bson.M{"pushTokens": strings.TrimSpace(reqBody.Token)}})
	util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Notifications disabled on this device"})
}

func (h *UserHandler) RegisterPushToken(w http.ResponseWriter, r *http.Request) {
	if userID, ok := h.pathUser(w, r); ok {
		h.registerPushToken(w, r, "users", userID)
	}
}

func (h *UserHandler) RemovePushToken(w http.ResponseWriter, r *http.Request) {
	if userID, ok := h.pathUser(w, r); ok {
		h.removePushToken(w, r, "users", userID)
	}
}

// --- Restaurants -----------------------------------------------------------

// publicRestaurant prepares a restaurant for diners: dishes the kitchen has
// switched off are removed and every list is a real array.
func publicRestaurant(rest models.Restaurant) models.Restaurant {
	// Work on a copy: the slice still points at the caller's data.
	rest.Data = append([]models.RestaurantData(nil), rest.Data...)
	for i := range rest.Data {
		data := &rest.Data[i]
		cats := data.Categories()
		menu := make(models.Menu, len(cats))
		for _, section := range sections(data, true) {
			menu[section.Key] = section.Items
		}
		// Older app builds read these three sections unconditionally.
		for _, c := range models.LegacyMenuCategories() {
			if _, ok := menu[c.Key]; !ok {
				menu[c.Key] = []models.MenuItem{}
			}
		}
		data.Menu = menu
		data.MenuCategories = cats
		data.OpeningHours = normalizeOpeningHours(data.OpeningHours)
		if data.Review == nil {
			data.Review = []models.Review{}
		}
		if data.About == nil {
			data.About = []models.About{}
		}
	}
	return rest
}

func (h *UserHandler) allRestaurants(r *http.Request, filter bson.M) ([]models.Restaurant, error) {
	cursor, err := h.DB.Collection("restaurants").Find(r.Context(), filter)
	if err != nil {
		return nil, err
	}
	var restaurants []models.Restaurant
	if err := cursor.All(r.Context(), &restaurants); err != nil {
		return nil, err
	}
	out := make([]models.Restaurant, 0, len(restaurants))
	for _, rest := range restaurants {
		// A restaurant whose setup never completed has nothing to show.
		if len(rest.Data) == 0 {
			continue
		}
		out = append(out, publicRestaurant(rest))
	}
	return out, nil
}

// FetchAllRestaurants lists restaurants. ?q= filters by name, location or
// cuisine.
func (h *UserHandler) FetchAllRestaurants(w http.ResponseWriter, r *http.Request) {
	restaurants, err := h.allRestaurants(r, bson.M{})
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Failed to fetch restaurants")
		return
	}

	if q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))); q != "" {
		filtered := restaurants[:0]
		for _, rest := range restaurants {
			data := rest.Data[0]
			haystack := strings.ToLower(data.RestaurantName + " " + data.Location + " " + strings.Join(data.Cuisines, " "))
			if strings.Contains(haystack, q) {
				filtered = append(filtered, rest)
			}
		}
		restaurants = filtered
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message":     "restaurants fetched successfully",
		"restaurants": restaurants,
	})
}

func (h *UserHandler) FetchRestaurant(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := bson.ObjectIDFromHex(r.PathValue("restaurantId"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid Restaurant ID format")
		return
	}
	restaurant, err := h.loadRestaurant(r.Context(), restaurantID)
	if err != nil || len(restaurant.Data) == 0 {
		util.JsonError(w, http.StatusNotFound, "Restaurant not found")
		return
	}
	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"restaurant": publicRestaurant(*restaurant),
	})
}

// haversineKm is the great-circle distance between two coordinates.
func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const earthRadiusKm = 6371.0
	rad := func(deg float64) float64 { return deg * math.Pi / 180 }
	dLat := rad(lat2 - lat1)
	dLng := rad(lng2 - lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return earthRadiusKm * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// FetchNearbyRestaurants lists restaurants within a radius of the diner,
// nearest first. Coordinates arrive as ?lat=&lng= or, from older builds, in a
// JSON body.
func (h *UserHandler) FetchNearbyRestaurants(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat, errLat := strconv.ParseFloat(q.Get("lat"), 64)
	lng, errLng := strconv.ParseFloat(q.Get("lng"), 64)
	if errLat != nil || errLng != nil {
		var body struct {
			Latitude  *float64 `json:"latitude"`
			Longitude *float64 `json:"longitude"`
		}
		if err := util.DecodeJSON(w, r, &body); err != nil || body.Latitude == nil || body.Longitude == nil {
			util.JsonError(w, http.StatusBadRequest, "Latitude and longitude are required")
			return
		}
		lat, lng = *body.Latitude, *body.Longitude
	}
	if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		util.JsonError(w, http.StatusBadRequest, "Latitude or longitude is out of range")
		return
	}

	radiusKm := 25.0
	if v, err := strconv.ParseFloat(q.Get("radiusKm"), 64); err == nil && v > 0 && v <= 500 {
		radiusKm = v
	}

	restaurants, err := h.allRestaurants(r, bson.M{})
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Failed to fetch restaurants")
		return
	}

	type nearby struct {
		models.Restaurant
		DistanceKm float64 `json:"distanceKm"`
	}
	results := make([]nearby, 0)
	for _, rest := range restaurants {
		data := rest.Data[0]
		// (0, 0) means the restaurant never set its position.
		if data.Latitude == 0 && data.Longitude == 0 {
			continue
		}
		distance := haversineKm(lat, lng, data.Latitude, data.Longitude)
		if distance <= radiusKm {
			results = append(results, nearby{Restaurant: rest, DistanceKm: math.Round(distance*10) / 10})
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].DistanceKm < results[j].DistanceKm })

	util.JsonResponse(w, http.StatusOK, results)
}

// --- Recently viewed -------------------------------------------------------

func (h *UserHandler) AddRecentlyViewed(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.pathUser(w, r)
	if !ok {
		return
	}
	var reqBody struct {
		RestaurantID string `json:"restaurantId"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	restaurantID, err := bson.ObjectIDFromHex(reqBody.RestaurantID)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid Restaurant ID format")
		return
	}

	users := h.DB.Collection("users")
	// Move the restaurant to the front, keeping the newest few.
	users.UpdateByID(r.Context(), userID, bson.M{"$pull": bson.M{"recentlyViewed": restaurantID}})
	res, err := users.UpdateByID(r.Context(), userID, bson.M{"$push": bson.M{"recentlyViewed": bson.M{
		"$each":     bson.A{restaurantID},
		"$position": 0,
		"$slice":    maxRecentViewed,
	}}})
	if err != nil || res.MatchedCount == 0 {
		util.JsonError(w, http.StatusNotFound, "Account not found")
		return
	}
	util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Saved"})
}

func (h *UserHandler) FetchRecentlyViewed(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.pathUser(w, r)
	if !ok {
		return
	}

	var user models.User
	opts := options.FindOne().SetProjection(bson.M{"recentlyViewed": 1})
	if err := h.DB.Collection("users").FindOne(r.Context(), bson.M{"_id": userID}, opts).Decode(&user); err != nil {
		util.JsonError(w, http.StatusNotFound, "Account not found")
		return
	}

	ordered := make([]models.Restaurant, 0, len(user.RecentlyViewed))
	if len(user.RecentlyViewed) > 0 {
		restaurants, err := h.allRestaurants(r, bson.M{"_id": bson.M{"$in": user.RecentlyViewed}})
		if err != nil {
			util.JsonError(w, http.StatusInternalServerError, "Failed to fetch restaurants")
			return
		}
		byID := make(map[bson.ObjectID]models.Restaurant, len(restaurants))
		for _, rest := range restaurants {
			byID[rest.ID] = rest
		}
		for _, id := range user.RecentlyViewed {
			if rest, ok := byID[id]; ok {
				ordered = append(ordered, rest)
			}
		}
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message":     "recently viewed restaurants",
		"restaurants": ordered,
	})
}

// --- Reviews ---------------------------------------------------------------

// AddReview records a diner's rating of a restaurant they have eaten at.
// Each diner has one review per restaurant; posting again replaces it.
func (h *UserHandler) AddReview(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.pathUser(w, r)
	if !ok {
		return
	}
	restaurantID, err := bson.ObjectIDFromHex(r.PathValue("restaurantId"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid Restaurant ID format")
		return
	}

	var reqBody struct {
		Rating    float64 `json:"rating"`
		ReviewTxt string  `json:"reviewTxt"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if reqBody.Rating < 1 || reqBody.Rating > 5 {
		util.JsonError(w, http.StatusBadRequest, "Choose a rating from 1 to 5 stars")
		return
	}
	text := strings.TrimSpace(reqBody.ReviewTxt)
	if len(text) > 600 {
		text = text[:600]
	}

	// Ratings should reflect real visits.
	err = h.DB.Collection("orders").FindOne(r.Context(), bson.M{
		"userId":       userID,
		"restaurantId": restaurantID,
		"orderStatus":  models.OrderCompleted,
	}).Err()
	if errors.Is(err, mongo.ErrNoDocuments) {
		err = h.DB.Collection("reservations").FindOne(r.Context(), bson.M{
			"userId":       userID,
			"restaurantId": restaurantID,
			"status":       models.ReservationCompleted,
		}).Err()
	}
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			util.JsonError(w, http.StatusForbidden, "You can review a restaurant after dining there")
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "Could not save your review")
		return
	}

	var user models.User
	if err := h.DB.Collection("users").FindOne(r.Context(), bson.M{"_id": userID}).Decode(&user); err != nil {
		util.JsonError(w, http.StatusNotFound, "Account not found")
		return
	}

	restaurant, err := h.loadRestaurant(r.Context(), restaurantID)
	if err != nil || len(restaurant.Data) == 0 {
		util.JsonError(w, http.StatusNotFound, "Restaurant not found")
		return
	}

	now := time.Now().UTC()
	review := models.Review{
		UserID:    &userID,
		Name:      user.Name,
		ReviewTxt: text,
		Rating:    reqBody.Rating,
		CreatedAt: &now,
	}

	reviews := make([]models.Review, 0, len(restaurant.Data[0].Review)+1)
	for _, existing := range restaurant.Data[0].Review {
		if existing.UserID != nil && *existing.UserID == userID {
			continue
		}
		reviews = append(reviews, existing)
	}
	reviews = append(reviews, review)

	sum := 0.0
	for _, rv := range reviews {
		sum += rv.Rating
	}
	average := math.Round(sum/float64(len(reviews))*10) / 10

	if _, err := h.DB.Collection("restaurants").UpdateByID(r.Context(), restaurantID, bson.M{"$set": bson.M{
		"data.0.review": reviews,
		"data.0.rate":   average,
		"updatedAt":     now,
	}}); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not save your review")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message":     "Thanks for your review",
		"rate":        average,
		"reviewCount": len(reviews),
	})
}
