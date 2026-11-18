package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type ReservationStatus string

const (
	ReservationActive    ReservationStatus = "active"
	ReservationCancelled ReservationStatus = "cancelled"
	ReservationCompleted ReservationStatus = "completed"
	ReservationSeated    ReservationStatus = "seated"
	ReservationNoShow    ReservationStatus = "no_show"
)

// ValidReservationStatus reports whether s is a status a reservation can hold.
func ValidReservationStatus(s string) bool {
	switch ReservationStatus(s) {
	case ReservationActive, ReservationCancelled, ReservationCompleted, ReservationSeated, ReservationNoShow:
		return true
	}
	return false
}

type ReservationInfo struct {
	ReservationID  string    `bson:"reservationID" json:"reservationID"`
	Name           string    `bson:"name" json:"name"`
	Email          string    `bson:"email" json:"email"`
	PhoneNumber    string    `bson:"phoneNumber" json:"phoneNumber"`
	BookingDate    time.Time `bson:"bookingDate" json:"bookingDate"`
	BookingFor     time.Time `bson:"bookingFor" json:"bookingFor"`
	EndTime        time.Time `bson:"endTime" json:"endTime"`
	Guests         int       `bson:"guests" json:"guests"`
	TableNumber    string    `bson:"tableNumber" json:"tableNumber"`
	DiningArea     string    `bson:"diningArea" json:"diningArea"`
	RestaurantName string    `bson:"restaurantName" json:"restaurantName"`
	Notes          string    `bson:"notes,omitempty" json:"notes,omitempty"`
}

type Reservation struct {
	ID              bson.ObjectID     `bson:"_id,omitempty" json:"id"`
	RestaurantID    bson.ObjectID     `bson:"restaurantId" json:"restaurantId"`
	UserID          bson.ObjectID     `bson:"userId" json:"userId"`
	ReservationInfo ReservationInfo   `bson:"reservationInfo" json:"reservationInfo"`
	UserOrder       *bson.ObjectID    `bson:"userOrder,omitempty" json:"userOrder,omitempty"`
	Status          ReservationStatus `bson:"status" json:"status"`
	// Source is "qr" for a guest who checked in by scanning a table's QR
	// code; empty for bookings made in the app or by the restaurant.
	Source string `bson:"source,omitempty" json:"source,omitempty"`
	// Tab is set while the host shares their menu so friends can add dishes
	// to this booking's bill.
	Tab       *Tab      `bson:"tab,omitempty" json:"-"`
	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
}

// MaxTabGuests caps how many friends can join one shared tab.
const MaxTabGuests = 20

// Tab is a booking's share link. Friends who open it say who they are and
// can then order dishes that go on the host's bill.
type Tab struct {
	// Code is the secret in the share link. Closing the tab clears it, so an
	// old link stops working; sharing again makes a new one.
	Code     string     `bson:"code,omitempty"`
	Open     bool       `bson:"open"`
	OpenedAt time.Time  `bson:"openedAt"`
	Guests   []TabGuest `bson:"guests,omitempty"`
}

// TabGuest is a friend who joined a shared tab.
type TabGuest struct {
	ID       string    `bson:"id" json:"id"`
	Name     string    `bson:"name" json:"name"`
	JoinedAt time.Time `bson:"joinedAt" json:"joinedAt"`
}

// IsOpen reports whether the share link currently works.
func (t *Tab) IsOpen() bool {
	return t != nil && t.Open && t.Code != ""
}

// Guest finds a friend on the tab by id.
func (t *Tab) Guest(id string) *TabGuest {
	if t == nil || id == "" {
		return nil
	}
	for i := range t.Guests {
		if t.Guests[i].ID == id {
			return &t.Guests[i]
		}
	}
	return nil
}
