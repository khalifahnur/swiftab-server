package handler

import (
	"errors"
	"testing"
	"time"

	models "swiftab/server/internal/model"
)

func booking(id, phone, name, source string, start, end time.Time, status models.ReservationStatus) models.Reservation {
	return models.Reservation{
		Status: status,
		Source: source,
		ReservationInfo: models.ReservationInfo{
			ReservationID: id, PhoneNumber: phone, Name: name, BookingFor: start, EndTime: end,
		},
	}
}

func TestPlanWalkIn(t *testing.T) {
	now := time.Date(2026, 11, 18, 18, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return now.Add(time.Duration(min) * time.Minute) }

	t.Run("free table seats the guest for the full stay", func(t *testing.T) {
		plan, err := planWalkIn(now, nil, nil, "712345678", "Ann")
		if err != nil || plan.Reuse != nil || !plan.End.Equal(now.Add(walkInStay)) {
			t.Fatalf("got %+v, %v", plan, err)
		}
	})

	t.Run("someone else at the table turns the guest away", func(t *testing.T) {
		holds := []models.Reservation{booking("RES-1", "0700111222", "Bo", "", at(-30), at(60), models.ReservationSeated)}
		if _, err := planWalkIn(now, holds, nil, "712345678", "Ann"); !errors.Is(err, errTableBusy) {
			t.Fatalf("got %v, want busy", err)
		}
	})

	t.Run("a booking arriving within the grace counts as at the table", func(t *testing.T) {
		holds := []models.Reservation{booking("RES-1", "0700111222", "Bo", "", at(10), at(100), models.ReservationActive)}
		if _, err := planWalkIn(now, holds, nil, "712345678", "Ann"); !errors.Is(err, errTableBusy) {
			t.Fatalf("got %v, want busy", err)
		}
	})

	t.Run("an open order keeps the table taken after the booking's end", func(t *testing.T) {
		holds := []models.Reservation{booking("RES-1", "0700111222", "Bo", "", at(-200), at(-20), models.ReservationSeated)}
		if _, err := planWalkIn(now, holds, map[string]bool{"RES-1": true}, "712345678", "Ann"); !errors.Is(err, errTableBusy) {
			t.Fatalf("got %v, want busy", err)
		}
	})

	t.Run("the same phone gets its booking back, in any format", func(t *testing.T) {
		holds := []models.Reservation{booking("RES-1", "+254 712 345 678", "Ann", "", at(-5), at(85), models.ReservationActive)}
		plan, err := planWalkIn(now, holds, nil, "712345678", "")
		if err != nil || plan.Reuse == nil || plan.Reuse.ReservationInfo.ReservationID != "RES-1" {
			t.Fatalf("got %+v, %v", plan, err)
		}
	})

	t.Run("a name alone only matches an earlier scan, not an app booking", func(t *testing.T) {
		app := []models.Reservation{booking("RES-1", "0712345678", "Ann Wanjiru", "", at(-5), at(85), models.ReservationSeated)}
		if _, err := planWalkIn(now, app, nil, "", "ann wanjiru"); !errors.Is(err, errTableBusy) {
			t.Fatalf("app booking matched by name: %v", err)
		}
		qr := []models.Reservation{booking("RES-2", "", "Ann Wanjiru", "qr", at(-5), at(85), models.ReservationSeated)}
		if plan, err := planWalkIn(now, qr, nil, "", "ann wanjiru"); err != nil || plan.Reuse == nil {
			t.Fatalf("earlier scan not matched: %+v, %v", plan, err)
		}
	})

	t.Run("a later booking shortens the stay", func(t *testing.T) {
		holds := []models.Reservation{booking("RES-1", "0700111222", "Bo", "", at(60), at(150), models.ReservationActive)}
		plan, err := planWalkIn(now, holds, nil, "712345678", "Ann")
		if err != nil || !plan.End.Equal(at(55)) {
			t.Fatalf("got %+v, %v", plan, err)
		}
	})

	t.Run("a booking too soon refuses the walk-in", func(t *testing.T) {
		holds := []models.Reservation{booking("RES-1", "0700111222", "Bo", "", at(30), at(120), models.ReservationActive)}
		plan, err := planWalkIn(now, holds, nil, "712345678", "Ann")
		if !errors.Is(err, errTableBookedSoon) || !plan.NextBooking.Equal(at(30)) {
			t.Fatalf("got %+v, %v", plan, err)
		}
	})
}

func TestFindTable(t *testing.T) {
	layout := &models.RestaurantLayout{TablePosition: []models.Table{{ID: "t-1", Name: "G1"}, {Name: "Old"}}}
	if tbl := findTable(layout, "t-1"); tbl == nil || tbl.Name != "G1" {
		t.Fatalf("by id: %+v", tbl)
	}
	if tbl := findTable(layout, "Old"); tbl == nil {
		t.Fatal("a table without an id is found by name")
	}
	if tbl := findTable(layout, "G1"); tbl != nil {
		t.Fatal("a table with an id is not found by its name")
	}
}
