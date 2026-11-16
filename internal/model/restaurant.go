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
	ID             bson.ObjectID    `bson:"_id,omitempty" json:"_id"`
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
	Image          string  `bson:"image" json:"image"`
	Logo           string  `bson:"logo,omitempty" json:"logo,omitempty"`
	RestaurantName string  `bson:"restaurantName" json:"restaurantName"`
	Location       string  `bson:"location" json:"location"`
	Latitude       float64 `bson:"latitude" json:"latitude"`
	Longitude      float64 `bson:"longitude" json:"longitude"`
	Rate           float64 `bson:"rate" json:"rate"`

	Cuisines       []string      `bson:"cuisines,omitempty" json:"cuisines,omitempty"`
	Amenities      []string      `bson:"amenities,omitempty" json:"amenities,omitempty"`
	PaymentMethods []string      `bson:"paymentMethods,omitempty" json:"paymentMethods,omitempty"`
	TaxRate        float64       `bson:"taxRate,omitempty" json:"taxRate"`
	ServiceCharge  float64       `bson:"serviceCharge,omitempty" json:"serviceCharge"`
	OpeningHours   []OpeningHour `bson:"openingHours,omitempty" json:"openingHours,omitempty"`
	// AcceptingReservations is nil for restaurants created before the setting
	// existed; treat nil as open.
	AcceptingReservations *bool `bson:"acceptingReservations,omitempty" json:"acceptingReservations,omitempty"`

	About          []About        `bson:"about" json:"about"`
	Menu           Menu           `bson:"menu" json:"menu"`
	MenuCategories []MenuCategory `bson:"menuCategories,omitempty" json:"menuCategories,omitempty"`
	Review         []Review       `bson:"review" json:"review"`
}

// IsAcceptingReservations reports whether diners may book a table.
func (d RestaurantData) IsAcceptingReservations() bool {
	return d.AcceptingReservations == nil || *d.AcceptingReservations
}

type OpeningHour struct {
	Day  string `bson:"day" json:"day"`
	Open bool   `bson:"open" json:"open"`
	From string `bson:"from" json:"from"`
	To   string `bson:"to" json:"to"`
}

type About struct {
	Description    string  `bson:"description" json:"description"`
	AveragePrice   float64 `bson:"averagePrice" json:"averagePrice"`
	HrsOfOperation string  `bson:"hrsOfOperation" json:"hrsOfOperation"`
	Phone          string  `bson:"phone" json:"phone"`
	Email          string  `bson:"email" json:"email"`
}

// MenuCategory is one section of a restaurant's menu. Key names the array
// under data.0.menu that holds the section's items.
type MenuCategory struct {
	Key   string `bson:"key" json:"key"`
	Label string `bson:"label" json:"label"`
}

// DefaultMenuCategories is what a newly created restaurant starts with. The
// first three match the sections every existing restaurant already has.
func DefaultMenuCategories() []MenuCategory {
	return []MenuCategory{
		{Key: "breakfast", Label: "Breakfast"},
		{Key: "lunch", Label: "Lunch"},
		{Key: "dinner", Label: "Dinner"},
		{Key: "starters", Label: "Starters"},
		{Key: "desserts", Label: "Desserts"},
		{Key: "drinks", Label: "Drinks"},
	}
}

// LegacyMenuCategories are the fixed sections restaurants had before sections
// became configurable.
func LegacyMenuCategories() []MenuCategory {
	return DefaultMenuCategories()[:3]
}

// Menu maps a category key to its items. It is stored as a document with one
// array per category, which is the shape the mobile apps already read
// ({"breakfast": [...], "lunch": [...]}).
type Menu map[string][]MenuItem

// UnmarshalBSONValue decodes the stored menu document, keeping every array
// field as a category and ignoring anything else (documents written by the
// previous Node backend carry an extra "_id" inside the menu).
func (m *Menu) UnmarshalBSONValue(typ byte, data []byte) error {
	out := Menu{}
	if bson.Type(typ) != bson.TypeEmbeddedDocument {
		*m = out
		return nil
	}
	elems, err := bson.Raw(data).Elements()
	if err != nil {
		return err
	}
	for _, el := range elems {
		val := el.Value()
		if val.Type != bson.TypeArray {
			continue
		}
		var items []MenuItem
		if err := val.Unmarshal(&items); err != nil {
			return err
		}
		if items == nil {
			items = []MenuItem{}
		}
		out[el.Key()] = items
	}
	*m = out
	return nil
}

// Categories returns the ordered menu sections for a restaurant: the configured
// ones first, then any section that holds items but was never registered.
func (d RestaurantData) Categories() []MenuCategory {
	cats := d.MenuCategories
	if len(cats) == 0 {
		cats = LegacyMenuCategories()
	}
	seen := make(map[string]bool, len(cats))
	out := make([]MenuCategory, 0, len(cats))
	for _, c := range cats {
		if c.Key == "" || seen[c.Key] {
			continue
		}
		seen[c.Key] = true
		out = append(out, c)
	}
	for key := range d.Menu {
		if !seen[key] {
			seen[key] = true
			out = append(out, MenuCategory{Key: key, Label: key})
		}
	}
	return out
}

type MenuItem struct {
	ID          bson.ObjectID `bson:"_id,omitempty" json:"_id"`
	Image       string        `bson:"image,omitempty" json:"image,omitempty"`
	Name        string        `bson:"name" json:"name"`
	Description string        `bson:"description,omitempty" json:"description,omitempty"`
	Cost        float64       `bson:"cost" json:"cost"`
	Rate        float64       `bson:"rate,omitempty" json:"rate,omitempty"`
	Quantity    int           `bson:"quantity,omitempty" json:"quantity,omitempty"`
	// Available is nil for items created before stock control existed; treat
	// nil as available.
	Available *bool    `bson:"available,omitempty" json:"available,omitempty"`
	Tags      []string `bson:"tags,omitempty" json:"tags,omitempty"`
	PrepTime  int      `bson:"prepTime,omitempty" json:"prepTime,omitempty"`
	// Note is a diner's instruction for the kitchen; only set on order lines.
	Note string `bson:"note,omitempty" json:"note,omitempty"`
}

// IsAvailable reports whether the item can currently be ordered.
func (i MenuItem) IsAvailable() bool {
	return i.Available == nil || *i.Available
}

type Review struct {
	UserID    *bson.ObjectID `bson:"userId,omitempty" json:"userId,omitempty"`
	Name      string         `bson:"name" json:"name"`
	Image     string         `bson:"image" json:"image"`
	ReviewTxt string         `bson:"reviewTxt" json:"reviewTxt"`
	Rating    float64        `bson:"rating" json:"rating"`
	CreatedAt *time.Time     `bson:"createdAt,omitempty" json:"createdAt,omitempty"`
}
