package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type User struct {
	ID                         bson.ObjectID `bson:"_id,omitempty" json:"id"`
	Name                       string        `bson:"name" json:"name"`
	Email                      string        `bson:"email" json:"email"`
	Password                   string        `bson:"password" json:"-"`
	PhoneNumber                string        `bson:"phoneNumber" json:"phoneNumber"`
	VerificationCode           string        `bson:"verificationCode,omitempty" json:"verificationCode,omitempty"`
	VerificationCodeExpiration *time.Time    `bson:"verificationCodeExpiration,omitempty" json:"verificationCodeExpiration,omitempty"`
	CreatedAt                  time.Time     `bson:"createdAt" json:"createdAt"`
	UpdatedAt                  time.Time     `bson:"updatedAt" json:"updatedAt"`
}
