package handler

import (
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
	"time"

	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const maxUploadBytes = 10 << 20

var weekDays = []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

var clockPattern = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// normalizeOpeningHours returns one entry per weekday, Monday first. Days the
// restaurant did not specify are returned as closed.
func normalizeOpeningHours(in []models.OpeningHour) []models.OpeningHour {
	byDay := make(map[string]models.OpeningHour, len(in))
	for _, h := range in {
		byDay[strings.ToLower(strings.TrimSpace(h.Day))] = h
	}
	out := make([]models.OpeningHour, 0, len(weekDays))
	for _, day := range weekDays {
		h, ok := byDay[strings.ToLower(day)]
		if !ok {
			out = append(out, models.OpeningHour{Day: day})
			continue
		}
		h.Day = day
		if !h.Open {
			h.From, h.To = "", ""
		}
		out = append(out, h)
	}
	return out
}

// validateOpeningHours checks that every open day has well-formed times.
func validateOpeningHours(hours []models.OpeningHour) error {
	for _, h := range hours {
		if !h.Open {
			continue
		}
		if !clockPattern.MatchString(h.From) || !clockPattern.MatchString(h.To) {
			return fmt.Errorf("%s needs opening and closing times in HH:MM format", h.Day)
		}
		if h.From == h.To {
			return fmt.Errorf("%s opens and closes at the same time", h.Day)
		}
	}
	return nil
}

// summarizeHours renders opening hours as the single line diners see, grouping
// consecutive days that share the same times: "Mon-Fri 08:00-22:00, Sat 10:00-23:00".
func summarizeHours(hours []models.OpeningHour) string {
	hours = normalizeOpeningHours(hours)
	short := func(day string) string { return day[:3] }

	var parts []string
	for i := 0; i < len(hours); {
		if !hours[i].Open {
			i++
			continue
		}
		j := i
		for j+1 < len(hours) && hours[j+1].Open && hours[j+1].From == hours[i].From && hours[j+1].To == hours[i].To {
			j++
		}
		label := short(hours[i].Day)
		if j > i {
			label += "-" + short(hours[j].Day)
		}
		parts = append(parts, fmt.Sprintf("%s %s-%s", label, hours[i].From, hours[i].To))
		i = j + 1
	}
	return strings.Join(parts, ", ")
}

func cleanList(in []string, limit int) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		key := strings.ToLower(v)
		if v == "" || seen[key] || len(v) > 60 {
			continue
		}
		seen[key] = true
		out = append(out, v)
		if len(out) == limit {
			break
		}
	}
	return out
}

// percentToRate accepts either a fraction (0.16) or a percentage (16) and
// returns the fraction, so both the dashboard and older clients agree.
func percentToRate(v float64) (float64, bool) {
	if v < 0 || v > 100 {
		return 0, false
	}
	if v > 1 {
		v = v / 100
	}
	return v, true
}

// restaurantInput is the set of profile fields the dashboard can send, both
// when creating a restaurant and when editing it. Pointers distinguish "not
// sent" from a zero value on update.
type restaurantInput struct {
	RestaurantName        *string               `json:"restaurantName"`
	Location              *string               `json:"location"`
	Latitude              *float64              `json:"latitude"`
	Longitude             *float64              `json:"longitude"`
	Description           *string               `json:"description"`
	AveragePrice          *float64              `json:"averagePrice"`
	HrsOfOperation        *string               `json:"hrsOfOperation"`
	Phone                 *string               `json:"phone"`
	Email                 *string               `json:"email"`
	Cuisines              *[]string             `json:"cuisines"`
	Amenities             *[]string             `json:"amenities"`
	PaymentMethods        *[]string             `json:"paymentMethods"`
	TaxRate               *float64              `json:"taxRate"`
	ServiceCharge         *float64              `json:"serviceCharge"`
	OpeningHours          *[]models.OpeningHour `json:"openingHours"`
	AcceptingReservations *bool                 `json:"acceptingReservations"`
}

// uploadFormImage uploads the first present file among the given form fields
// and returns its URL, or "" when none of the fields carried a file.
func (b *Base) uploadFormImage(r *http.Request, folder string, fields ...string) (string, error) {
	for _, field := range fields {
		file, header, err := r.FormFile(field)
		if err != nil {
			continue
		}
		url, err := b.uploadImage(r, file, header, folder)
		file.Close()
		return url, err
	}
	return "", nil
}

func (b *Base) uploadImage(r *http.Request, file multipart.File, header *multipart.FileHeader, folder string) (string, error) {
	if header != nil {
		if header.Size > maxUploadBytes {
			return "", fmt.Errorf("image is larger than 10 MB")
		}
		if ct := header.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "image/") {
			return "", fmt.Errorf("only image files can be uploaded")
		}
	}
	return b.Image.Upload(r.Context(), file, folder)
}

// AddRestaurantData creates the admin's restaurant from the setup wizard.
//
// It expects multipart form data: a "data" field holding JSON
// ({"title": ..., "data": [{...}]}), a cover photo under "cover" and an
// optional "logo". The legacy field name "file" is still accepted for the cover.
func (h *AdminHandler) AddRestaurantData(w http.ResponseWriter, r *http.Request) {
	admin, err := h.loadAdmin(r)
	if err != nil {
		util.JsonError(w, http.StatusUnauthorized, "Admin not authenticated")
		return
	}
	if admin.RestaurantID != nil {
		util.JsonError(w, http.StatusConflict, "Your restaurant is already set up. Edit it from Settings.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 2*maxUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Error parsing form data")
		return
	}

	dataString := r.FormValue("data")
	if dataString == "" {
		util.JsonError(w, http.StatusBadRequest, "Data is invalid or empty")
		return
	}

	// The wizard nests contact details under about[0]; flat fields are accepted too.
	var parsed struct {
		Title string `json:"title"`
		Data  []struct {
			restaurantInput
			About []struct {
				Description    string  `json:"description"`
				AveragePrice   float64 `json:"averagePrice"`
				HrsOfOperation string  `json:"hrsOfOperation"`
				Phone          string  `json:"phone"`
				Email          string  `json:"email"`
			} `json:"about"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(dataString), &parsed); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid JSON in data field")
		return
	}
	if len(parsed.Data) == 0 {
		util.JsonError(w, http.StatusBadRequest, "Data is invalid or empty")
		return
	}

	in := parsed.Data[0]
	about := models.About{}
	if len(in.About) > 0 {
		about = models.About{
			Description:    strings.TrimSpace(in.About[0].Description),
			AveragePrice:   in.About[0].AveragePrice,
			HrsOfOperation: strings.TrimSpace(in.About[0].HrsOfOperation),
			Phone:          strings.TrimSpace(in.About[0].Phone),
			Email:          strings.TrimSpace(in.About[0].Email),
		}
	}
	if in.Description != nil {
		about.Description = strings.TrimSpace(*in.Description)
	}
	if in.AveragePrice != nil {
		about.AveragePrice = *in.AveragePrice
	}
	if in.HrsOfOperation != nil {
		about.HrsOfOperation = strings.TrimSpace(*in.HrsOfOperation)
	}
	if in.Phone != nil {
		about.Phone = strings.TrimSpace(*in.Phone)
	}
	if in.Email != nil {
		about.Email = strings.TrimSpace(*in.Email)
	}

	data := models.RestaurantData{
		About:          []models.About{about},
		Review:         []models.Review{},
		Menu:           models.Menu{},
		MenuCategories: models.DefaultMenuCategories(),
	}
	for _, c := range data.MenuCategories {
		data.Menu[c.Key] = []models.MenuItem{}
	}

	if in.RestaurantName != nil {
		data.RestaurantName = strings.TrimSpace(*in.RestaurantName)
	}
	if in.Location != nil {
		data.Location = strings.TrimSpace(*in.Location)
	}
	if data.RestaurantName == "" {
		util.JsonError(w, http.StatusBadRequest, "Restaurant name is required")
		return
	}
	if data.Location == "" {
		util.JsonError(w, http.StatusBadRequest, "Location is required")
		return
	}
	if in.Latitude != nil {
		data.Latitude = *in.Latitude
	}
	if in.Longitude != nil {
		data.Longitude = *in.Longitude
	}
	if data.Latitude < -90 || data.Latitude > 90 || data.Longitude < -180 || data.Longitude > 180 {
		util.JsonError(w, http.StatusBadRequest, "Latitude or longitude is out of range")
		return
	}
	if about.AveragePrice < 0 {
		util.JsonError(w, http.StatusBadRequest, "Average price cannot be negative")
		return
	}
	if in.Cuisines != nil {
		data.Cuisines = cleanList(*in.Cuisines, 12)
	}
	if in.Amenities != nil {
		data.Amenities = cleanList(*in.Amenities, 20)
	}
	if in.PaymentMethods != nil {
		data.PaymentMethods = cleanList(*in.PaymentMethods, 8)
	}
	if in.TaxRate != nil {
		rate, ok := percentToRate(*in.TaxRate)
		if !ok {
			util.JsonError(w, http.StatusBadRequest, "Tax rate must be between 0 and 100%")
			return
		}
		data.TaxRate = rate
	}
	if in.ServiceCharge != nil {
		rate, ok := percentToRate(*in.ServiceCharge)
		if !ok {
			util.JsonError(w, http.StatusBadRequest, "Service charge must be between 0 and 100%")
			return
		}
		data.ServiceCharge = rate
	}
	if in.OpeningHours != nil && len(*in.OpeningHours) > 0 {
		hours := normalizeOpeningHours(*in.OpeningHours)
		if err := validateOpeningHours(hours); err != nil {
			util.JsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		data.OpeningHours = hours
		if summary := summarizeHours(hours); summary != "" {
			data.About[0].HrsOfOperation = summary
		}
	}
	data.AcceptingReservations = boolPtr(true)

	cover, err := h.uploadFormImage(r, "restaurants", "cover", "file", "coverImage")
	if err != nil {
		util.JsonError(w, http.StatusBadGateway, "Could not upload the cover image: "+err.Error())
		return
	}
	if cover == "" {
		util.JsonError(w, http.StatusBadRequest, "A cover image is required")
		return
	}
	data.Image = cover

	logo, err := h.uploadFormImage(r, "restaurants/logos", "logo")
	if err != nil {
		util.JsonError(w, http.StatusBadGateway, "Could not upload the logo: "+err.Error())
		return
	}
	data.Logo = logo

	title := strings.TrimSpace(parsed.Title)
	if title == "" {
		title = "New Restaurants"
	}

	now := time.Now().UTC()
	trialEnds := now.Add(14 * 24 * time.Hour)
	newRestaurant := models.Restaurant{
		ID:                 bson.NewObjectID(),
		Title:              title,
		Data:               []models.RestaurantData{data},
		SubscriptionStatus: models.SubTrialing,
		SubscriptionPlan:   "starter",
		TrialEndsAt:        &trialEnds,
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	if _, err = h.DB.Collection("restaurants").InsertOne(r.Context(), newRestaurant); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error saving restaurant data")
		return
	}

	// Only link if the admin still has no restaurant, so two concurrent
	// submissions cannot both win.
	res, err := h.DB.Collection("admins").UpdateOne(r.Context(),
		// A nil match covers both a missing field and an explicit null.
		bson.M{"_id": admin.ID, "restaurantId": nil},
		bson.M{"$set": bson.M{"restaurantId": newRestaurant.ID, "updatedAt": now}},
	)
	if err != nil || res.MatchedCount == 0 {
		h.DB.Collection("restaurants").DeleteOne(r.Context(), bson.M{"_id": newRestaurant.ID})
		if err != nil {
			util.JsonError(w, http.StatusInternalServerError, "Could not link the restaurant to your account")
			return
		}
		util.JsonError(w, http.StatusConflict, "Your restaurant is already set up. Edit it from Settings.")
		return
	}

	// The session token was issued before the restaurant existed; reissue it
	// so the dashboard works without signing in again.
	admin.RestaurantID = &newRestaurant.ID
	if err := h.startSession(w, admin); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Restaurant created, but the session could not be refreshed. Please sign in again.")
		return
	}

	util.JsonResponse(w, http.StatusCreated, map[string]interface{}{
		"message":    "Restaurant data added successfully",
		"restaurant": newRestaurant,
	})
}

// UpdateRestaurant edits the restaurant profile from Settings. It accepts
// either JSON or multipart form data with a JSON "data" field plus optional
// "cover" and "logo" images. Only the fields that are sent change.
func (h *AdminHandler) UpdateRestaurant(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Set up your restaurant first")
		return
	}

	var in restaurantInput
	set := bson.M{}

	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, 2*maxUploadBytes+(1<<20))
		if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
			util.JsonError(w, http.StatusBadRequest, "Error parsing form data")
			return
		}
		if raw := r.FormValue("data"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &in); err != nil {
				util.JsonError(w, http.StatusBadRequest, "Invalid JSON in data field")
				return
			}
		}
		cover, err := h.uploadFormImage(r, "restaurants", "cover", "file")
		if err != nil {
			util.JsonError(w, http.StatusBadGateway, "Could not upload the cover image: "+err.Error())
			return
		}
		if cover != "" {
			set["data.0.image"] = cover
		}
		logo, err := h.uploadFormImage(r, "restaurants/logos", "logo")
		if err != nil {
			util.JsonError(w, http.StatusBadGateway, "Could not upload the logo: "+err.Error())
			return
		}
		if logo != "" {
			set["data.0.logo"] = logo
		}
	} else if err := util.DecodeJSON(w, r, &in); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if in.RestaurantName != nil {
		name := strings.TrimSpace(*in.RestaurantName)
		if name == "" {
			util.JsonError(w, http.StatusBadRequest, "Restaurant name cannot be empty")
			return
		}
		set["data.0.restaurantName"] = name
	}
	if in.Location != nil {
		location := strings.TrimSpace(*in.Location)
		if location == "" {
			util.JsonError(w, http.StatusBadRequest, "Location cannot be empty")
			return
		}
		set["data.0.location"] = location
	}
	if in.Latitude != nil {
		if *in.Latitude < -90 || *in.Latitude > 90 {
			util.JsonError(w, http.StatusBadRequest, "Latitude must be between -90 and 90")
			return
		}
		set["data.0.latitude"] = *in.Latitude
	}
	if in.Longitude != nil {
		if *in.Longitude < -180 || *in.Longitude > 180 {
			util.JsonError(w, http.StatusBadRequest, "Longitude must be between -180 and 180")
			return
		}
		set["data.0.longitude"] = *in.Longitude
	}
	if in.Description != nil {
		set["data.0.about.0.description"] = strings.TrimSpace(*in.Description)
	}
	if in.AveragePrice != nil {
		if *in.AveragePrice < 0 {
			util.JsonError(w, http.StatusBadRequest, "Average price cannot be negative")
			return
		}
		set["data.0.about.0.averagePrice"] = *in.AveragePrice
	}
	if in.Phone != nil {
		set["data.0.about.0.phone"] = strings.TrimSpace(*in.Phone)
	}
	if in.Email != nil {
		email := strings.TrimSpace(*in.Email)
		if email != "" {
			normalized, ok := util.NormalizeEmail(email)
			if !ok {
				util.JsonError(w, http.StatusBadRequest, "Enter a valid restaurant email")
				return
			}
			email = normalized
		}
		set["data.0.about.0.email"] = email
	}
	if in.HrsOfOperation != nil {
		set["data.0.about.0.hrsOfOperation"] = strings.TrimSpace(*in.HrsOfOperation)
	}
	if in.Cuisines != nil {
		set["data.0.cuisines"] = cleanList(*in.Cuisines, 12)
	}
	if in.Amenities != nil {
		set["data.0.amenities"] = cleanList(*in.Amenities, 20)
	}
	if in.PaymentMethods != nil {
		methods := cleanList(*in.PaymentMethods, 8)
		if len(methods) == 0 {
			util.JsonError(w, http.StatusBadRequest, "Select at least one payment method")
			return
		}
		set["data.0.paymentMethods"] = methods
	}
	if in.TaxRate != nil {
		rate, ok := percentToRate(*in.TaxRate)
		if !ok {
			util.JsonError(w, http.StatusBadRequest, "Tax rate must be between 0 and 100%")
			return
		}
		set["data.0.taxRate"] = rate
	}
	if in.ServiceCharge != nil {
		rate, ok := percentToRate(*in.ServiceCharge)
		if !ok {
			util.JsonError(w, http.StatusBadRequest, "Service charge must be between 0 and 100%")
			return
		}
		set["data.0.serviceCharge"] = rate
	}
	if in.OpeningHours != nil {
		hours := normalizeOpeningHours(*in.OpeningHours)
		if err := validateOpeningHours(hours); err != nil {
			util.JsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		set["data.0.openingHours"] = hours
		// Keep the one-line summary diners see in step with the schedule.
		set["data.0.about.0.hrsOfOperation"] = summarizeHours(hours)
	}
	if in.AcceptingReservations != nil {
		set["data.0.acceptingReservations"] = *in.AcceptingReservations
	}

	if len(set) == 0 {
		util.JsonResponse(w, http.StatusOK, map[string]string{"message": "No changes provided"})
		return
	}
	set["updatedAt"] = time.Now().UTC()

	res, err := h.DB.Collection("restaurants").UpdateByID(r.Context(), restaurantID, bson.M{"$set": set})
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not save restaurant settings")
		return
	}
	if res.MatchedCount == 0 {
		util.JsonError(w, http.StatusNotFound, "Restaurant not found")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Restaurant settings saved"})
}
