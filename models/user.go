package models

import "go.mongodb.org/mongo-driver/bson/primitive"

type Contact struct {
	Name  string `bson:"name" json:"name"`
	Phone string `bson:"phone" json:"phone"`
}

type VehicleDetails struct {
	VehicleName       string `bson:"vehicle_name" json:"vehicle_name"`
	VehicleType       string `bson:"vehicle_type" json:"vehicle_type"`
	Seats             int    `bson:"seats" json:"seats"`
	SeatingLayout     string `bson:"seating_layout" json:"seating_layout"`
	VehicleNumber     string `bson:"vehicle_number" json:"vehicle_number"`
	DrivingLicenseURL string `bson:"dl_url" json:"dl_url"`
	RCURL             string `bson:"rc_url" json:"rc_url"`
	PollutionCertURL  string `bson:"pollution_url" json:"pollution_url"`
	VehicleImageURL   string `bson:"vehicle_image_url" json:"vehicle_image_url"`
	OwnershipProofURL string `bson:"ownership_url" json:"ownership_url"`
}

type User struct {
	ID              primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	PhoneNumber     string             `bson:"phone_number" json:"phone_number"`
	Name            string             `bson:"name" json:"name"`
	OTP             string             `bson:"otp" json:"otp"`
	TrustedContacts []Contact          `bson:"trusted_contacts" json:"trusted_contacts"`
	Role            string             `bson:"role" json:"role"`
	Language        string             `bson:"language" json:"language"`
	Vehicle         *VehicleDetails    `bson:"vehicle,omitempty" json:"vehicle,omitempty"`
}
