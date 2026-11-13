package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Notification categories for admin & platform filtering.
const (
	NotifCategoryUsers    = "users"
	NotifCategoryDrivers  = "drivers"
	NotifCategoryRides    = "rides"
	NotifCategoryBookings = "bookings"
	NotifCategoryParcels  = "parcels"
	NotifCategoryReviews  = "reviews"
	NotifCategorySystem   = "system"
)

// AdminView constants matching AdminDashboardScreen.tsx AdminView routes.
const (
	AdminViewDashboard          = "dashboard"
	AdminViewDriverVerification = "driver_verification"
	AdminViewDocumentReview     = "document_review"
	AdminViewDrivers            = "drivers"
	AdminViewVehicles           = "vehicles"
	AdminViewActiveRides        = "active_rides"
	AdminViewCompletedRides     = "completed_rides"
	AdminViewCancelledRides     = "cancelled_rides"
	AdminViewBookings           = "bookings"
	AdminViewParcels            = "parcels"
	AdminViewAdminUsers         = "admin_users"
	AdminViewNotifications      = "NOTIFICATIONS"
)

// Notification type constants for admin platform events and user notifications.
const (
	NotifTypeUserRegistered         = "user_registered"
	NotifTypeAdminPromoted          = "admin_promoted"
	NotifTypeDriverKYCSubmitted     = "driver_kyc_submitted"
	NotifTypeDriverKYCResubmitted   = "driver_kyc_resubmitted"
	NotifTypeVehicleUpdated         = "vehicle_updated"
	NotifTypeDriverVerified         = "driver_verified"
	NotifTypeDriverRejected         = "driver_rejected"
	NotifTypeDriverReKYCRequested   = "driver_rekyc_requested"
	NotifTypeDriverDeleted          = "driver_deleted"
	NotifTypeRideCreated            = "ride_created"
	NotifTypeRideStarted            = "ride_started"
	NotifTypeRideCompleted          = "ride_completed"
	NotifTypeRideCancelled          = "ride_cancelled"
	NotifTypeBookingCreated         = "booking_created"
	NotifTypeBookingConfirmed       = "booking_confirmed"
	NotifTypeBookingRejected        = "booking_rejected"
	NotifTypeBookingCancelled       = "booking_cancelled"
	NotifTypeBookingCompleted       = "booking_completed"
	NotifTypeParcelCreated          = "parcel_created"
	NotifTypeParcelConfirmed        = "parcel_confirmed"
	NotifTypeParcelRejected         = "parcel_rejected"
	NotifTypeParcelCancelled        = "parcel_cancelled"
	NotifTypeParcelCompleted        = "parcel_completed"
	NotifTypeReviewSubmitted        = "review_submitted"
	NotifTypeIncompleteUsersCleaned = "incomplete_users_cleaned"
)

type Notification struct {
	ID         primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID     primitive.ObjectID `bson:"userId" json:"userId"`
	Title      string             `bson:"title" json:"title"`
	Message    string             `bson:"message" json:"message"`
	Type       string             `bson:"type" json:"type"`                                 // e.g. "document_verification", "booking_created"
	Category   string             `bson:"category,omitempty" json:"category,omitempty"`     // e.g. "drivers", "rides", "bookings", "parcels"
	TargetView string             `bson:"targetView,omitempty" json:"targetView,omitempty"` // AdminView target e.g. "driver_verification", "bookings"
	RelatedId  string             `bson:"relatedId,omitempty" json:"relatedId,omitempty"`   // Primary entity ID (bookingId, rideId, userId, etc.)
	Metadata   map[string]string  `bson:"metadata,omitempty" json:"metadata,omitempty"`     // Structured deep-link & search context fields
	Read       bool               `bson:"read" json:"read"`
	CreatedAt  time.Time          `bson:"createdAt" json:"createdAt"`
}
