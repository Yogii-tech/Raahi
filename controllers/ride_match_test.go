package controllers

import (
	"regexp"
	"testing"

	"raahi-backend/models"
)

func TestClassifyRideMatch(t *testing.T) {
	tests := []struct {
		name               string
		ride               models.Ride
		searchPickup       string
		searchDropoff      string
		wantMatched        bool
		wantMatchType      string
		wantIsPartial      bool
		wantMatchedPickup  string
		wantMatchedDropoff string
	}{
		{
			name: "Full match: Bageshwar -> Delhi on Bageshwar -> Haldwani -> Delhi ride",
			ride: models.Ride{
				Pickup:  "Bageshwar",
				Dropoff: "Delhi",
				Route:   []string{"Bageshwar", "Haldwani", "Delhi"},
			},
			searchPickup:       "Bageshwar",
			searchDropoff:      "Delhi",
			wantMatched:        true,
			wantMatchType:      "full",
			wantIsPartial:      false,
			wantMatchedPickup:  "Bageshwar",
			wantMatchedDropoff: "Delhi",
		},
		{
			name: "Full match on intermediate sub-segment: Haldwani -> Delhi on Bageshwar -> Haldwani -> Delhi ride",
			ride: models.Ride{
				Pickup:  "Bageshwar",
				Dropoff: "Delhi",
				Route:   []string{"Bageshwar", "Haldwani", "Delhi"},
			},
			searchPickup:       "Haldwani",
			searchDropoff:      "Delhi",
			wantMatched:        true,
			wantMatchType:      "full",
			wantIsPartial:      false,
			wantMatchedPickup:  "Haldwani",
			wantMatchedDropoff: "Delhi",
		},
		{
			name: "Common pickup match: Bageshwar -> Delhi passenger search matching Bageshwar -> Haldwani driver ride",
			ride: models.Ride{
				Pickup:  "Bageshwar",
				Dropoff: "Haldwani",
				Route:   []string{"Bageshwar", "Almora", "Haldwani"},
			},
			searchPickup:       "Bageshwar",
			searchDropoff:      "Delhi",
			wantMatched:        true,
			wantMatchType:      "common_pickup",
			wantIsPartial:      true,
			wantMatchedPickup:  "Bageshwar",
			wantMatchedDropoff: "Haldwani",
		},
		{
			name: "Common pickup match at intermediate stop: Almora -> Delhi passenger search matching Bageshwar -> Almora -> Haldwani driver ride",
			ride: models.Ride{
				Pickup:  "Bageshwar",
				Dropoff: "Haldwani",
				Route:   []string{"Bageshwar", "Almora", "Haldwani"},
			},
			searchPickup:       "Almora",
			searchDropoff:      "Delhi",
			wantMatched:        true,
			wantMatchType:      "common_pickup",
			wantIsPartial:      true,
			wantMatchedPickup:  "Almora",
			wantMatchedDropoff: "Haldwani",
		},
		{
			name: "Common dropoff match: Bageshwar -> Delhi passenger search matching Haldwani -> Delhi driver ride",
			ride: models.Ride{
				Pickup:  "Haldwani",
				Dropoff: "Delhi",
				Route:   []string{"Haldwani", "Moradabad", "Delhi"},
			},
			searchPickup:       "Bageshwar",
			searchDropoff:      "Delhi",
			wantMatched:        true,
			wantMatchType:      "common_dropoff",
			wantIsPartial:      true,
			wantMatchedPickup:  "Haldwani",
			wantMatchedDropoff: "Delhi",
		},
		{
			name: "Reject pickup at final destination: Haldwwani -> Bageshwar should NOT match pickup Bageshwar",
			ride: models.Ride{
				Pickup:  "Haldwani",
				Dropoff: "Bageshwar",
				Route:   []string{"Haldwani", "Almora", "Bageshwar"},
			},
			searchPickup:  "Bageshwar",
			searchDropoff: "Delhi",
			wantMatched:   false,
		},
		{
			name: "Reject reverse direction ride: Delhi -> Haldwani -> Bageshwar should NOT match Bageshwar -> Delhi",
			ride: models.Ride{
				Pickup:  "Delhi",
				Dropoff: "Bageshwar",
				Route:   []string{"Delhi", "Haldwani", "Bageshwar"},
			},
			searchPickup:  "Bageshwar",
			searchDropoff: "Delhi",
			wantMatched:   false,
		},
		{
			name: "Reject non-matching ride: Dehradun -> Haridwar should NOT match Bageshwar -> Delhi",
			ride: models.Ride{
				Pickup:  "Dehradun",
				Dropoff: "Haridwar",
				Route:   []string{"Dehradun", "Haridwar"},
			},
			searchPickup:  "Bageshwar",
			searchDropoff: "Delhi",
			wantMatched:   false,
		},
		{
			name: "Pickup-only search matches ride starting at pickup with isPartial false",
			ride: models.Ride{
				Pickup:  "Bageshwar",
				Dropoff: "Haldwani",
				Route:   []string{"Bageshwar", "Haldwani"},
			},
			searchPickup:       "Bageshwar",
			searchDropoff:      "",
			wantMatched:        true,
			wantMatchType:      "common_pickup",
			wantIsPartial:      false,
			wantMatchedPickup:  "Bageshwar",
			wantMatchedDropoff: "Haldwani",
		},
		{
			name: "Empty search matches all rides as full",
			ride: models.Ride{
				Pickup:  "Bageshwar",
				Dropoff: "Haldwani",
				Route:   []string{"Bageshwar", "Haldwani"},
			},
			searchPickup:  "",
			searchDropoff: "",
			wantMatched:   true,
			wantMatchType: "full",
			wantIsPartial: false,
		},
		{
			name: "Reject same-city search (exact pickup == dropoff)",
			ride: models.Ride{
				Pickup:  "Bageshwar",
				Dropoff: "Haldwani",
				Route:   []string{"Bageshwar", "Almora", "Haldwani"},
			},
			searchPickup:  "Bageshwar",
			searchDropoff: "Bageshwar",
			wantMatched:   false,
		},
		{
			name: "Reject same-city search (fuzzy equivalent pickup and dropoff)",
			ride: models.Ride{
				Pickup:  "Bageshwar",
				Dropoff: "Haldwani",
				Route:   []string{"Bageshwar", "Almora", "Haldwani"},
			},
			searchPickup:  "Bageshwar",
			searchDropoff: "Bageshwar, Uttarakhand",
			wantMatched:   false,
		},
		{
			name: "Handle empty ride.Dropoff with whitespace and duplicate entries in ride.Route",
			ride: models.Ride{
				Pickup:  "Bageshwar",
				Dropoff: "   ",
				Route:   []string{"  Bageshwar  ", " ", "Bageshwar", "Almora", "  Haldwani  ", ""},
			},
			searchPickup:       "Bageshwar",
			searchDropoff:      "Delhi",
			wantMatched:        true,
			wantMatchType:      "common_pickup",
			wantIsPartial:      true,
			wantMatchedPickup:  "Bageshwar",
			wantMatchedDropoff: "Haldwani",
		},
		{
			name: "Reject degenerate route with only duplicate stops",
			ride: models.Ride{
				Pickup:  "Bageshwar",
				Dropoff: "Bageshwar",
				Route:   []string{"Bageshwar", "  bageshwar "},
			},
			searchPickup:  "Bageshwar",
			searchDropoff: "Delhi",
			wantMatched:   false,
		},
		{
			name: "Substring stop names: exact match prioritized over prefix substring (Kota vs Kotabagh)",
			ride: models.Ride{
				Pickup:  "Kota",
				Dropoff: "Haldwani",
				Route:   []string{"Kota", "Kotabagh", "Haldwani"},
			},
			searchPickup:       "Kotabagh",
			searchDropoff:      "Delhi",
			wantMatched:        true,
			wantMatchType:      "common_pickup",
			wantIsPartial:      true,
			wantMatchedPickup:  "Kotabagh",
			wantMatchedDropoff: "Haldwani",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			matched, matchType, isPartial, matchedPickup, matchedDropoff := classifyRideMatch(tc.ride, tc.searchPickup, tc.searchDropoff)
			if matched != tc.wantMatched {
				t.Fatalf("matched = %v, want %v", matched, tc.wantMatched)
			}
			if !tc.wantMatched {
				return
			}
			if matchType != tc.wantMatchType {
				t.Errorf("matchType = %q, want %q", matchType, tc.wantMatchType)
			}
			if isPartial != tc.wantIsPartial {
				t.Errorf("isPartial = %v, want %v", isPartial, tc.wantIsPartial)
			}
			if matchedPickup != tc.wantMatchedPickup {
				t.Errorf("matchedPickup = %q, want %q", matchedPickup, tc.wantMatchedPickup)
			}
			if matchedDropoff != tc.wantMatchedDropoff {
				t.Errorf("matchedDropoff = %q, want %q", matchedDropoff, tc.wantMatchedDropoff)
			}
		})
	}
}

func TestComputeSegmentPricingAndSorting(t *testing.T) {
	// 1. Common pickup spanning full driver ride uses driver's exact PricePerSeat and TotalDistanceM/1000
	driverRide := models.Ride{
		Pickup:         "Bageshwar",
		Dropoff:        "Haldwani",
		Route:          []string{"Bageshwar", "Almora", "Haldwani"},
		PricePerSeat:   350,
		TotalDistanceM: 160000,
		DiscoveredStops: []models.StopInfo{
			{Name: "Bageshwar", DistanceM: 0},
			{Name: "Almora", DistanceM: 65000},
			{Name: "Haldwani", DistanceM: 160000},
		},
	}

	price, distKm, normP, normD := computeSegmentPricing(driverRide, "Bageshwar", "Haldwani")
	if price != 350 || distKm != 160 || normP != "Bageshwar" || normD != "Haldwani" {
		t.Errorf("full-span pricing got (price=%v, distKm=%v, p=%q, d=%q), want (350, 160, Bageshwar, Haldwani)", price, distKm, normP, normD)
	}

	// 2. Sub-segment pricing uses DiscoveredStops proportional calculation
	subPrice, subDistKm, subP, subD := computeSegmentPricing(driverRide, "Almora", "Haldwani")
	if subDistKm != 95 || subPrice <= 0 || subPrice >= 350 || subP != "Almora" || subD != "Haldwani" {
		t.Errorf("sub-segment pricing got (price=%v, distKm=%v, p=%q, d=%q), expected distKm=95 and proportional price < 350", subPrice, subDistKm, subP, subD)
	}

	// 2b. Substring intermediate stop (e.g. "New Delhi" on "Delhi -> New Delhi -> Haldwani") does not falsely trigger spansFullRide
	substringRide := models.Ride{
		Pickup:         "Delhi",
		Dropoff:        "Haldwani",
		Route:          []string{"Delhi", "New Delhi", "Haldwani"},
		PricePerSeat:   600,
		TotalDistanceM: 300000,
		DiscoveredStops: []models.StopInfo{
			{Name: "Delhi", DistanceM: 0},
			{Name: "New Delhi", DistanceM: 50000},
			{Name: "Haldwani", DistanceM: 300000},
		},
	}
	ndPrice, ndDistKm, ndP, ndD := computeSegmentPricing(substringRide, "New Delhi", "Haldwani")
	if ndDistKm != 250 || ndPrice >= 600 || ndP != "New Delhi" || ndD != "Haldwani" {
		t.Errorf("substring stop pricing got (price=%v, distKm=%v, p=%q, d=%q), want distKm=250 and proportional price < 600", ndPrice, ndDistKm, ndP, ndD)
	}

	// 3. Sorting order: full matches (by takenSeats desc) -> common_pickup (by takenSeats desc) -> common_dropoff (by takenSeats desc)
	results := []RideResponse{
		{
			Ride:           models.Ride{Pickup: "Haldwani", Dropoff: "Delhi", TakenSeats: []int{1, 2, 3}},
			MatchType:      "common_dropoff",
			IsPartialMatch: true,
		},
		{
			Ride:           models.Ride{Pickup: "Bageshwar", Dropoff: "Haldwani", TakenSeats: []int{1}},
			MatchType:      "common_pickup",
			IsPartialMatch: true,
		},
		{
			Ride:           models.Ride{Pickup: "Bageshwar", Dropoff: "Delhi", TakenSeats: []int{}},
			MatchType:      "full",
			IsPartialMatch: false,
		},
		{
			Ride:           models.Ride{Pickup: "Bageshwar", Dropoff: "Haldwani", TakenSeats: []int{1, 2}},
			MatchType:      "common_pickup",
			IsPartialMatch: true,
		},
		{
			Ride:           models.Ride{Pickup: "Bageshwar", Dropoff: "Delhi", TakenSeats: []int{1, 2}},
			MatchType:      "full",
			IsPartialMatch: false,
		},
	}

	sortRideResponses(results)

	expectedOrder := []struct {
		matchType string
		taken     int
	}{
		{"full", 2},
		{"full", 0},
		{"common_pickup", 2},
		{"common_pickup", 1},
		{"common_dropoff", 3},
	}

	for i, exp := range expectedOrder {
		if results[i].MatchType != exp.matchType || len(results[i].TakenSeats) != exp.taken {
			t.Errorf("sorted[%d] = (%s, taken=%d), want (%s, taken=%d)",
				i, results[i].MatchType, len(results[i].TakenSeats), exp.matchType, exp.taken)
		}
	}
}

func TestResolveBookingSegmentAndConsistency(t *testing.T) {
	driverRide := models.Ride{
		Pickup:         "Bageshwar",
		Dropoff:        "Haldwani",
		Route:          []string{"Bageshwar", "Almora", "Haldwani"},
		PricePerSeat:   350,
		TotalDistanceM: 160000,
		DiscoveredStops: []models.StopInfo{
			{Name: "Bageshwar", DistanceM: 0},
			{Name: "Almora", DistanceM: 65000},
			{Name: "Haldwani", DistanceM: 160000},
		},
	}

	// 1. Booking with matchedPickup/matchedDropoff ("Almora" -> "Haldwani") matches GetAvailableRides pricing
	_, _, _, availMatchedP, availMatchedD := classifyRideMatch(driverRide, "Almora", "Delhi")
	availSegPrice, _, _, _ := computeSegmentPricing(driverRide, availMatchedP, availMatchedD)

	valid, normP, normD, bookSegPrice, totalPrice := resolveBookingSegment(driverRide, availMatchedP, availMatchedD, 2)
	if !valid || normP != "Almora" || normD != "Haldwani" || bookSegPrice != availSegPrice || totalPrice != availSegPrice*2 {
		t.Errorf("resolveBookingSegment(Almora->Haldwani, 2) = (valid=%v, p=%q, d=%q, seg=%v, total=%v), want (true, Almora, Haldwani, %v, %v)",
			valid, normP, normD, bookSegPrice, totalPrice, availSegPrice, availSegPrice*2)
	}

	// 2. Booking with raw search endpoints ("Almora" -> "Delhi") normalizes to ("Almora" -> "Haldwani") with identical pricing
	validRaw, rawP, rawD, rawSegPrice, rawTotal := resolveBookingSegment(driverRide, "Almora", "Delhi", 2)
	if !validRaw || rawP != "Almora" || rawD != "Haldwani" || rawSegPrice != availSegPrice || rawTotal != availSegPrice*2 {
		t.Errorf("resolveBookingSegment(Almora->Delhi, 2) = (valid=%v, p=%q, d=%q, seg=%v, total=%v), want (true, Almora, Haldwani, %v, %v)",
			validRaw, rawP, rawD, rawSegPrice, rawTotal, availSegPrice, availSegPrice*2)
	}

	// 3. Reject invalid booking segments (reverse direction, same city, unrelated stops)
	invalidCases := []struct {
		pickup  string
		dropoff string
	}{
		{"Haldwani", "Bageshwar"},
		{"Bageshwar", "Bageshwar"},
		{"Dehradun", "Haridwar"},
		{"Haldwani", "Delhi"}, // pickup at driver's final destination
	}
	for _, ic := range invalidCases {
		if ok, _, _, _, _ := resolveBookingSegment(driverRide, ic.pickup, ic.dropoff, 1); ok {
			t.Errorf("expected resolveBookingSegment(%q, %q) to be rejected, but got valid=true", ic.pickup, ic.dropoff)
		}
	}

	// 4. Segment seat overlap consistency: Bageshwar->Almora does NOT overlap Almora->Haldwani, but overlaps Bageshwar->Haldwani
	normRoute := normalizeRideRoute(driverRide)
	if segmentsOverlap("Bageshwar", "Almora", "Almora", "Haldwani", normRoute) {
		t.Errorf("expected Bageshwar->Almora and Almora->Haldwani NOT to overlap")
	}
	if !segmentsOverlap("Bageshwar", "Haldwani", "Almora", "Haldwani", normRoute) {
		t.Errorf("expected Bageshwar->Haldwani and Almora->Haldwani to overlap")
	}

	// 5. buildLocationRegex aligns comma-qualified location queries with FuzzyMatch and escapes regex metacharacters
	rx := buildLocationRegex("Bageshwar, Uttarakhand")
	compiled, err := regexp.Compile(rx.Pattern)
	if err != nil {
		t.Fatalf("failed to compile buildLocationRegex pattern %q: %v", rx.Pattern, err)
	}
	if !compiled.MatchString("Bageshwar") {
		t.Errorf("expected regex %q to match stop 'Bageshwar'", rx.Pattern)
	}
	dotRx := buildLocationRegex("A.B. Road")
	dotCompiled, err := regexp.Compile(dotRx.Pattern)
	if err != nil {
		t.Fatalf("failed to compile dot pattern: %v", err)
	}
	if dotCompiled.MatchString("AXB Road") {
		t.Errorf("expected '.' in 'A.B. Road' to be escaped by regexp.QuoteMeta, but matched 'AXB Road'")
	}
}
