package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type Waiter struct {
	ID                         bson.ObjectID  `bson:"_id,omitempty" json:"id"`
	FirstName                  string         `bson:"firstname" json:"firstname"`
	LastName                   string         `bson:"lastname" json:"lastname"`
	Email                      string         `bson:"email" json:"email"`
	Password                   string         `bson:"password" json:"-"`
	PhoneNumber                string         `bson:"phoneNumber" json:"phoneNumber"`
	RestaurantID               *bson.ObjectID `bson:"restaurantId,omitempty" json:"restaurantId,omitempty"`
	ValidationCode             string         `bson:"validationcode,omitempty" json:"validationcode,omitempty"`
	ValidationCodeExpiration   *time.Time     `bson:"validationcodeExpiration,omitempty" json:"validationcodeExpiration,omitempty"`
	VerificationCode           string         `bson:"verificationCode,omitempty" json:"verificationCode,omitempty"`
	VerificationCodeExpiration *time.Time     `bson:"verificationCodeExpiration,omitempty" json:"verificationCodeExpiration,omitempty"`
	CreatedAt                  time.Time      `bson:"createdAt" json:"createdAt"`
	UpdatedAt                  time.Time      `bson:"updatedAt" json:"updatedAt"`
}
