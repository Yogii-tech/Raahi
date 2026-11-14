package models

import (
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// MinRequiredVehicleDocuments is the minimum number of distinct uploaded KYC
// document URLs required before a driver can submit or be verified.
const MinRequiredVehicleDocuments = 3

// CountVehicleDocuments returns the count of distinct non-empty uploaded document URLs
// across all five Vehicle document fields (DL, RC, Pollution, Vehicle Image, Ownership).
func CountVehicleDocuments(v *Vehicle) int {
	if v == nil {
		return 0
	}
	seen := make(map[string]bool, 5)
	for _, rawURL := range []string{v.DLUrl, v.RCUrl, v.PollutionUrl, v.VehicleImageUrl, v.OwnershipUrl} {
		clean := strings.TrimSpace(rawURL)
		if clean != "" {
			seen[clean] = true
		}
	}
	return len(seen)
}

// VerificationStatus represents the explicit driver KYC verification state.
type VerificationStatus string

const (
	VerificationStatusNone     VerificationStatus = ""
	VerificationStatusPending  VerificationStatus = "pending"
	VerificationStatusVerified VerificationStatus = "verified"
	VerificationStatusRejected VerificationStatus = "rejected"
)

var validVerificationTransitions = map[VerificationStatus]map[VerificationStatus]bool{
	VerificationStatusNone: {
		VerificationStatusPending:  true,
		VerificationStatusVerified: true,
		VerificationStatusRejected: true,
	},
	VerificationStatusPending: {
		VerificationStatusVerified: true,
		VerificationStatusRejected: true,
	},
	VerificationStatusVerified: {
		VerificationStatusPending:  true,
		VerificationStatusRejected: true,
	},
	VerificationStatusRejected: {
		VerificationStatusPending:  true,
		VerificationStatusVerified: true,
	},
}

// CanTransitionVerificationStatus reports whether transitioning driver KYC
// verification status from `from` to `to` is allowed.
func CanTransitionVerificationStatus(from, to VerificationStatus) bool {
	allowed, ok := validVerificationTransitions[from]
	if !ok {
		return false
	}
	return allowed[to]
}

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
	ID                 primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	PhoneNumber        string             `bson:"phone_number" json:"phone_number"`
	Name               string             `bson:"name" json:"name"`
	OTP                string             `bson:"otp" json:"-"` // never expose hash to clients
	OTPExpiry          time.Time          `bson:"otp_expiry" json:"-"` // OTP valid for 10 minutes
	TrustedContacts    []Contact          `bson:"trusted_contacts" json:"trusted_contacts"`
	Role               string             `bson:"role" json:"role"`
	Language           string             `bson:"language,omitempty" json:"language,omitempty"`
	Location           string             `bson:"location,omitempty" json:"location"`
	VerificationStatus string             `bson:"verification_status,omitempty" json:"verification_status"` // "pending", "verified", "rejected"
	RejectionReason    string             `bson:"rejection_reason,omitempty" json:"rejection_reason"`
	SubmittedAt        time.Time          `bson:"submitted_at,omitempty" json:"submitted_at"`
	Vehicle            *Vehicle           `bson:"vehicle,omitempty" json:"vehicle"`
	TokenVersion       int                `bson:"token_version" json:"-"`
	FCMToken           string             `bson:"fcm_token,omitempty" json:"-"`  // legacy single device push token
	FCMTokens          []string           `bson:"fcm_tokens,omitempty" json:"-"` // all active device push tokens
}

