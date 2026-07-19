package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"swiftab/server/internal/middleware"
	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type TableHandler struct {
	DB *mongo.Database
}

func (h *TableHandler) getRestaurantID(r *http.Request) (bson.ObjectID, error) {
	rid, ok := r.Context().Value(middleware.RestaurantIDKey).(bson.ObjectID)
	if !ok {
		return bson.NilObjectID, errors.New("missing restaurant ID")
	}
	return rid, nil
}

func (h *TableHandler) FetchRestaurantInfo(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.getRestaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Restaurant ID is required")
		return
	}

	opts := options.FindOne().SetProjection(bson.M{"diningAreas": 1})

	var layout models.RestaurantLayout
	err = h.DB.Collection("restaurantlayouts").FindOne(r.Context(), bson.M{"restaurantId": restaurantID}, opts).Decode(&layout)

	if err != nil {
		if err == mongo.ErrNoDocuments {
			util.JsonError(w, http.StatusNotFound, "Dining areas not found")
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "Error fetching restaurant layout")
		return
	}

	// Ensure we return an empty array [] instead of null if no areas exist
	if layout.DiningAreas == nil {
		layout.DiningAreas = []string{}
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"diningAreas": layout.DiningAreas,
	})
}

func (h *TableHandler) FetchResTable(w http.ResponseWriter, r *http.Request) {
	restaurantIDStr := r.PathValue("restaurantId")
	if restaurantIDStr == "" {
		util.JsonError(w, http.StatusBadRequest, "Invalid or missing restaurantId")
		return
	}

	restID, err := bson.ObjectIDFromHex(restaurantIDStr)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid restaurantId format")
		return
	}

	var layout models.RestaurantLayout
	err = h.DB.Collection("restaurantlayouts").FindOne(r.Context(), bson.M{"restaurantId": restID}).Decode(&layout)

	if err != nil {
		if err == mongo.ErrNoDocuments {
			util.JsonError(w, http.StatusNotFound, "Tables not found for this restaurant")
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "Server error")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message":              "Fetched tables successfully",
		"restaurantLayoutData": layout,
	})
}

func (h *TableHandler) FetchRestaurantTables(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.getRestaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusNotFound, "restaurant id not found")
		return
	}

	var layout models.RestaurantLayout
	err = h.DB.Collection("restaurantlayouts").FindOne(r.Context(), bson.M{"restaurantId": restaurantID}).Decode(&layout)

	if err != nil {
		if err == mongo.ErrNoDocuments {
			util.JsonError(w, http.StatusNotFound, "tables not found for this restaurant")
			return
		}
		util.JsonError(w, http.StatusInternalServerError, "Server error")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message":              "fetched tables",
		"restaurantLayoutData": layout,
	})
}

func (h *TableHandler) SaveLayoutInfo(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.getRestaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Restaurant ID is required")
		return
	}

	var reqBody struct {
		DiningAreas   []string `json:"diningAreas"`
		TableCapacity int      `json:"tableCapacity"` // Maps incoming JSON
		TotalTables   int      `json:"totalTables"`
	}

	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	defer r.Body.Close()

	if len(reqBody.DiningAreas) == 0 {
		util.JsonError(w, http.StatusBadRequest, "Dining areas are required")
		return
	}

	// Build the Upsert query using bson.M
	filter := bson.M{"restaurantId": restaurantID}
	update := bson.M{
		"$set": bson.M{
			"restaurantId":  restaurantID,
			"diningAreas":   reqBody.DiningAreas,
			"totalCapacity": reqBody.TableCapacity,
			"totalTables":   reqBody.TotalTables,
		},
	}

	opts := options.FindOneAndUpdate().
		SetUpsert(true).
		SetReturnDocument(options.After)

	var layoutInfo models.RestaurantLayout
	err = h.DB.Collection("restaurantlayouts").FindOneAndUpdate(r.Context(), filter, update, opts).Decode(&layoutInfo)

	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message": "Layout updated successfully",
		"layout": map[string]interface{}{
			"diningAreas":   layoutInfo.DiningAreas,
			"totalCapacity": layoutInfo.TotalCapacity,
			"totalTables":   layoutInfo.TotalTables,
		},
	})
}

func (h *TableHandler) SaveTables(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.getRestaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Restaurant ID is required")
		return
	}

	var reqBody struct {
		Tables []models.Table `json:"tables"`
	}

	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	defer r.Body.Close()

	if reqBody.Tables == nil {
		util.JsonError(w, http.StatusBadRequest, "Tables data must be an array")
		return
	}

	filter := bson.M{"restaurantId": restaurantID}
	update := bson.M{
		"$set": bson.M{
			"restaurantId":  restaurantID,
			"tablePosition": reqBody.Tables,
		},
	}

	opts := options.FindOneAndUpdate().
		SetUpsert(true).
		SetReturnDocument(options.After)

	var layout models.RestaurantLayout
	err = h.DB.Collection("restaurantlayouts").FindOneAndUpdate(r.Context(), filter, update, opts).Decode(&layout)

	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Failed to save tables")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message": "Tables saved successfully",
		"tables":  layout.TablePosition,
	})
}
