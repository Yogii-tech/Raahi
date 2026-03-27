package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type Ride struct {
	ID            primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	DriverID      primitive.ObjectID `bson:"driverId" json:"driverId"`
	VehicleModel  string             `bson:"vehicleModel" json:"vehicleModel"`
	VehicleNumber string             `bson:"vehicleNumber" json:"vehicleNumber"`
	Pickup        string             `bson:"pickup" json:"pickup"`
	Dropoff       string             `bson:"dropoff" json:"dropoff"`
	DepartureTime string             `bson:"departureTime" json:"departureTime"`
	SeatsTotal    int                `bson:"seatsTotal" json:"seatsTotal"`
	SeatsBooked   int                `bson:"seatsBooked" json:"seatsBooked"`
	PricePerSeat  float64            `bson:"pricePerSeat" json:"pricePerSeat"`
	TakenSeats    []int              `bson:"takenSeats" json:"takenSeats"`
	DriverName    string             `bson:"driverName" json:"driverName"`
	Status        string             `bson:"status" json:"status"` // "available", "completed", "cancelled"
	CreatedAt     time.Time          `bson:"createdAt" json:"createdAt"`
}

type Booking struct {
	ID                primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	RideID            primitive.ObjectID `bson:"rideId" json:"rideId"`
	PassengerID       primitive.ObjectID `bson:"passengerId" json:"passengerId"`
	SeatsRequested    int                `bson:"seatsRequested" json:"seatsRequested"`
	SeatLayout        []int              `bson:"seatLayout" json:"seatLayout"` // Selected seat indexes
	RoofCarrier       bool               `bson:"roofCarrier" json:"roofCarrier"`
	MotionSickness    bool               `bson:"motionSickness" json:"motionSickness"`
	Status            string             `bson:"status" json:"status"` // "pending", "accepted", "rejected"
	ViewedByPassenger bool               `bson:"viewedByPassenger" json:"viewedByPassenger"`
	ViewedByDriver    bool               `bson:"viewedByDriver" json:"viewedByDriver"`
	CreatedAt         time.Time          `bson:"createdAt" json:"createdAt"`
}
