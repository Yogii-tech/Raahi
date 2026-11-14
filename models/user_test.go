package models

import "testing"

func TestCountVehicleDocuments(t *testing.T) {
	tests := []struct {
		name    string
		vehicle *Vehicle
		want    int
	}{
		{
			name:    "nil vehicle returns 0",
			vehicle: nil,
			want:    0,
		},
		{
			name:    "whitespace-only URLs do not count",
			vehicle: &Vehicle{DLUrl: "   ", RCUrl: "\t", OwnershipUrl: "\n"},
			want:    0,
		},
		{
			name: "two distinct URLs return 2",
			vehicle: &Vehicle{
				DLUrl: "https://cdn.example.com/dl.jpg",
				RCUrl: "https://cdn.example.com/rc.jpg",
			},
			want: 2,
		},
		{
			name: "duplicate trimmed URL across three slots counts as 1",
			vehicle: &Vehicle{
				DLUrl:        "https://cdn.example.com/same.jpg",
				RCUrl:        "  https://cdn.example.com/same.jpg ",
				PollutionUrl: "https://cdn.example.com/same.jpg",
			},
			want: 1,
		},
		{
			name: "three distinct core docs return 3",
			vehicle: &Vehicle{
				DLUrl:        "https://cdn.example.com/dl.jpg",
				RCUrl:        "https://cdn.example.com/rc.jpg",
				PollutionUrl: "https://cdn.example.com/puc.jpg",
			},
			want: 3,
		},
		{
			name: "ownership doc counts toward minimum three distinct documents",
			vehicle: &Vehicle{
				DLUrl:        "https://cdn.example.com/dl.jpg",
				RCUrl:        "https://cdn.example.com/rc.jpg",
				OwnershipUrl: "https://cdn.example.com/ownership.jpg",
			},
			want: 3,
		},
		{
			name: "all five distinct documents return 5",
			vehicle: &Vehicle{
				DLUrl:           "https://cdn.example.com/dl.jpg",
				RCUrl:           "https://cdn.example.com/rc.jpg",
				PollutionUrl:    "https://cdn.example.com/puc.jpg",
				VehicleImageUrl: "https://cdn.example.com/car.jpg",
				OwnershipUrl:    "https://cdn.example.com/ownership.jpg",
			},
			want: 5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CountVehicleDocuments(tc.vehicle); got != tc.want {
				t.Errorf("CountVehicleDocuments() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDriverVerificationTransitions(t *testing.T) {
	tests := []struct {
		from VerificationStatus
		to   VerificationStatus
		want bool
	}{
		{VerificationStatusNone, VerificationStatusPending, true},
		{VerificationStatusNone, VerificationStatusVerified, true},
		{VerificationStatusPending, VerificationStatusVerified, true},
		{VerificationStatusPending, VerificationStatusRejected, true},
		{VerificationStatusRejected, VerificationStatusPending, true},
		{VerificationStatusRejected, VerificationStatusVerified, true},
		{VerificationStatusVerified, VerificationStatusPending, true},
		{VerificationStatusVerified, VerificationStatusRejected, true},
		{VerificationStatusVerified, VerificationStatusVerified, false},
		{VerificationStatusRejected, VerificationStatusRejected, false},
		{VerificationStatus("unknown"), VerificationStatusVerified, false},
	}

	for _, tc := range tests {
		if got := CanTransitionVerificationStatus(tc.from, tc.to); got != tc.want {
			t.Errorf("CanTransitionVerificationStatus(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}
