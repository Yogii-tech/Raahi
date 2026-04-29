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
	PickupLat     float64            `bson:"pickupLat" json:"pickupLat"`
	PickupLng     float64            `bson:"pickupLng" json:"pickupLng"`
	Dropoff       string             `bson:"dropoff" json:"dropoff"`
	DropoffLat    float64            `bson:"dropoffLat" json:"dropoffLat"`
	DropoffLng    float64            `bson:"dropoffLng" json:"dropoffLng"`
	Date          string             `bson:"date" json:"date"`
	DepartureTime string             `bson:"departureTime" json:"departureTime"`
	SeatsTotal    int                `bson:"seatsTotal" json:"seatsTotal"`
	SeatingLayout string             `bson:"seatingLayout" json:"seatingLayout"`
	SeatsBooked   int                `bson:"seatsBooked" json:"seatsBooked"`
	PricePerSeat  float64            `bson:"pricePerSeat" json:"pricePerSeat"`
	TakenSeats    []int              `bson:"takenSeats" json:"takenSeats"`
	DriverName    string             `bson:"driverName" json:"driverName"`
	Status        string             `bson:"status" json:"status"` // "available", "completed", "cancelled"
	CompletedAt   time.Time          `bson:"completedAt" json:"completedAt"`
	CreatedAt     time.Time          `bson:"createdAt" json:"createdAt"`
}

type Booking struct {
	ID                primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	RideID            primitive.ObjectID `bson:"rideId" json:"rideId"`
	PassengerID       primitive.ObjectID `bson:"passengerId" json:"passengerId"`
	Type              string             `bson:"type" json:"type"` // "passenger" or "parcel"
	
	// Passenger specific
	SeatsRequested    int                `bson:"seatsRequested" json:"seatsRequested"`
	SeatLayout        []int              `bson:"seatLayout" json:"seatLayout"` // Selected seat indexes
	RoofCarrier       bool               `bson:"roofCarrier" json:"roofCarrier"`
	MotionSickness    bool               `bson:"motionSickness" json:"motionSickness"`
	
	// Parcel specific
	Pickup            string             `bson:"pickup" json:"pickup"`
	Dropoff           string             `bson:"dropoff" json:"dropoff"`
	RecipientName     string             `bson:"recipientName" json:"recipientName"`
	ContactNumber     string             `bson:"contactNumber" json:"contactNumber"`
	DropLocation      string             `bson:"dropLocation" json:"dropLocation"`
	Notes             string             `bson:"notes" json:"notes"`
	ParcelSize        string             `bson:"parcelSize" json:"parcelSize"`
	Price             string             `bson:"price" json:"price"`
	PhotoURL          string             `bson:"photoUrl" json:"photoUrl"`

	Status            string             `bson:"status" json:"status"` // "pending", "accepted", "rejected", "completed"
	CompletedAt       time.Time          `bson:"completedAt" json:"completedAt"`
	ViewedByPassenger bool               `bson:"viewedByPassenger" json:"viewedByPassenger"`
	ViewedByDriver    bool               `bson:"viewedByDriver" json:"viewedByDriver"`
	CreatedAt         time.Time          `bson:"createdAt" json:"createdAt"`
}
