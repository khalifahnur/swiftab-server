package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"swiftab/server/internal/middleware"
	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type MenuHandler struct {
	DB    *mongo.Database
	Image *util.CloudinaryService
}

func (h *MenuHandler) getRestaurantID(r *http.Request) (bson.ObjectID, error) {
	rid, ok := r.Context().Value(middleware.RestaurantIDKey).(bson.ObjectID)
	if !ok {
		return bson.NilObjectID, errors.New("missing restaurant ID")
	}
	return rid, nil
}

func (h *MenuHandler) AddMenu(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.getRestaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusUnauthorized, "Restaurant ID missing")
		return
	}

	menuType := r.PathValue("menuType")

	if menuType != "breakfast" && menuType != "lunch" && menuType != "dinner" {
		util.JsonError(w, http.StatusBadRequest, "Invalid menu type")
		return
	}

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Error parsing form data")
		return
	}

	name := r.FormValue("name")
	description := r.FormValue("description")
	costStr := r.FormValue("cost")

	if name == "" || costStr == "" {
		util.JsonError(w, http.StatusBadRequest, "Invalid menu item data")
		return
	}

	cost, err := strconv.ParseFloat(costStr, 64)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Cost must be a valid number")
		return
	}

	var rate float64 = 0
	if rateStr := r.FormValue("rate"); rateStr != "" {
		rate, _ = strconv.ParseFloat(rateStr, 64)
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Image file is required")
		return
	}
	defer file.Close()

	imageURL, err := h.Image.UploadMenuImage(r.Context(), file)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Failed to upload image")
		return
	}

	newItem := models.MenuItem{
		ID:          bson.NewObjectID().Hex(),
		Name:        name,
		Description: description,
		Cost:        cost,
		Rate:        rate,
		Image:       imageURL,
	}

	updatePath := fmt.Sprintf("data.0.menu.%s", menuType)
	update := bson.M{"$push": bson.M{updatePath: newItem}}

	res, err := h.DB.Collection("restaurants").UpdateByID(r.Context(), restaurantID, update)
	if err != nil || res.MatchedCount == 0 {
		util.JsonError(w, http.StatusNotFound, "Restaurant not found")
		return
	}

	util.JsonResponse(w, http.StatusCreated, map[string]string{
		"message": "Menu item added successfully",
	})
}

func (h *MenuHandler) UpdateMenuItem(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.getRestaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusUnauthorized, "Restaurant ID missing")
		return
	}

	menuType := r.PathValue("menuType")
	itemIDStr := r.PathValue("itemId")

	if menuType != "breakfast" && menuType != "lunch" && menuType != "dinner" {
		util.JsonError(w, http.StatusBadRequest, "Invalid menu type")
		return
	}

	var reqBody struct {
		Menu        string  `json:"menu"`
		Description string  `json:"description"`
		Price       float64 `json:"price"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	defer r.Body.Close()

	updatePath := fmt.Sprintf("data.0.menu.%s", menuType)
	setFields := bson.M{}

	if reqBody.Menu != "" {
		setFields[fmt.Sprintf("%s.$.name", updatePath)] = reqBody.Menu
	}
	if reqBody.Description != "" {
		setFields[fmt.Sprintf("%s.$.description", updatePath)] = reqBody.Description
	}
	if reqBody.Price != 0 {
		setFields[fmt.Sprintf("%s.$.cost", updatePath)] = reqBody.Price
	}

	if len(setFields) == 0 {
		util.JsonResponse(w, http.StatusOK, map[string]string{"message": "No changes provided"})
		return
	}

	filter := bson.M{
		"_id":                             restaurantID,
		fmt.Sprintf("%s._id", updatePath): itemIDStr,
	}
	update := bson.M{"$set": setFields}

	res, err := h.DB.Collection("restaurants").UpdateOne(r.Context(), filter, update)
	if err != nil || res.MatchedCount == 0 {
		util.JsonError(w, http.StatusNotFound, "Menu item not found or update failed")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{
		"message": "Menu item updated successfully",
	})
}

func (h *MenuHandler) DeleteMenuItem(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.getRestaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusUnauthorized, "Restaurant ID missing")
		return
	}

	menuType := r.PathValue("menuType")
	itemIDStr := r.PathValue("itemId")

	if menuType != "breakfast" && menuType != "lunch" && menuType != "dinner" {
		util.JsonError(w, http.StatusBadRequest, "Invalid menu type")
		return
	}

	updatePath := fmt.Sprintf("data.0.menu.%s", menuType)

	update := bson.M{
		"$pull": bson.M{
			updatePath: bson.M{"_id": itemIDStr},
		},
	}

	res, err := h.DB.Collection("restaurants").UpdateByID(r.Context(), restaurantID, update)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error deleting menu item")
		return
	}

	if res.MatchedCount == 0 {
		util.JsonError(w, http.StatusNotFound, "Restaurant not found")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{
		"message": "Menu item deleted successfully",
	})
}

func (h *MenuHandler) GetMenu(w http.ResponseWriter, r *http.Request) {
	var targetRestID bson.ObjectID
	authID, err := h.getRestaurantID(r)
	if err == nil {
		targetRestID = authID
	} else {
		paramID := r.PathValue("restaurantId")
		if paramID == "" {
			util.JsonError(w, http.StatusBadRequest, "No restaurantId provided")
			return
		}
		targetRestID, err = bson.ObjectIDFromHex(paramID)
		if err != nil {
			util.JsonError(w, http.StatusBadRequest, "Invalid Restaurant ID format")
			return
		}
	}
	opts := options.FindOne().SetProjection(bson.M{"data.menu": 1})

	var restaurant models.Restaurant
	err = h.DB.Collection("restaurants").FindOne(r.Context(), bson.M{"_id": targetRestID}, opts).Decode(&restaurant)

	if err != nil {
		if err == mongo.ErrNoDocuments {
			util.JsonError(w, http.StatusNotFound, "Restaurant not found")
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "Error retrieving menu")
		return
	}

	if len(restaurant.Data) == 0 {
		util.JsonError(w, http.StatusNotFound, "Menu not found for this restaurant")
		return
	}

	util.JsonResponse(w, http.StatusOK, restaurant.Data[0].Menu)
}
