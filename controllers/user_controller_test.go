package controllers

import (
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	"raahi-backend/models"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestValidateDriverVehicleKYC(t *testing.T) {
	validBase := func() models.Vehicle {
		return models.Vehicle{
			VehicleName:   "Mahindra Bolero",
			VehicleNumber: "UK02TA1234",
			Seats:         7,
			RatePerKm:     12,
		}
	}

	const wantDocErr = "At least 3 vehicle documents must be uploaded before verification"

	t.Run("rejects nil vehicle and invalid metadata", func(t *testing.T) {
		if got := validateDriverVehicleKYC(nil); got == "" {
			t.Errorf("expected error for nil vehicle")
		}
		vNoNum := validBase()
		vNoNum.VehicleNumber = "  "
		vNoNum.DLUrl = "https://cdn.example.com/dl.jpg"
		vNoNum.RCUrl = "https://cdn.example.com/rc.jpg"
		vNoNum.PollutionUrl = "https://cdn.example.com/puc.jpg"
		if got := validateDriverVehicleKYC(&vNoNum); got == "" {
			t.Errorf("expected error for missing vehicle number")
		}

		vBadSeats := validBase()
		vBadSeats.Seats = 0
		vBadSeats.DLUrl = "https://cdn.example.com/dl.jpg"
		vBadSeats.RCUrl = "https://cdn.example.com/rc.jpg"
		vBadSeats.PollutionUrl = "https://cdn.example.com/puc.jpg"
		if got := validateDriverVehicleKYC(&vBadSeats); got == "" {
			t.Errorf("expected error for 0 seats")
		}

		vBadRate := validBase()
		vBadRate.RatePerKm = math.NaN()
		vBadRate.DLUrl = "https://cdn.example.com/dl.jpg"
		vBadRate.RCUrl = "https://cdn.example.com/rc.jpg"
		vBadRate.PollutionUrl = "https://cdn.example.com/puc.jpg"
		if got := validateDriverVehicleKYC(&vBadRate); got == "" {
			t.Errorf("expected error for NaN RatePerKm")
		}
	})

	t.Run("blocks fewer than 3 distinct documents", func(t *testing.T) {
		v0 := validBase()
		if got := validateDriverVehicleKYC(&v0); got != wantDocErr {
			t.Errorf("0 docs: got %q, want %q", got, wantDocErr)
		}

		v2 := validBase()
		v2.DLUrl = "https://cdn.example.com/dl.jpg"
		v2.RCUrl = "https://cdn.example.com/rc.jpg"
		if got := validateDriverVehicleKYC(&v2); got != wantDocErr {
			t.Errorf("2 docs: got %q, want %q", got, wantDocErr)
		}

		vDup := validBase()
		vDup.DLUrl = "https://cdn.example.com/shared.jpg"
		vDup.RCUrl = "https://cdn.example.com/shared.jpg"
		vDup.PollutionUrl = "https://cdn.example.com/puc.jpg"
		if got := validateDriverVehicleKYC(&vDup); got != wantDocErr {
			t.Errorf("duplicate URL yielding 2 distinct docs: got %q, want %q", got, wantDocErr)
		}
	})

	t.Run("allows 3, 4, and 5 distinct documents", func(t *testing.T) {
		v3Core := validBase()
		v3Core.DLUrl = "https://cdn.example.com/dl.jpg"
		v3Core.RCUrl = "https://cdn.example.com/rc.jpg"
		v3Core.PollutionUrl = "https://cdn.example.com/puc.jpg"
		if got := validateDriverVehicleKYC(&v3Core); got != "" {
			t.Errorf("3 core docs unexpectedly rejected: %q", got)
		}

		v3WithOwnership := validBase()
		v3WithOwnership.DLUrl = "https://cdn.example.com/dl.jpg"
		v3WithOwnership.VehicleImageUrl = "https://cdn.example.com/car.jpg"
		v3WithOwnership.OwnershipUrl = "https://cdn.example.com/ownership.jpg"
		if got := validateDriverVehicleKYC(&v3WithOwnership); got != "" {
			t.Errorf("3 docs with ownership unexpectedly rejected: %q", got)
		}

		v4 := v3Core
		v4.VehicleImageUrl = "https://cdn.example.com/car.jpg"
		if got := validateDriverVehicleKYC(&v4); got != "" {
			t.Errorf("4 docs unexpectedly rejected: %q", got)
		}

		v5 := v4
		v5.OwnershipUrl = "https://cdn.example.com/ownership.jpg"
		if got := validateDriverVehicleKYC(&v5); got != "" {
			t.Errorf("5 docs unexpectedly rejected: %q", got)
		}
	})
}

func TestValidateAdminDriverVerification(t *testing.T) {
	threeDocVehicle := &models.Vehicle{
		VehicleName:   "Alto K10",
		VehicleNumber: "UK01AB5555",
		Seats:         4,
		RatePerKm:     10,
		DLUrl:         "https://cdn.example.com/dl.jpg",
		RCUrl:         "https://cdn.example.com/rc.jpg",
		OwnershipUrl:  "https://cdn.example.com/own.jpg",
	}
	twoDocVehicle := &models.Vehicle{
		VehicleName:   "Alto K10",
		VehicleNumber: "UK01AB5555",
		Seats:         4,
		RatePerKm:     10,
		DLUrl:         "https://cdn.example.com/dl.jpg",
		RCUrl:         "https://cdn.example.com/rc.jpg",
	}

	t.Run("rejects admin approval when driver has fewer than 3 documents", func(t *testing.T) {
		driver := models.User{
			ID:                 primitive.NewObjectID(),
			Role:               "driver",
			VerificationStatus: string(models.VerificationStatusPending),
			Vehicle:            twoDocVehicle,
		}
		code, errMsg := validateAdminDriverVerification(driver, string(models.VerificationStatusVerified))
		if code != http.StatusBadRequest || errMsg != "At least 3 vehicle documents must be uploaded before verification" {
			t.Fatalf("got (%d, %q), want (400, 3-doc error)", code, errMsg)
		}
	})

	t.Run("allows admin approval for pending, none, and rejected drivers with 3+ docs", func(t *testing.T) {
		for _, fromStatus := range []string{
			string(models.VerificationStatusNone),
			string(models.VerificationStatusPending),
			string(models.VerificationStatusRejected),
		} {
			driver := models.User{
				ID:                 primitive.NewObjectID(),
				Role:               "driver",
				VerificationStatus: fromStatus,
				Vehicle:            threeDocVehicle,
			}
			code, errMsg := validateAdminDriverVerification(driver, string(models.VerificationStatusVerified))
			if code != 0 || errMsg != "" {
				t.Errorf("fromStatus=%q: got (%d, %q), want (0, empty)", fromStatus, code, errMsg)
			}
		}
	})

	t.Run("allows rejecting incomplete 2-doc driver and returns 409 for already verified", func(t *testing.T) {
		pendingTwoDocs := models.User{
			ID:                 primitive.NewObjectID(),
			Role:               "driver",
			VerificationStatus: string(models.VerificationStatusPending),
			Vehicle:            twoDocVehicle,
		}
		if code, errMsg := validateAdminDriverVerification(pendingTwoDocs, string(models.VerificationStatusRejected)); code != 0 || errMsg != "" {
			t.Errorf("rejecting 2-doc pending driver failed: (%d, %q)", code, errMsg)
		}

		alreadyVerified := models.User{
			ID:                 primitive.NewObjectID(),
			Role:               "driver",
			VerificationStatus: string(models.VerificationStatusVerified),
			Vehicle:            threeDocVehicle,
		}
		if code, _ := validateAdminDriverVerification(alreadyVerified, string(models.VerificationStatusVerified)); code != http.StatusConflict {
			t.Errorf("re-verifying already verified driver got status %d, want 409", code)
		}
	})
}

func TestBuildDriverProfileUpdateCAS_ThreeDocs(t *testing.T) {
	uid := primitive.NewObjectID()
	now := time.Date(2026, 11, 14, 11, 30, 0, 0, time.UTC)
	threeDocVehicle := &models.Vehicle{
		VehicleName:     "Innova",
		VehicleType:     "SUV",
		VehicleNumber:   "UK04AB1111",
		Seats:           6,
		SeatingLayout:   "suv",
		RatePerKm:       15,
		DLUrl:           "https://cdn.example.com/dl.jpg",
		RCUrl:           "https://cdn.example.com/rc.jpg",
		VehicleImageUrl: "https://cdn.example.com/car.jpg",
	}

	setMap, _, isFirst, isResub, conflict := buildDriverProfileUpdateCAS(
		uid,
		models.User{ID: uid},
		"Mahesh",
		"driver",
		"en",
		threeDocVehicle,
		now,
	)
	if conflict != "" || !isFirst || isResub {
		t.Fatalf("first 3-doc submission unexpected flags: first=%v resub=%v conflict=%q", isFirst, isResub, conflict)
	}
	if setMap["verification_status"] != string(models.VerificationStatusPending) {
		t.Errorf("verification_status = %v, want pending", setMap["verification_status"])
	}

	// Rejected driver resubmitting with 3 docs transitions cleanly to pending
	setResub, _, isFirst2, isResub2, conflict2 := buildDriverProfileUpdateCAS(
		uid,
		models.User{ID: uid, Role: "driver", VerificationStatus: string(models.VerificationStatusRejected)},
		"Mahesh",
		"driver",
		"en",
		threeDocVehicle,
		now,
	)
	if conflict2 != "" || isFirst2 || !isResub2 {
		t.Fatalf("3-doc resubmission unexpected flags: first=%v resub=%v conflict=%q", isFirst2, isResub2, conflict2)
	}
	if setResub["verification_status"] != string(models.VerificationStatusPending) || setResub["rejection_reason"] != "" {
		t.Errorf("resubmission setMap unexpected: %#v", setResub)
	}
}

func TestBuildAdminDriverRow_DocumentCount(t *testing.T) {
	uid := primitive.NewObjectID()
	driver := models.User{
		ID:                 uid,
		Name:               "Harish",
		PhoneNumber:        "+919800011122",
		Role:               "driver",
		VerificationStatus: string(models.VerificationStatusPending),
		Vehicle: &models.Vehicle{
			VehicleName:     "Bolero",
			VehicleNumber:   "UK02TA7777",
			Seats:           7,
			DLUrl:           "https://cdn.example.com/dl.jpg",
			RCUrl:           "https://cdn.example.com/rc.jpg",
			OwnershipUrl:    "https://cdn.example.com/own.jpg",
			VehicleImageUrl: "",
			PollutionUrl:    "",
		},
	}

	row := buildAdminDriverRow(driver, 4)
	if row.DocumentCount != 3 {
		t.Fatalf("DocumentCount = %d, want 3", row.DocumentCount)
	}

	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("json.Marshal(row): %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if decoded["documentCount"] != float64(3) {
		t.Errorf("JSON documentCount = %v, want 3", decoded["documentCount"])
	}

	noVehicleRow := buildAdminDriverRow(models.User{ID: uid, Role: "driver"}, 0)
	if noVehicleRow.DocumentCount != 0 || noVehicleRow.VerificationStatus != "pending" {
		t.Errorf("nil vehicle defaults unexpected: %+v", noVehicleRow)
	}
}
