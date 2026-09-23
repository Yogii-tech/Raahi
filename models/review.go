package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Review is submitted by a passenger after a completed ride.
type Review struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	DriverID    primitive.ObjectID `bson:"driverId" json:"driverId"`
	PassengerID primitive.ObjectID `bson:"passengerId" json:"passengerId"`
	RideID      primitive.ObjectID `bson:"rideId" json:"rideId"`
	Rating      int                `bson:"rating" json:"rating"` // 1–5
	Comment     string             `bson:"comment,omitempty" json:"comment,omitempty"`
	PassengerName string           `bson:"passengerName,omitempty" json:"passengerName,omitempty"`
	CreatedAt   time.Time          `bson:"createdAt" json:"createdAt"`
}
