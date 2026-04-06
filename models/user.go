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
	RefreshToken    string             `bson:"refresh_token" json:"refresh_token"`
	TrustedContacts []Contact          `bson:"trusted_contacts" json:"trusted_contacts"`
	Role            string             `bson:"role" json:"role"`
	Language        string             `bson:"language" json:"language"`
	Vehicle         *VehicleDetails    `bson:"vehicle,omitempty" json:"vehicle,omitempty"`
	// Verification
	VerificationStatus string          `bson:"verification_status" json:"verification_status"`
	VerificationNote   string          `bson:"verification_note" json:"verification_note"`
	BlurFlags          map[string]bool `bson:"blur_flags,omitempty" json:"blur_flags,omitempty"`
	// Owner / Driver split
	IsVehicleOwner         bool   `bson:"is_vehicle_owner" json:"is_vehicle_owner"`
	OwnerName              string `bson:"owner_name,omitempty" json:"owner_name,omitempty"`
	OwnerPhone             string `bson:"owner_phone,omitempty" json:"owner_phone,omitempty"`
	AuthorizationLetterURL string `bson:"authorization_letter_url,omitempty" json:"authorization_letter_url,omitempty"`
	// VAHAN verification
	VAHANVerified  bool   `bson:"vahan_verified" json:"vahan_verified"`
	VAHANOwnerName string `bson:"vahan_owner_name,omitempty" json:"vahan_owner_name,omitempty"`
}

const (
	RoleAdmin     = "admin"
	RoleDriver    = "driver"
	RolePassenger = "passenger"
)
