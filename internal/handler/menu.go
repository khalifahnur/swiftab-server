package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type MenuHandler struct {
	*Base
}

const maxMenuCategories = 20

// menuSection is one menu category with its items, in display order.
type menuSection struct {
	Key   string            `json:"key"`
	Label string            `json:"label"`
	Items []models.MenuItem `json:"items"`
}

// menuPath is the document path of a category's item array. Callers must have
// validated key with util.ValidCategoryKey first.
func menuPath(key string) string {
	return "data.0.menu." + key
}

// loadMenu returns the restaurant's first data block, which holds the menu.
func (h *MenuHandler) loadMenu(r *http.Request, restaurantID bson.ObjectID) (*models.RestaurantData, int, string) {
	restaurant, err := h.loadRestaurant(r.Context(), restaurantID)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, http.StatusNotFound, "Restaurant not found"
		}
		return nil, http.StatusInternalServerError, "Error retrieving menu"
	}
	if len(restaurant.Data) == 0 {
		return nil, http.StatusNotFound, "Menu not found for this restaurant"
	}
	return &restaurant.Data[0], 0, ""
}

// sections builds the ordered category list. With onlyAvailable set, items
// the kitchen has switched off are left out (what diners see).
func sections(data *models.RestaurantData, onlyAvailable bool) []menuSection {
	cats := data.Categories()
	out := make([]menuSection, 0, len(cats))
	for _, c := range cats {
		items := make([]models.MenuItem, 0, len(data.Menu[c.Key]))
		for _, item := range data.Menu[c.Key] {
			if onlyAvailable && !item.IsAvailable() {
				continue
			}
			items = append(items, item)
		}
		out = append(out, menuSection{Key: c.Key, Label: c.Label, Items: items})
	}
	return out
}

// menuMap renders sections in the legacy {"breakfast": [...]} shape.
func menuMap(secs []menuSection) map[string][]models.MenuItem {
	out := make(map[string][]models.MenuItem, len(secs))
	for _, s := range secs {
		out[s.Key] = s.Items
	}
	return out
}

func (h *MenuHandler) hasCategory(data *models.RestaurantData, key string) bool {
	for _, c := range data.Categories() {
		if c.Key == key {
			return true
		}
	}
	return false
}

// categoriesWith returns the restaurant's effective category list plus any
// extra ones. Saving it back is what first stores the list for a restaurant
// created before categories were configurable, so its sections are kept.
func categoriesWith(data *models.RestaurantData, extra ...models.MenuCategory) []models.MenuCategory {
	return append(data.Categories(), extra...)
}

// --- Dashboard: categories -------------------------------------------------

// GetAdminMenu returns every category with all of its items, including the
// ones currently switched off.
func (h *MenuHandler) GetAdminMenu(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Set up your restaurant first")
		return
	}
	data, status, msg := h.loadMenu(r, restaurantID)
	if data == nil {
		util.JsonError(w, status, msg)
		return
	}
	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"categories": sections(data, false),
	})
}

func (h *MenuHandler) AddCategory(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Set up your restaurant first")
		return
	}

	var reqBody struct {
		Label string `json:"label"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	label := strings.TrimSpace(reqBody.Label)
	if label == "" || len(label) > 40 {
		util.JsonError(w, http.StatusBadRequest, "Give the category a name of up to 40 characters")
		return
	}
	key := util.Slugify(label)
	// "categories" is part of the API's own URLs for this resource.
	if !util.ValidCategoryKey(key) || key == "categories" {
		util.JsonError(w, http.StatusBadRequest, "Choose a different category name that starts with a letter")
		return
	}

	data, status, msg := h.loadMenu(r, restaurantID)
	if data == nil {
		util.JsonError(w, status, msg)
		return
	}
	if h.hasCategory(data, key) {
		util.JsonError(w, http.StatusConflict, "A category with that name already exists")
		return
	}
	if len(data.Categories()) >= maxMenuCategories {
		util.JsonError(w, http.StatusBadRequest, fmt.Sprintf("A menu can have at most %d categories", maxMenuCategories))
		return
	}

	category := models.MenuCategory{Key: key, Label: label}
	_, err = h.DB.Collection("restaurants").UpdateByID(r.Context(), restaurantID, bson.M{"$set": bson.M{
		"data.0.menuCategories": categoriesWith(data, category),
		menuPath(key):           []models.MenuItem{},
		"updatedAt":             time.Now().UTC(),
	}})
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not add the category")
		return
	}

	util.JsonResponse(w, http.StatusCreated, map[string]interface{}{
		"message":  "Category added",
		"category": category,
	})
}

func (h *MenuHandler) RenameCategory(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Set up your restaurant first")
		return
	}

	key := r.PathValue("key")
	var reqBody struct {
		Label string `json:"label"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	label := strings.TrimSpace(reqBody.Label)
	if label == "" || len(label) > 40 {
		util.JsonError(w, http.StatusBadRequest, "Give the category a name of up to 40 characters")
		return
	}

	data, status, msg := h.loadMenu(r, restaurantID)
	if data == nil {
		util.JsonError(w, status, msg)
		return
	}
	cats := data.Categories()
	found := false
	for i := range cats {
		if cats[i].Key == key {
			cats[i].Label = label
			found = true
		}
	}
	if !found {
		util.JsonError(w, http.StatusNotFound, "Category not found")
		return
	}

	if _, err := h.DB.Collection("restaurants").UpdateByID(r.Context(), restaurantID, bson.M{"$set": bson.M{
		"data.0.menuCategories": cats,
		"updatedAt":             time.Now().UTC(),
	}}); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not rename the category")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Category renamed"})
}

// ReorderCategories sets the display order. Keys that are omitted keep their
// relative order after the ones that are listed.
func (h *MenuHandler) ReorderCategories(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Set up your restaurant first")
		return
	}

	var reqBody struct {
		Order []string `json:"order"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	data, status, msg := h.loadMenu(r, restaurantID)
	if data == nil {
		util.JsonError(w, status, msg)
		return
	}

	current := data.Categories()
	byKey := make(map[string]models.MenuCategory, len(current))
	for _, c := range current {
		byKey[c.Key] = c
	}
	ordered := make([]models.MenuCategory, 0, len(current))
	placed := map[string]bool{}
	for _, key := range reqBody.Order {
		if c, ok := byKey[key]; ok && !placed[key] {
			placed[key] = true
			ordered = append(ordered, c)
		}
	}
	for _, c := range current {
		if !placed[c.Key] {
			ordered = append(ordered, c)
		}
	}

	if _, err := h.DB.Collection("restaurants").UpdateByID(r.Context(), restaurantID, bson.M{"$set": bson.M{
		"data.0.menuCategories": ordered,
		"updatedAt":             time.Now().UTC(),
	}}); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not reorder categories")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Categories reordered"})
}

// DeleteCategory removes a category. A category that still has dishes is only
// removed when the request confirms it with ?force=true.
func (h *MenuHandler) DeleteCategory(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Set up your restaurant first")
		return
	}

	key := r.PathValue("key")
	if !util.ValidCategoryKey(key) {
		util.JsonError(w, http.StatusBadRequest, "Invalid category")
		return
	}

	data, status, msg := h.loadMenu(r, restaurantID)
	if data == nil {
		util.JsonError(w, status, msg)
		return
	}
	if !h.hasCategory(data, key) {
		util.JsonError(w, http.StatusNotFound, "Category not found")
		return
	}
	if n := len(data.Menu[key]); n > 0 && r.URL.Query().Get("force") != "true" {
		util.JsonError(w, http.StatusConflict, fmt.Sprintf("This category still has %d dish(es). Move or delete them first.", n))
		return
	}

	remaining := make([]models.MenuCategory, 0)
	for _, c := range data.Categories() {
		if c.Key != key {
			remaining = append(remaining, c)
		}
	}
	if len(remaining) == 0 {
		util.JsonError(w, http.StatusBadRequest, "A menu needs at least one category")
		return
	}

	if _, err := h.DB.Collection("restaurants").UpdateByID(r.Context(), restaurantID, bson.M{
		"$set":   bson.M{"data.0.menuCategories": remaining, "updatedAt": time.Now().UTC()},
		"$unset": bson.M{menuPath(key): ""},
	}); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Could not delete the category")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{"message": "Category deleted"})
}

// --- Dashboard: items ------------------------------------------------------

// itemInput collects the dish fields from either a multipart form or JSON.
// The dashboard historically sent "menu" and "price" when editing; both
// spellings are accepted.
type itemInput struct {
	Name        *string
	Description *string
	Cost        *float64
	Rate        *float64
	Available   *bool
	Tags        *[]string
	PrepTime    *int
	Category    *string
	ImageURL    string
}

func parseTags(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}
	}
	var tags []string
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &tags); err == nil {
			return cleanList(tags, 8)
		}
	}
	return cleanList(strings.Split(raw, ","), 8)
}

// readItemInput parses a create/update request. For multipart requests it
// also uploads the image, if one was attached.
func (h *MenuHandler) readItemInput(w http.ResponseWriter, r *http.Request) (*itemInput, int, string) {
	in := &itemInput{}

	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+(1<<20))
		if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
			return nil, http.StatusBadRequest, "Error parsing form data"
		}
		form := func(names ...string) (string, bool) {
			for _, n := range names {
				if vals, ok := r.MultipartForm.Value[n]; ok && len(vals) > 0 {
					return vals[0], true
				}
			}
			return "", false
		}

		if v, ok := form("name", "menu"); ok {
			in.Name = &v
		}
		if v, ok := form("description"); ok {
			in.Description = &v
		}
		if v, ok := form("cost", "price"); ok && strings.TrimSpace(v) != "" {
			cost, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return nil, http.StatusBadRequest, "Price must be a valid number"
			}
			in.Cost = &cost
		}
		if v, ok := form("rate"); ok && strings.TrimSpace(v) != "" {
			if rate, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				in.Rate = &rate
			}
		}
		if v, ok := form("available"); ok && v != "" {
			available := v == "true" || v == "1"
			in.Available = &available
		}
		if v, ok := form("tags"); ok {
			tags := parseTags(v)
			in.Tags = &tags
		}
		if v, ok := form("prepTime"); ok && strings.TrimSpace(v) != "" {
			if mins, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				in.PrepTime = &mins
			}
		}
		if v, ok := form("category"); ok && v != "" {
			in.Category = &v
		}

		url, err := h.uploadFormImage(r, "menu", "file", "image")
		if err != nil {
			return nil, http.StatusBadGateway, "Could not upload the image: " + err.Error()
		}
		in.ImageURL = url
		if in.ImageURL == "" {
			// The dashboard may send an image URL instead of a file.
			if v, ok := form("imageUrl", "file", "image"); ok && strings.HasPrefix(v, "https://") {
				in.ImageURL = v
			}
		}
		return in, 0, ""
	}

	var body struct {
		Name        *string   `json:"name"`
		Menu        *string   `json:"menu"`
		Description *string   `json:"description"`
		Cost        *float64  `json:"cost"`
		Price       *float64  `json:"price"`
		Rate        *float64  `json:"rate"`
		Available   *bool     `json:"available"`
		Tags        *[]string `json:"tags"`
		PrepTime    *int      `json:"prepTime"`
		Category    *string   `json:"category"`
		ImageURL    string    `json:"imageUrl"`
	}
	if err := util.DecodeJSON(w, r, &body); err != nil {
		return nil, http.StatusBadRequest, "Invalid JSON body"
	}
	in.Name = body.Name
	if in.Name == nil {
		in.Name = body.Menu
	}
	in.Description = body.Description
	in.Cost = body.Cost
	if in.Cost == nil {
		in.Cost = body.Price
	}
	in.Rate = body.Rate
	in.Available = body.Available
	if body.Tags != nil {
		tags := cleanList(*body.Tags, 8)
		in.Tags = &tags
	}
	in.PrepTime = body.PrepTime
	in.Category = body.Category
	if strings.HasPrefix(body.ImageURL, "https://") {
		in.ImageURL = body.ImageURL
	}
	return in, 0, ""
}

func (h *MenuHandler) AddMenu(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Set up your restaurant first")
		return
	}

	menuType := r.PathValue("menuType")
	if !util.ValidCategoryKey(menuType) {
		util.JsonError(w, http.StatusBadRequest, "Invalid menu type")
		return
	}

	data, status, msg := h.loadMenu(r, restaurantID)
	if data == nil {
		util.JsonError(w, status, msg)
		return
	}
	if !h.hasCategory(data, menuType) {
		util.JsonError(w, http.StatusBadRequest, "That menu category does not exist")
		return
	}

	in, status, msg := h.readItemInput(w, r)
	if in == nil {
		util.JsonError(w, status, msg)
		return
	}

	name := ""
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if name == "" || in.Cost == nil {
		util.JsonError(w, http.StatusBadRequest, "A dish needs a name and a price")
		return
	}
	if *in.Cost < 0 {
		util.JsonError(w, http.StatusBadRequest, "Price cannot be negative")
		return
	}

	newItem := models.MenuItem{
		ID:        bson.NewObjectID(),
		Name:      name,
		Cost:      *in.Cost,
		Image:     in.ImageURL,
		Available: boolPtr(true),
	}
	if in.Description != nil {
		newItem.Description = strings.TrimSpace(*in.Description)
	}
	if in.Rate != nil && *in.Rate >= 0 && *in.Rate <= 5 {
		newItem.Rate = *in.Rate
	}
	if in.Available != nil {
		newItem.Available = in.Available
	}
	if in.Tags != nil {
		newItem.Tags = *in.Tags
	}
	if in.PrepTime != nil && *in.PrepTime > 0 {
		newItem.PrepTime = *in.PrepTime
	}

	// $push needs an array; a category registered before it held any dish may
	// have no array yet, so create it in the same update when missing.
	update := bson.M{"$push": bson.M{menuPath(menuType): newItem}}
	if _, exists := data.Menu[menuType]; !exists {
		update = bson.M{"$set": bson.M{menuPath(menuType): []models.MenuItem{newItem}}}
	}

	res, err := h.DB.Collection("restaurants").UpdateByID(r.Context(), restaurantID, update)
	if err != nil || res.MatchedCount == 0 {
		util.JsonError(w, http.StatusInternalServerError, "Could not add the dish")
		return
	}

	util.JsonResponse(w, http.StatusCreated, map[string]interface{}{
		"message": "Menu item added successfully",
		"item":    newItem,
	})
}

func (h *MenuHandler) UpdateMenuItem(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Set up your restaurant first")
		return
	}

	menuType := r.PathValue("menuType")
	if !util.ValidCategoryKey(menuType) {
		util.JsonError(w, http.StatusBadRequest, "Invalid menu type")
		return
	}
	itemID, err := bson.ObjectIDFromHex(r.PathValue("itemId"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid Item ID format")
		return
	}

	in, status, msg := h.readItemInput(w, r)
	if in == nil {
		util.JsonError(w, status, msg)
		return
	}

	data, status, msg := h.loadMenu(r, restaurantID)
	if data == nil {
		util.JsonError(w, status, msg)
		return
	}

	var item *models.MenuItem
	for i := range data.Menu[menuType] {
		if data.Menu[menuType][i].ID == itemID {
			item = &data.Menu[menuType][i]
			break
		}
	}
	if item == nil {
		util.JsonError(w, http.StatusNotFound, "Menu item not found")
		return
	}

	changed := false
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			util.JsonError(w, http.StatusBadRequest, "A dish needs a name")
			return
		}
		item.Name = name
		changed = true
	}
	if in.Description != nil {
		item.Description = strings.TrimSpace(*in.Description)
		changed = true
	}
	if in.Cost != nil {
		if *in.Cost < 0 {
			util.JsonError(w, http.StatusBadRequest, "Price cannot be negative")
			return
		}
		item.Cost = *in.Cost
		changed = true
	}
	if in.Available != nil {
		item.Available = in.Available
		changed = true
	}
	if in.Tags != nil {
		item.Tags = *in.Tags
		changed = true
	}
	if in.PrepTime != nil && *in.PrepTime >= 0 {
		item.PrepTime = *in.PrepTime
		changed = true
	}
	if in.ImageURL != "" {
		item.Image = in.ImageURL
		changed = true
	}

	target := menuType
	if in.Category != nil && *in.Category != menuType {
		target = *in.Category
		if !util.ValidCategoryKey(target) || !h.hasCategory(data, target) {
			util.JsonError(w, http.StatusBadRequest, "That menu category does not exist")
			return
		}
		changed = true
	}

	if !changed {
		util.JsonResponse(w, http.StatusOK, map[string]string{"message": "No changes provided"})
		return
	}

	coll := h.DB.Collection("restaurants")
	sourcePath := menuPath(menuType)

	if target == menuType {
		res, err := coll.UpdateOne(r.Context(),
			bson.M{"_id": restaurantID, sourcePath + "._id": itemID},
			bson.M{"$set": bson.M{sourcePath + ".$": item}},
		)
		if err != nil || res.MatchedCount == 0 {
			util.JsonError(w, http.StatusNotFound, "Menu item not found or update failed")
			return
		}
	} else {
		// Moving between categories: add to the new section first so a failure
		// part-way never loses the dish, then remove it from the old one.
		push := bson.M{"$push": bson.M{menuPath(target): item}}
		if _, exists := data.Menu[target]; !exists {
			push = bson.M{"$set": bson.M{menuPath(target): []models.MenuItem{*item}}}
		}
		if _, err := coll.UpdateByID(r.Context(), restaurantID, push); err != nil {
			util.JsonError(w, http.StatusInternalServerError, "Could not move the dish")
			return
		}
		if _, err := coll.UpdateByID(r.Context(), restaurantID,
			bson.M{"$pull": bson.M{sourcePath: bson.M{"_id": itemID}}},
		); err != nil {
			util.JsonError(w, http.StatusInternalServerError, "The dish was copied to the new category but could not be removed from the old one")
			return
		}
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message": "Menu item updated successfully",
		"item":    item,
	})
}

func (h *MenuHandler) DeleteMenuItem(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Set up your restaurant first")
		return
	}

	menuType := r.PathValue("menuType")
	if !util.ValidCategoryKey(menuType) {
		util.JsonError(w, http.StatusBadRequest, "Invalid menu type")
		return
	}

	itemID, err := bson.ObjectIDFromHex(r.PathValue("itemId"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid Item ID format")
		return
	}

	res, err := h.DB.Collection("restaurants").UpdateByID(r.Context(), restaurantID, bson.M{
		"$pull": bson.M{menuPath(menuType): bson.M{"_id": itemID}},
	})
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error deleting menu item")
		return
	}
	if res.MatchedCount == 0 {
		util.JsonError(w, http.StatusNotFound, "Restaurant not found")
		return
	}
	if res.ModifiedCount == 0 {
		util.JsonError(w, http.StatusNotFound, "Menu item not found in this category")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]string{
		"message": "Menu item deleted successfully",
	})
}

// --- Read-only menu for apps ----------------------------------------------

// GetMenu returns the menu as {"<category>": [items]} for the restaurant in
// the caller's scope.
func (h *MenuHandler) GetMenu(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusUnauthorized, "Restaurant not authenticated")
		return
	}
	data, status, msg := h.loadMenu(r, restaurantID)
	if data == nil {
		util.JsonError(w, status, msg)
		return
	}
	util.JsonResponse(w, http.StatusOK, menuMap(sections(data, false)))
}

func (h *MenuHandler) publicMenu(w http.ResponseWriter, r *http.Request) []menuSection {
	restaurantID, err := bson.ObjectIDFromHex(r.PathValue("restaurantId"))
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid Restaurant ID format")
		return nil
	}
	data, status, msg := h.loadMenu(r, restaurantID)
	if data == nil {
		util.JsonError(w, status, msg)
		return nil
	}
	return sections(data, true)
}

// GetMenuByRestaurantID returns what diners can order, keyed by category.
func (h *MenuHandler) GetMenuByRestaurantID(w http.ResponseWriter, r *http.Request) {
	if secs := h.publicMenu(w, r); secs != nil {
		util.JsonResponse(w, http.StatusOK, menuMap(secs))
	}
}

// GetMenuSections returns what diners can order as an ordered list of
// categories, which is what an app needs to render tabs for any menu.
func (h *MenuHandler) GetMenuSections(w http.ResponseWriter, r *http.Request) {
	if secs := h.publicMenu(w, r); secs != nil {
		util.JsonResponse(w, http.StatusOK, map[string]interface{}{"categories": secs})
	}
}
