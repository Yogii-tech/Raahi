package controllers

import (
	"bytes"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"raahi-backend/models"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"
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

	// 3. Sorting order:
	//    - Primary: match tier ("full" -> "common_pickup" -> "common_dropoff")
	//    - Secondary: effectiveRidePrice descending (High to Low), preferring SegmentPricePerSeat when > 0 else PricePerSeat
	//    - Tertiary tie-breaker: len(TakenSeats) descending
	results := []RideResponse{
		{
			Ride:           models.Ride{Pickup: "Haldwani", Dropoff: "Delhi", PricePerSeat: 300, TakenSeats: []int{1, 2, 3}},
			MatchType:      "common_dropoff",
			IsPartialMatch: true,
		},
		{
			Ride:                models.Ride{Pickup: "Haldwani", Dropoff: "Delhi", PricePerSeat: 600, TakenSeats: []int{1}},
			SegmentPricePerSeat: 450,
			MatchType:           "common_dropoff",
			IsPartialMatch:      true,
		},
		{
			Ride:           models.Ride{Pickup: "Bageshwar", Dropoff: "Haldwani", PricePerSeat: 350, TakenSeats: []int{1}},
			MatchType:      "common_pickup",
			IsPartialMatch: true,
		},
		{
			Ride:                models.Ride{Pickup: "Bageshwar", Dropoff: "Haldwani", PricePerSeat: 500, TakenSeats: []int{}},
			SegmentPricePerSeat: 400,
			MatchType:           "common_pickup",
			IsPartialMatch:      true,
		},
		{
			Ride:           models.Ride{Pickup: "Bageshwar", Dropoff: "Haldwani", PricePerSeat: 350, TakenSeats: []int{1, 2}},
			MatchType:      "common_pickup",
			IsPartialMatch: true,
		},
		{
			Ride:           models.Ride{Pickup: "Bageshwar", Dropoff: "Delhi", PricePerSeat: 500, TakenSeats: []int{}},
			MatchType:      "full",
			IsPartialMatch: false,
		},
		{
			Ride:                models.Ride{Pickup: "Bageshwar", Dropoff: "Delhi", PricePerSeat: 900, TakenSeats: []int{1}},
			SegmentPricePerSeat: 750,
			MatchType:           "full",
			IsPartialMatch:      false,
		},
		{
			Ride:           models.Ride{Pickup: "Bageshwar", Dropoff: "Delhi", PricePerSeat: 500, TakenSeats: []int{1, 2}},
			MatchType:      "full",
			IsPartialMatch: false,
		},
	}

	sortRideResponses(results)

	expectedOrder := []struct {
		matchType string
		effPrice  float64
		taken     int
	}{
		{"full", 750, 1},
		{"full", 500, 2},
		{"full", 500, 0},
		{"common_pickup", 400, 0},
		{"common_pickup", 350, 2},
		{"common_pickup", 350, 1},
		{"common_dropoff", 450, 1},
		{"common_dropoff", 300, 3},
	}

	if len(results) != len(expectedOrder) {
		t.Fatalf("len(results) = %d, want %d", len(results), len(expectedOrder))
	}

	for i, exp := range expectedOrder {
		gotPrice := effectiveRidePrice(results[i])
		if results[i].MatchType != exp.matchType || gotPrice != exp.effPrice || len(results[i].TakenSeats) != exp.taken {
			t.Errorf("sorted[%d] = (%s, effPrice=%v, taken=%d), want (%s, effPrice=%v, taken=%d)",
				i, results[i].MatchType, gotPrice, len(results[i].TakenSeats), exp.matchType, exp.effPrice, exp.taken)
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

func TestBackendSecurityControls(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 1. UpdateProfile privilege escalation block: role="admin" must be rejected with 400 Bad Request
	if isAllowedUserProfileRole("admin") {
		t.Errorf("expected isAllowedUserProfileRole('admin') to be false")
	}
	for _, validRole := range []string{"passenger", "driver", "parceller"} {
		if !isAllowedUserProfileRole(validRole) {
			t.Errorf("expected isAllowedUserProfileRole(%q) to be true", validRole)
		}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("userId", primitive.NewObjectID())
	c.Request = httptest.NewRequest(http.MethodPut, "/api/user/profile", bytes.NewBufferString(`{"name":"Mallory","role":"admin"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	UpdateProfile(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected UpdateProfile with role='admin' to return 400, got %d", w.Code)
	}

	// 2. KYC document tampering detection for verified drivers
	baseVehicle := &models.Vehicle{
		VehicleName:     "Innova",
		VehicleNumber:   "UK04AB1234",
		DLUrl:           "/uploads/dl1.jpg",
		RCUrl:           "/uploads/rc1.jpg",
		PollutionUrl:    "/uploads/puc1.jpg",
		VehicleImageUrl: "/uploads/car1.jpg",
	}
	sameVehicle := &models.Vehicle{
		VehicleName:     "Innova Crysta",
		VehicleNumber:   "UK04AB1234",
		DLUrl:           "/uploads/dl1.jpg",
		RCUrl:           "/uploads/rc1.jpg",
		PollutionUrl:    "/uploads/puc1.jpg",
		VehicleImageUrl: "/uploads/car1.jpg",
	}
	tamperedVehicle := &models.Vehicle{
		VehicleName:     "Innova",
		VehicleNumber:   "UK04AB1234",
		DLUrl:           "/uploads/fake_dl.jpg",
		RCUrl:           "/uploads/rc1.jpg",
		PollutionUrl:    "/uploads/puc1.jpg",
		VehicleImageUrl: "/uploads/car1.jpg",
	}
	if hasDriverVehicleChanged(baseVehicle, sameVehicle) {
		t.Errorf("expected unchanged KYC URLs/number to return false")
	}
	if !hasDriverVehicleChanged(baseVehicle, tamperedVehicle) {
		t.Errorf("expected modified DLUrl to trigger hasDriverVehicleChanged=true")
	}

	// 3. Chat IDOR authorization helper
	passengerID := primitive.NewObjectID()
	driverID := primitive.NewObjectID()
	outsiderID := primitive.NewObjectID()
	if !isAuthorizedForBookingChat(passengerID, passengerID, driverID) {
		t.Errorf("expected passenger to be authorized for booking chat")
	}
	if !isAuthorizedForBookingChat(driverID, passengerID, driverID) {
		t.Errorf("expected driver to be authorized for booking chat")
	}
	if isAuthorizedForBookingChat(outsiderID, passengerID, driverID) {
		t.Errorf("expected third-party user to be rejected from booking chat")
	}
	if isAuthorizedForBookingChat(primitive.NilObjectID, passengerID, driverID) {
		t.Errorf("expected NilObjectID to be rejected from booking chat")
	}

	// 4. Auth action rate limiter (PromoteAdmin brute-force protection)
	testKey := "test_promote:" + primitive.NewObjectID().Hex()
	for i := 0; i < 5; i++ {
		if !checkAuthActionRateLimit(testKey, 5, 10*time.Minute) {
			t.Errorf("expected attempt %d to be allowed", i+1)
		}
	}
	if checkAuthActionRateLimit(testKey, 5, 10*time.Minute) {
		t.Errorf("expected 6th attempt within window to be rate-limited")
	}

	// 5. Geo coordinate validation (reject NaN, Inf, out-of-range lat/lon)
	if !isValidGeoCoordinate(29.8370, 79.7713) {
		t.Errorf("expected valid Uttarakhand coordinates to pass")
	}
	if isValidGeoCoordinate(math.NaN(), 79.0) || isValidGeoCoordinate(29.0, math.Inf(1)) || isValidGeoCoordinate(95.0, 79.0) || isValidGeoCoordinate(29.0, -185.0) {
		t.Errorf("expected NaN/Inf/out-of-bounds coordinates to be rejected")
	}

	// 6. CSV formula injection neutralization
	escaped := sanitizeCSV("=cmd|' /C calc'!A0")
	if !strings.HasPrefix(escaped, "\"'=cmd") {
		t.Errorf("expected formula prefix to be neutralized with leading single quote, got %s", escaped)
	}
}

func TestSprint1BookingSeatAndParcelValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 1. Parcel bookings must NOT consume passenger seats (SeatsRequested == 0, empty SeatLayout)
	parcelSeats, parcelLayout, parcelErr := prepareBookingSeats("parcel", 2, []int{1, 2}, 9)
	if parcelErr != "" || parcelSeats != 0 || len(parcelLayout) != 0 {
		t.Errorf("prepareBookingSeats(parcel) = (%d, %v, %q), want (0, [], \"\")", parcelSeats, parcelLayout, parcelErr)
	}
	if reserve, count := shouldReserveSeatsForBooking(models.Booking{Type: "parcel", SeatsRequested: 1}); reserve || count != 0 {
		t.Errorf("shouldReserveSeatsForBooking(parcel) = (%v, %d), want (false, 0)", reserve, count)
	}
	if reserve, count := shouldReserveSeatsForBooking(models.Booking{Type: "seat", SeatsRequested: 2, SeatLayout: []int{1, 2}}); !reserve || count != 2 {
		t.Errorf("shouldReserveSeatsForBooking(seat) = (%v, %d), want (true, 2)", reserve, count)
	}

	// 2. Empty SeatLayout on a seat booking is rejected (helper + HTTP handler)
	if _, _, errMsg := prepareBookingSeats("seat", 1, []int{}, 9); errMsg == "" {
		t.Errorf("expected empty SeatLayout for seat booking to be rejected")
	}
	wEmpty := httptest.NewRecorder()
	cEmpty, _ := gin.CreateTestContext(wEmpty)
	cEmpty.Set("userId", primitive.NewObjectID())
	cEmpty.Params = gin.Params{{Key: "rideId", Value: primitive.NewObjectID().Hex()}}
	cEmpty.Request = httptest.NewRequest(http.MethodPost, "/api/rides/id/book", bytes.NewBufferString(`{"type":"seat","pickup":"Bageshwar","dropoff":"Haldwani","seatsRequested":1,"seatLayout":[]}`))
	cEmpty.Request.Header.Set("Content-Type", "application/json")
	BookRide(cEmpty)
	if wEmpty.Code != http.StatusBadRequest {
		t.Errorf("BookRide with empty seatLayout returned %d, want 400", wEmpty.Code)
	}

	// 3. Mismatched seatsRequested vs len(seatLayout) is rejected (helper + HTTP handler)
	if _, _, errMsg := prepareBookingSeats("seat", 2, []int{1}, 9); errMsg == "" {
		t.Errorf("expected seatsRequested != len(seatLayout) to be rejected")
	}
	wMismatch := httptest.NewRecorder()
	cMismatch, _ := gin.CreateTestContext(wMismatch)
	cMismatch.Set("userId", primitive.NewObjectID())
	cMismatch.Params = gin.Params{{Key: "rideId", Value: primitive.NewObjectID().Hex()}}
	cMismatch.Request = httptest.NewRequest(http.MethodPost, "/api/rides/id/book", bytes.NewBufferString(`{"type":"seat","pickup":"Bageshwar","dropoff":"Haldwani","seatsRequested":2,"seatLayout":[1]}`))
	cMismatch.Request.Header.Set("Content-Type", "application/json")
	BookRide(cMismatch)
	if wMismatch.Code != http.StatusBadRequest {
		t.Errorf("BookRide with mismatched seatsRequested returned %d, want 400", wMismatch.Code)
	}

	// 4. Out-of-range 1-indexed seat indices (0, negative, > SeatsTotal) and duplicates are rejected
	invalidSeatCases := [][]int{
		{0},
		{-1},
		{10}, // > seatsTotal (9)
		{1, 1},
	}
	for _, seats := range invalidSeatCases {
		if _, _, errMsg := prepareBookingSeats("seat", len(seats), seats, 9); errMsg == "" {
			t.Errorf("expected seatLayout %v for seatsTotal=9 to be rejected", seats)
		}
	}

	// HTTP handler also rejects 0-indexed seat before hitting DB
	wZero := httptest.NewRecorder()
	cZero, _ := gin.CreateTestContext(wZero)
	cZero.Set("userId", primitive.NewObjectID())
	cZero.Params = gin.Params{{Key: "rideId", Value: primitive.NewObjectID().Hex()}}
	cZero.Request = httptest.NewRequest(http.MethodPost, "/api/rides/id/book", bytes.NewBufferString(`{"type":"seat","pickup":"Bageshwar","dropoff":"Haldwani","seatsRequested":1,"seatLayout":[0]}`))
	cZero.Request.Header.Set("Content-Type", "application/json")
	BookRide(cZero)
	if wZero.Code != http.StatusBadRequest {
		t.Errorf("BookRide with 0-indexed seat [0] returned %d, want 400", wZero.Code)
	}

	// 5. Valid 1-indexed seat selection succeeds
	gotSeats, gotLayout, gotErr := prepareBookingSeats("seat", 2, []int{1, 9}, 9)
	if gotErr != "" || gotSeats != 2 || len(gotLayout) != 2 || gotLayout[0] != 1 || gotLayout[1] != 9 {
		t.Errorf("prepareBookingSeats valid 1-indexed = (%d, %v, %q), want (2, [1 9], \"\")", gotSeats, gotLayout, gotErr)
	}
}

func TestSprint2ComputeTakenSeatsFromBookingsAndRateCleanup(t *testing.T) {
	ride := models.Ride{
		ID:                 primitive.NewObjectID(),
		Pickup:             "Bageshwar",
		Dropoff:            "Haldwani",
		Route:              []string{"Bageshwar", "Almora", "Haldwani"},
		SeatsTotal:         7,
		ManualBlockedSeats: []int{7, 1},
	}

	bookings := []models.Booking{
		{
			RideID:     ride.ID,
			Type:       "seat",
			Pickup:     "Bageshwar",
			Dropoff:    "Almora",
			SeatLayout: []int{1, 2},
			Status:     "accepted",
		},
		{
			RideID:     ride.ID,
			Type:       "seat",
			Pickup:     "Almora",
			Dropoff:    "Haldwani",
			SeatLayout: []int{3},
			Status:     "pending",
		},
		{
			RideID:     ride.ID,
			Type:       "parcel",
			Pickup:     "Bageshwar",
			Dropoff:    "Haldwani",
			SeatLayout: []int{4, 5}, // parcel bookings must be ignored
			Status:     "accepted",
		},
	}

	// 1. Classic mode (empty search segment): all non-parcel bookings + deduplicated ManualBlockedSeats
	allTaken := computeTakenSeatsFromBookings(ride, bookings, "", "")
	sort.Ints(allTaken)
	wantAll := []int{1, 2, 3, 7}
	if len(allTaken) != len(wantAll) {
		t.Fatalf("computeTakenSeatsFromBookings(full) = %v, want %v", allTaken, wantAll)
	}
	for i := range wantAll {
		if allTaken[i] != wantAll[i] {
			t.Fatalf("computeTakenSeatsFromBookings(full) = %v, want %v", allTaken, wantAll)
		}
	}

	// 2. Segment-scoped search (Almora -> Haldwani): excludes non-overlapping Bageshwar -> Almora booking (seat 2),
	//    keeps overlapping Almora -> Haldwani (seat 3) + ManualBlockedSeats (1, 7).
	segTaken := computeTakenSeatsFromBookings(ride, bookings, "Almora", "Haldwani")
	sort.Ints(segTaken)
	wantSeg := []int{1, 3, 7}
	if len(segTaken) != len(wantSeg) {
		t.Fatalf("computeTakenSeatsFromBookings(Almora->Haldwani) = %v, want %v", segTaken, wantSeg)
	}
	for i := range wantSeg {
		if segTaken[i] != wantSeg[i] {
			t.Fatalf("computeTakenSeatsFromBookings(Almora->Haldwani) = %v, want %v", segTaken, wantSeg)
		}
	}

	// 3. authRateStore eviction deletes fully expired buckets and prunes expired timestamps
	now := time.Now()
	expiredKey := "test_expired:" + primitive.NewObjectID().Hex()
	mixedKey := "test_mixed:" + primitive.NewObjectID().Hex()

	authRateMu.Lock()
	authRateStore[expiredKey] = []time.Time{now.Add(-30 * time.Minute), now.Add(-20 * time.Minute)}
	authRateStore[mixedKey] = []time.Time{now.Add(-30 * time.Minute), now.Add(-2 * time.Minute)}
	authRateMu.Unlock()

	cleanupAuthRateStore(now.Add(-15 * time.Minute))

	authRateMu.Lock()
	_, hasExpired := authRateStore[expiredKey]
	mixedEntries := append([]time.Time(nil), authRateStore[mixedKey]...)
	delete(authRateStore, mixedKey)
	authRateMu.Unlock()

	if hasExpired {
		t.Errorf("expected expired bucket %q to be evicted from authRateStore", expiredKey)
	}
	if len(mixedEntries) != 1 {
		t.Errorf("expected mixed bucket %q to retain 1 active entry after eviction, got %d", mixedKey, len(mixedEntries))
	}
}
