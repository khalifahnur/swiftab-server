package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type Admin struct {
	ID                         bson.ObjectID  `bson:"_id,omitempty" json:"id"`
	Email                      string         `bson:"email" json:"email"`
	Name                       string         `bson:"name" json:"name"`
	Password                   string         `bson:"password" json:"-"`
	PhoneNumber                string         `bson:"phoneNumber" json:"phoneNumber"`
	RestaurantID               *bson.ObjectID `bson:"restaurantId,omitempty" json:"restaurantId,omitempty"`
	VerificationCode           string         `bson:"verificationCode,omitempty" json:"-"`
	VerificationCodeExpiration *time.Time     `bson:"verificationCodeExpiration,omitempty" json:"-"`
	CreatedAt                  time.Time      `bson:"createdAt" json:"createdAt"`
	UpdatedAt                  time.Time      `bson:"updatedAt" json:"updatedAt"`
}
