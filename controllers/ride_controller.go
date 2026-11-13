package controllers

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"raahi-backend/config"
	"raahi-backend/models"
	"raahi-backend/utils"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var rideCollection *mongo.Collection
var bookingCollection *mongo.Collection
var recentRoutesCollection *mongo.Collection
var usersCollection *mongo.Collection

func InitializeRideCollection() {
	rideCollection = config.Database.Collection("rides")
	bookingCollection = config.Database.Collection("bookings")
	recentRoutesCollection = config.Database.Collection("recent_routes")
	usersCollection = config.Database.Collection("users")

	// Create sparse unique index for Booking ID (BID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = bookingCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "bookingId", Value: 1}},
		Options: options.Index().SetUnique(true).SetSparse(true),
	})

	BackfillBookingIDs()
}

func BackfillBookingIDs() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cursor, err := bookingCollection.Find(ctx, bson.M{
			"status": "accepted",
			"$or": []bson.M{
				{"bookingId": ""},
				{"bookingId": bson.M{"$exists": false}},
			},
		}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}))

		if err != nil {
			return
		}
		defer cursor.Close(ctx)

		for cursor.Next(ctx) {
			var b models.Booking
			if err := cursor.Decode(&b); err == nil && b.BookingID == "" {
				bid, err := utils.GenerateNextBookingID(ctx)
				if err == nil && bid != "" {
					_, _ = bookingCollection.UpdateOne(ctx, bson.M{"_id": b.ID}, bson.M{"$set": bson.M{"bookingId": bid}})
					log.Printf("[BACKFILL BID] Assigned %s to existing accepted booking %s", bid, b.ID.Hex())
				}
			}
		}
	}()
}

func CreateRide(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	var body struct {
		Pickup        string   `json:"pickup" binding:"required,min=2,max=200"`
		Dropoff       string   `json:"dropoff" binding:"required,min=2,max=200"`
		Route         []string `json:"route"`
		VehicleModel  string   `json:"vehicleModel" binding:"required"`
		VehicleNumber string   `json:"vehicleNumber" binding:"required"`
		DepartureTime string   `json:"departureTime" binding:"required"`
		Date          string   `json:"date" binding:"required"`
		SeatsTotal    int      `json:"seatsTotal" binding:"required,min=1,max=10"`
		PricePerSeat  float64  `json:"pricePerSeat" binding:"required,min=0"`
	}

	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// 10-minute gap rule check: Prevent same driver from posting rides within 10 minutes
	findLastCtx, findLastCancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer findLastCancel()

	var lastRide models.Ride
	err := rideCollection.FindOne(findLastCtx, bson.M{"driverId": userId}, options.FindOne().SetSort(bson.D{{Key: "createdAt", Value: -1}})).Decode(&lastRide)
	if err == nil {
		timeSinceLastRide := time.Since(lastRide.CreatedAt)
		if timeSinceLastRide < 10*time.Minute {
			remaining := (10 * time.Minute) - timeSinceLastRide
			mins := int(remaining.Minutes())
			if int(remaining.Seconds())%60 > 0 {
				mins++
			}
			if mins < 1 {
				mins = 1
			}
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":            fmt.Sprintf("Please wait 10 minutes between posting rides. You can post a new ride in %d minute(s).", mins),
				"remainingSeconds": int(remaining.Seconds()),
			})
			return
		}
	}

	findCtx, findCancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer findCancel()

	// Fetch driver Name, Role, and VerificationStatus
	var driver struct {
		Name               string `bson:"name"`
		Role               string `bson:"role"`
		VerificationStatus string `bson:"verification_status"`
	}
	err = config.Database.Collection("users").FindOne(findCtx, bson.M{"_id": userId}).Decode(&driver)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch driver info"})
		return
	}

	if (driver.Role != "driver" && driver.Role != "admin") || driver.VerificationStatus != string(models.VerificationStatusVerified) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Only verified drivers can post rides"})
		return
	}

	if math.IsNaN(body.PricePerSeat) || math.IsInf(body.PricePerSeat, 0) || body.PricePerSeat < 0 || body.PricePerSeat > 100000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid price per seat"})
		return
	}

	ongoingCount, ongoingErr := rideCollection.CountDocuments(findCtx, bson.M{
		"driverId": userId,
		"status":   string(models.RideStatusOngoing),
	})
	if ongoingErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify active rides"})
		return
	}
	if ongoingCount > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Cannot create a new ride while you have an ongoing ride. Complete your current ride first."})
		return
	}

	delCtx, delCancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer delCancel()

	// Replace this driver's previous open rides WITHOUT orphaning passenger bookings.
	// (Previously this hard-deleted every "available" ride, leaving booked passengers
	// pointing at a ride that no longer existed.)
	if err := retireOpenRides(delCtx, userId); err != nil {
		log.Printf("[CreateRide] Failed to retire previous rides for driver %s: %v", userId.Hex(), err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset available rides"})
		return
	}

	// Build final route array starting with pickup and including user-entered intermediate stops
	route := []string{body.Pickup}
	seenStops := make(map[string]bool)
	seenStops[strings.ToLower(strings.TrimSpace(body.Pickup))] = true

	// Add driver's manually specified route stops (if provided)
	for _, s := range body.Route {
		sClean := strings.ToLower(strings.TrimSpace(s))
		if sClean != "" && !seenStops[sClean] {
			seenStops[sClean] = true
			route = append(route, s)
		}
	}

	// --- AUTO-DISCOVER INTERMEDIATE STOPS FROM MAPS (NON-BLOCKING TIMEOUT) ---
	var discoveredStops []models.StopInfo
	var routeCoords [][]float64
	var totalDistM float64

	// Quick non-blocking attempt (max 1.5s) to avoid hanging ride creation if external map APIs are slow
	type geoRes struct {
		lat, lon float64
		err      error
	}
	pChan := make(chan geoRes, 1)
	dChan := make(chan geoRes, 1)

	go func() {
		lat, lon, err := utils.Geocode(body.Pickup)
		pChan <- geoRes{lat, lon, err}
	}()
	go func() {
		lat, lon, err := utils.Geocode(body.Dropoff)
		dChan <- geoRes{lat, lon, err}
	}()

	select {
	case pRes := <-pChan:
		select {
		case dRes := <-dChan:
			if pRes.err == nil && dRes.err == nil {
				stops, coords, dist, discErr := utils.DiscoverIntermediateStops(pRes.lat, pRes.lon, dRes.lat, dRes.lon, 3.0)
				if discErr == nil && len(stops) > 0 {
					discoveredStops = stops
					routeCoords = make([][]float64, len(coords))
					for i, c := range coords {
						routeCoords[i] = []float64{c[0], c[1]}
					}
					totalDistM = dist
					for _, s := range stops {
						sClean := strings.ToLower(strings.TrimSpace(s.Name))
						if sClean != "" && !seenStops[sClean] {
							seenStops[sClean] = true
							// Insert before dropoff
							route = append(route, s.Name)
						}
					}
				}
			}
		case <-time.After(1500 * time.Millisecond):
			log.Printf("[CreateRide] Map geocoding timed out softly — proceeding with text route")
		}
	case <-time.After(1500 * time.Millisecond):
		log.Printf("[CreateRide] Map geocoding timed out softly — proceeding with text route")
	}

	sDropClean := strings.ToLower(strings.TrimSpace(body.Dropoff))
	if !seenStops[sDropClean] {
		route = append(route, body.Dropoff) // Always end with dropoff
	}

	ride := models.Ride{
		DriverID:        userId,
		DriverName:      driver.Name,
		Pickup:          body.Pickup,
		Dropoff:         body.Dropoff,
		Route:           route,
		RouteCoords:     routeCoords,
		TotalDistanceM:  totalDistM,
		DiscoveredStops: discoveredStops,
		VehicleModel:    body.VehicleModel,
		VehicleNumber:   body.VehicleNumber,
		DepartureTime:   body.DepartureTime,
		Date:            body.Date,
		SeatsTotal:      body.SeatsTotal,
		SeatsBooked:     0,
		PricePerSeat:    body.PricePerSeat,
		Status:          string(models.RideStatusAvailable),
		CreatedAt:       time.Now(),
	}

	insCtx, insCancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer insCancel()

	_, err = rideCollection.InsertOne(insCtx, ride)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create ride"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":         "Ride created successfully",
		"discoveredStops": discoveredStops,
		"totalDistanceKm": totalDistM / 1000,
	})
}

// RoutePreview returns the auto-discovered intermediate stops for a given route
// without creating a ride (for front-end preview)
func RoutePreview(c *gin.Context) {
	pickup := c.Query("pickup")
	dropoff := c.Query("dropoff")

	if pickup == "" || dropoff == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pickup and dropoff are required"})
		return
	}

	// Geocode pickup and dropoff in parallel to halve the latency
	type geoResult struct {
		lat, lon float64
		err      error
	}
	pCh := make(chan geoResult, 1)
	dCh := make(chan geoResult, 1)

	go func() {
		lat, lon, err := utils.Geocode(pickup)
		pCh <- geoResult{lat, lon, err}
	}()
	go func() {
		lat, lon, err := utils.Geocode(dropoff)
		dCh <- geoResult{lat, lon, err}
	}()

	pRes := <-pCh
	dRes := <-dCh

	if pRes.err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Could not geocode pickup: " + pRes.err.Error()})
		return
	}
	if dRes.err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Could not geocode dropoff: " + dRes.err.Error()})
		return
	}

	stops, _, totalDistM, err := utils.DiscoverIntermediateStops(pRes.lat, pRes.lon, dRes.lat, dRes.lon, 3.0)
	if err != nil {
		// Don't fail hard — return empty stops so the UI can still post the ride
		log.Printf("RoutePreview discovery warning: %v", err)
		c.JSON(http.StatusOK, gin.H{
			"stops":           []interface{}{},
			"totalDistanceKm": 0,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"stops":           stops,
		"totalDistanceKm": totalDistM / 1000,
	})
}


var locationPattern = regexp.MustCompile(`^[a-zA-Z0-9\s\-,.]+$`)

func getStopIndex(route []string, stop string) int {
	stopClean := strings.TrimSpace(stop)
	if stopClean == "" {
		return -1
	}
	// First pass: exact case-insensitive match so substring stops (e.g. "Kota" vs "Kotabagh") don't collide
	for i, s := range route {
		if strings.EqualFold(strings.TrimSpace(s), stopClean) {
			return i
		}
	}
	// Second pass: fuzzy substring match
	for i, s := range route {
		if utils.FuzzyMatch(s, stopClean) {
			return i
		}
	}
	return -1
}

// normalizeRideRoute returns a clean, trimmed, case-insensitively deduplicated route slice,
// falling back to [ride.Pickup, ride.Dropoff] when ride.Route has no valid stops.
func normalizeRideRoute(ride models.Ride) []string {
	var clean []string
	seen := make(map[string]bool, len(ride.Route)+2)
	for _, s := range ride.Route {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if !seen[key] {
			seen[key] = true
			clean = append(clean, trimmed)
		}
	}
	if len(clean) == 0 {
		p := strings.TrimSpace(ride.Pickup)
		d := strings.TrimSpace(ride.Dropoff)
		if p != "" {
			clean = append(clean, p)
			seen[strings.ToLower(p)] = true
		}
		if d != "" && !seen[strings.ToLower(d)] {
			clean = append(clean, d)
		}
	}
	return clean
}

func segmentsOverlap(p1, d1, p2, d2 string, route []string) bool {
	if len(route) == 0 {
		return true // Fallback to full overlap if no route defined
	}

	p1Idx := getStopIndex(route, p1)
	d1Idx := getStopIndex(route, d1)
	p2Idx := getStopIndex(route, p2)
	d2Idx := getStopIndex(route, d2)

	// If stops aren't in route, assume overlap to be safe
	if p1Idx == -1 || d1Idx == -1 || p2Idx == -1 || d2Idx == -1 {
		return true
	}

	// Ensure indices are in order
	if p1Idx > d1Idx {
		p1Idx, d1Idx = d1Idx, p1Idx
	}
	if p2Idx > d2Idx {
		p2Idx, d2Idx = d2Idx, p2Idx
	}

	// Segments overlap if one starts before the other ends
	return d1Idx > p2Idx && d2Idx > p1Idx
}

func getTakenSeats(ctx context.Context, ride models.Ride, searchPickup, searchDropoff string) []int {
	taken, err := queryTakenSeats(ctx, ride, searchPickup, searchDropoff, primitive.NilObjectID)
	if err != nil {
		return []int{}
	}
	return taken
}

// computeTakenSeatsFromBookings computes the deduplicated taken seats for a ride
// from a pre-fetched slice of active (pending/accepted) bookings plus the ride's
// manualBlockedSeats. When searchPickup and searchDropoff are both non-empty, only
// bookings whose route segment overlaps the requested segment are counted.
func computeTakenSeatsFromBookings(ride models.Ride, bookings []models.Booking, searchPickup, searchDropoff string) []int {
	normRoute := normalizeRideRoute(ride)
	takenMap := make(map[int]bool)
	for _, b := range bookings {
		if b.Type == "parcel" {
			continue
		}
		// If searching for specific segment, only include overlapping bookings
		if searchPickup != "" && searchDropoff != "" {
			if segmentsOverlap(searchPickup, searchDropoff, b.Pickup, b.Dropoff, normRoute) {
				for _, s := range b.SeatLayout {
					takenMap[s] = true
				}
			}
		} else {
			// If no specific segment, show all taken seats (classic behavior)
			for _, s := range b.SeatLayout {
				takenMap[s] = true
			}
		}
	}

	// MERGE manual blocked seats by driver (Offline bookings)
	for _, s := range ride.ManualBlockedSeats {
		takenMap[s] = true
	}

	taken := make([]int, 0, len(takenMap))
	for s := range takenMap {
		taken = append(taken, s)
	}
	return taken
}

// batchBookingsForRides loads all pending/accepted bookings for the given ride IDs
// in a single MongoDB query and groups them by rideId.
func batchBookingsForRides(ctx context.Context, rideIDs []primitive.ObjectID) (map[primitive.ObjectID][]models.Booking, error) {
	grouped := make(map[primitive.ObjectID][]models.Booking, len(rideIDs))
	if len(rideIDs) == 0 {
		return grouped, nil
	}

	uniqueIDs := make([]primitive.ObjectID, 0, len(rideIDs))
	seen := make(map[primitive.ObjectID]bool, len(rideIDs))
	for _, id := range rideIDs {
		if !id.IsZero() && !seen[id] {
			seen[id] = true
			uniqueIDs = append(uniqueIDs, id)
		}
	}
	if len(uniqueIDs) == 0 {
		return grouped, nil
	}

	cursor, err := bookingCollection.Find(ctx, bson.M{
		"rideId": bson.M{"$in": uniqueIDs},
		"status": bson.M{"$in": []string{"pending", "accepted"}},
	})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var bookings []models.Booking
	if err := cursor.All(ctx, &bookings); err != nil {
		return nil, err
	}
	for _, b := range bookings {
		grouped[b.RideID] = append(grouped[b.RideID], b)
	}
	return grouped, nil
}

// queryTakenSeats returns the seats taken on a ride (optionally for a segment),
// ignoring the booking with excludeID (pass NilObjectID to ignore none).
// Unlike getTakenSeats it reports DB errors, so callers that must be strict
// (booking verification) can fail safely.
func queryTakenSeats(ctx context.Context, ride models.Ride, searchPickup, searchDropoff string, excludeID primitive.ObjectID) ([]int, error) {
	filter := bson.M{
		"rideId": ride.ID,
		"status": bson.M{"$in": []string{"pending", "accepted"}},
	}
	if !excludeID.IsZero() {
		filter["_id"] = bson.M{"$ne": excludeID}
	}
	cursor, err := bookingCollection.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var bookings []models.Booking
	if err := cursor.All(ctx, &bookings); err != nil {
		return nil, err
	}

	return computeTakenSeatsFromBookings(ride, bookings, searchPickup, searchDropoff), nil
}

// RideResponse represents an available ride returned by GetAvailableRides,
// enriched with segment-specific pricing and route match metadata.
type RideResponse struct {
	models.Ride
	SegmentPricePerSeat float64 `json:"segmentPricePerSeat,omitempty"`
	SegmentDistanceKm   float64 `json:"segmentDistanceKm,omitempty"`
	MatchedPickup       string  `json:"matchedPickup,omitempty"`
	MatchedDropoff      string  `json:"matchedDropoff,omitempty"`
	MatchType           string  `json:"matchType,omitempty"`
	IsPartialMatch      bool    `json:"isPartialMatch,omitempty"`
}

// classifyRideMatch determines whether a candidate ride matches the passenger's search
// (as a "full" match, a "common_pickup" partial match, or a "common_dropoff" partial match)
// and returns the effective segment endpoints on the driver's route.
func classifyRideMatch(ride models.Ride, searchPickup, searchDropoff string) (matched bool, matchType string, isPartial bool, matchedPickup, matchedDropoff string) {
	searchPickup = strings.TrimSpace(searchPickup)
	searchDropoff = strings.TrimSpace(searchDropoff)

	if searchPickup == "" && searchDropoff == "" {
		return true, "full", false, "", ""
	}

	// Reject same-city searches (e.g. pickup == dropoff or fuzzy-equivalent endpoints)
	if searchPickup != "" && searchDropoff != "" && utils.FuzzyMatch(searchPickup, searchDropoff) {
		return false, "", false, "", ""
	}

	route := normalizeRideRoute(ride)
	if len(route) < 2 {
		return false, "", false, "", ""
	}

	pickupIdx := -1
	if searchPickup != "" {
		pickupIdx = getStopIndex(route, searchPickup)
	}
	dropoffIdx := -1
	if searchDropoff != "" {
		dropoffIdx = getStopIndex(route, searchDropoff)
	}

	// Full match: both pickup and dropoff exist in route in forward order.
	// If both exist in reverse order (or same stop), reject so reverse-direction rides never match.
	if searchPickup != "" && searchDropoff != "" && pickupIdx >= 0 && dropoffIdx >= 0 {
		if pickupIdx < dropoffIdx && !utils.FuzzyMatch(route[pickupIdx], route[dropoffIdx]) {
			return true, "full", false, route[pickupIdx], route[dropoffIdx]
		}
		return false, "", false, "", ""
	}

	// Common starting point match: driver starts at or passes through pickup and continues onward.
	if searchPickup != "" && pickupIdx >= 0 && pickupIdx < len(route)-1 {
		endStop := strings.TrimSpace(ride.Dropoff)
		if endStop == "" {
			endStop = route[len(route)-1]
		}
		if !utils.FuzzyMatch(route[pickupIdx], endStop) {
			return true, "common_pickup", searchDropoff != "", route[pickupIdx], endStop
		}
	}

	// Common destination match: driver starts at ride.Pickup and reaches passenger's dropoff.
	if searchDropoff != "" && dropoffIdx > 0 {
		startStop := strings.TrimSpace(ride.Pickup)
		if startStop == "" {
			startStop = route[0]
		}
		if !utils.FuzzyMatch(startStop, route[dropoffIdx]) {
			return true, "common_dropoff", searchPickup != "", startStop, route[dropoffIdx]
		}
	}

	return false, "", false, "", ""
}

func computeSegmentPricing(ride models.Ride, matchedPickup, matchedDropoff string) (segPrice float64, segDistKm float64, normPickup string, normDropoff string) {
	normPickup = strings.TrimSpace(matchedPickup)
	normDropoff = strings.TrimSpace(matchedDropoff)

	route := normalizeRideRoute(ride)
	rideStart := strings.TrimSpace(ride.Pickup)
	if rideStart == "" && len(route) > 0 {
		rideStart = route[0]
	}
	rideEnd := strings.TrimSpace(ride.Dropoff)
	if rideEnd == "" && len(route) > 0 {
		rideEnd = route[len(route)-1]
	}

	spansFullRide := rideStart != "" && rideEnd != "" &&
		(strings.EqualFold(normPickup, rideStart) || (len(route) >= 2 && getStopIndex(route, normPickup) == 0)) &&
		(strings.EqualFold(normDropoff, rideEnd) || (len(route) >= 2 && getStopIndex(route, normDropoff) == len(route)-1))

	if !spansFullRide {
		pIdx, pStop := utils.MatchStopInRoute(ride.DiscoveredStops, normPickup)
		dIdx, dStop := utils.MatchStopInRoute(ride.DiscoveredStops, normDropoff)

		if len(ride.DiscoveredStops) > 0 {
			if pIdx == -1 && rideStart != "" && utils.FuzzyMatch(normPickup, rideStart) {
				pIdx = 0
				pStop = models.StopInfo{Name: rideStart, DistanceM: 0}
			}
			if dIdx == -1 && rideEnd != "" && utils.FuzzyMatch(normDropoff, rideEnd) && ride.TotalDistanceM > 0 {
				dIdx = len(ride.DiscoveredStops)
				dStop = models.StopInfo{Name: rideEnd, DistanceM: ride.TotalDistanceM}
			}
		}

		if pIdx != -1 && dIdx != -1 && dStop.DistanceM > pStop.DistanceM {
			segDistKm = (dStop.DistanceM - pStop.DistanceM) / 1000
			segPrice = utils.CalculateSegmentPrice(ride.PricePerSeat, ride.TotalDistanceM, pStop.DistanceM, dStop.DistanceM)
			outPickup := strings.TrimSpace(pStop.Name)
			if outPickup == "" {
				outPickup = normPickup
			}
			outDropoff := strings.TrimSpace(dStop.Name)
			if outDropoff == "" {
				outDropoff = normDropoff
			}
			return segPrice, segDistKm, outPickup, outDropoff
		}
	}

	return ride.PricePerSeat, ride.TotalDistanceM / 1000, normPickup, normDropoff
}

// resolveBookingSegment validates a booking's requested pickup and dropoff against a ride's route,
// returning the normalized segment endpoints on the driver's route, per-seat segment price, and total price.
func resolveBookingSegment(ride models.Ride, reqPickup, reqDropoff string, seats int) (valid bool, normPickup, normDropoff string, segPricePerSeat, totalPrice float64) {
	reqPickup = strings.TrimSpace(reqPickup)
	reqDropoff = strings.TrimSpace(reqDropoff)
	if reqPickup == "" || reqDropoff == "" {
		return false, "", "", 0, 0
	}

	matched, _, _, matchedPickup, matchedDropoff := classifyRideMatch(ride, reqPickup, reqDropoff)
	if !matched || matchedPickup == "" || matchedDropoff == "" {
		return false, "", "", 0, 0
	}

	segPrice, _, nPickup, nDropoff := computeSegmentPricing(ride, matchedPickup, matchedDropoff)
	if seats <= 0 {
		seats = 1
	}
	total := math.Round(segPrice * float64(seats))
	return true, nPickup, nDropoff, segPrice, total
}

// buildLocationRegex builds a safe case-insensitive MongoDB regex for a validated location query.
// When the location includes a comma-qualified suffix (e.g. "Bageshwar, Uttarakhand"), it also
// matches the primary locality name before the comma so MongoDB filtering aligns with utils.FuzzyMatch.
func buildLocationRegex(loc string) primitive.Regex {
	loc = strings.TrimSpace(loc)
	quotedFull := regexp.QuoteMeta(loc)
	if idx := strings.Index(loc, ","); idx > 0 {
		primary := strings.TrimSpace(loc[:idx])
		if len(primary) >= 2 && !strings.EqualFold(primary, loc) {
			quotedPrimary := regexp.QuoteMeta(primary)
			return primitive.Regex{Pattern: "(?i)(?:" + quotedFull + "|" + quotedPrimary + ")", Options: ""}
		}
	}
	return primitive.Regex{Pattern: "(?i)" + quotedFull, Options: ""}
}

func matchPriority(r RideResponse) int {
	if !r.IsPartialMatch || r.MatchType == "full" {
		return 0
	}
	if r.MatchType == "common_pickup" {
		return 1
	}
	if r.MatchType == "common_dropoff" {
		return 2
	}
	return 3
}

func effectiveRidePrice(r RideResponse) float64 {
	if r.SegmentPricePerSeat > 0 {
		return r.SegmentPricePerSeat
	}
	return r.PricePerSeat
}

func sortRideResponses(results []RideResponse) {
	for i := 0; i < len(results)-1; i++ {
		for j := 0; j < len(results)-i-1; j++ {
			pJ := matchPriority(results[j])
			pNext := matchPriority(results[j+1])
			priceJ := effectiveRidePrice(results[j])
			priceNext := effectiveRidePrice(results[j+1])
			shouldSwap := false
			if pJ != pNext {
				shouldSwap = pJ > pNext
			} else if priceJ != priceNext {
				shouldSwap = priceJ < priceNext
			} else {
				shouldSwap = len(results[j].TakenSeats) < len(results[j+1].TakenSeats)
			}
			if shouldSwap {
				results[j], results[j+1] = results[j+1], results[j]
			}
		}
	}
}

func GetAvailableRides(c *gin.Context) {
	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	pickup := strings.TrimSpace(c.Query("pickup"))
	dropoff := strings.TrimSpace(c.Query("dropoff"))
	date := strings.TrimSpace(c.Query("date"))

	// Validate user-controlled inputs before using them in DB query construction
	const maxLocationLen = 100

	if pickup != "" {
		if len(pickup) > maxLocationLen || !locationPattern.MatchString(pickup) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid pickup"})
			return
		}
	}

	if dropoff != "" {
		if len(dropoff) > maxLocationLen || !locationPattern.MatchString(dropoff) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid dropoff"})
			return
		}
	}

	if date != "" {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid date format. Use YYYY-MM-DD"})
			return
		}
	}

	log.Printf("🔍 SEARCH REQUEST: pickup='%s', dropoff='%s', date='%s'", pickup, dropoff, date)

	// Build DB filter: always filter available rides, optionally by date,
	// and match rides whose route contains pickup OR dropoff (to support both full and common-point matches).
	matchStage := bson.M{"status": "available"}
	if date != "" {
		matchStage["date"] = date
	}

	var routeConditions []bson.M
	if pickup != "" {
		routeConditions = append(routeConditions, bson.M{
			"route": bson.M{"$regex": buildLocationRegex(pickup)},
		})
	}
	if dropoff != "" {
		routeConditions = append(routeConditions, bson.M{
			"route": bson.M{"$regex": buildLocationRegex(dropoff)},
		})
	}
	if len(routeConditions) == 1 {
		matchStage["route"] = routeConditions[0]["route"]
	} else if len(routeConditions) > 1 {
		matchStage["$or"] = routeConditions
	}

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: matchStage}},
	}

	cursor, err := rideCollection.Aggregate(dbCtx, pipeline)
	if err != nil {
		log.Printf("❌ AGGREGATION ERROR: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Search failed"})
		return
	}

	var matchedRides []models.Ride
	if err := cursor.All(dbCtx, &matchedRides); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse matching rides"})
		return
	}

	type candidateMatch struct {
		ride           models.Ride
		matchType      string
		isPartial      bool
		matchedPickup  string
		matchedDropoff string
	}
	candidates := make([]candidateMatch, 0, len(matchedRides))
	candidateRideIDs := make([]primitive.ObjectID, 0, len(matchedRides))
	for _, ride := range matchedRides {
		matched, matchType, isPartial, matchedPickup, matchedDropoff := classifyRideMatch(ride, pickup, dropoff)
		if !matched {
			continue
		}
		candidates = append(candidates, candidateMatch{
			ride:           ride,
			matchType:      matchType,
			isPartial:      isPartial,
			matchedPickup:  matchedPickup,
			matchedDropoff: matchedDropoff,
		})
		candidateRideIDs = append(candidateRideIDs, ride.ID)
	}

	bookingsByRide, err := batchBookingsForRides(dbCtx, candidateRideIDs)
	if err != nil {
		log.Printf("❌ BATCH BOOKINGS ERROR: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check seat availability"})
		return
	}

	var results []RideResponse
	for _, cand := range candidates {
		ride := cand.ride
		ride.TakenSeats = computeTakenSeatsFromBookings(ride, bookingsByRide[ride.ID], cand.matchedPickup, cand.matchedDropoff)
		if len(ride.TakenSeats) >= ride.SeatsTotal {
			continue
		}

		response := RideResponse{
			Ride:           ride,
			MatchType:      cand.matchType,
			IsPartialMatch: cand.isPartial,
		}

		if cand.matchedPickup != "" && cand.matchedDropoff != "" {
			segPrice, segDistKm, normPickup, normDropoff := computeSegmentPricing(ride, cand.matchedPickup, cand.matchedDropoff)
			response.SegmentPricePerSeat = segPrice
			response.SegmentDistanceKm = segDistKm
			response.MatchedPickup = normPickup
			response.MatchedDropoff = normDropoff
		}

		results = append(results, response)
	}

	// Sort results:
	// 1. Full matches (!IsPartialMatch) first, sorted by effectiveRidePrice descending (then len(TakenSeats) descending)
	// 2. Common starting point ("common_pickup") matches, sorted by effectiveRidePrice descending (then len(TakenSeats) descending)
	// 3. Common destination ("common_dropoff") matches, sorted by effectiveRidePrice descending (then len(TakenSeats) descending)
	if len(results) > 1 {
		sortRideResponses(results)
	}

	if results == nil {
		results = []RideResponse{}
	}
	c.JSON(http.StatusOK, results)
}

func GetRideDetails(c *gin.Context) {
	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	rideIdHex := c.Param("rideId")
	rideId, err := primitive.ObjectIDFromHex(rideIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ride ID"})
		return
	}
	pickup := strings.TrimSpace(c.Query("pickup"))
	dropoff := strings.TrimSpace(c.Query("dropoff"))

	var ride models.Ride
	err = rideCollection.FindOne(dbCtx, bson.M{"_id": rideId}).Decode(&ride)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found"})
		return
	}

	response := RideResponse{Ride: ride}
	segPickup, segDropoff := pickup, dropoff
	if pickup != "" || dropoff != "" {
		if matched, matchType, isPartial, matchedPickup, matchedDropoff := classifyRideMatch(ride, pickup, dropoff); matched {
			segPickup = matchedPickup
			segDropoff = matchedDropoff
			response.MatchType = matchType
			response.IsPartialMatch = isPartial
			if matchedPickup != "" && matchedDropoff != "" {
				segPrice, segDistKm, normPickup, normDropoff := computeSegmentPricing(ride, matchedPickup, matchedDropoff)
				response.SegmentPricePerSeat = segPrice
				response.SegmentDistanceKm = segDistKm
				response.MatchedPickup = normPickup
				response.MatchedDropoff = normDropoff
				segPickup = normPickup
				segDropoff = normDropoff
			}
		}
	}

	response.TakenSeats = getTakenSeats(dbCtx, ride, segPickup, segDropoff)
	c.JSON(http.StatusOK, response)
}

// prepareBookingSeats validates and normalizes seat selection for a booking.
// Parcel bookings never consume passenger seats (SeatsRequested=0, empty SeatLayout).
// Seat bookings must provide a non-empty 1-indexed SeatLayout matching SeatsRequested.
// Pass seatsTotal < 0 to skip upper-bound seat validation before the ride document is loaded.
func prepareBookingSeats(bookingType string, seatsRequested int, seatLayout []int, seatsTotal int) (int, []int, string) {
	if bookingType == "parcel" {
		return 0, []int{}, ""
	}
	if len(seatLayout) == 0 {
		return 0, nil, "Please select at least one seat"
	}
	if seatsRequested != len(seatLayout) {
		return 0, nil, "seatsRequested must match the number of selected seats"
	}

	seen := make(map[int]bool, len(seatLayout))
	normalized := make([]int, len(seatLayout))
	for i, requestedSeat := range seatLayout {
		if requestedSeat < 1 || (seatsTotal >= 0 && requestedSeat > seatsTotal) {
			return 0, nil, "Invalid seat index"
		}
		if seen[requestedSeat] {
			return 0, nil, "Duplicate seat selection is not allowed"
		}
		seen[requestedSeat] = true
		normalized[i] = requestedSeat
	}
	return len(normalized), normalized, ""
}

// shouldReserveSeatsForBooking reports whether accepting a booking must increment
// ride.seatsBooked and returns the number of seats to reserve. Only "seat" bookings
// consume passenger capacity; parcel bookings never reduce available seats.
func shouldReserveSeatsForBooking(booking models.Booking) (bool, int) {
	if booking.Type != "seat" {
		return false, 0
	}
	requestedSeats := booking.SeatsRequested
	if requestedSeats < 1 {
		requestedSeats = len(booking.SeatLayout)
	}
	if requestedSeats < 1 {
		requestedSeats = 1
	}
	return true, requestedSeats
}

func BookRide(c *gin.Context) {
	passengerId := c.MustGet("userId").(primitive.ObjectID)
	rideIdHex := c.Param("rideId")
	rideId, err := primitive.ObjectIDFromHex(rideIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ride ID"})
		return
	}

	var body struct {
		Type           string `json:"type" binding:"required,oneof=seat parcel"`
		Pickup         string `json:"pickup" binding:"required"`
		Dropoff        string `json:"dropoff" binding:"required"`
		SeatsRequested int    `json:"seatsRequested"`
		SeatLayout     []int  `json:"seatLayout"`
		RoofCarrier    bool   `json:"roofCarrier"`
		MotionSickness bool   `json:"motionSickness"`
		// Parcel fields
		ParcelSize    string `json:"parcelSize"`
		RecipientName string `json:"recipientName"`
		ContactNumber string `json:"contactNumber"`
		DropLocation  string `json:"dropLocation"`
		Notes         string `json:"notes"`
		PhotoUrl      string `json:"photoUrl"`
		Price         string `json:"price"`
	}

	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	bookingType := body.Type
	if bookingType == "" {
		bookingType = "seat" // dynamic default for backward compatibility
	}

	// Pre-DB seat selection validation so empty/mismatched/0-indexed/duplicate seat layouts
	// are rejected immediately for seat bookings, and parcel bookings normalize to 0 seats.
	seatsCount, seatLayout, seatErr := prepareBookingSeats(bookingType, body.SeatsRequested, body.SeatLayout, -1)
	if seatErr != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": seatErr})
		return
	}

	// SECURITY: Prevent driver from booking their own ride
	checkCtx, checkCancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer checkCancel()
	var rideForCheck models.Ride
	if err := rideCollection.FindOne(checkCtx, bson.M{"_id": rideId}).Decode(&rideForCheck); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found"})
		return
	}
	if passengerId == rideForCheck.DriverID {
		c.JSON(http.StatusForbidden, gin.H{"error": "You cannot book your own ride"})
		return
	}
	if rideForCheck.Status != "available" {
		c.JSON(http.StatusConflict, gin.H{"error": "This ride is no longer accepting bookings"})
		return
	}

	seatsCount, seatLayout, seatErr = prepareBookingSeats(bookingType, body.SeatsRequested, body.SeatLayout, rideForCheck.SeatsTotal)
	if seatErr != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": seatErr})
		return
	}

	validSegment, effectivePickup, effectiveDropoff, _, computedTotalPrice := resolveBookingSegment(rideForCheck, body.Pickup, body.Dropoff, seatsCount)
	if !validSegment {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid pickup or dropoff for this ride's route"})
		return
	}

	bookingPrice := strings.TrimSpace(body.Price)
	if bookingType == "seat" && computedTotalPrice > 0 {
		bookingPrice = fmt.Sprintf("%.0f", computedTotalPrice)
	}

	if bookingType == "seat" {
		// SECURITY: Prevent same passenger from booking the same ride twice (seat rides only)
		existingCount, _ := bookingCollection.CountDocuments(checkCtx, bson.M{
			"rideId":      rideId,
			"passengerId": passengerId,
			"type":        "seat",
			"status":      bson.M{"$in": []string{"pending", "accepted"}},
		})
		if existingCount > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "You already have an active booking for this ride. Check your notifications for the status."})
			return
		}

		// Verify that requested seats are not already taken or pending approval
		takenSeats := getTakenSeats(checkCtx, rideForCheck, effectivePickup, effectiveDropoff)
		takenMap := make(map[int]bool, len(takenSeats))
		for _, seat := range takenSeats {
			takenMap[seat] = true
		}
		for _, requestedSeat := range seatLayout {
			if takenMap[requestedSeat] {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Seat %d is already taken or pending approval", requestedSeat)})
				return
			}
		}
	}

	booking := models.Booking{
		RideID:         rideId,
		PassengerID:    passengerId,
		Type:           bookingType,
		Pickup:         effectivePickup,
		Dropoff:        effectiveDropoff,
		SeatsRequested: seatsCount,
		SeatLayout:     seatLayout,
		RoofCarrier:    body.RoofCarrier,
		MotionSickness: body.MotionSickness,
		// Parcel fields
		ParcelSize:    body.ParcelSize,
		RecipientName: body.RecipientName,
		ContactNumber: body.ContactNumber,
		DropLocation:  body.DropLocation,
		Notes:         body.Notes,
		PhotoUrl:      body.PhotoUrl,
		Price:         bookingPrice,
		Status:        "pending",
		CreatedAt:     time.Now(),
	}

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	result, err := bookingCollection.InsertOne(dbCtx, booking)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to store booking"})
		return
	}

	// CONCURRENCY: The availability checks above ran BEFORE the insert, so two
	// simultaneous requests for the same seat could both pass them. Re-verify now
	// that our booking is persisted; if it conflicts, roll it back.
	newBookingID, _ := result.InsertedID.(primitive.ObjectID)
	conflictMsg, verifyErr := verifyNewBooking(dbCtx, rideId, newBookingID, passengerId, bookingType, effectivePickup, effectiveDropoff, seatsCount, seatLayout)
	if verifyErr != nil || conflictMsg != "" {
		rbCtx, rbCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = bookingCollection.DeleteOne(rbCtx, bson.M{"_id": newBookingID})
		rbCancel()
		if verifyErr != nil {
			log.Printf("[BookRide] Verification failed for booking %s: %v", newBookingID.Hex(), verifyErr)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to confirm booking. Please try again."})
			return
		}
		c.JSON(http.StatusConflict, gin.H{"error": conflictMsg})
		return
	}

	msg := "Booking request sent to driver"
	if bookingType == "parcel" {
		msg = "Parcel pickup request sent to driver"
	}

	// Notify the driver via FCM push (fire-and-forget)
	go func() {
		fcmCtx, fcmCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer fcmCancel()
		var ride models.Ride
		if err := rideCollection.FindOne(fcmCtx, bson.M{"_id": rideId}).Decode(&ride); err == nil {
			var driver struct {
				FCMToken  string   `bson:"fcm_token"`
				FCMTokens []string `bson:"fcm_tokens"`
				Name      string   `bson:"name"`
			}
			err2 := usersCollection.FindOne(fcmCtx, bson.M{"_id": ride.DriverID}).Decode(&driver)
			driverTokens := utils.CollectTokens(driver.FCMToken, driver.FCMTokens)
			if err2 != nil {
				log.Printf("[FCM] Driver lookup error for ride %s (DriverID %s): %v", rideId.Hex(), ride.DriverID.Hex(), err2)
			} else if len(driverTokens) == 0 {
				log.Printf("[FCM] Driver %s has no FCM tokens stored in DB", ride.DriverID.Hex())
			} else {
				title := "🚗 New Booking Request"
				numSeats := booking.SeatsRequested
				if numSeats <= 0 && len(booking.SeatLayout) > 0 {
					numSeats = len(booking.SeatLayout)
				}
				if numSeats <= 0 {
					numSeats = 1
				}
				var body string
				if numSeats > 1 {
					body = fmt.Sprintf("A passenger requested %d seats on your ride from %s to %s", numSeats, booking.Pickup, booking.Dropoff)
				} else {
					body = fmt.Sprintf("A passenger requested 1 seat on your ride from %s to %s", booking.Pickup, booking.Dropoff)
				}
				if bookingType == "parcel" {
					title = "📦 New Parcel Request"
					body = "A new parcel pickup request has been received."
				}
				log.Printf("[FCM] Sending booking request push to driver %s...", ride.DriverID.Hex())
				// Pass rich data so the SW notification shows full ride details and
				// tapping it deep-links straight into the requests overlay.
				bookingObjectId := result.InsertedID.(primitive.ObjectID)
				CreateNotification(ride.DriverID, title, body, "booking_request", bookingObjectId.Hex(), map[string]string{
					"bookingId": bookingObjectId.Hex(),
					"rideId":    rideId.Hex(),
					"pickup":    booking.Pickup,
					"dropoff":   booking.Dropoff,
					"url":       "/?openRequests=true",
				})
			}
		} else {
			log.Printf("[FCM] Ride lookup error for ride %s: %v", rideId.Hex(), err)
		}
	}()

	c.JSON(http.StatusCreated, gin.H{"message": msg, "bookingId": result.InsertedID})
}

func GetDriverRequests(c *gin.Context) {
	driverId := c.MustGet("userId").(primitive.ObjectID)
	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	// Sub-query to get all rides created by this driver
	cursor, err := rideCollection.Find(dbCtx, bson.M{"driverId": driverId})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch driver rides"})
		return
	}

	var rides []models.Ride
	if err := cursor.All(dbCtx, &rides); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse rides"})
		return
	}

	// Build rideId → ride map for enrichment
	rideMap := make(map[primitive.ObjectID]models.Ride)
	var rideIds []primitive.ObjectID
	for _, ride := range rides {
		rideIds = append(rideIds, ride.ID)
		rideMap[ride.ID] = ride
	}
	if len(rideIds) == 0 {
		// A nil slice encodes as BSON null and "$in" would error — no rides means no requests.
		c.JSON(http.StatusOK, []interface{}{})
		return
	}

	// Filter bookings for those rideIds (newest first)
	opts := options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}})
	cursor, err = bookingCollection.Find(dbCtx, bson.M{"rideId": bson.M{"$in": rideIds}}, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch requests"})
		return
	}

	var bookings []models.Booking
	if err := cursor.All(dbCtx, &bookings); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse bookings"})
		return
	}

	// Enrich each booking with passenger name, phone, and ride details
	type EnrichedBooking struct {
		models.Booking
		PassengerName  string     `json:"passengerName"`
		PassengerPhone string     `json:"passengerPhone"`
		Ride           models.Ride `json:"ride"`
	}

	// PERF: Load all passengers in ONE query instead of one query per booking (N+1).
	passengerIDs := make([]primitive.ObjectID, 0, len(bookings))
	seenPassenger := make(map[primitive.ObjectID]bool, len(bookings))
	for _, b := range bookings {
		if !seenPassenger[b.PassengerID] {
			seenPassenger[b.PassengerID] = true
			passengerIDs = append(passengerIDs, b.PassengerID)
		}
	}
	passengerMap := make(map[primitive.ObjectID]models.User, len(passengerIDs))
	if len(passengerIDs) > 0 {
		pCursor, pErr := usersCollection.Find(dbCtx,
			bson.M{"_id": bson.M{"$in": passengerIDs}},
			options.Find().SetProjection(bson.M{"name": 1, "phone_number": 1}),
		)
		if pErr == nil {
			var passengers []models.User
			if err := pCursor.All(dbCtx, &passengers); err == nil {
				for _, p := range passengers {
					passengerMap[p.ID] = p
				}
			}
		}
	}

	var enriched []EnrichedBooking
	for _, b := range bookings {
		passenger := passengerMap[b.PassengerID]
		ride := rideMap[b.RideID]
		enriched = append(enriched, EnrichedBooking{
			Booking:        b,
			PassengerName:  passenger.Name,
			PassengerPhone: passenger.PhoneNumber,
			Ride:           ride,
		})
	}

	if enriched == nil {
		enriched = []EnrichedBooking{}
	}
	c.JSON(http.StatusOK, enriched)
}

func GetPassengerBookings(c *gin.Context) {
	passengerId := c.MustGet("userId").(primitive.ObjectID)
	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	pipeline := []bson.M{
		{"$match": bson.M{"passengerId": passengerId}},
		{"$sort": bson.M{"createdAt": -1}},
		{"$lookup": bson.M{
			"from":         "rides",
			"localField":   "rideId",
			"foreignField": "_id",
			"as":           "rideDetails",
		}},
	}

	cursor, err := bookingCollection.Aggregate(dbCtx, pipeline)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch bookings"})
		return
	}

	type aggregatedResult struct {
		models.Booking `bson:",inline"`
		RideDetails    []models.Ride `bson:"rideDetails"`
	}

	var results []aggregatedResult
	if err := cursor.All(dbCtx, &results); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse bookings"})
		return
	}

	type BookingResponse struct {
		models.Booking
		Ride        models.Ride `json:"ride"`
		DriverPhone string      `json:"driverPhone"`
	}

	// PERF: Batch load bookings and driver phones for all rides in ONE query each instead of N+1.
	rideIDs := make([]primitive.ObjectID, 0, len(results))
	driverIDs := make([]primitive.ObjectID, 0, len(results))
	seenDrivers := make(map[primitive.ObjectID]bool, len(results))
	for _, res := range results {
		if len(res.RideDetails) > 0 {
			r := res.RideDetails[0]
			rideIDs = append(rideIDs, r.ID)
			if !r.DriverID.IsZero() && !seenDrivers[r.DriverID] {
				seenDrivers[r.DriverID] = true
				driverIDs = append(driverIDs, r.DriverID)
			}
		}
	}

	bookingsByRide, bErr := batchBookingsForRides(dbCtx, rideIDs)
	if bErr != nil {
		log.Printf("[GetPassengerBookings] batchBookingsForRides warning: %v", bErr)
		bookingsByRide = make(map[primitive.ObjectID][]models.Booking)
	}

	driverPhones := make(map[primitive.ObjectID]string, len(driverIDs))
	if len(driverIDs) > 0 {
		dCursor, dErr := usersCollection.Find(dbCtx,
			bson.M{"_id": bson.M{"$in": driverIDs}},
			options.Find().SetProjection(bson.M{"phone_number": 1}),
		)
		if dErr == nil && dCursor != nil {
			var drivers []struct {
				ID          primitive.ObjectID `bson:"_id"`
				PhoneNumber string             `bson:"phone_number"`
			}
			if dCursor.All(dbCtx, &drivers) == nil {
				for _, d := range drivers {
					driverPhones[d.ID] = d.PhoneNumber
				}
			}
			dCursor.Close(dbCtx)
		}
	}

	var response []BookingResponse
	for _, res := range results {
		ride := models.Ride{}
		driverPhone := ""
		if len(res.RideDetails) > 0 {
			ride = res.RideDetails[0]
			// Populate real-time taken seats for THIS booking's segment
			ride.TakenSeats = computeTakenSeatsFromBookings(ride, bookingsByRide[ride.ID], res.Booking.Pickup, res.Booking.Dropoff)
			driverPhone = driverPhones[ride.DriverID]
		}
		response = append(response, BookingResponse{
			Booking:     res.Booking,
			Ride:        ride,
			DriverPhone: driverPhone,
		})
	}

	if response == nil {
		response = []BookingResponse{}
	}
	c.JSON(http.StatusOK, response)
}

func SaveRecentRide(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	var body struct {
		Pickup   string `json:"pickup" binding:"required,max=200"`
		Dropoff  string `json:"dropoff" binding:"required,max=200"`
		RideType string `json:"rideType" binding:"required,oneof=seat parcel"`
	}

	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// Store in separate collection so it doesn't pollute the available rides pool
	recentEntry := bson.M{
		"userId":    userId,
		"pickup":    body.Pickup,
		"dropoff":   body.Dropoff,
		"rideType":  body.RideType,
		"createdAt": time.Now(),
	}

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	_, err := recentRoutesCollection.InsertOne(dbCtx, recentEntry)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save recent ride"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Recent ride saved"})
}

// checkAcceptBookingSeatConflict validates that accepting `target` does not conflict
// with `ride.ManualBlockedSeats`, `ride.SeatsTotal`, or any already-accepted booking
// on an overlapping route segment. Parcel bookings never conflict with seat capacity.
func checkAcceptBookingSeatConflict(ride models.Ride, target models.Booking, otherAccepted []models.Booking) string {
	if target.Type != "seat" {
		return ""
	}
	blockedSet := make(map[int]bool, len(ride.ManualBlockedSeats))
	for _, s := range ride.ManualBlockedSeats {
		blockedSet[s] = true
	}
	for _, s := range target.SeatLayout {
		if s < 1 || (ride.SeatsTotal > 0 && s > ride.SeatsTotal) {
			return fmt.Sprintf("Cannot accept booking: invalid seat %d", s)
		}
		if blockedSet[s] {
			return fmt.Sprintf("Cannot accept booking: seat %d is blocked by driver", s)
		}
	}

	normRoute := normalizeRideRoute(ride)
	for _, other := range otherAccepted {
		if other.ID == target.ID || other.Type != "seat" {
			continue
		}
		if !segmentsOverlap(target.Pickup, target.Dropoff, other.Pickup, other.Dropoff, normRoute) {
			continue
		}
		otherSeats := make(map[int]bool, len(other.SeatLayout))
		for _, s := range other.SeatLayout {
			otherSeats[s] = true
		}
		for _, s := range target.SeatLayout {
			if otherSeats[s] {
				return fmt.Sprintf("Cannot accept booking: seat %d is already accepted for an overlapping segment", s)
			}
		}
	}
	return ""
}

func countDistinctAcceptedSeats(acceptedBookings []models.Booking) int {
	occupied := make(map[int]bool)
	for _, b := range acceptedBookings {
		if b.Type != "seat" {
			continue
		}
		for _, s := range b.SeatLayout {
			occupied[s] = true
		}
	}
	return len(occupied)
}

func UpdateBookingStatus(c *gin.Context) {
	bookingIdHex := c.Param("bookingId")
	bookingId, err := primitive.ObjectIDFromHex(bookingIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid booking ID"})
		return
	}

	var body struct {
		Status string `json:"status" binding:"required,oneof=accepted rejected"` // strictly validated
	}

	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	driverId := c.MustGet("userId").(primitive.ObjectID)

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	// Get the booking to find the rideId and seatsRequested
	var booking models.Booking
	err = bookingCollection.FindOne(dbCtx, bson.M{"_id": bookingId}).Decode(&booking)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Booking not found"})
		return
	}

	// Explicit booking FSM transition guard
	if !models.CanTransitionBookingStatus(models.BookingStatus(booking.Status), models.BookingStatus(body.Status)) {
		c.JSON(http.StatusConflict, gin.H{"error": "Booking status already updated"})
		return
	}

	// Authorization check IDOR & ride status check
	var ride models.Ride
	err = rideCollection.FindOne(dbCtx, bson.M{"_id": booking.RideID}).Decode(&ride)
	if err != nil || ride.DriverID != driverId {
		c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized"})
		return
	}
	if ride.Status != string(models.RideStatusAvailable) {
		c.JSON(http.StatusConflict, gin.H{"error": "Cannot update a booking for a ride that is " + ride.Status})
		return
	}

	if body.Status == string(models.BookingStatusAccepted) {
		// Fail closed on driver lookup error or unverified status
		var driver struct {
			VerificationStatus string `bson:"verification_status"`
		}
		if err := config.Database.Collection("users").FindOne(dbCtx, bson.M{"_id": driverId}).Decode(&driver); err != nil || driver.VerificationStatus != string(models.VerificationStatusVerified) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Only verified drivers can accept rides"})
			return
		}

		// Pre-CAS segment-aware seat & manual-block conflict check
		if booking.Type == "seat" {
			acceptedCursor, accErr := bookingCollection.Find(dbCtx, bson.M{
				"rideId": booking.RideID,
				"type":   "seat",
				"status": string(models.BookingStatusAccepted),
			})
			if accErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify seat availability"})
				return
			}
			var acceptedBookings []models.Booking
			if decErr := acceptedCursor.All(dbCtx, &acceptedBookings); decErr != nil {
				acceptedCursor.Close(dbCtx)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify seat availability"})
				return
			}
			acceptedCursor.Close(dbCtx)

			if conflict := checkAcceptBookingSeatConflict(ride, booking, acceptedBookings); conflict != "" {
				c.JSON(http.StatusConflict, gin.H{"error": conflict})
				return
			}
		}
	}

	// Atomic check that ride is still "available" before transitioning booking
	rideCheckRes, rideCheckErr := rideCollection.UpdateOne(
		dbCtx,
		bson.M{"_id": booking.RideID, "status": string(models.RideStatusAvailable)},
		bson.M{"$set": bson.M{"status": string(models.RideStatusAvailable)}},
	)
	if rideCheckErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify ride status"})
		return
	}
	if rideCheckRes.MatchedCount == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Ride is no longer available"})
		return
	}

	// CONCURRENCY: Only transition the booking if it is STILL pending.
	updateRes, err := bookingCollection.UpdateOne(
		dbCtx,
		bson.M{"_id": bookingId, "status": string(models.BookingStatusPending)},
		bson.M{"$set": bson.M{"status": body.Status}},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update booking"})
		return
	}
	if updateRes.MatchedCount == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Booking status already updated"})
		return
	}

	rollbackAcceptedBooking := func() {
		rbCtx, rbCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rbCancel()
		_, _ = bookingCollection.UpdateOne(
			rbCtx,
			bson.M{"_id": bookingId, "status": string(models.BookingStatusAccepted)},
			bson.M{"$set": bson.M{"status": string(models.BookingStatusPending)}},
		)
	}

	if body.Status == string(models.BookingStatusAccepted) {
		// Post-CAS verification: re-read ride & accepted bookings to eliminate TOCTOU races
		var latestRide models.Ride
		if err := rideCollection.FindOne(dbCtx, bson.M{"_id": booking.RideID}).Decode(&latestRide); err != nil || latestRide.Status != string(models.RideStatusAvailable) {
			rollbackAcceptedBooking()
			c.JSON(http.StatusConflict, gin.H{"error": "Ride is no longer available"})
			return
		}

		if booking.Type == "seat" {
			acceptedCursor, accErr := bookingCollection.Find(dbCtx, bson.M{
				"rideId": booking.RideID,
				"type":   "seat",
				"status": string(models.BookingStatusAccepted),
			})
			if accErr != nil {
				rollbackAcceptedBooking()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify seat availability"})
				return
			}
			var acceptedBookings []models.Booking
			if decErr := acceptedCursor.All(dbCtx, &acceptedBookings); decErr != nil {
				acceptedCursor.Close(dbCtx)
				rollbackAcceptedBooking()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify seat availability"})
				return
			}
			acceptedCursor.Close(dbCtx)

			if conflict := checkAcceptBookingSeatConflict(latestRide, booking, acceptedBookings); conflict != "" {
				rollbackAcceptedBooking()
				c.JSON(http.StatusConflict, gin.H{"error": conflict})
				return
			}

			distinctSeats := countDistinctAcceptedSeats(acceptedBookings)
			_, _ = rideCollection.UpdateOne(
				dbCtx,
				bson.M{"_id": booking.RideID, "status": string(models.RideStatusAvailable)},
				bson.M{"$set": bson.M{"seatsBooked": distinctSeats}},
			)
		}

		// Assign unique Booking ID (BID) only after acceptance is verified
		if booking.BookingID == "" {
			newBID, errBID := utils.GenerateNextBookingID(c.Request.Context())
			if errBID == nil && newBID != "" {
				_, _ = bookingCollection.UpdateOne(
					dbCtx,
					bson.M{"_id": bookingId, "status": string(models.BookingStatusAccepted)},
					bson.M{"$set": bson.M{"bookingId": newBID}},
				)
				booking.BookingID = newBID
			}
		}
	}


	// Capture status before goroutine so the closure doesn't race on body
	capturedStatus := body.Status

	// Notify passenger of decision via DB notification + FCM Push
	go func() {
		var pushTitle, pushBody string
		if capturedStatus == "accepted" {
			pushTitle = "✅ Booking Accepted!"
			if booking.SeatsRequested > 1 {
				pushBody = fmt.Sprintf("Your booking for %d seats from %s to %s has been accepted by the driver.", booking.SeatsRequested, booking.Pickup, booking.Dropoff)
			} else {
				pushBody = "Your ride from " + booking.Pickup + " to " + booking.Dropoff + " has been accepted by the driver."
			}
		} else {
			pushTitle = "❌ Booking Declined"
			if booking.SeatsRequested > 1 {
				pushBody = fmt.Sprintf("Sorry, the driver declined your booking for %d seats from %s to %s.", booking.SeatsRequested, booking.Pickup, booking.Dropoff)
			} else {
				pushBody = "Sorry, the driver declined your booking from " + booking.Pickup + " to " + booking.Dropoff + "."
			}
		}
		log.Printf("[FCM] Creating booking status notification & push for passenger %s...", booking.PassengerID.Hex())
		CreateNotification(booking.PassengerID, pushTitle, pushBody, "booking_status", bookingId.Hex(), map[string]string{
			"bookingId": bookingId.Hex(),
			"url":       "/?openTrips=true",
		})
	}()

	c.JSON(http.StatusOK, gin.H{"message": "Booking status updated"})
}

func GetRecentRides(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	role := c.Query("role") // "driver" or "passenger"

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	limit := 5
	if limitStr := c.Query("limit"); limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit >= 0 {
			limit = parsedLimit
		}
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}})

	if limit > 0 {
		opts.SetLimit(int64(limit))
	}

	// Drivers: return their own posted rides from `rides` collection
	if role == "driver" {
		cursor, err := rideCollection.Find(
			dbCtx,
			bson.M{"driverId": userId},
			opts,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch rides"})
			return
		}
		defer cursor.Close(dbCtx)

		var rides []models.Ride
		for cursor.Next(dbCtx) {
			var ride models.Ride
			if err := cursor.Decode(&ride); err == nil {
				rides = append(rides, ride)
			} else {
				log.Printf("[GetRecentRides] Skipping unparseable ride doc: %v", err)
			}
		}

		if rides == nil {
			rides = []models.Ride{}
		}

		// Enrich rides with bookings and takenSeats for driver view
		type EnrichedDriverRide struct {
			models.Ride
			Bookings []models.Booking `json:"bookings"`
		}

		// PERF: Fetch all bookings across all driver rides in ONE query instead of 2*N queries.
		rideIDs := make([]primitive.ObjectID, 0, len(rides))
		for _, r := range rides {
			rideIDs = append(rideIDs, r.ID)
		}
		allBookingsByRide := make(map[primitive.ObjectID][]models.Booking, len(rideIDs))
		activeBookingsByRide := make(map[primitive.ObjectID][]models.Booking, len(rideIDs))
		if len(rideIDs) > 0 {
			bCursor, bErr := bookingCollection.Find(dbCtx, bson.M{"rideId": bson.M{"$in": rideIDs}})
			if bErr == nil && bCursor != nil {
				var allBookings []models.Booking
				if bCursor.All(dbCtx, &allBookings) == nil {
					for _, b := range allBookings {
						allBookingsByRide[b.RideID] = append(allBookingsByRide[b.RideID], b)
						if b.Status == "pending" || b.Status == "accepted" {
							activeBookingsByRide[b.RideID] = append(activeBookingsByRide[b.RideID], b)
						}
					}
				}
				bCursor.Close(dbCtx)
			}
		}

		var enrichedRides []EnrichedDriverRide
		for _, r := range rides {
			r.TakenSeats = computeTakenSeatsFromBookings(r, activeBookingsByRide[r.ID], "", "")
			r.SeatsBooked = len(r.TakenSeats)

			rideBookings := allBookingsByRide[r.ID]
			if rideBookings == nil {
				rideBookings = []models.Booking{}
			}

			enrichedRides = append(enrichedRides, EnrichedDriverRide{
				Ride:     r,
				Bookings: rideBookings,
			})
		}

		if enrichedRides == nil {
			c.JSON(http.StatusOK, []EnrichedDriverRide{})
			return
		}
		c.JSON(http.StatusOK, enrichedRides)
		return
	}

	// Passengers (default): first fetch actual booked rides from `bookings` collection
	pipeline := []bson.M{
		{"$match": bson.M{"passengerId": userId}},
		{"$sort": bson.M{"createdAt": -1}},
		{"$limit": int64(limit)},
		{"$lookup": bson.M{
			"from":         "rides",
			"localField":   "rideId",
			"foreignField": "_id",
			"as":           "rideDetails",
		}},
	}

	bCursor, bErr := bookingCollection.Aggregate(dbCtx, pipeline)
	if bErr == nil {
		var results []struct {
			models.Booking `bson:",inline"`
			RideDetails    []models.Ride `bson:"rideDetails"`
		}
		if err := bCursor.All(dbCtx, &results); err == nil && len(results) > 0 {
			bCursor.Close(dbCtx)
			var passengerRecent []bson.M
			for _, res := range results {
				pickup := res.Booking.Pickup
				dropoff := res.Booking.Dropoff
				date := ""
				depTime := ""
				if len(res.RideDetails) > 0 {
					if pickup == "" {
						pickup = res.RideDetails[0].Pickup
					}
					if dropoff == "" {
						dropoff = res.RideDetails[0].Dropoff
					}
					date = res.RideDetails[0].Date
					depTime = res.RideDetails[0].DepartureTime
				}
				passengerRecent = append(passengerRecent, bson.M{
					"pickup":        pickup,
					"dropoff":       dropoff,
					"date":          date,
					"departureTime": depTime,
					"createdAt":     res.Booking.CreatedAt,
				})
			}
			c.JSON(http.StatusOK, passengerRecent)
			return
		}
		if bCursor != nil {
			bCursor.Close(dbCtx)
		}
	}

	// Fallback for passengers with no bookings: return search history from `recent_routes`
	cursor, err := recentRoutesCollection.Find(
		dbCtx,
		bson.M{"userId": userId},
		opts,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch recent rides"})
		return
	}
	defer cursor.Close(dbCtx)

	var recent []bson.M
	for cursor.Next(dbCtx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err == nil {
			recent = append(recent, doc)
		}
	}

	if recent == nil {
		recent = []bson.M{}
	}

	c.JSON(http.StatusOK, recent)
}

func CompleteRide(c *gin.Context) {
	rideIdHex := c.Param("rideId")
	rideId, err := primitive.ObjectIDFromHex(rideIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ride ID"})
		return
	}

	userId := c.MustGet("userId").(primitive.ObjectID)

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	now := time.Now()
	var ride models.Ride
	err = rideCollection.FindOneAndUpdate(
		dbCtx,
		bson.M{
			"_id":      rideId,
			"driverId": userId,
			"status":   bson.M{"$in": []string{string(models.RideStatusAvailable), string(models.RideStatusOngoing)}},
		},
		bson.M{"$set": bson.M{"status": string(models.RideStatusCompleted), "completedAt": now}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&ride)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			var existing models.Ride
			if findErr := rideCollection.FindOne(dbCtx, bson.M{"_id": rideId}).Decode(&existing); findErr != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found"})
				return
			}
			if existing.DriverID != userId {
				c.JSON(http.StatusForbidden, gin.H{"error": "Only the driver can complete this ride"})
				return
			}
			if existing.Status == string(models.RideStatusCancelled) {
				c.JSON(http.StatusConflict, gin.H{"error": "Cannot complete a cancelled ride"})
				return
			}
			if existing.Status == string(models.RideStatusCompleted) {
				c.JSON(http.StatusConflict, gin.H{"error": "Ride is already completed"})
				return
			}
			c.JSON(http.StatusConflict, gin.H{"error": "Cannot complete a ride in status " + existing.Status})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to complete ride"})
		return
	}

	// Collect accepted passenger IDs BEFORE updating booking statuses
	var completedPassengerIDs []primitive.ObjectID
	{
		collectCtx, collectCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer collectCancel()
		pcursor, pcursorErr := bookingCollection.Find(collectCtx, bson.M{"rideId": rideId, "status": string(models.BookingStatusAccepted)})
		if pcursorErr == nil {
			seen := make(map[primitive.ObjectID]bool)
			for pcursor.Next(collectCtx) {
				var b models.Booking
				if pcursor.Decode(&b) == nil {
					if !seen[b.PassengerID] {
						seen[b.PassengerID] = true
						completedPassengerIDs = append(completedPassengerIDs, b.PassengerID)
					}
				}
			}
			pcursor.Close(collectCtx)
		}
	}

	if _, err := bookingCollection.UpdateMany(
		dbCtx,
		bson.M{"rideId": rideId, "status": string(models.BookingStatusAccepted)},
		bson.M{"$set": bson.M{"status": string(models.BookingStatusCompleted), "completedAt": now}},
	); err != nil {
		log.Printf("[CompleteRide] Failed to mark accepted bookings completed for ride %s: %v", rideId.Hex(), err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ride completed, but failed to update accepted bookings"})
		return
	}

	if _, err := bookingCollection.UpdateMany(
		dbCtx,
		bson.M{"rideId": rideId, "status": string(models.BookingStatusPending)},
		bson.M{"$set": bson.M{"status": string(models.BookingStatusCancelled), "cancelledAt": now}},
	); err != nil {
		log.Printf("[CompleteRide] Failed to cancel pending bookings for ride %s: %v", rideId.Hex(), err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ride completed, but failed to cancel pending bookings"})
		return
	}

	// Notify all accepted passengers that the ride is complete (DB notif + FCM push)
	for _, passengerID := range completedPassengerIDs {
		pid := passengerID // capture loop var
		go CreateNotification(pid, "🏁 Ride Completed!",
			"Your ride from "+ride.Pickup+" to "+ride.Dropoff+" is complete. Tap to rate your experience.",
			"ride_completed", rideId.Hex(), nil)
	}

	c.JSON(http.StatusOK, gin.H{"message": "Ride completed successfully"})
}

func StartRide(c *gin.Context) {
	rideIdStr := c.Param("rideId")
	rideId, err := primitive.ObjectIDFromHex(rideIdStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ride ID"})
		return
	}

	userId := c.MustGet("userId").(primitive.ObjectID)

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	var ride models.Ride
	err = rideCollection.FindOneAndUpdate(
		dbCtx,
		bson.M{
			"_id":      rideId,
			"driverId": userId,
			"status":   string(models.RideStatusAvailable),
		},
		bson.M{"$set": bson.M{"status": string(models.RideStatusOngoing), "startedAt": time.Now()}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&ride)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			var existing models.Ride
			if findErr := rideCollection.FindOne(dbCtx, bson.M{"_id": rideId}).Decode(&existing); findErr != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found"})
				return
			}
			if existing.DriverID != userId {
				c.JSON(http.StatusForbidden, gin.H{"error": "Only the driver can start this ride"})
				return
			}
			if existing.Status == string(models.RideStatusOngoing) {
				// Idempotent: already ongoing, do NOT re-fire push notifications
				c.JSON(http.StatusOK, gin.H{"message": "Ride is already ongoing"})
				return
			}
			c.JSON(http.StatusConflict, gin.H{"error": "Cannot start a ride that is already " + existing.Status})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to start ride"})
		return
	}

	// Notify all accepted passengers that the ride has started (DB notif + FCM push)
	go func(startedRideId primitive.ObjectID, ridePickup, rideDropoff string) {
		notifCtx, notifCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer notifCancel()
		cursor, err := bookingCollection.Find(notifCtx, bson.M{"rideId": startedRideId, "status": string(models.BookingStatusAccepted)})
		if err != nil {
			return
		}
		defer cursor.Close(notifCtx)
		notifiedPassengers := make(map[primitive.ObjectID]bool)
		for cursor.Next(notifCtx) {
			var b models.Booking
			if cursor.Decode(&b) == nil {
				if !notifiedPassengers[b.PassengerID] {
					notifiedPassengers[b.PassengerID] = true
					go CreateNotification(b.PassengerID, "🚀 Your Ride Has Started!",
						"Your ride from "+ridePickup+" to "+rideDropoff+" is now on the way!",
						"ride_started", startedRideId.Hex(), nil)
				}
			}
		}
	}(rideId, ride.Pickup, ride.Dropoff)

	c.JSON(http.StatusOK, gin.H{"message": "Ride started successfully"})
}


func MarkNotificationsViewed(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	// SECURITY: Derive role from database, not from user-controlled query parameter
	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	var userInfo struct {
		Role string `bson:"role"`
	}
	err := usersCollection.FindOne(dbCtx, bson.M{"_id": userId}).Decode(&userInfo)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not found"})
		return
	}
	role := userInfo.Role

	var filter bson.M
	var update bson.M

	if role == "driver" {
		// Driver sees requests for THEIR rides
		cursor, err := rideCollection.Find(dbCtx, bson.M{"driverId": userId})
		if err != nil || cursor == nil {
			c.JSON(http.StatusOK, gin.H{"message": "Notifications marked as viewed"})
			return
		}
		var rideIds []primitive.ObjectID
		for cursor.Next(dbCtx) {
			var r models.Ride
			if cursor.Decode(&r) == nil {
				rideIds = append(rideIds, r.ID)
			}
		}
		cursor.Close(dbCtx)
		if len(rideIds) == 0 {
			c.JSON(http.StatusOK, gin.H{"message": "Notifications marked as viewed"})
			return
		}
		filter = bson.M{"rideId": bson.M{"$in": rideIds}, "viewedByDriver": false}
		update = bson.M{"$set": bson.M{"viewedByDriver": true}}
	} else {
		// Passenger sees THEIR bookings
		filter = bson.M{"passengerId": userId, "viewedByPassenger": false}
		update = bson.M{"$set": bson.M{"viewedByPassenger": true}}
	}

	bookingCollection.UpdateMany(dbCtx, filter, update)
	c.JSON(http.StatusOK, gin.H{"message": "Notifications marked as viewed"})
}

func ToggleBlockSeat(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	rideIdHex := c.Param("rideId")
	rideId, err := primitive.ObjectIDFromHex(rideIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ride ID"})
		return
	}

	var body struct {
		SeatIndex int `json:"seatIndex"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// 1. Fetch the ride and verify ownership and status
	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	var ride models.Ride
	err = rideCollection.FindOne(dbCtx, bson.M{"_id": rideId}).Decode(&ride)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found"})
		return
	}

	if ride.DriverID != userId {
		c.JSON(http.StatusForbidden, gin.H{"error": "Only the driver can block seats"})
		return
	}
	if ride.Status != string(models.RideStatusAvailable) {
		c.JSON(http.StatusConflict, gin.H{"error": "Seats can only be blocked or unblocked on available rides"})
		return
	}

	if body.SeatIndex < 1 || body.SeatIndex > ride.SeatsTotal {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid seat index"})
		return
	}

	isBlocked := false
	for _, s := range ride.ManualBlockedSeats {
		if s == body.SeatIndex {
			isBlocked = true
			break
		}
	}

	var updatedRide models.Ride
	if isBlocked {
		err = rideCollection.FindOneAndUpdate(
			dbCtx,
			bson.M{
				"_id":                rideId,
				"driverId":           userId,
				"status":             string(models.RideStatusAvailable),
				"manualBlockedSeats": body.SeatIndex,
			},
			bson.M{"$pull": bson.M{"manualBlockedSeats": body.SeatIndex}},
			options.FindOneAndUpdate().SetReturnDocument(options.After),
		).Decode(&updatedRide)
		if err != nil {
			if err == mongo.ErrNoDocuments {
				var latest models.Ride
				if findErr := rideCollection.FindOne(dbCtx, bson.M{"_id": rideId}).Decode(&latest); findErr == nil {
					if latest.Status != string(models.RideStatusAvailable) {
						c.JSON(http.StatusConflict, gin.H{"error": "Seats can only be blocked or unblocked on available rides"})
						return
					}
					c.JSON(http.StatusOK, gin.H{
						"message":            "Seat status updated",
						"manualBlockedSeats": latest.ManualBlockedSeats,
					})
					return
				}
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update seat block"})
			return
		}
	} else {
		activeBookingFilter := bson.M{
			"rideId":     rideId,
			"type":       "seat",
			"status":     bson.M{"$in": []string{string(models.BookingStatusPending), string(models.BookingStatusAccepted)}},
			"seatLayout": body.SeatIndex,
		}
		bookedCount, countErr := bookingCollection.CountDocuments(dbCtx, activeBookingFilter)
		if countErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check seat bookings"})
			return
		}
		if bookedCount > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Cannot block seat %d: seat is already booked or pending approval", body.SeatIndex)})
			return
		}

		err = rideCollection.FindOneAndUpdate(
			dbCtx,
			bson.M{
				"_id":                rideId,
				"driverId":           userId,
				"status":             string(models.RideStatusAvailable),
				"manualBlockedSeats": bson.M{"$ne": body.SeatIndex},
			},
			bson.M{"$addToSet": bson.M{"manualBlockedSeats": body.SeatIndex}},
			options.FindOneAndUpdate().SetReturnDocument(options.After),
		).Decode(&updatedRide)
		if err != nil {
			if err == mongo.ErrNoDocuments {
				var latest models.Ride
				if findErr := rideCollection.FindOne(dbCtx, bson.M{"_id": rideId}).Decode(&latest); findErr == nil {
					if latest.Status != string(models.RideStatusAvailable) {
						c.JSON(http.StatusConflict, gin.H{"error": "Seats can only be blocked or unblocked on available rides"})
						return
					}
					c.JSON(http.StatusOK, gin.H{
						"message":            "Seat status updated",
						"manualBlockedSeats": latest.ManualBlockedSeats,
					})
					return
				}
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update seat block"})
			return
		}

		// Re-verify no booking raced in for this seat during $addToSet
		postCount, postErr := bookingCollection.CountDocuments(dbCtx, activeBookingFilter)
		if postErr != nil || postCount > 0 {
			rbCtx, rbCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = rideCollection.UpdateOne(rbCtx, bson.M{"_id": rideId}, bson.M{"$pull": bson.M{"manualBlockedSeats": body.SeatIndex}})
			rbCancel()
			if postErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to confirm seat block"})
				return
			}
			c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Cannot block seat %d: seat was just booked by a passenger", body.SeatIndex)})
			return
		}
	}

	if updatedRide.ManualBlockedSeats == nil {
		updatedRide.ManualBlockedSeats = []int{}
	}
	c.JSON(http.StatusOK, gin.H{
		"message":            "Seat status updated",
		"manualBlockedSeats": updatedRide.ManualBlockedSeats,
	})
}

// retireOpenRides replaces a driver's previously posted "available" rides when they
// post a new one, WITHOUT orphaning passenger bookings:
//   - rides nobody has booked are deleted (same clean-up behaviour as before);
//   - rides with pending/accepted bookings are marked "cancelled" (kept for history),
//     their active bookings are cancelled and the affected passengers are notified.
func retireOpenRides(ctx context.Context, driverID primitive.ObjectID) error {
	cursor, err := rideCollection.Find(ctx, bson.M{"driverId": driverID, "status": string(models.RideStatusAvailable)})
	if err != nil {
		return err
	}
	var oldRides []models.Ride
	if err := cursor.All(ctx, &oldRides); err != nil {
		return err
	}
	if len(oldRides) == 0 {
		return nil
	}

	allIDs := make([]primitive.ObjectID, 0, len(oldRides))
	rideByID := make(map[primitive.ObjectID]models.Ride, len(oldRides))
	for _, r := range oldRides {
		allIDs = append(allIDs, r.ID)
		rideByID[r.ID] = r
	}

	activeStatuses := bson.M{"$in": []string{string(models.BookingStatusPending), string(models.BookingStatusAccepted)}}
	bookedRideIDs, err := bookingCollection.Distinct(ctx, "rideId", bson.M{
		"rideId": bson.M{"$in": allIDs},
		"status": activeStatuses,
	})
	if err != nil {
		return err
	}
	booked := make(map[primitive.ObjectID]bool, len(bookedRideIDs))
	for _, v := range bookedRideIDs {
		if id, ok := v.(primitive.ObjectID); ok {
			booked[id] = true
		}
	}

	var toCancel, toDelete []primitive.ObjectID
	for _, id := range allIDs {
		if booked[id] {
			toCancel = append(toCancel, id)
		} else {
			toDelete = append(toDelete, id)
		}
	}

	now := time.Now()
	if len(toCancel) > 0 {
		if _, err := rideCollection.UpdateMany(ctx,
			bson.M{"_id": bson.M{"$in": toCancel}, "status": string(models.RideStatusAvailable)},
			bson.M{"$set": bson.M{"status": string(models.RideStatusCancelled), "cancelledAt": now, "seatsBooked": 0}},
		); err != nil {
			return err
		}
	}
	if len(toDelete) > 0 {
		if _, err := rideCollection.DeleteMany(ctx,
			bson.M{"_id": bson.M{"$in": toDelete}, "status": string(models.RideStatusAvailable)},
		); err != nil {
			return err
		}
	}

	// Cancel every booking still active on the retired rides. This runs AFTER the
	// rides were retired, so it also catches any booking that raced in during the
	// steps above (BookRide re-checks the ride's status after inserting).
	bCursor, err := bookingCollection.Find(ctx, bson.M{"rideId": bson.M{"$in": allIDs}, "status": activeStatuses})
	if err != nil {
		return err
	}
	var affected []models.Booking
	if err := bCursor.All(ctx, &affected); err != nil {
		return err
	}
	if len(affected) == 0 {
		return nil
	}

	affectedIDs := make([]primitive.ObjectID, 0, len(affected))
	for _, b := range affected {
		affectedIDs = append(affectedIDs, b.ID)
	}
	if _, err := bookingCollection.UpdateMany(ctx,
		bson.M{"_id": bson.M{"$in": affectedIDs}, "status": activeStatuses},
		bson.M{"$set": bson.M{"status": string(models.BookingStatusCancelled), "cancelledAt": now}},
	); err != nil {
		return err
	}

	for _, b := range affected {
		r := rideByID[b.RideID]
		bookingHex := b.ID.Hex()
		go CreateNotification(b.PassengerID, "❌ Ride Cancelled",
			"Your booking from "+b.Pickup+" to "+b.Dropoff+" was cancelled because the driver replaced the ride ("+r.Pickup+" → "+r.Dropoff+"). Please book another ride.",
			"booking_status", bookingHex, map[string]string{
				"bookingId": bookingHex,
				"url":       "/?openTrips=true",
			})
	}
	log.Printf("[CreateRide] Driver %s: deleted %d unbooked ride(s), cancelled %d booked ride(s) and %d booking(s)",
		driverID.Hex(), len(toDelete), len(toCancel), len(affected))
	return nil
}

// verifyNewBooking re-validates a booking AFTER it has been inserted.
//
// Because this check runs after the insert, two concurrent requests for the same
// seat can never both succeed: whichever request checks second is guaranteed to
// see the other's booking. In the rare case both check at the same instant, both
// are rolled back and the passengers simply retry — overbooking is impossible.
// This works on standalone MongoDB too (no replica-set transactions required).
//
// Returns a user-facing conflict message ("" if the booking is valid), or an error
// if verification could not be performed (caller should roll back to be safe).
func verifyNewBooking(ctx context.Context, rideID, bookingID, passengerID primitive.ObjectID, bookingType, pickup, dropoff string, seatsRequested int, seatLayout []int) (string, error) {
	// Re-read the ride: it may have been cancelled/replaced since our first check.
	var ride models.Ride
	if err := rideCollection.FindOne(ctx, bson.M{"_id": rideID}).Decode(&ride); err != nil {
		if err == mongo.ErrNoDocuments {
			return "This ride is no longer available", nil
		}
		return "", err
	}
	if ride.Status != "available" {
		return "This ride is no longer accepting bookings", nil
	}

	if bookingType == "parcel" {
		return "", nil
	}

	// Same passenger double-submitting (e.g. double tap) on the same ride.
	dup, err := bookingCollection.CountDocuments(ctx, bson.M{
		"_id":         bson.M{"$ne": bookingID},
		"rideId":      rideID,
		"passengerId": passengerID,
		"type":        "seat",
		"status":      bson.M{"$in": []string{"pending", "accepted"}},
	})
	if err != nil {
		return "", err
	}
	if dup > 0 {
		return "You already have an active booking for this ride. Check your notifications for the status.", nil
	}

	if _, _, seatErr := prepareBookingSeats(bookingType, seatsRequested, seatLayout, ride.SeatsTotal); seatErr != "" {
		return seatErr, nil
	}

	taken, err := queryTakenSeats(ctx, ride, pickup, dropoff, bookingID)
	if err != nil {
		return "", err
	}
	takenMap := make(map[int]bool, len(taken))
	for _, s := range taken {
		takenMap[s] = true
	}
	for _, s := range seatLayout {
		if takenMap[s] {
			return fmt.Sprintf("Seat %d was just taken by another passenger. Please pick a different seat.", s), nil
		}
	}
	return "", nil
}
