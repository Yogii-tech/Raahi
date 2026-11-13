package models

import "testing"

func TestCanTransitionRideStatus(t *testing.T) {
	allStatuses := []RideStatus{
		RideStatusAvailable,
		RideStatusOngoing,
		RideStatusCompleted,
		RideStatusCancelled,
		RideStatus("unknown"),
		RideStatus(""),
	}

	allowed := map[RideStatus]map[RideStatus]bool{
		RideStatusAvailable: {
			RideStatusOngoing:   true,
			RideStatusCompleted: true,
			RideStatusCancelled: true,
		},
		RideStatusOngoing: {
			RideStatusCompleted: true,
		},
	}

	for _, from := range allStatuses {
		for _, to := range allStatuses {
			want := allowed[from][to]
			if got := CanTransitionRideStatus(from, to); got != want {
				t.Errorf("CanTransitionRideStatus(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}

	if !IsValidRideStatus(RideStatusOngoing) || IsValidRideStatus(RideStatus("bogus")) {
		t.Errorf("IsValidRideStatus returned unexpected result")
	}
}

func TestCanTransitionBookingStatus(t *testing.T) {
	allStatuses := []BookingStatus{
		BookingStatusPending,
		BookingStatusAccepted,
		BookingStatusRejected,
		BookingStatusCompleted,
		BookingStatusCancelled,
		BookingStatus("unknown"),
		BookingStatus(""),
	}

	allowed := map[BookingStatus]map[BookingStatus]bool{
		BookingStatusPending: {
			BookingStatusAccepted:  true,
			BookingStatusRejected:  true,
			BookingStatusCancelled: true,
		},
		BookingStatusAccepted: {
			BookingStatusCompleted: true,
			BookingStatusCancelled: true,
		},
	}

	for _, from := range allStatuses {
		for _, to := range allStatuses {
			want := allowed[from][to]
			if got := CanTransitionBookingStatus(from, to); got != want {
				t.Errorf("CanTransitionBookingStatus(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}

	if !IsValidBookingStatus(BookingStatusCancelled) || IsValidBookingStatus(BookingStatus("bogus")) {
		t.Errorf("IsValidBookingStatus returned unexpected result")
	}
}

func TestCanTransitionVerificationStatus(t *testing.T) {
	allStatuses := []VerificationStatus{
		VerificationStatusNone,
		VerificationStatusPending,
		VerificationStatusVerified,
		VerificationStatusRejected,
		VerificationStatus("unknown"),
	}

	allowed := map[VerificationStatus]map[VerificationStatus]bool{
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

	for _, from := range allStatuses {
		for _, to := range allStatuses {
			want := allowed[from][to]
			if got := CanTransitionVerificationStatus(from, to); got != want {
				t.Errorf("CanTransitionVerificationStatus(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}
}
