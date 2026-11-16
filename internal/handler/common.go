package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"swiftab/server/internal/config"
	"swiftab/server/internal/events"
	"swiftab/server/internal/middleware"
	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Base holds what every handler group shares.
type Base struct {
	DB     *mongo.Database
	Cfg    *config.Config
	Image  *util.CloudinaryService
	Mailer *util.Mailer
	Hub    *events.Hub
}

var errNoRestaurant = errors.New("missing restaurant ID")

func (b *Base) restaurantID(r *http.Request) (bson.ObjectID, error) {
	if id, ok := middleware.RestaurantID(r); ok {
		return id, nil
	}
	return bson.NilObjectID, errNoRestaurant
}

func (b *Base) adminID(r *http.Request) (bson.ObjectID, error) {
	if id, ok := middleware.AdminID(r); ok {
		return id, nil
	}
	return bson.NilObjectID, errors.New("missing admin ID")
}

// location is the business timezone, defaulting to UTC when unconfigured.
func (b *Base) location() *time.Location {
	if b.Cfg != nil && b.Cfg.Location != nil {
		return b.Cfg.Location
	}
	return time.UTC
}

// startOfDay returns midnight of t's calendar day in the business timezone.
func (b *Base) startOfDay(t time.Time) time.Time {
	local := t.In(b.location())
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, b.location())
}

func (b *Base) loadRestaurant(ctx context.Context, id bson.ObjectID) (*models.Restaurant, error) {
	var restaurant models.Restaurant
	if err := b.DB.Collection("restaurants").FindOne(ctx, bson.M{"_id": id}).Decode(&restaurant); err != nil {
		return nil, err
	}
	return &restaurant, nil
}

// loadLayout returns the restaurant's floor plan, or an empty normalised
// layout when none has been saved yet.
func (b *Base) loadLayout(ctx context.Context, restaurantID bson.ObjectID) (*models.RestaurantLayout, error) {
	var layout models.RestaurantLayout
	err := b.DB.Collection("restaurantlayouts").FindOne(ctx, bson.M{"restaurantId": restaurantID.Hex()}).Decode(&layout)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}
	layout.RestaurantID = restaurantID.Hex()
	layout.Normalize()
	return &layout, nil
}

// publish tells connected dashboards that something changed.
func (b *Base) publish(restaurantID bson.ObjectID, e events.Event) {
	if b.Hub != nil {
		b.Hub.Publish(restaurantID.Hex(), e)
	}
}

// ownsUser reports whether the caller may act for the given diner. A request
// without a token is allowed through here; middleware.Auth.Mobile is what
// rejects anonymous callers once tokens are mandatory.
func ownsUser(r *http.Request, userID bson.ObjectID) bool {
	if authed, ok := middleware.AuthedUser(r); ok {
		return authed == userID
	}
	// A waiter or admin token must not act as a diner.
	return middleware.Role(r) == ""
}

// waiterMayAccess reports whether the caller may act on a restaurant's floor.
func waiterMayAccess(r *http.Request, restaurantID bson.ObjectID) bool {
	role := middleware.Role(r)
	if role == "" {
		return true
	}
	if role != util.RoleWaiter {
		return false
	}
	scoped, ok := middleware.RestaurantID(r)
	return ok && scoped == restaurantID
}

// userPushTokens returns a diner's registered devices.
func (b *Base) userPushTokens(ctx context.Context, userID bson.ObjectID) []string {
	var user struct {
		PushTokens []string `bson:"pushTokens"`
	}
	opts := options.FindOne().SetProjection(bson.M{"pushTokens": 1})
	if err := b.DB.Collection("users").FindOne(ctx, bson.M{"_id": userID}, opts).Decode(&user); err != nil {
		return nil
	}
	return user.PushTokens
}

// notifyUser pushes a notification to a diner's devices, if any are registered.
func (b *Base) notifyUser(userID bson.ObjectID, msg util.PushMessage) {
	if userID.IsZero() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	util.SendPush(b.userPushTokens(ctx, userID), msg)
}

// notifyWaiters pushes a notification to every waiter of a restaurant who is
// signed in on a device. Waiters who clocked out are skipped.
func (b *Base) notifyWaiters(restaurantID bson.ObjectID, msg util.PushMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	opts := options.Find().SetProjection(bson.M{"pushTokens": 1, "onDuty": 1, "dutyStartedAt": 1})
	cursor, err := b.DB.Collection("waiters").Find(ctx, bson.M{
		"restaurantId": restaurantID,
		"pushTokens.0": bson.M{"$exists": true},
	}, opts)
	if err != nil {
		return
	}
	var waiters []models.Waiter
	if err := cursor.All(ctx, &waiters); err != nil {
		return
	}
	var tokens []string
	for _, w := range waiters {
		// dutyStartedAt is only set once a waiter has used the duty switch;
		// until then they have not opted out of anything.
		if w.DutyStartedAt != nil && !w.OnDuty {
			continue
		}
		tokens = append(tokens, w.PushTokens...)
	}
	util.SendPush(tokens, msg)
}

func boolPtr(v bool) *bool {
	return &v
}

// isTransactionUnsupported reports whether an error means the MongoDB
// deployment cannot run multi-document transactions (a standalone server).
func isTransactionUnsupported(err error) bool {
	if err == nil {
		return false
	}
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) && cmdErr.Code == 20 {
		return true
	}
	return strings.Contains(err.Error(), "Transaction numbers are only allowed on a replica set member or mongos")
}
