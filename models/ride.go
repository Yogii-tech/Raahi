package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type Ride struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID    primitive.ObjectID `bson:"userId" json:"userId"`
	Pickup    string             `bson:"pickup" json:"pickup"`
	Dropoff   string             `bson:"dropoff" json:"dropoff"`
	RideType  string             `bson:"rideType" json:"rideType"`
	CreatedAt time.Time          `bson:"createdAt" json:"createdAt"`
}
