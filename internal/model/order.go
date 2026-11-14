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

// ValidOrderStatus reports whether s is a status an order can be moved to.
func ValidOrderStatus(s string) bool {
	switch OrderStatus(s) {
	case OrderPlaced, OrderServed, OrderReadyToPay, OrderCompleted, OrderCancelled:
		return true
	}
	return false
}

// IsOpen reports whether an order still occupies its table.
func (s OrderStatus) IsOpen() bool {
	return s == OrderPlaced || s == OrderServed || s == OrderReadyToPay
}

type PaymentStatus string

const (
	PaymentPaid   PaymentStatus = "paid"
	PaymentUnpaid PaymentStatus = "unpaid"
)

type Order struct {
	ID             bson.ObjectID `bson:"_id,omitempty" json:"id"`
	OrderNumber    string        `bson:"orderId,omitempty" json:"orderId,omitempty"`
	UserID         bson.ObjectID `bson:"userId" json:"userId"`
	RestaurantName string        `bson:"restaurantName" json:"restaurantName"`
	RestaurantID   bson.ObjectID `bson:"restaurantId" json:"restaurantId"`
	// ReservationID is the human-readable reservation code (RES-xxxx-...), not
	// the reservation document's _id.
	ReservationID    string         `bson:"reservationId" json:"reservationId"`
	TableNumber      string         `bson:"tableNumber" json:"tableNumber"`
	Guests           int            `bson:"guests,omitempty" json:"guests"`
	Items            []MenuItem     `bson:"items" json:"items"`
	TotalAmount      float64        `bson:"totalAmount" json:"totalAmount"`
	OrderStatus      OrderStatus    `bson:"orderStatus" json:"orderStatus"`
	PaymentStatus    PaymentStatus  `bson:"paymentStatus" json:"paymentStatus"`
	PaymentMethod    string         `bson:"paymentMethod,omitempty" json:"paymentMethod,omitempty"`
	PaymentReference string         `bson:"paymentReference,omitempty" json:"paymentReference,omitempty"`
	ServedBy         *bson.ObjectID `bson:"servedBy,omitempty" json:"servedBy,omitempty"`
	ServedAt         *time.Time     `bson:"servedAt,omitempty" json:"servedAt,omitempty"`
	PaidAt           *time.Time     `bson:"paidAt,omitempty" json:"paidAt,omitempty"`
	CreatedAt        time.Time      `bson:"createdAt" json:"createdAt"`
	UpdatedAt        time.Time      `bson:"updatedAt" json:"updatedAt"`
}
