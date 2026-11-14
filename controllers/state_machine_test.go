package controllers

import (
	"testing"

	"raahi-backend/models"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestCheckAcceptBookingSeatConflict(t *testing.T) {
	ride := models.Ride{
		ID:                 primitive.NewObjectID(),
		Pickup:             "Bageshwar",
		Dropoff:            "Haldwani",
		Route:              []string{"Bageshwar", "Almora", "Haldwani"},
		SeatsTotal:         4,
		ManualBlockedSeats: []int{4},
	}

	existingAccepted := []models.Booking{
		{
			ID:         primitive.NewObjectID(),
			RideID:     ride.ID,
			Type:       "seat",
			Pickup:     "Bageshwar",
			Dropoff:    "Almora",
			SeatLayout: []int{1, 2},
			Status:     string(models.BookingStatusAccepted),
		},
	}

	t.Run("allows non-overlapping segment reusing same physical seat", func(t *testing.T) {
		target := models.Booking{
			ID:         primitive.NewObjectID(),
			RideID:     ride.ID,
			Type:       "seat",
			Pickup:     "Almora",
			Dropoff:    "Haldwani",
			SeatLayout: []int{1, 2},
		}
		if msg := checkAcceptBookingSeatConflict(ride, target, existingAccepted); msg != "" {
			t.Fatalf("expected non-overlapping segment to be allowed, got %q", msg)
		}
	})

	t.Run("rejects overlapping segment on already-accepted seat", func(t *testing.T) {
		target := models.Booking{
			ID:         primitive.NewObjectID(),
			RideID:     ride.ID,
			Type:       "seat",
			Pickup:     "Bageshwar",
			Dropoff:    "Haldwani",
			SeatLayout: []int{2, 3},
		}
		if msg := checkAcceptBookingSeatConflict(ride, target, existingAccepted); msg == "" {
			t.Fatal("expected overlapping segment seat conflict to be rejected")
		}
	})

	t.Run("rejects manually blocked seat", func(t *testing.T) {
		target := models.Booking{
			ID:         primitive.NewObjectID(),
			RideID:     ride.ID,
			Type:       "seat",
			Pickup:     "Almora",
			Dropoff:    "Haldwani",
			SeatLayout: []int{4},
		}
		if msg := checkAcceptBookingSeatConflict(ride, target, existingAccepted); msg == "" {
			t.Fatal("expected manually blocked seat 4 to be rejected")
		}
	})

	t.Run("rejects out-of-range seat", func(t *testing.T) {
		target := models.Booking{
			ID:         primitive.NewObjectID(),
			RideID:     ride.ID,
			Type:       "seat",
			Pickup:     "Almora",
			Dropoff:    "Haldwani",
			SeatLayout: []int{5},
		}
		if msg := checkAcceptBookingSeatConflict(ride, target, existingAccepted); msg == "" {
			t.Fatal("expected out-of-range seat 5 to be rejected")
		}
	})

	t.Run("allows parcel bookings regardless of seats", func(t *testing.T) {
		target := models.Booking{
			ID:     primitive.NewObjectID(),
			RideID: ride.ID,
			Type:   "parcel",
		}
		if msg := checkAcceptBookingSeatConflict(ride, target, existingAccepted); msg != "" {
			t.Fatalf("expected parcel booking to be allowed, got %q", msg)
		}
	})
}

func TestCountDistinctAcceptedSeats(t *testing.T) {
	bookings := []models.Booking{
		{Type: "seat", SeatLayout: []int{1, 2}},
		{Type: "seat", SeatLayout: []int{2, 3}},
		{Type: "parcel", SeatLayout: []int{4}},
	}
	if got := countDistinctAcceptedSeats(bookings); got != 3 {
		t.Fatalf("countDistinctAcceptedSeats = %d, want 3", got)
	}
}

func TestDriverVehicleKYCAndLanguageHelpers(t *testing.T) {
	base := &models.Vehicle{
		VehicleName:     "Bolero",
		VehicleType:     "SUV",
		Seats:           7,
		VehicleNumber:   "UK02TA1234",
		DLUrl:           "https://cdn.example/dl.png",
		RCUrl:           "https://cdn.example/rc.png",
		PollutionUrl:    "https://cdn.example/puc.png",
		VehicleImageUrl: "https://cdn.example/img.png",
		SeatingLayout:   "suv",
		RatePerKm:       5.0,
	}

	rateOnlyChange := *base
	rateOnlyChange.RatePerKm = 7.5
	if hasDriverVehicleChanged(base, &rateOnlyChange) {
		t.Errorf("RatePerKm change must NOT trigger re-KYC")
	}

	nameChange := *base
	nameChange.VehicleName = "Maxx"
	if !hasDriverVehicleChanged(base, &nameChange) {
		t.Errorf("VehicleName change MUST trigger re-KYC")
	}

	zeroSeats := *base
	zeroSeats.Seats = 0
	if validateDriverVehicleKYC(&zeroSeats) == "" {
		t.Errorf("expected Seats=0 to fail KYC validation")
	}

	threeDocs := *base
	threeDocs.RCUrl = "   "
	if msg := validateDriverVehicleKYC(&threeDocs); msg != "" {
		t.Errorf("expected 3 distinct documents (with empty RCUrl) to pass KYC validation, got %q", msg)
	}

	twoDocs := threeDocs
	twoDocs.PollutionUrl = "   "
	if validateDriverVehicleKYC(&twoDocs) == "" {
		t.Errorf("expected fewer than 3 documents to fail KYC validation")
	}

	if msg := validateDriverVehicleKYC(base); msg != "" {
		t.Errorf("expected valid vehicle with optional OwnershipUrl empty to pass, got %q", msg)
	}

	if lang, ok := normalizeLanguage(" HI "); !ok || lang != "hi" {
		t.Errorf("normalizeLanguage(\" HI \") = (%q, %v), want (\"hi\", true)", lang, ok)
	}
	if _, ok := normalizeLanguage("fr"); ok {
		t.Errorf("normalizeLanguage(\"fr\") unexpectedly succeeded")
	}
}
