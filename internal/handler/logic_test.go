package handler

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	models "swiftab/server/internal/model"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func testMenu() (map[bson.ObjectID]models.MenuItem, bson.ObjectID, bson.ObjectID, bson.ObjectID) {
	burger, soda, fish := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
	off := false
	data := models.RestaurantData{Menu: models.Menu{
		"lunch":  {{ID: burger, Name: "Beef Burger", Cost: 850}, {ID: fish, Name: "Tilapia", Cost: 1200, Available: &off}},
		"drinks": {{ID: soda, Name: "Soda", Cost: 120}},
	}}
	return menuIndex(data), burger, soda, fish
}

func TestPriceItemsUsesTheRestaurantsPricesNotTheClients(t *testing.T) {
	menu, burger, soda, _ := testMenu()

	lines, total, err := priceItems([]models.MenuItem{
		{ID: burger, Name: "Free burger", Cost: 1, Quantity: 2},
		{ID: soda, Cost: 0, Quantity: 3},
	}, menu)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2*850+3*120 {
		t.Errorf("total = %v, want %v", total, 2*850+3*120)
	}
	if lines[0].Name != "Beef Burger" || lines[0].Cost != 850 || lines[0].Quantity != 2 {
		t.Errorf("first line = %+v", lines[0])
	}
}

func TestPriceItemsRejectsWhatCannotBeOrdered(t *testing.T) {
	menu, burger, _, fish := testMenu()

	cases := []struct {
		name string
		req  []models.MenuItem
		want string
	}{
		{"empty order", nil, "at least one"},
		{"dish not on the menu", []models.MenuItem{{ID: bson.NewObjectID(), Name: "Lobster", Quantity: 1}}, "Lobster is no longer on the menu"},
		{"dish switched off", []models.MenuItem{{ID: fish, Quantity: 1}}, "Tilapia is currently unavailable"},
		{"absurd quantity", []models.MenuItem{{ID: burger, Quantity: 500}}, "at most"},
	}
	for _, tc := range cases {
		_, _, err := priceItems(tc.req, menu)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

func TestPriceItemsQuantityAndDuplicates(t *testing.T) {
	menu, burger, _, _ := testMenu()

	// A missing or zero quantity means one.
	lines, total, err := priceItems([]models.MenuItem{{ID: burger}}, menu)
	if err != nil || lines[0].Quantity != 1 || total != 850 {
		t.Errorf("zero quantity: lines=%+v total=%v err=%v", lines, total, err)
	}

	// The same dish twice is one line; a kitchen note keeps lines apart.
	lines, total, err = priceItems([]models.MenuItem{
		{ID: burger, Quantity: 1},
		{ID: burger, Quantity: 2},
		{ID: burger, Quantity: 1, Note: "no onions"},
	}, menu)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Quantity != 3 || lines[1].Note != "no onions" {
		t.Errorf("lines = %+v", lines)
	}
	if total != 4*850 {
		t.Errorf("total = %v, want %v", total, 4*850)
	}
}

func TestMergeItemsAddsToAnOpenBill(t *testing.T) {
	a, b := bson.NewObjectID(), bson.NewObjectID()
	existing := []models.MenuItem{{ID: a, Name: "Burger", Cost: 850, Quantity: 1}}
	merged := mergeItems(existing, []models.MenuItem{
		{ID: a, Name: "Burger", Cost: 850, Quantity: 2},
		{ID: b, Name: "Soda", Cost: 120, Quantity: 1},
	})
	if len(merged) != 2 || merged[0].Quantity != 3 || merged[1].Name != "Soda" {
		t.Errorf("merged = %+v", merged)
	}
	if existing[0].Quantity != 1 {
		t.Error("mergeItems modified the caller's slice")
	}
	if got := orderTotal(merged); got != 3*850+120 {
		t.Errorf("orderTotal = %v", got)
	}
}

func TestOpeningHours(t *testing.T) {
	in := []models.OpeningHour{
		{Day: "friday", Open: true, From: "08:00", To: "23:00"},
		{Day: "Monday", Open: true, From: "08:00", To: "22:00"},
		{Day: "Tuesday", Open: true, From: "08:00", To: "22:00"},
		{Day: "Wednesday", Open: false, From: "08:00", To: "22:00"},
		{Day: "Thursday", Open: true, From: "08:00", To: "22:00"},
	}
	hours := normalizeOpeningHours(in)
	if len(hours) != 7 || hours[0].Day != "Monday" || hours[6].Day != "Sunday" {
		t.Fatalf("normalised hours = %+v", hours)
	}
	if hours[2].From != "" || hours[2].To != "" {
		t.Errorf("a closed day should carry no times: %+v", hours[2])
	}
	if hours[5].Open || hours[6].Open {
		t.Error("days that were not sent should be closed")
	}
	if got, want := summarizeHours(in), "Mon-Tue 08:00-22:00, Thu 08:00-22:00, Fri 08:00-23:00"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	if got := summarizeHours(nil); got != "" {
		t.Errorf("summary of no hours = %q", got)
	}

	if err := validateOpeningHours(hours); err != nil {
		t.Errorf("valid hours rejected: %v", err)
	}
	bad := []models.OpeningHour{{Day: "Monday", Open: true, From: "8am", To: "22:00"}}
	if err := validateOpeningHours(bad); err == nil {
		t.Error("malformed time accepted")
	}
	same := []models.OpeningHour{{Day: "Monday", Open: true, From: "09:00", To: "09:00"}}
	if err := validateOpeningHours(same); err == nil {
		t.Error("identical open and close time accepted")
	}
}

func TestPercentToRate(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
		ok   bool
	}{{16, 0.16, true}, {0.16, 0.16, true}, {0, 0, true}, {100, 1, true}, {-1, 0, false}, {150, 0, false}}
	for _, tc := range cases {
		got, ok := percentToRate(tc.in)
		if ok != tc.ok || math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("percentToRate(%v) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCleanFloorsKeepsIdsAndNumbersNewFloors(t *testing.T) {
	floors, err := cleanFloors([]models.Floor{
		{ID: "1", Name: "Garden"},
		{ID: "2", Name: " Fountain "},
		{ID: "", Name: "Rooftop"}, // added in the editor, not saved before
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []models.Floor{{ID: "1", Name: "Garden"}, {ID: "2", Name: "Fountain"}, {ID: "3", Name: "Rooftop"}}
	if !reflect.DeepEqual(floors, want) {
		t.Errorf("floors = %+v, want %+v", floors, want)
	}

	// Deleting floor 2 and adding another must not reuse a live id.
	floors, err = cleanFloors([]models.Floor{{ID: "1", Name: "Garden"}, {ID: "3", Name: "Rooftop"}, {Name: "Bar"}})
	if err != nil {
		t.Fatal(err)
	}
	if floors[2].ID != "4" {
		t.Errorf("new floor id = %q, want 4", floors[2].ID)
	}

	if _, err := cleanFloors([]models.Floor{{ID: "1", Name: "Garden"}, {ID: "2", Name: "garden"}}); err == nil {
		t.Error("two floors with the same name accepted")
	}
	if _, err := cleanFloors([]models.Floor{{ID: "1", Name: "  "}}); err == nil {
		t.Error("a floor with no name accepted")
	}
}

func TestCleanTables(t *testing.T) {
	floors := []models.Floor{{ID: "1", Name: "Garden"}}
	table := func(id, name, floor string, chairs int) models.Table {
		t := models.Table{ID: id, Name: name, FloorID: floor, Shape: "round", Size: models.Size{Width: 110, Height: 110}, Status: "Occupied"}
		for i := 0; i < chairs; i++ {
			t.Chairs = append(t.Chairs, models.Chair{ID: id + "-c", Position: "top"})
		}
		return t
	}

	tables, err := cleanTables([]models.Table{table("a", "T1", "1", 6), table("b", "T2", "1", 0)}, floors)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 2 || len(tables[0].Chairs) != 6 {
		t.Errorf("tables = %+v", tables)
	}
	if tables[1].Chairs == nil {
		t.Error("a table with no chairs should save an empty list")
	}
	if tables[0].Status != "" {
		t.Error("live status must not be stored with the layout")
	}
	if capacityOf(tables) != 6 {
		t.Errorf("capacity = %d, want 6", capacityOf(tables))
	}

	rejected := map[string][]models.Table{
		"duplicate table name":     {table("a", "T1", "1", 2), table("b", "t1", "1", 2)},
		"table on a deleted floor": {table("a", "T1", "9", 2)},
		"duplicate id":             {table("a", "T1", "1", 2), table("a", "T2", "1", 2)},
		"too many chairs":          {table("a", "T1", "1", 40)},
		"no name":                  {table("a", " ", "1", 2)},
	}
	for name, in := range rejected {
		if _, err := cleanTables(in, floors); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestResolvePeriod(t *testing.T) {
	nairobi, err := time.LoadLocation("Africa/Nairobi")
	if err != nil {
		t.Fatal(err)
	}
	today := time.Date(2026, 11, 14, 0, 0, 0, 0, nairobi)

	week := resolvePeriod("weekly", today)
	if len(week.Buckets) != 7 || week.Buckets[0].Key != "2026-11-08" || week.Buckets[6].Key != "2026-11-14" {
		t.Errorf("weekly buckets = %+v", week.Buckets)
	}
	if week.Buckets[6].Label != "Sat" {
		t.Errorf("14 Nov 2026 labelled %q, want Sat", week.Buckets[6].Label)
	}
	if !week.PrevStart.Equal(today.AddDate(0, 0, -13)) {
		t.Errorf("weekly previous start = %v", week.PrevStart)
	}

	month := resolvePeriod("monthly", today)
	if len(month.Buckets) != 30 || month.Buckets[29].Key != "2026-11-14" || month.Buckets[0].Key != "2026-10-16" {
		t.Errorf("monthly buckets run %s..%s (%d)", month.Buckets[0].Key, month.Buckets[29].Key, len(month.Buckets))
	}

	year := resolvePeriod("yearly", today)
	if !year.Monthly || len(year.Buckets) != 12 || year.Buckets[0].Key != "2025-12" || year.Buckets[11].Key != "2026-11" {
		t.Errorf("yearly buckets = %+v", year.Buckets)
	}

	if got := resolvePeriod("nonsense", today).Name; got != "weekly" {
		t.Errorf("unknown period resolved to %q, want weekly", got)
	}
}

func TestStartOfDayUsesTheBusinessTimezone(t *testing.T) {
	nairobi, _ := time.LoadLocation("Africa/Nairobi")
	base := &Base{}
	base.Cfg = nil
	// 22:30 UTC on the 13th is already 01:30 on the 14th in Nairobi.
	instant := time.Date(2026, 11, 13, 22, 30, 0, 0, time.UTC)
	if got := base.startOfDay(instant); !got.Equal(time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("without a timezone, start of day = %v", got)
	}

	base = baseWithLocation(nairobi)
	want := time.Date(2026, 11, 14, 0, 0, 0, 0, nairobi)
	if got := base.startOfDay(instant); !got.Equal(want) {
		t.Errorf("start of day in Nairobi = %v, want %v", got, want)
	}
}

func TestHaversine(t *testing.T) {
	// Nairobi CBD to Westlands is roughly 4 km.
	d := haversineKm(-1.2864, 36.8172, -1.2676, 36.8108)
	if d < 1.5 || d > 3 {
		t.Errorf("CBD to Westlands = %.2f km", d)
	}
	// Nairobi to Mombasa is about 440 km.
	d = haversineKm(-1.2864, 36.8172, -4.0435, 39.6682)
	if d < 420 || d > 460 {
		t.Errorf("Nairobi to Mombasa = %.0f km", d)
	}
	if haversineKm(1, 1, 1, 1) != 0 {
		t.Error("distance to self is not zero")
	}
}

func TestHourLabelAndPctChange(t *testing.T) {
	labels := map[int]string{0: "12 AM", 9: "9 AM", 12: "12 PM", 19: "7 PM", 23: "11 PM"}
	for hour, want := range labels {
		if got := hourLabel(hour); got != want {
			t.Errorf("hourLabel(%d) = %q, want %q", hour, got, want)
		}
	}
	if pctChange(150, 100) != 50 || pctChange(50, 100) != -50 || pctChange(10, 0) != 100 || pctChange(0, 0) != 0 {
		t.Error("pctChange gave an unexpected result")
	}
}

func TestPublicRestaurantHidesUnavailableDishesAndKeepsLegacySections(t *testing.T) {
	off := false
	rest := models.Restaurant{Data: []models.RestaurantData{{
		MenuCategories: []models.MenuCategory{{Key: "drinks", Label: "Drinks"}},
		Menu: models.Menu{"drinks": {
			{Name: "Soda", Cost: 120},
			{Name: "Fresh juice", Cost: 300, Available: &off},
		}},
	}}}
	out := publicRestaurant(rest)
	data := out.Data[0]

	if len(data.Menu["drinks"]) != 1 || data.Menu["drinks"][0].Name != "Soda" {
		t.Errorf("drinks = %+v", data.Menu["drinks"])
	}
	// Released app builds read breakfast, lunch and dinner unconditionally.
	for _, key := range []string{"breakfast", "lunch", "dinner"} {
		if items, ok := data.Menu[key]; !ok || items == nil {
			t.Errorf("%s section missing or null", key)
		}
	}
	if data.Review == nil || data.About == nil || len(data.OpeningHours) != 7 {
		t.Errorf("lists not normalised: review=%v about=%v hours=%d", data.Review, data.About, len(data.OpeningHours))
	}
	if len(rest.Data[0].Menu["drinks"]) != 2 {
		t.Error("publicRestaurant must not change what the admin sees")
	}
}

func TestGenerateReservationIDIsUniquePerBooking(t *testing.T) {
	when := time.Date(2026, 11, 14, 19, 0, 0, 0, time.UTC)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		seen[GenerateReservationID("65f0c0c0c0c0c0c0c0c0abcd", when)] = true
	}
	// Two tables booked for the same minute used to get the same code.
	if len(seen) < 40 {
		t.Errorf("only %d distinct codes in 50", len(seen))
	}
	for id := range seen {
		if !strings.HasPrefix(id, "RES-ABCD-") {
			t.Errorf("unexpected format %q", id)
		}
	}
}
