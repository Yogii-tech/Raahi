package controllers

import (
	"context"
	"math"
	"testing"
	"time"

	"raahi-backend/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestValidateDriverVehicleKYC_Extended(t *testing.T) {
	validVehicle := func() *models.Vehicle {
		return &models.Vehicle{
			VehicleName:     "Innova Crysta",
			VehicleType:     "car",
			VehicleNumber:   "UK07AB1234",
			Seats:           6,
			RatePerKm:       12.5,
			DLUrl:           "https://res.cloudinary.com/demo/image/upload/dl.jpg",
			RCUrl:           "https://res.cloudinary.com/demo/image/upload/rc.jpg",
			PollutionUrl:    "https://res.cloudinary.com/demo/image/upload/puc.jpg",
			VehicleImageUrl: "https://res.cloudinary.com/demo/image/upload/veh.jpg",
			SeatingLayout:   "suv",
		}
	}

	if msg := validateDriverVehicleKYC(nil); msg == "" {
		t.Error("expected nil vehicle to be rejected")
	}

	if msg := validateDriverVehicleKYC(validVehicle()); msg != "" {
		t.Errorf("expected valid SUV vehicle without ownership_url to pass, got %q", msg)
	}

	bus := validVehicle()
	bus.Seats = 40
	bus.SeatingLayout = "bus_2x2"
	if msg := validateDriverVehicleKYC(bus); msg != "" {
		t.Errorf("expected 40-seat bus to pass, got %q", msg)
	}

	tooManySeats := validVehicle()
	tooManySeats.Seats = 61
	if msg := validateDriverVehicleKYC(tooManySeats); msg == "" {
		t.Error("expected seats > 60 to be rejected")
	}

	zeroSeats := validVehicle()
	zeroSeats.Seats = 0
	if msg := validateDriverVehicleKYC(zeroSeats); msg == "" {
		t.Error("expected seats <= 0 to be rejected")
	}

	nanRate := validVehicle()
	nanRate.RatePerKm = math.NaN()
	if msg := validateDriverVehicleKYC(nanRate); msg == "" {
		t.Error("expected NaN rate_per_km to be rejected")
	}

	negRate := validVehicle()
	negRate.RatePerKm = -5
	if msg := validateDriverVehicleKYC(negRate); msg == "" {
		t.Error("expected negative rate_per_km to be rejected")
	}

	noName := validVehicle()
	noName.VehicleName = "  "
	if msg := validateDriverVehicleKYC(noName); msg == "" {
		t.Error("expected blank vehicle_name to be rejected")
	}
}

func TestBuildDriverProfileUpdateCAS(t *testing.T) {
	userID := primitive.NewObjectID()
	now := time.Now()
	baseVehicle := models.Vehicle{
		VehicleName:     "Dzire",
		VehicleType:     "car",
		VehicleNumber:   "UK07CD5678",
		Seats:           4,
		RatePerKm:       10,
		DLUrl:           "https://res.cloudinary.com/demo/image/upload/dl1.jpg",
		RCUrl:           "https://res.cloudinary.com/demo/image/upload/rc1.jpg",
		PollutionUrl:    "https://res.cloudinary.com/demo/image/upload/puc1.jpg",
		VehicleImageUrl: "https://res.cloudinary.com/demo/image/upload/veh1.jpg",
	}

	t.Run("blocks pending-review vehicle doc swap with conflict", func(t *testing.T) {
		current := models.User{
			ID:                 userID,
			Role:               "driver",
			VerificationStatus: "pending",
			Vehicle:            &baseVehicle,
		}
		swapped := baseVehicle
		swapped.DLUrl = "https://res.cloudinary.com/demo/image/upload/dl2.jpg"

		_, _, _, _, conflictMsg := buildDriverProfileUpdateCAS(userID, current, "Ramesh", "driver", "en", &swapped, now)
		if conflictMsg == "" {
			t.Fatal("expected conflict message when changing KYC docs while verification_status=pending")
		}
	})

	t.Run("allows verified driver rate update without downgrading verification_status", func(t *testing.T) {
		current := models.User{
			ID:                 userID,
			Role:               "driver",
			VerificationStatus: "verified",
			Vehicle:            &baseVehicle,
		}
		updatedRate := baseVehicle
		updatedRate.RatePerKm = 15

		setMap, filter, isFirst, isResub, conflictMsg := buildDriverProfileUpdateCAS(userID, current, "Ramesh", "driver", "en", &updatedRate, now)
		if conflictMsg != "" {
			t.Fatalf("unexpected conflict: %s", conflictMsg)
		}
		if isFirst || isResub {
			t.Fatalf("expected rate update not to be treated as KYC submission (first=%v resub=%v)", isFirst, isResub)
		}
		if setMap["verification_status"] == "pending" {
			t.Fatal("rate update must not reset verified status back to pending")
		}
		if filter["role"] != "driver" || filter["verification_status"] != "verified" {
			t.Fatalf("expected CAS filter to lock role & verification_status, got %v", filter)
		}
	})

	t.Run("verified driver changing KYC docs requires re-verification and triggers resubmission", func(t *testing.T) {
		current := models.User{
			ID:                 userID,
			Role:               "driver",
			VerificationStatus: "verified",
			Vehicle:            &baseVehicle,
		}
		changedVeh := baseVehicle
		changedVeh.VehicleNumber = "UK08ZZ9999"

		setMap, filter, _, isResub, conflictMsg := buildDriverProfileUpdateCAS(userID, current, "Ramesh", "driver", "en", &changedVeh, now)
		if conflictMsg != "" {
			t.Fatalf("unexpected conflict: %s", conflictMsg)
		}
		if !isResub {
			t.Fatal("expected vehicle change on verified driver to flag resubmission")
		}
		if setMap["verification_status"] != "pending" {
			t.Fatalf("expected verification_status=pending on vehicle change, got %v", setMap["verification_status"])
		}
		if filter["verification_status"] != "verified" {
			t.Fatalf("expected CAS filter to lock previous verified state, got %v", filter)
		}
	})
}

func TestBuildReKYCUpdateDoc(t *testing.T) {
	veh := &models.Vehicle{DLUrl: "https://example.com/dl.jpg", RCUrl: "https://example.com/rc.jpg"}
	withVeh := buildReKYCUpdateDoc(models.User{Vehicle: veh}, "Blurry RC")
	withSet, ok := withVeh["$set"].(bson.M)
	if !ok {
		t.Fatalf("expected $set bson.M in ReKYC update doc, got %T (%v)", withVeh["$set"], withVeh)
	}
	if withSet["verification_status"] != "rejected" || withSet["rejection_reason"] != "Blurry RC" {
		t.Fatalf("unexpected ReKYC $set fields: %v", withSet)
	}
	if val, hasVehicle := withSet["vehicle.dl_url"]; !hasVehicle || val != "" {
		t.Fatalf("expected vehicle.dl_url to be cleared when vehicle != nil, got %v", withSet)
	}

	// Must NOT panic when driver.Vehicle is nil, and must not reference vehicle.* fields
	withoutVeh := buildReKYCUpdateDoc(models.User{Vehicle: nil}, "Missing vehicle info")
	withoutSet, ok := withoutVeh["$set"].(bson.M)
	if !ok {
		t.Fatalf("expected $set bson.M when vehicle is nil, got %T (%v)", withoutVeh["$set"], withoutVeh)
	}
	if withoutSet["verification_status"] != "rejected" {
		t.Fatalf("expected verification_status=rejected when vehicle is nil, got %v", withoutSet)
	}
	if _, hasVehicle := withoutSet["vehicle.dl_url"]; hasVehicle {
		t.Fatalf("did not expect vehicle.dl_url when vehicle is nil, got %v", withoutSet)
	}
}

func TestIncompleteUserCleanupGuards(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

	normalCutoff := incompleteUserCleanupCutoff(false, now)
	if !normalCutoff.Equal(now.Add(-24 * time.Hour)) {
		t.Errorf("normal cutoff = %v, want %v", normalCutoff, now.Add(-24*time.Hour))
	}

	forceCutoff := incompleteUserCleanupCutoff(true, now)
	if forceCutoff.After(now.Add(-15 * time.Minute)) {
		t.Errorf("force=true must preserve at least a 15m grace window, got %v (now=%v)", forceCutoff, now)
	}

	filter := BuildIncompleteUserCleanupFilter(normalCutoff)
	andClauses, ok := filter["$and"].([]bson.M)
	if !ok || len(andClauses) == 0 {
		t.Fatalf("expected non-empty $and clauses in cleanup filter, got %v", filter)
	}
	foundAdminExclusion := false
	for _, clause := range andClauses {
		if roleCond, hasRole := clause["role"].(bson.M); hasRole {
			if roleCond["$ne"] == "admin" {
				foundAdminExclusion = true
			}
		}
	}
	if !foundAdminExclusion {
		t.Fatalf("expected role $ne admin clause inside $and filter, got %v", andClauses)
	}
}

func TestValidateAndSanitizeParcelBooking(t *testing.T) {
	_, _, _, _, _, _, _, errMsg := validateAndSanitizeParcelBooking("", "Asha", "9876543210", "Haldwani", "", "", "100")
	if errMsg == "" {
		t.Error("expected missing parcel size to fail validation")
	}

	_, _, _, _, _, _, _, errMsg = validateAndSanitizeParcelBooking("small", "Asha", "abc", "Haldwani", "", "", "100")
	if errMsg == "" {
		t.Error("expected invalid contact number to fail validation")
	}

	_, _, _, _, _, _, _, errMsg = validateAndSanitizeParcelBooking("small", "Asha", "9876543210", "Haldwani", "", "javascript:alert(1)", "100")
	if errMsg == "" {
		t.Error("expected unsafe photo URL scheme to fail validation")
	}

	_, _, _, _, _, _, _, errMsg = validateAndSanitizeParcelBooking("small", "Asha", "9876543210", "Haldwani", "", "", "-50")
	if errMsg == "" {
		t.Error("expected negative parcel price to fail validation")
	}

	cleanSize, cleanRec, cleanContact, cleanDrop, cleanNotes, _, cleanPrice, errMsg := validateAndSanitizeParcelBooking(
		"medium",
		"<b>Asha</b>",
		"9876543210",
		"Haldwani <script>alert(1)</script>Bus Stand",
		"Handle carefully",
		"https://res.cloudinary.com/demo/image/upload/box.jpg",
		"250",
	)
	if errMsg != "" {
		t.Fatalf("expected valid parcel booking to pass, got %q", errMsg)
	}
	if cleanSize != "medium" || cleanRec != "&lt;b&gt;Asha&lt;/b&gt;" || cleanContact != "9876543210" || cleanPrice != "250" {
		t.Fatalf("unexpected sanitized parcel fields: size=%q rec=%q contact=%q price=%q", cleanSize, cleanRec, cleanContact, cleanPrice)
	}
	if cleanDrop != "Haldwani &lt;script&gt;alert(1)&lt;/script&gt;Bus Stand" || cleanNotes != "Handle carefully" {
		t.Fatalf("unexpected sanitized drop/notes: drop=%q notes=%q", cleanDrop, cleanNotes)
	}
}

func TestCountDistinctAcceptedSeats_SeatAndParcelConsistency(t *testing.T) {
	bookings := []models.Booking{
		{Type: "seat", Status: string(models.BookingStatusAccepted), SeatLayout: []int{1, 2}},
		{Type: "seat", Status: string(models.BookingStatusCompleted), SeatLayout: []int{2, 3}},
		{Type: "parcel", Status: string(models.BookingStatusAccepted), SeatLayout: []int{4}}, // parcel must be ignored
	}
	if got := countDistinctAcceptedSeats(bookings); got != 3 {
		t.Fatalf("countDistinctAcceptedSeats = %d, want 3 distinct seats", got)
	}

	var decoded []models.Booking
	if err := decodeCursorAll(context.Background(), nil, &decoded); err != nil {
		t.Fatalf("decodeCursorAll(nil) returned error: %v", err)
	}
}
