package db

import (
	"context"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func Connect(uri string) *mongo.Client {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	clientOptions := options.Client().ApplyURI(uri)
	client, err := mongo.Connect(clientOptions)
	if err != nil {
		log.Fatalf("Failed to connect to MongoDB: %v", err)
	}

	err = client.Ping(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to ping MongoDB: %v", err)
	}

	log.Println("Successfully connected to MongoDB")
	return client
}

// EnsureIndexes creates the indexes the API's queries rely on. It runs in the
// background and only logs failures: a missing index makes queries slower,
// not wrong, so it must never stop the server from starting. Creating an
// index that already exists is a no-op.
func EnsureIndexes(db *mongo.Database) {
	indexes := map[string][]mongo.IndexModel{
		"orders": {
			{Keys: bson.D{{Key: "restaurantId", Value: 1}, {Key: "createdAt", Value: -1}}},
			{Keys: bson.D{{Key: "restaurantId", Value: 1}, {Key: "orderStatus", Value: 1}, {Key: "createdAt", Value: -1}}},
			{Keys: bson.D{{Key: "userId", Value: 1}, {Key: "createdAt", Value: -1}}},
		},
		"reservations": {
			{Keys: bson.D{{Key: "restaurantId", Value: 1}, {Key: "reservationInfo.bookingFor", Value: 1}}},
			{Keys: bson.D{{Key: "userId", Value: 1}, {Key: "status", Value: 1}}},
			{Keys: bson.D{{Key: "reservationInfo.reservationID", Value: 1}}},
		},
		"restaurantlayouts": {
			{Keys: bson.D{{Key: "restaurantId", Value: 1}}},
		},
		"waiters": {
			{Keys: bson.D{{Key: "restaurantId", Value: 1}}},
		},
		"promotions": {
			{Keys: bson.D{{Key: "restaurantId", Value: 1}}},
			{Keys: bson.D{{Key: "isActive", Value: 1}, {Key: "endDate", Value: 1}}},
		},
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for collection, models := range indexes {
			if _, err := db.Collection(collection).Indexes().CreateMany(ctx, models); err != nil {
				log.Printf("Could not create indexes on %s: %v", collection, err)
			}
		}
	}()
}
