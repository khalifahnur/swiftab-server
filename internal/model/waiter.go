package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type Waiter struct {
	ID           bson.ObjectID  `bson:"_id,omitempty" json:"id"`
	FirstName    string         `bson:"firstname" json:"firstname"`
	LastName     string         `bson:"lastname" json:"lastname"`
	Email        string         `bson:"email" json:"email"`
	Password     string         `bson:"password" json:"-"`
	PhoneNumber  string         `bson:"phoneNumber" json:"phoneNumber"`
	RestaurantID *bson.ObjectID `bson:"restaurantId,omitempty" json:"restaurantId,omitempty"`
	// ValidationCode is the invite code the waiter enters in the app to claim
	// the account. It is never serialised from this struct; the admin API
	// returns it explicitly while the invite is pending.
	ValidationCode           string     `bson:"validationcode,omitempty" json:"-"`
	ValidationCodeExpiration *time.Time `bson:"validationcodeExpiration,omitempty" json:"-"`
	// ValidatedAt records a successful code check; the password may be set for
	// a short window afterwards.
	ValidatedAt                *time.Time `bson:"validatedAt,omitempty" json:"-"`
	VerificationCode           string     `bson:"verificationCode,omitempty" json:"-"`
	VerificationCodeExpiration *time.Time `bson:"verificationCodeExpiration,omitempty" json:"-"`
	OnDuty                     bool       `bson:"onDuty,omitempty" json:"onDuty"`
	DutyStartedAt              *time.Time `bson:"dutyStartedAt,omitempty" json:"dutyStartedAt,omitempty"`
	PushTokens                 []string   `bson:"pushTokens,omitempty" json:"-"`
	CreatedAt                  time.Time  `bson:"createdAt" json:"createdAt"`
	UpdatedAt                  time.Time  `bson:"updatedAt" json:"updatedAt"`
}

// IsActivated reports whether the waiter has finished signing up in the app.
func (w Waiter) IsActivated() bool {
	return w.Password != ""
}
