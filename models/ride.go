package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type StopInfo struct {
	Name      string  `bson:"name" json:"name"`
	DistanceM float64 `bson:"distanceM" json:"distanceM"` // Distance from start in meters
	Lat       float64 `bson:"lat" json:"lat"`
	Lon       float64 `bson:"lon" json:"lon"`
}

type Ride struct {
	ID                 primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	DriverID           primitive.ObjectID `bson:"driverId" json:"driverId"`
	VehicleModel       string             `bson:"vehicleModel" json:"vehicleModel"`
	VehicleNumber      string             `bson:"vehicleNumber" json:"vehicleNumber"`
	Pickup             string             `bson:"pickup" json:"pickup"`
	Dropoff            string             `bson:"dropoff" json:"dropoff"`
	Route              []string           `bson:"route" json:"route"`                                         // Ordered list of stops: [Kotdwar, Lansdowne, Pauri]
	RouteCoords        [][]float64        `bson:"routeCoords,omitempty" json:"routeCoords,omitempty"`         // [[lat,lon], ...] road geometry from OSRM
	TotalDistanceM     float64            `bson:"totalDistanceM,omitempty" json:"totalDistanceM,omitempty"`   // Total route distance in meters
	DiscoveredStops    []StopInfo         `bson:"discoveredStops,omitempty" json:"discoveredStops,omitempty"` // Auto-discovered intermediate settlements
	DepartureTime      string             `bson:"departureTime" json:"departureTime"`
	Date               string             `bson:"date" json:"date"`
	SeatsTotal         int                `bson:"seatsTotal" json:"seatsTotal"`
	SeatsBooked        int                `bson:"seatsBooked" json:"seatsBooked"`
	PricePerSeat       float64            `bson:"pricePerSeat" json:"pricePerSeat"`
	TakenSeats         []int              `bson:"takenSeats" json:"takenSeats"`
	ManualBlockedSeats []int              `bson:"manualBlockedSeats" json:"manualBlockedSeats"`
	DriverName         string             `bson:"driverName" json:"driverName"`
	Status             string             `bson:"status" json:"status"` // "available", "completed", "cancelled"
	CreatedAt          time.Time          `bson:"createdAt" json:"createdAt"`
}

type Booking struct {
	ID                primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	RideID            primitive.ObjectID `bson:"rideId" json:"rideId"`
	PassengerID       primitive.ObjectID `bson:"passengerId" json:"passengerId"`
	Type              string             `bson:"type" json:"type"` // "seat" or "parcel"
	Pickup            string             `bson:"pickup" json:"pickup"`
	Dropoff           string             `bson:"dropoff" json:"dropoff"`
	SeatsRequested    int                `bson:"seatsRequested" json:"seatsRequested"`
	SeatLayout        []int              `bson:"seatLayout" json:"seatLayout"` // Selected seat indexes
	RoofCarrier       bool               `bson:"roofCarrier" json:"roofCarrier"`
	MotionSickness    bool               `bson:"motionSickness" json:"motionSickness"`
	ParcelSize        string             `bson:"parcelSize,omitempty" json:"parcelSize,omitempty"`
	RecipientName     string             `bson:"recipientName,omitempty" json:"recipientName,omitempty"`
	ContactNumber     string             `bson:"contactNumber,omitempty" json:"contactNumber,omitempty"`
	DropLocation      string             `bson:"dropLocation,omitempty" json:"dropLocation,omitempty"`
	Notes             string             `bson:"notes,omitempty" json:"notes,omitempty"`
	PhotoUrl          string             `bson:"photoUrl,omitempty" json:"photoUrl,omitempty"`
	Price             string             `bson:"price,omitempty" json:"price,omitempty"`
	Status            string             `bson:"status" json:"status"` // "pending", "accepted", "rejected"
	ViewedByPassenger bool               `bson:"viewedByPassenger" json:"viewedByPassenger"`
	ViewedByDriver    bool               `bson:"viewedByDriver" json:"viewedByDriver"`
	CreatedAt         time.Time          `bson:"createdAt" json:"createdAt"`
}
