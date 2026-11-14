package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type PromotionHandler struct {
	*Base
}

const maxPromotions = 50

// promotionInput is what the dashboard sends when creating or editing an
// offer. Dates are calendar days (YYYY-MM-DD) in the business timezone.
type promotionInput struct {
	Title              *string  `json:"title"`
	Description        *string  `json:"description"`
	Type               *string  `json:"type"`
	DiscountPercentage *float64 `json:"discountPercentage"`
	PaymentMethod      *string  `json:"paymentMethod"`
	StartDate          *string  `json:"startDate"`
	EndDate            *string  `json:"endDate"`
	IsActive           *bool    `json:"isActive"`
	TermsAndConditions *string  `json:"termsAndConditions"`
}

func (h *PromotionHandler) parseDay(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if len(value) > 10 {
		value = value[:10]
	}
	return time.ParseInLocation("2006-01-02", value, h.location())
}

// readPromotion parses JSON or multipart ("data" JSON plus an optional
// "image") and returns the fields along with an uploaded image URL.
func (h *PromotionHandler) readPromotion(w http.ResponseWriter, r *http.Request) (*promotionInput, string, int, string) {
	var in promotionInput
	image := ""

	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+(1<<20))
		if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
			return nil, "", http.StatusBadRequest, "Error parsing form data"
		}
		if raw := r.FormValue("data"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &in); err != nil {
				return nil, "", http.StatusBadRequest, "Invalid JSON in data field"
			}
		}
		url, err := h.uploadFormImage(r, "promotions", "image", "file")
		if err != nil {
			return nil, "", http.StatusBadGateway, "Could not upload the image: " + err.Error()
		}
		image = url
		return &in, image, 0, ""
	}

	if err := util.DecodeJSON(w, r, &in); err != nil {
		return nil, "", http.StatusBadRequest, "Invalid request body"
	}
	return &in, image, 0, ""
}

// apply copies the sent fields onto a promotion and validates the result.
func (h *PromotionHandler) apply(p *models.Promotion, in *promotionInput) error {
	if in.Title != nil {
		p.Title = strings.TrimSpace(*in.Title)
	}
	if in.Description != nil {
		p.Description = strings.TrimSpace(*in.Description)
	}
	if in.Type != nil {
		p.Type = models.PromotionType(strings.ToUpper(strings.TrimSpace(*in.Type)))
	}
	if in.DiscountPercentage != nil {
		p.DiscountPercentage = *in.DiscountPercentage
	}
	if in.PaymentMethod != nil {
		p.PaymentMethod = util.NormalizePaymentMethod(*in.PaymentMethod)
	}
	if in.TermsAndConditions != nil {
		p.TermsAndConditions = strings.TrimSpace(*in.TermsAndConditions)
	}
	if in.IsActive != nil {
		p.IsActive = *in.IsActive
	}
	if in.StartDate != nil {
		day, err := h.parseDay(*in.StartDate)
		if err != nil {
			return errors.New("Start date must be a valid date")
		}
		p.StartDate = day
	}
	if in.EndDate != nil {
		day, err := h.parseDay(*in.EndDate)
		if err != nil {
			return errors.New("End date must be a valid date")
		}
		// An offer runs through the whole of its last day.
		p.EndDate = day.AddDate(0, 0, 1).Add(-time.Second)
	}

	switch {
	case p.Title == "" || len(p.Title) > 100:
		return errors.New("Give the promotion a title of up to 100 characters")
	case p.Description == "" || len(p.Description) > 500:
		return errors.New("Describe the promotion in up to 500 characters")
	case !models.ValidPromotionType(string(p.Type)):
		return errors.New("Choose a promotion type")
	case p.StartDate.IsZero() || p.EndDate.IsZero():
		return errors.New("Choose when the promotion starts and ends")
	case p.EndDate.Before(p.StartDate):
		return errors.New("The end date must be on or after the start date")
	case len(p.TermsAndConditions) > 1000:
		return errors.New("Terms can be at most 1000 characters")
	}
	if p.Type == models.PromoDiscount {
		if p.DiscountPercentage < 1 || p.DiscountPercentage > 100 {
			return errors.New("Discount must be between 1 and 100 percent")
		}
	} else {
		p.DiscountPercentage = 0
	}
	if p.Type == models.PromoPaymentOffer {
		if p.PaymentMethod == "" {
			return errors.New("Choose which payment method the offer applies to")
		}
	} else {
		p.PaymentMethod = ""
	}
	return nil
}

func (h *PromotionHandler) List(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Set up your restaurant first")
		return
	}

	opts := options.Find().SetSort(bson.M{"createdAt": -1})
	cursor, err := h.DB.Collection("promotions").Find(r.Context(), bson.M{"restaurantId": restaurantID}, opts)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not load promotions")
		return
	}
	promotions := []models.Promotion{}
	if err := cursor.All(r.Context(), &promotions); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not load promotions")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{"promotions": promotions})
}

func (h *PromotionHandler) Create(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Set up your restaurant first")
		return
	}

	count, err := h.DB.Collection("promotions").CountDocuments(r.Context(), bson.M{"restaurantId": restaurantID})
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not create the promotion")
		return
	}
	if count >= maxPromotions {
		util.JsonError(w, http.StatusBadRequest, "Delete an old promotion before adding another")
		return
	}

	in, image, status, msg := h.readPromotion(w, r)
	if in == nil {
		util.JsonError(w, status, msg)
		return
	}

	now := time.Now().UTC()
	promotion := models.Promotion{
		ID:           bson.NewObjectID(),
		RestaurantID: restaurantID,
		IsActive:     true,
		Image:        image,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := h.apply(&promotion, in); err != nil {
		util.JsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	if _, err := h.DB.Collection("promotions").InsertOne(r.Context(), promotion); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not create the promotion")
		return
	}

	util.JsonResponse(w, http.StatusCreated, map[string]interface{}{
		"message":   "Promotion created",
		"promotion": promotion,
	})
}

func (h *PromotionHandler) Update(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Set up your restaurant first")
		return
	}
	promotionID, err := bson.ObjectIDFromHex(r.PathValue("id"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid promotion ID")
		return
	}

	filter := bson.M{"_id": promotionID, "restaurantId": restaurantID}
	var promotion models.Promotion
	if err := h.DB.Collection("promotions").FindOne(r.Context(), filter).Decode(&promotion); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			util.JsonError(w, http.StatusNotFound, "Promotion not found")
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "Could not update the promotion")
		return
	}

	in, image, status, msg := h.readPromotion(w, r)
	if in == nil {
		util.JsonError(w, status, msg)
		return
	}
	if image != "" {
		promotion.Image = image
	}
	if err := h.apply(&promotion, in); err != nil {
		util.JsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	promotion.UpdatedAt = time.Now().UTC()

	if _, err := h.DB.Collection("promotions").ReplaceOne(r.Context(), filter, promotion); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not update the promotion")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message":   "Promotion updated",
		"promotion": promotion,
	})
}

func (h *PromotionHandler) Delete(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Set up your restaurant first")
		return
	}
	promotionID, err := bson.ObjectIDFromHex(r.PathValue("id"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid promotion ID")
		return
	}

	res, err := h.DB.Collection("promotions").DeleteOne(r.Context(), bson.M{"_id": promotionID, "restaurantId": restaurantID})
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not delete the promotion")
		return
	}
	if res.DeletedCount == 0 {
		util.JsonError(w, http.StatusNotFound, "Promotion not found")
		return
	}
	util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Promotion deleted"})
}

// ListPublic returns the offers diners can use today, with the restaurant
// each belongs to. ?restaurantId= narrows it to one restaurant.
func (h *PromotionHandler) ListPublic(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	filter := bson.M{
		"isActive":  true,
		"startDate": bson.M{"$lte": now},
		"endDate":   bson.M{"$gte": now},
	}
	if raw := r.URL.Query().Get("restaurantId"); raw != "" {
		id, err := bson.ObjectIDFromHex(raw)
		if err != nil {
			util.JsonError(w, http.StatusBadRequest, "Invalid Restaurant ID format")
			return
		}
		filter["restaurantId"] = id
	}

	opts := options.Find().SetSort(bson.M{"endDate": 1}).SetLimit(50)
	cursor, err := h.DB.Collection("promotions").Find(r.Context(), filter, opts)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not load promotions")
		return
	}
	var promotions []models.Promotion
	if err := cursor.All(r.Context(), &promotions); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not load promotions")
		return
	}

	ids := bson.A{}
	seen := map[bson.ObjectID]bool{}
	for _, p := range promotions {
		if !seen[p.RestaurantID] {
			seen[p.RestaurantID] = true
			ids = append(ids, p.RestaurantID)
		}
	}

	type restaurantBrief struct {
		Name     string
		Image    string
		Location string
	}
	briefs := map[bson.ObjectID]restaurantBrief{}
	if len(ids) > 0 {
		projection := bson.M{"data.restaurantName": 1, "data.image": 1, "data.location": 1}
		cursor, err := h.DB.Collection("restaurants").Find(r.Context(), bson.M{"_id": bson.M{"$in": ids}}, options.Find().SetProjection(projection))
		if err == nil {
			var restaurants []models.Restaurant
			if cursor.All(r.Context(), &restaurants) == nil {
				for _, rest := range restaurants {
					if len(rest.Data) > 0 {
						briefs[rest.ID] = restaurantBrief{
							Name:     rest.Data[0].RestaurantName,
							Image:    rest.Data[0].Image,
							Location: rest.Data[0].Location,
						}
					}
				}
			}
		}
	}

	out := make([]map[string]interface{}, 0, len(promotions))
	for _, p := range promotions {
		brief, ok := briefs[p.RestaurantID]
		if !ok {
			continue // the restaurant no longer exists
		}
		image := p.Image
		if image == "" {
			image = brief.Image
		}
		out = append(out, map[string]interface{}{
			"_id":                p.ID.Hex(),
			"title":              p.Title,
			"description":        p.Description,
			"type":               p.Type,
			"discountPercentage": p.DiscountPercentage,
			"paymentMethod":      p.PaymentMethod,
			"startDate":          p.StartDate,
			"endDate":            p.EndDate,
			"termsAndConditions": p.TermsAndConditions,
			"image":              image,
			"restaurantId":       p.RestaurantID.Hex(),
			"restaurantName":     brief.Name,
			"restaurantImage":    brief.Image,
			"restaurantLocation": brief.Location,
		})
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{"promotions": out})
}
