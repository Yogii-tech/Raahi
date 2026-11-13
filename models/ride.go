package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// RideStatus represents the explicit lifecycle state of a Ride.
type RideStatus string

const (
	RideStatusAvailable RideStatus = "available"
	RideStatusOngoing   RideStatus = "ongoing"
	RideStatusCompleted RideStatus = "completed"
	RideStatusCancelled RideStatus = "cancelled"
)

var validRideTransitions = map[RideStatus]map[RideStatus]bool{
	RideStatusAvailable: {
		RideStatusOngoing:   true,
		RideStatusCompleted: true,
		RideStatusCancelled: true,
	},
	RideStatusOngoing: {
		RideStatusCompleted: true,
	},
	RideStatusCompleted: {},
	RideStatusCancelled: {},
}

// IsValidRideStatus reports whether s is a recognized RideStatus value.
func IsValidRideStatus(s RideStatus) bool {
	_, ok := validRideTransitions[s]
	return ok
}

// CanTransitionRideStatus reports whether transitioning a Ride from `from` to `to`
// is permitted by the explicit Ride state machine.
func CanTransitionRideStatus(from, to RideStatus) bool {
	allowed, ok := validRideTransitions[from]
	if !ok {
		return false
	}
	return allowed[to]
}

// BookingStatus represents the explicit lifecycle state of a Booking.
type BookingStatus string

const (
	BookingStatusPending   BookingStatus = "pending"
	BookingStatusAccepted  BookingStatus = "accepted"
	BookingStatusRejected  BookingStatus = "rejected"
	BookingStatusCompleted BookingStatus = "completed"
	BookingStatusCancelled BookingStatus = "cancelled"
)

var validBookingTransitions = map[BookingStatus]map[BookingStatus]bool{
	BookingStatusPending: {
		BookingStatusAccepted:  true,
		BookingStatusRejected:  true,
		BookingStatusCancelled: true,
	},
	BookingStatusAccepted: {
		BookingStatusCompleted: true,
		BookingStatusCancelled: true,
	},
	BookingStatusRejected:  {},
	BookingStatusCompleted: {},
	BookingStatusCancelled: {},
}

// IsValidBookingStatus reports whether s is a recognized BookingStatus value.
func IsValidBookingStatus(s BookingStatus) bool {
	_, ok := validBookingTransitions[s]
	return ok
}

// CanTransitionBookingStatus reports whether transitioning a Booking from `from` to `to`
// is permitted by the explicit Booking state machine.
func CanTransitionBookingStatus(from, to BookingStatus) bool {
	allowed, ok := validBookingTransitions[from]
	if !ok {
		return false
	}
	return allowed[to]
}

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
	Status             string             `bson:"status" json:"status"` // "available", "ongoing", "completed", "cancelled"
	StartedAt          time.Time          `bson:"startedAt,omitempty" json:"startedAt,omitempty"`
	CompletedAt        time.Time          `bson:"completedAt,omitempty" json:"completedAt,omitempty"`
	CancelledAt        time.Time          `bson:"cancelledAt,omitempty" json:"cancelledAt,omitempty"`
	CreatedAt          time.Time          `bson:"createdAt" json:"createdAt"`
}

type Booking struct {
	ID                primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	BookingID         string             `bson:"bookingId,omitempty" json:"bookingId"`
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
	Status            string             `bson:"status" json:"status"` // "pending", "accepted", "rejected", "completed", "cancelled"
	ViewedByPassenger bool               `bson:"viewedByPassenger" json:"viewedByPassenger"`
	ViewedByDriver    bool               `bson:"viewedByDriver" json:"viewedByDriver"`
	CompletedAt       time.Time          `bson:"completedAt,omitempty" json:"completedAt,omitempty"`
	CancelledAt       time.Time          `bson:"cancelledAt,omitempty" json:"cancelledAt,omitempty"`
	CreatedAt         time.Time          `bson:"createdAt" json:"createdAt"`
}
