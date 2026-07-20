package models

import (
	"go.mongodb.org/mongo-driver/v2/bson"
)

type RestaurantLayout struct {
	ID            bson.ObjectID `bson:"_id,omitempty" json:"_id"`
	RestaurantID  string        `bson:"restaurantId" json:"restaurantId"`
	DiningAreas   []string      `bson:"diningAreas" json:"diningAreas"`
	TotalTables   int           `bson:"totalTables" json:"totalTables"`
	TotalCapacity int           `bson:"totalCapacity" json:"totalCapacity"`
	TablePosition []Table       `bson:"tablePosition" json:"tablePosition"`
}

type Table struct {
	ObjectID bson.ObjectID `bson:"_id,omitempty" json:"_id,omitempty"`
	ID       string        `bson:"id" json:"id"`
	Name     string        `bson:"name" json:"name"`
	Status   string        `bson:"status,omitempty" json:"status,omitempty"`
	Position Position      `bson:"position" json:"position"`
	Rotation float64       `bson:"rotation" json:"rotation"`
	Shape    string        `bson:"shape" json:"shape"`
	Size     Size          `bson:"size" json:"size"`
	Chairs   []Chair       `bson:"chairs" json:"chairs"`
	FloorID  string        `bson:"floorId" json:"floorId"`
}

type Position struct {
	X float64 `bson:"x" json:"x"`
	Y float64 `bson:"y" json:"y"`
}

type Size struct {
	Width  float64 `bson:"width" json:"width"`
	Height float64 `bson:"height" json:"height"`
}

type Chair struct {
	ObjectID bson.ObjectID `bson:"_id,omitempty" json:"_id,omitempty"`
	ID       string        `bson:"id" json:"id"`
	Position string        `bson:"position" json:"position"`
}
