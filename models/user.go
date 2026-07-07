package models

import (
	"go.mongodb.org/mongo-driver/bson/primitive"
	"time"
)

type Contact struct {
	Name  string `bson:"name" json:"name"`
	Phone string `bson:"phone" json:"phone"`
}

type Vehicle struct {
	VehicleName     string  `bson:"vehicle_name" json:"vehicle_name"`
	VehicleType     string  `bson:"vehicle_type" json:"vehicle_type"`
	Seats           int     `bson:"seats" json:"seats"`
	VehicleNumber   string  `bson:"vehicle_number" json:"vehicle_number"`
	DLUrl           string  `bson:"dl_url" json:"dl_url"`
	RCUrl           string  `bson:"rc_url" json:"rc_url"`
	PollutionUrl    string  `bson:"pollution_url" json:"pollution_url"`
	VehicleImageUrl string  `bson:"vehicle_image_url" json:"vehicle_image_url"`
	OwnershipUrl    string  `bson:"ownership_url" json:"ownership_url"`
	SeatingLayout   string  `bson:"seating_layout" json:"seating_layout"` // "sedan", "suv", "bus_2x2"
	RatePerKm       float64 `bson:"rate_per_km" json:"rate_per_km"`       // Default price per KM
}

type User struct {
	ID              primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	PhoneNumber     string             `bson:"phone_number" json:"phone_number"`
	Name            string             `bson:"name" json:"name"`
	OTP             string             `bson:"otp" json:"-"` // never expose hash to clients
	OTPExpiry       time.Time          `bson:"otp_expiry" json:"-"` // OTP valid for 10 minutes
	TrustedContacts []Contact          `bson:"trusted_contacts" json:"trusted_contacts"`
	Role            string             `bson:"role" json:"role"`
	Vehicle         *Vehicle           `bson:"vehicle,omitempty" json:"vehicle"`
	TokenVersion    int                `bson:"token_version" json:"-"`
}
