package models

import (
	"strconv"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type RestaurantLayout struct {
	ID           bson.ObjectID `bson:"_id,omitempty" json:"_id"`
	RestaurantID string        `bson:"restaurantId" json:"restaurantId"`
	// Floors are the dining areas with stable ids; tables point at them through
	// Table.FloorID. DiningAreas mirrors the floor names in order, which is the
	// only form older app builds understand.
	Floors        []Floor  `bson:"floors,omitempty" json:"floors"`
	DiningAreas   []string `bson:"diningAreas" json:"diningAreas"`
	TotalTables   int      `bson:"totalTables" json:"totalTables"`
	TotalCapacity int      `bson:"totalCapacity" json:"totalCapacity"`
	TablePosition []Table  `bson:"tablePosition" json:"tablePosition"`
}

type Floor struct {
	ID   string `bson:"id" json:"id"`
	Name string `bson:"name" json:"name"`
}

// Normalize fills in whichever of Floors/DiningAreas is missing and makes
// sure no list is nil, so clients always get arrays.
//
// Layouts saved before floors had ids only stored names, and their tables
// reference a floor by its 1-based position ("1", "2", ...). Normalize
// reproduces exactly those ids so existing tables stay on their floor.
func (l *RestaurantLayout) Normalize() {
	if len(l.Floors) == 0 {
		l.Floors = make([]Floor, 0, len(l.DiningAreas))
		for i, name := range l.DiningAreas {
			l.Floors = append(l.Floors, Floor{ID: strconv.Itoa(i + 1), Name: name})
		}
	}
	l.DiningAreas = make([]string, 0, len(l.Floors))
	for _, f := range l.Floors {
		l.DiningAreas = append(l.DiningAreas, f.Name)
	}
	if l.TablePosition == nil {
		l.TablePosition = []Table{}
	}
	for i := range l.TablePosition {
		if l.TablePosition[i].Chairs == nil {
			l.TablePosition[i].Chairs = []Chair{}
		}
	}
}

// FloorName resolves a table's floor id to the floor's display name.
func (l *RestaurantLayout) FloorName(floorID string) string {
	for _, f := range l.Floors {
		if f.ID == floorID {
			return f.Name
		}
	}
	return ""
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
