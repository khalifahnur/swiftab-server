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
)

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
}

type Reservation struct {
	ID              bson.ObjectID     `bson:"_id,omitempty" json:"id"`
	RestaurantID    bson.ObjectID     `bson:"restaurantId" json:"restaurantId"`
	UserID          bson.ObjectID     `bson:"userId" json:"userId"`
	ReservationInfo ReservationInfo   `bson:"reservationInfo" json:"reservationInfo"`
	UserOrder       *bson.ObjectID    `bson:"userOrder,omitempty" json:"userOrder,omitempty"`
	Status          ReservationStatus `bson:"status" json:"status"`
	CreatedAt       time.Time         `bson:"createdAt" json:"createdAt"`
	UpdatedAt       time.Time         `bson:"updatedAt" json:"updatedAt"`
}
