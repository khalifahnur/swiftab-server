package handler

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type TableHandler struct {
	*Base
}

const (
	maxFloors = 30
	maxTables = 500
	maxChairs = 20
)

func layoutResponse(message string, layout *models.RestaurantLayout) map[string]interface{} {
	return map[string]interface{}{
		"message":              message,
		"restaurantLayoutData": layout,
	}
}

// FetchRestaurantInfo returns the restaurant's dining areas.
func (h *TableHandler) FetchRestaurantInfo(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Restaurant ID is required")
		return
	}

	layout, err := h.loadLayout(r.Context(), restaurantID)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Error fetching restaurant layout")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"diningAreas": layout.DiningAreas,
		"floors":      layout.Floors,
	})
}

// FetchResTable returns a restaurant's floor plan to the apps. The restaurant
// comes from the URL, or from the caller's scope on the legacy route.
func (h *TableHandler) FetchResTable(w http.ResponseWriter, r *http.Request) {
	var restaurantID bson.ObjectID
	if raw := r.PathValue("restaurantId"); raw != "" {
		id, err := bson.ObjectIDFromHex(raw)
		if err != nil {
			util.JsonError(w, http.StatusBadRequest, "Invalid or missing restaurantId")
			return
		}
		restaurantID = id
	} else {
		id, err := h.restaurantID(r)
		if err != nil {
			util.JsonError(w, http.StatusBadRequest, "Invalid or missing restaurantId")
			return
		}
		restaurantID = id
	}

	layout, err := h.loadLayout(r.Context(), restaurantID)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Server error")
		return
	}

	message := "Fetched tables successfully"
	if len(layout.TablePosition) == 0 {
		message = "No tables found yet"
	}
	util.JsonResponse(w, http.StatusOK, layoutResponse(message, layout))
}

// FetchRestaurantTables returns the floor plan for the dashboard editor.
func (h *TableHandler) FetchRestaurantTables(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Set up your restaurant first")
		return
	}

	layout, err := h.loadLayout(r.Context(), restaurantID)
	if err != nil {
		log.Printf("error fetching res tables:%v", err)
		util.JsonError(w, http.StatusInternalServerError, "Server error")
		return
	}

	message := "fetched tables"
	if len(layout.TablePosition) == 0 {
		message = "no tables found for this restaurant"
	}
	util.JsonResponse(w, http.StatusOK, layoutResponse(message, layout))
}

// nextFloorID returns an id no existing floor uses. Ids stay numeric strings
// so a new floor continues the "1", "2", ... sequence older layouts rely on.
func nextFloorID(floors []models.Floor) string {
	max := 0
	for _, f := range floors {
		if n, err := strconv.Atoi(f.ID); err == nil && n > max {
			max = n
		}
	}
	return strconv.Itoa(max + 1)
}

// cleanFloors validates floors sent by the editor and gives an id to any
// floor that arrived without one.
func cleanFloors(in []models.Floor) ([]models.Floor, error) {
	if len(in) > maxFloors {
		return nil, fmt.Errorf("a restaurant can have at most %d floors", maxFloors)
	}
	out := make([]models.Floor, 0, len(in))
	ids := map[string]bool{}
	names := map[string]bool{}
	for _, f := range in {
		f.Name = strings.TrimSpace(f.Name)
		f.ID = strings.TrimSpace(f.ID)
		if f.Name == "" || len(f.Name) > 60 {
			return nil, fmt.Errorf("every floor needs a name of up to 60 characters")
		}
		if names[strings.ToLower(f.Name)] {
			return nil, fmt.Errorf("two floors are both called %q", f.Name)
		}
		names[strings.ToLower(f.Name)] = true
		if f.ID == "" || ids[f.ID] || len(f.ID) > 40 {
			f.ID = nextFloorID(out)
			for ids[f.ID] {
				f.ID += "x"
			}
		}
		ids[f.ID] = true
		out = append(out, f)
	}
	return out, nil
}

// cleanTables validates tables sent by the editor against the floors they
// claim to sit on.
func cleanTables(in []models.Table, floors []models.Floor) ([]models.Table, error) {
	if len(in) > maxTables {
		return nil, fmt.Errorf("a restaurant can have at most %d tables", maxTables)
	}
	floorIDs := make(map[string]bool, len(floors))
	for _, f := range floors {
		floorIDs[f.ID] = true
	}

	out := make([]models.Table, 0, len(in))
	ids := map[string]bool{}
	names := map[string]bool{}
	for _, t := range in {
		t.ID = strings.TrimSpace(t.ID)
		t.Name = strings.TrimSpace(t.Name)
		if t.ID == "" || ids[t.ID] {
			return nil, fmt.Errorf("every table needs a unique id")
		}
		ids[t.ID] = true
		if t.Name == "" || len(t.Name) > 20 {
			return nil, fmt.Errorf("every table needs a name of up to 20 characters")
		}
		// Reservations and orders refer to a table by its name, so names must
		// be unique across the whole restaurant, not just one floor.
		if names[strings.ToLower(t.Name)] {
			return nil, fmt.Errorf("two tables are both named %q; table names must be unique", t.Name)
		}
		names[strings.ToLower(t.Name)] = true
		if !floorIDs[t.FloorID] {
			return nil, fmt.Errorf("table %q is on a floor that no longer exists", t.Name)
		}
		switch t.Shape {
		case "rectangle", "round", "square":
		default:
			t.Shape = "rectangle"
		}
		if t.Size.Width <= 0 || t.Size.Height <= 0 || t.Size.Width > 1000 || t.Size.Height > 1000 {
			return nil, fmt.Errorf("table %q has an invalid size", t.Name)
		}
		if len(t.Chairs) > maxChairs {
			return nil, fmt.Errorf("table %q has more than %d chairs", t.Name, maxChairs)
		}
		if t.Chairs == nil {
			t.Chairs = []models.Chair{}
		}
		// Live status is derived from orders and reservations, never stored.
		t.Status = ""
		out = append(out, t)
	}
	return out, nil
}

func capacityOf(tables []models.Table) int {
	total := 0
	for _, t := range tables {
		total += len(t.Chairs)
	}
	return total
}

// SaveLayoutInfo sets the restaurant's dining areas by name. Areas that
// already exist keep their id (and therefore their tables).
func (h *TableHandler) SaveLayoutInfo(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Restaurant ID is required")
		return
	}

	var reqBody struct {
		DiningAreas []string `json:"diningAreas"`
	}
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	names := cleanList(reqBody.DiningAreas, maxFloors)
	if len(names) == 0 {
		util.JsonError(w, http.StatusBadRequest, "Dining areas are required")
		return
	}

	layout, err := h.loadLayout(r.Context(), restaurantID)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	existing := make(map[string]models.Floor, len(layout.Floors))
	for _, f := range layout.Floors {
		existing[strings.ToLower(f.Name)] = f
	}
	floors := make([]models.Floor, 0, len(names))
	for _, name := range names {
		if f, ok := existing[strings.ToLower(name)]; ok {
			floors = append(floors, f)
		}
	}
	for _, name := range names {
		if _, ok := existing[strings.ToLower(name)]; !ok {
			floors = append(floors, models.Floor{ID: nextFloorID(append(layout.Floors, floors...)), Name: name})
		}
	}

	// Tables on a floor that was removed go with it.
	kept := make([]models.Table, 0, len(layout.TablePosition))
	floorIDs := map[string]bool{}
	for _, f := range floors {
		floorIDs[f.ID] = true
	}
	for _, t := range layout.TablePosition {
		if floorIDs[t.FloorID] {
			kept = append(kept, t)
		}
	}

	if err := h.saveLayout(r, restaurantID, floors, kept); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	areaNames := make([]string, 0, len(floors))
	for _, f := range floors {
		areaNames = append(areaNames, f.Name)
	}
	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message":     "Layout updated successfully",
		"diningAreas": areaNames,
		"floors":      floors,
		"layout": map[string]interface{}{
			"diningAreas":   areaNames,
			"floors":        floors,
			"totalCapacity": capacityOf(kept),
			"totalTables":   len(kept),
		},
	})
}

func (h *TableHandler) saveLayout(r *http.Request, restaurantID bson.ObjectID, floors []models.Floor, tables []models.Table) error {
	names := make([]string, 0, len(floors))
	for _, f := range floors {
		names = append(names, f.Name)
	}
	_, err := h.DB.Collection("restaurantlayouts").UpdateOne(r.Context(),
		bson.M{"restaurantId": restaurantID.Hex()},
		bson.M{"$set": bson.M{
			"restaurantId":  restaurantID.Hex(),
			"floors":        floors,
			"diningAreas":   names,
			"tablePosition": tables,
			"totalTables":   len(tables),
			"totalCapacity": capacityOf(tables),
		}},
		options.UpdateOne().SetUpsert(true),
	)
	return err
}

// SaveTables stores the floor plan from the editor: the floors and every
// table with its chairs. Sending "floors" lets the editor add, rename and
// remove floors in the same save; without it the stored floors are kept.
func (h *TableHandler) SaveTables(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Restaurant ID is required")
		return
	}

	var reqBody struct {
		Tables []models.Table `json:"tables"`
		Floors []models.Floor `json:"floors"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if err := util.DecodeJSON(w, r, &reqBody); err != nil {
		util.JsonError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if reqBody.Tables == nil {
		util.JsonError(w, http.StatusBadRequest, "Tables data must be an array")
		return
	}

	floors := reqBody.Floors
	if floors == nil {
		layout, err := h.loadLayout(r.Context(), restaurantID)
		if err != nil {
			util.JsonError(w, http.StatusInternalServerError, "Failed to save tables")
			return
		}
		floors = layout.Floors
	}

	floors, err = cleanFloors(floors)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	tables, err := cleanTables(reqBody.Tables, floors)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.saveLayout(r, restaurantID, floors, tables); err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Failed to save tables")
		return
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"message": "Tables saved successfully",
		"tables":  tables,
		"floors":  floors,
	})
}

// --- Live table status -----------------------------------------------------

// reservedWindow is how far ahead an upcoming booking marks its table as
// reserved on the live floor view.
const reservedWindow = 90 * time.Minute

// tableState is what is happening at one table right now.
type tableState struct {
	Status string `json:"status"` // free | reserved | occupied
	Guests int    `json:"guests"`
	// Detail is the guest name for a reservation or the order state for a
	// seated table.
	Detail string     `json:"detail,omitempty"`
	Since  *time.Time `json:"since,omitempty"`
	Until  *time.Time `json:"until,omitempty"`
}

// liveTableStates derives each table's status from open orders and current
// reservations. Tables that are free are absent from the map.
func (b *Base) liveTableStates(r *http.Request, restaurantID bson.ObjectID, now time.Time) (map[string]tableState, error) {
	states := map[string]tableState{}

	// Bookings that are in progress or about to start hold their table.
	var reservations []models.Reservation
	cursor, err := b.DB.Collection("reservations").Find(r.Context(), bson.M{
		"restaurantId":               restaurantID,
		"status":                     bson.M{"$in": bson.A{"active", "seated"}},
		"reservationInfo.bookingFor": bson.M{"$lte": now.Add(reservedWindow)},
		"reservationInfo.endTime":    bson.M{"$gte": now},
	})
	if err != nil {
		return nil, err
	}
	if err := cursor.All(r.Context(), &reservations); err != nil {
		return nil, err
	}
	for _, res := range reservations {
		info := res.ReservationInfo
		from, until := info.BookingFor, info.EndTime
		state := tableState{Status: "reserved", Guests: info.Guests, Detail: info.Name, Since: &from, Until: &until}
		if res.Status == models.ReservationSeated {
			state.Status = "occupied"
		}
		states[info.TableNumber] = state
	}

	// An open order means guests are at the table, whatever the booking says.
	var orders []models.Order
	cursor, err = b.DB.Collection("orders").Find(r.Context(), bson.M{
		"restaurantId": restaurantID,
		"orderStatus":  bson.M{"$in": bson.A{"placed", "served", "ready_to_pay"}},
		"createdAt":    bson.M{"$gte": now.Add(-openOrderWindow)},
	})
	if err != nil {
		return nil, err
	}
	if err := cursor.All(r.Context(), &orders); err != nil {
		return nil, err
	}
	for _, o := range orders {
		since := o.CreatedAt
		state := states[o.TableNumber]
		state.Status = "occupied"
		state.Detail = string(o.OrderStatus)
		state.Since = &since
		if o.Guests > state.Guests {
			state.Guests = o.Guests
		}
		states[o.TableNumber] = state
	}

	return states, nil
}

// FetchLiveFloor returns the floor plan with each table's current status, for
// the dashboard's live view and the waiter app.
func (h *TableHandler) FetchLiveFloor(w http.ResponseWriter, r *http.Request) {
	var restaurantID bson.ObjectID
	if raw := r.PathValue("restaurantId"); raw != "" {
		id, err := bson.ObjectIDFromHex(raw)
		if err != nil {
			util.JsonError(w, http.StatusBadRequest, "Valid Restaurant ID is required")
			return
		}
		if !waiterMayAccess(r, id) {
			util.JsonError(w, http.StatusForbidden, "You do not work at this restaurant")
			return
		}
		restaurantID = id
	} else {
		id, err := h.restaurantID(r)
		if err != nil {
			util.JsonError(w, http.StatusBadRequest, "Set up your restaurant first")
			return
		}
		restaurantID = id
	}

	layout, err := h.loadLayout(r.Context(), restaurantID)
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Server error")
		return
	}
	states, err := h.liveTableStates(r, restaurantID, time.Now().UTC())
	if err != nil {
		util.JsonError(w, http.StatusInternalServerError, "Server error")
		return
	}

	type liveTable struct {
		models.Table
		Seats int        `json:"seats"`
		Live  tableState `json:"live"`
	}
	tables := make([]liveTable, 0, len(layout.TablePosition))
	counts := map[string]int{"free": 0, "reserved": 0, "occupied": 0}
	for _, t := range layout.TablePosition {
		state, ok := states[t.Name]
		if !ok {
			state = tableState{Status: "free"}
		}
		t.Status = state.Status
		counts[state.Status]++
		tables = append(tables, liveTable{Table: t, Seats: len(t.Chairs), Live: state})
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"floors": layout.Floors,
		"tables": tables,
		"counts": counts,
	})
}
