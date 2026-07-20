package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type SubscriptionStatus string

const (
	SubTrialing SubscriptionStatus = "trialing"
	SubActive   SubscriptionStatus = "active"
	SubPastDue  SubscriptionStatus = "past_due"
	SubCanceled SubscriptionStatus = "canceled"
)

type Restaurant struct {
	ID             bson.ObjectID    `bson:"_id,omitempty" json:"id"`
	OrganizationID bson.ObjectID    `bson:"organizationId,omitempty" json:"organizationId"`
	Title          string           `bson:"title" json:"title"`
	Data           []RestaurantData `bson:"data" json:"data"`

	SubscriptionStatus SubscriptionStatus `bson:"subscriptionStatus" json:"subscriptionStatus"`
	SubscriptionPlan   string             `bson:"subscriptionPlan" json:"subscriptionPlan"`
	TrialEndsAt        *time.Time         `bson:"trialEndsAt,omitempty" json:"trialEndsAt,omitempty"`

	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
}

type RestaurantData struct {
	Image          string   `bson:"image" json:"image"`
	RestaurantName string   `bson:"restaurantName" json:"restaurantName"`
	Location       string   `bson:"location" json:"location"`
	Latitude       float64  `bson:"latitude" json:"latitude"`
	Longitude      float64  `bson:"longitude" json:"longitude"`
	Rate           float64  `bson:"rate" json:"rate"`
	About          []About  `bson:"about" json:"about"`
	Menu           Menu     `bson:"menu" json:"menu"`
	Review         []Review `bson:"review" json:"review"`
}

type About struct {
	Description    string  `bson:"description" json:"description"`
	AveragePrice   float64 `bson:"averagePrice" json:"averagePrice"`
	HrsOfOperation string  `bson:"hrsOfOperation" json:"hrsOfOperation"`
	Phone          string  `bson:"phone" json:"phone"`
	Email          string  `bson:"email" json:"email"`
}

type Menu struct {
	Breakfast []MenuItem `bson:"breakfast" json:"breakfast"`
	Lunch     []MenuItem `bson:"lunch" json:"lunch"`
	Dinner    []MenuItem `bson:"dinner" json:"dinner"`
}

type MenuItem struct {
	ID          bson.ObjectID `bson:"_id,omitempty" json:"_id"`
	Image       string        `bson:"image,omitempty" json:"image,omitempty"`
	Name        string        `bson:"name" json:"name"`
	Description string        `bson:"description,omitempty" json:"description,omitempty"`
	Cost        float64       `bson:"cost" json:"cost"`
	Rate        float64       `bson:"rate,omitempty" json:"rate,omitempty"`
	Quantity    int           `bson:"quantity,omitempty" json:"quantity,omitempty"`
}

type Review struct {
	Name      string  `bson:"name" json:"name"`
	Image     string  `bson:"image" json:"image"`
	ReviewTxt string  `bson:"reviewTxt" json:"reviewTxt"`
	Rating    float64 `bson:"rating" json:"rating"`
}
