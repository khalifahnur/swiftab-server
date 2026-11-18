package handler

import (
	"testing"
	"time"

	"swiftab/server/internal/config"
	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMergeItemsKeepsEachFriendsLinesApart(t *testing.T) {
	dish := bson.NewObjectID()
	host := models.MenuItem{ID: dish, Name: "Chips", Cost: 200, Quantity: 1}
	jane := models.MenuItem{ID: dish, Name: "Chips", Cost: 200, Quantity: 2, AddedBy: "Jane", AddedByID: "g1"}

	got := mergeItems([]models.MenuItem{host}, []models.MenuItem{jane})
	if len(got) != 2 {
		t.Fatalf("host's and Jane's chips merged into %d lines, want 2", len(got))
	}
	got = mergeItems(got, []models.MenuItem{{ID: dish, Cost: 200, Quantity: 1, AddedBy: "Jane", AddedByID: "g1"}})
	if len(got) != 2 || got[1].Quantity != 3 {
		t.Fatalf("Jane ordering chips again: got %+v, want her line at 3", got)
	}
	if got[0].Quantity != 1 {
		t.Fatalf("host's line changed to %d", got[0].Quantity)
	}
}

func TestFriendLines(t *testing.T) {
	items := []models.MenuItem{
		{Name: "Host dish", Cost: 100, Quantity: 1},
		{Name: "Jane dish", Cost: 300, Quantity: 2, AddedByID: "g1"},
		{Name: "Sam dish", Cost: 50, Quantity: 1, AddedByID: "g2"},
	}
	mine := friendLines(items, "g1")
	if len(mine) != 1 || mine[0].Name != "Jane dish" || orderTotal(mine) != 600 {
		t.Fatalf("friendLines for g1 = %+v", mine)
	}
	if got := friendLines(items, "nobody"); len(got) != 0 {
		t.Fatalf("a friend with no dishes sees %+v", got)
	}
}

func TestTabOpenable(t *testing.T) {
	now := time.Date(2026, 11, 18, 19, 0, 0, 0, time.UTC)
	booking := func(status models.ReservationStatus, end time.Time) *models.Reservation {
		return &models.Reservation{Status: status, ReservationInfo: models.ReservationInfo{EndTime: end}}
	}
	cases := []struct {
		name string
		res  *models.Reservation
		want bool
	}{
		{"upcoming", booking(models.ReservationActive, now.Add(3*time.Hour)), true},
		{"seated", booking(models.ReservationSeated, now.Add(time.Hour)), true},
		{"seated past its end time", booking(models.ReservationSeated, now.Add(-time.Minute)), false},
		{"completed", booking(models.ReservationCompleted, now.Add(time.Hour)), false},
		{"cancelled", booking(models.ReservationCancelled, now.Add(time.Hour)), false},
		{"no show", booking(models.ReservationNoShow, now.Add(time.Hour)), false},
	}
	for _, c := range cases {
		if got := tabOpenable(c.res, now); got != c.want {
			t.Errorf("%s: tabOpenable = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCleanGuestName(t *testing.T) {
	if got := cleanGuestName("  Jane   Wanjiru "); got != "Jane Wanjiru" {
		t.Errorf("got %q", got)
	}
	long := cleanGuestName("Abcdefghij Abcdefghij Abcdefghij Abcdefghij Abcdefghij")
	if len([]rune(long)) > 40 {
		t.Errorf("name not capped: %q", long)
	}
}

func TestTabSummary(t *testing.T) {
	h := &DineHandler{Base: &Base{Cfg: &config.Config{WebURL: "https://www.swiftab.co.ke"}}}
	res := &models.Reservation{
		Status:          models.ReservationSeated,
		ReservationInfo: models.ReservationInfo{EndTime: time.Now().Add(time.Hour)},
		Tab: &models.Tab{Code: "abcdefghijkl", Open: true, Guests: []models.TabGuest{
			{ID: "g1", Name: "Jane"},
			{ID: "g2", Name: "Sam"},
		}},
	}
	orders := []aggFloorOrder{
		{Order: models.Order{OrderStatus: models.OrderServed, Items: []models.MenuItem{
			{Cost: 100, Quantity: 1},
			{Cost: 300, Quantity: 2, AddedByID: "g1"},
		}}},
		{Order: models.Order{OrderStatus: models.OrderCancelled, Items: []models.MenuItem{
			{Cost: 999, Quantity: 1, AddedByID: "g2"},
		}}},
	}
	body := h.tabSummary(res, orders)
	if body["open"] != true || body["url"] != "https://www.swiftab.co.ke/tab/abcdefghijkl" {
		t.Fatalf("summary = %+v", body)
	}
	guests := body["guests"].([]map[string]interface{})
	if guests[0]["items"] != 2 || guests[0]["total"] != 600.0 {
		t.Errorf("Jane's tally = %+v", guests[0])
	}
	if guests[1]["items"] != 0 {
		t.Errorf("Sam's cancelled dish was counted: %+v", guests[1])
	}

	res.Tab = &models.Tab{Open: false}
	body = h.tabSummary(res, nil)
	if body["open"] != false || body["url"] != nil {
		t.Errorf("closed tab still has a link: %+v", body)
	}
}

func TestTabGuestTokenCarriesItsTab(t *testing.T) {
	token, err := util.GenerateTabGuestToken("65f0c0c0c0c0c0c0c0c0c0c0", "RES-AAAA-1", "abcdefghijkl", "g1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := util.ParseToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Role != util.RoleGuest || claims.TabCode != "abcdefghijkl" || claims.GuestID != "g1" || claims.ReservationID != "RES-AAAA-1" {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestNewTabCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		code, err := newTabCode()
		if err != nil || len(code) != 12 || seen[code] {
			t.Fatalf("code %q err %v", code, err)
		}
		seen[code] = true
	}
}
