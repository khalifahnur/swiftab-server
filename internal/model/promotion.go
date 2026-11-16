package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type PromotionType string

const (
	PromoDiscount     PromotionType = "DISCOUNT"
	PromoPaymentOffer PromotionType = "PAYMENT_OFFER"
	PromoSpecial      PromotionType = "SPECIAL"
	PromoSeasonal     PromotionType = "SEASONAL"
)

func ValidPromotionType(t string) bool {
	switch PromotionType(t) {
	case PromoDiscount, PromoPaymentOffer, PromoSpecial, PromoSeasonal:
		return true
	}
	return false
}

type Promotion struct {
	ID                 bson.ObjectID `bson:"_id,omitempty" json:"_id"`
	RestaurantID       bson.ObjectID `bson:"restaurantId" json:"restaurantId"`
	Title              string        `bson:"title" json:"title"`
	Description        string        `bson:"description" json:"description"`
	Type               PromotionType `bson:"type" json:"type"`
	DiscountPercentage float64       `bson:"discountPercentage,omitempty" json:"discountPercentage,omitempty"`
	PaymentMethod      string        `bson:"paymentMethod,omitempty" json:"paymentMethod,omitempty"`
	StartDate          time.Time     `bson:"startDate" json:"startDate"`
	EndDate            time.Time     `bson:"endDate" json:"endDate"`
	IsActive           bool          `bson:"isActive" json:"isActive"`
	TermsAndConditions string        `bson:"termsAndConditions,omitempty" json:"termsAndConditions,omitempty"`
	Image              string        `bson:"image,omitempty" json:"image,omitempty"`
	CreatedAt          time.Time     `bson:"createdAt" json:"createdAt"`
	UpdatedAt          time.Time     `bson:"updatedAt" json:"updatedAt"`
}
