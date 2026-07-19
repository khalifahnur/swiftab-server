package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type OrderStatus string

const (
	OrderPlaced     OrderStatus = "placed"
	OrderServed     OrderStatus = "served"
	OrderReadyToPay OrderStatus = "ready_to_pay"
	OrderCompleted  OrderStatus = "completed"
	OrderCancelled  OrderStatus = "cancelled"
)

type PaymentStatus string

const (
	PaymentPaid   PaymentStatus = "paid"
	PaymentUnpaid PaymentStatus = "unpaid"
)

type Order struct {
	ID             bson.ObjectID  `bson:"_id,omitempty" json:"id"`
	UserID         bson.ObjectID  `bson:"userId" json:"userId"`
	RestaurantName string         `bson:"restaurantName" json:"restaurantName"`
	RestaurantID   bson.ObjectID  `bson:"restaurantId" json:"restaurantId"`
	ReservationID  bson.ObjectID  `bson:"reservationId" json:"reservationId"`
	TableNumber    string         `bson:"tableNumber" json:"tableNumber"`
	Items          []MenuItem     `bson:"items" json:"items"` // MenuItem is defined in restaurant.go
	TotalAmount    float64        `bson:"totalAmount" json:"totalAmount"`
	OrderStatus    OrderStatus    `bson:"orderStatus" json:"orderStatus"`
	PaymentStatus  PaymentStatus  `bson:"paymentStatus" json:"paymentStatus"`
	PaymentMethod  string         `bson:"paymentMethod,omitempty" json:"paymentMethod,omitempty"`
	ServedBy       *bson.ObjectID `bson:"servedBy,omitempty" json:"servedBy,omitempty"`
	CreatedAt      time.Time      `bson:"createdAt" json:"createdAt"`
	UpdatedAt      time.Time      `bson:"updatedAt" json:"updatedAt"`
}
