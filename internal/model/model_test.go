package models

import (
	"encoding/json"
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Restaurants written by the previous backend store an "_id" inside the menu
// document next to the category arrays. Decoding must keep the arrays and
// skip everything else, or every such restaurant becomes unreadable.
func TestMenuDecodesLegacyDocumentWithEmbeddedID(t *testing.T) {
	itemID := bson.NewObjectID()
	stored := bson.M{
		"restaurantName": "Mama Oliech",
		"menu": bson.M{
			"_id":       bson.NewObjectID(),
			"breakfast": bson.A{bson.M{"_id": itemID, "name": "Chai", "cost": 80.0}},
			"lunch":     bson.A{},
			"drinks":    bson.A{bson.M{"_id": bson.NewObjectID(), "name": "Soda", "cost": 100}},
		},
	}
	raw, err := bson.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}

	var data RestaurantData
	if err := bson.Unmarshal(raw, &data); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(data.Menu) != 3 {
		t.Fatalf("decoded %d categories, want 3: %v", len(data.Menu), data.Menu)
	}
	if _, ok := data.Menu["_id"]; ok {
		t.Error("the embedded _id was decoded as a category")
	}
	if got := data.Menu["breakfast"]; len(got) != 1 || got[0].Name != "Chai" || got[0].Cost != 80 || got[0].ID != itemID {
		t.Errorf("breakfast = %+v", got)
	}
	// An integer price must decode into the float field.
	if got := data.Menu["drinks"]; len(got) != 1 || got[0].Cost != 100 {
		t.Errorf("drinks = %+v", got)
	}
	if got := data.Menu["lunch"]; got == nil || len(got) != 0 {
		t.Errorf("an empty category should decode to an empty list, got %v", got)
	}
}

func TestMenuRoundTripsThroughBSONAndKeepsTheAppShapeInJSON(t *testing.T) {
	in := RestaurantData{
		RestaurantName: "Test",
		Menu: Menu{
			"starters": {{ID: bson.NewObjectID(), Name: "Samosa", Cost: 150}},
			"drinks":   {},
		},
	}
	raw, err := bson.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out RestaurantData
	if err := bson.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in.Menu, out.Menu) {
		t.Errorf("round trip changed the menu:\n in: %+v\nout: %+v", in.Menu, out.Menu)
	}

	// The apps read the menu as {"<category>": [items]}.
	encoded, _ := json.Marshal(out.Menu)
	var shape map[string][]map[string]interface{}
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatalf("menu JSON is not a map of arrays: %s", encoded)
	}
	if shape["starters"][0]["name"] != "Samosa" || shape["starters"][0]["_id"] == "" {
		t.Errorf("unexpected JSON: %s", encoded)
	}
}

func TestMissingMenuDecodesWithoutError(t *testing.T) {
	raw, _ := bson.Marshal(bson.M{"restaurantName": "No menu yet"})
	var data RestaurantData
	if err := bson.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Menu["breakfast"]) != 0 {
		t.Error("expected no items")
	}
}

func keys(cats []MenuCategory) []string {
	out := make([]string, len(cats))
	for i, c := range cats {
		out[i] = c.Key
	}
	return out
}

func TestCategories(t *testing.T) {
	// A restaurant from before configurable categories keeps its three sections.
	legacy := RestaurantData{Menu: Menu{"breakfast": {}, "lunch": {}, "dinner": {}}}
	if got := keys(legacy.Categories()); !reflect.DeepEqual(got, []string{"breakfast", "lunch", "dinner"}) {
		t.Errorf("legacy categories = %v", got)
	}

	// Configured order wins, and a section that holds dishes but was never
	// registered still shows up rather than hiding those dishes.
	custom := RestaurantData{
		MenuCategories: []MenuCategory{{Key: "drinks", Label: "Drinks"}, {Key: "mains", Label: "Mains"}},
		Menu:           Menu{"mains": {}, "drinks": {}, "specials": {{Name: "Nyama choma"}}},
	}
	if got := keys(custom.Categories()); !reflect.DeepEqual(got, []string{"drinks", "mains", "specials"}) {
		t.Errorf("custom categories = %v", got)
	}
}

func TestItemAndReservationDefaults(t *testing.T) {
	off := false
	if !(MenuItem{}).IsAvailable() {
		t.Error("an item with no availability flag must be orderable")
	}
	if (MenuItem{Available: &off}).IsAvailable() {
		t.Error("an item switched off must not be orderable")
	}
	if !(RestaurantData{}).IsAcceptingReservations() {
		t.Error("a restaurant with no setting must accept reservations")
	}
	if (RestaurantData{AcceptingReservations: &off}).IsAcceptingReservations() {
		t.Error("a restaurant that paused reservations must not accept them")
	}
}

func TestLayoutNormalize(t *testing.T) {
	// Layouts saved before floors had ids: tables point at "1", "2", ...
	legacy := RestaurantLayout{
		DiningAreas:   []string{"Garden", "Fountain"},
		TablePosition: []Table{{ID: "t1", Name: "K-1", FloorID: "2"}},
	}
	legacy.Normalize()
	want := []Floor{{ID: "1", Name: "Garden"}, {ID: "2", Name: "Fountain"}}
	if !reflect.DeepEqual(legacy.Floors, want) {
		t.Errorf("floors = %+v, want %+v", legacy.Floors, want)
	}
	if legacy.FloorName("2") != "Fountain" {
		t.Errorf("table on floor 2 resolved to %q", legacy.FloorName("2"))
	}
	if legacy.TablePosition[0].Chairs == nil {
		t.Error("chairs should be an empty list, not null")
	}

	// Floors with their own ids define the area names.
	modern := RestaurantLayout{
		Floors:      []Floor{{ID: "7", Name: "Rooftop"}},
		DiningAreas: []string{"stale name"},
	}
	modern.Normalize()
	if !reflect.DeepEqual(modern.DiningAreas, []string{"Rooftop"}) {
		t.Errorf("diningAreas = %v", modern.DiningAreas)
	}

	// A restaurant with nothing saved yet serialises arrays, not nulls.
	var empty RestaurantLayout
	empty.Normalize()
	encoded, _ := json.Marshal(empty)
	var shape map[string]interface{}
	json.Unmarshal(encoded, &shape)
	for _, field := range []string{"floors", "diningAreas", "tablePosition"} {
		if _, isArray := shape[field].([]interface{}); !isArray {
			t.Errorf("%s = %v, want an empty array", field, shape[field])
		}
	}
}

func TestSensitiveFieldsAreNotSerialised(t *testing.T) {
	encoded, _ := json.Marshal(Waiter{Password: "hash", ValidationCode: "123456", PushTokens: []string{"t"}})
	for _, secret := range []string{"hash", "123456", "pushTokens", "password"} {
		if json.Valid(encoded) && contains(string(encoded), secret) {
			t.Errorf("waiter JSON leaks %q: %s", secret, encoded)
		}
	}
	encoded, _ = json.Marshal(User{Password: "hash", VerificationCode: "654321"})
	for _, secret := range []string{"hash", "654321"} {
		if contains(string(encoded), secret) {
			t.Errorf("user JSON leaks %q: %s", secret, encoded)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
