package controllers

import (
	"context"
	"errors"
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
	// Note: bookings.bookingId sparse unique index is managed declaratively in
	// config.CollectionIndexSpecs(), and accepted bookings receive bookingId
	// atomically during acceptance in UpdateBookingStatus (no uncoordinated startup backfill).
}

func BackfillBookingIDs() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cursor, err := bookingCollection.Find(ctx, bson.M{
		"status": bson.M{"$in": []string{string(models.BookingStatusAccepted), string(models.BookingStatusCompleted)}},
		"$or": []bson.M{
			{"bookingId": ""},
			{"bookingId": bson.M{"$exists": false}},
		},
	}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}))
	if err != nil {
		log.Printf("[BACKFILL BID] Query failed: %v", err)
		return
	}
	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		var b models.Booking
		if err := cursor.Decode(&b); err != nil {
			log.Printf("[BACKFILL BID] Decode error: %v", err)
			continue
		}
		if b.BookingID != "" {
			continue
		}
		bid, err := utils.GenerateNextBookingID(ctx)
		if err != nil || bid == "" {
			log.Printf("[BACKFILL BID] GenerateNextBookingID failed for %s: %v", b.ID.Hex(), err)
			return
		}
		res, err := bookingCollection.UpdateOne(
			ctx,
			bson.M{
				"_id": b.ID,
				"$or": []bson.M{
					{"bookingId": ""},
					{"bookingId": bson.M{"$exists": false}},
				},
			},
			bson.M{"$set": bson.M{"bookingId": bid}},
		)
		if err != nil {
			log.Printf("[BACKFILL BID] Failed to update booking %s with %s: %v", b.ID.Hex(), bid, err)
			continue
		}
		if res.ModifiedCount > 0 {
			log.Printf("[BACKFILL BID] Assigned %s to existing booking %s", bid, b.ID.Hex())
		}
	}
	if err := cursor.Err(); err != nil {
		log.Printf("[BACKFILL BID] Cursor iteration error: %v", err)
	}
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

	body.Pickup = strings.TrimSpace(body.Pickup)
	body.Dropoff = strings.TrimSpace(body.Dropoff)
	body.VehicleModel = strings.TrimSpace(body.VehicleModel)
	body.VehicleNumber = strings.TrimSpace(body.VehicleNumber)
	body.DepartureTime = strings.TrimSpace(body.DepartureTime)
	body.Date = strings.TrimSpace(body.Date)

	if body.DepartureTime == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Departure time is required"})
		return
	}
	if _, dateErr := time.Parse("2006-01-02", body.Date); dateErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid date format. Use YYYY-MM-DD"})
		return
	}
	if math.IsNaN(body.PricePerSeat) || math.IsInf(body.PricePerSeat, 0) || body.PricePerSeat < 0 || body.PricePerSeat > 100000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid price per seat"})
		return
	}

	// 10-minute gap rule check: Prevent same driver from posting rides within 10 minutes
	findLastCtx, findLastCancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer findLastCancel()

	var lastRide models.Ride
	err := rideCollection.FindOne(findLastCtx, bson.M{"driverId": userId}, options.FindOne().SetSort(bson.D{{Key: "createdAt", Value: -1}})).Decode(&lastRide)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to verify ride creation cooldown"})
		return
	}
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

	// Fetch driver Name, PhoneNumber, Role, and VerificationStatus
	var driver struct {
		Name               string `bson:"name"`
		PhoneNumber        string `bson:"phone_number"`
		Role               string `bson:"role"`
		VerificationStatus string `bson:"verification_status"`
	}
	err = config.Database.Collection("users").FindOne(findCtx, bson.M{"_id": userId}).Decode(&driver)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Driver account not found"})
			return
		}
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to fetch driver info"})
		return
	}

	if (driver.Role != "driver" && driver.Role != "admin") || driver.VerificationStatus != string(models.VerificationStatusVerified) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Only verified drivers can post rides"})
		return
	}

	ongoingCount, ongoingErr := rideCollection.CountDocuments(findCtx, bson.M{
		"driverId": userId,
		"status":   string(models.RideStatusOngoing),
	})
	if ongoingErr != nil {
		c.JSON(config.DBErrorHTTPStatus(ongoingErr), gin.H{"error": "Failed to verify active rides"})
		return
	}
	if ongoingCount > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Cannot create a new ride while you have an ongoing ride. Complete your current ride first."})
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
			route = append(route, strings.TrimSpace(s))
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

	rideID := primitive.NewObjectID()
	ride := models.Ride{
		ID:              rideID,
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

	txCtx, txCancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer txCancel()

	var retiredNotices []retiredBookingNotice
	if err := config.RunInTransaction(txCtx, func(sc context.Context) error {
		txOngoing, txOngoingErr := rideCollection.CountDocuments(sc, bson.M{
			"driverId": userId,
			"status":   string(models.RideStatusOngoing),
		})
		if txOngoingErr != nil {
			return txOngoingErr
		}
		if txOngoing > 0 {
			return newTxErr(http.StatusConflict, "Cannot create a new ride while you have an ongoing ride. Complete your current ride first.")
		}

		// Soft-cancel driver's previous open rides and their open bookings atomically
		// before inserting the new active ride (never hard-deletes rides).
		notices, retireErr := retireOpenRidesInTx(sc, userId)
		if retireErr != nil {
			return fmt.Errorf("retire previous rides: %w", retireErr)
		}
		retiredNotices = notices

		if _, err := rideCollection.InsertOne(sc, ride); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return newTxErr(http.StatusConflict, "An active ride already exists for this driver. Please try again.")
			}
			return err
		}
		return nil
	}); err != nil {
		respondTxError(c, err, "Failed to create ride")
		return
	}

	sendRetiredRideNotices(retiredNotices)
	notifyAdminsForRetiredBookings(retiredNotices, "driver posted a replacement ride")

	driverLabel := strings.TrimSpace(driver.Name)
	if driverLabel == "" {
		driverLabel = driver.PhoneNumber
	}
	NotifyAdminsAsync(AdminNotificationInput{
		Title:      "New Ride Posted",
		Message:    fmt.Sprintf("%s posted a ride: %s → %s (%s at %s, %d seats @ ₹%.0f).", driverLabel, ride.Pickup, ride.Dropoff, ride.Date, ride.DepartureTime, ride.SeatsTotal, ride.PricePerSeat),
		Type:       models.NotifTypeRideCreated,
		Category:   models.NotifCategoryRides,
		TargetView: models.AdminViewActiveRides,
		RelatedID:  ride.ID.Hex(),
		Metadata: map[string]string{
			"rideId":        ride.ID.Hex(),
			"driverId":      userId.Hex(),
			"actorName":     driver.Name,
			"actorPhone":    driver.PhoneNumber,
			"vehicleName":   ride.VehicleModel,
			"vehicleNumber": ride.VehicleNumber,
			"pickup":        ride.Pickup,
			"dropoff":       ride.Dropoff,
			"date":          ride.Date,
			"departureTime": ride.DepartureTime,
			"status":        string(models.RideStatusAvailable),
		},
	})

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

func getTakenSeats(ctx context.Context, ride models.Ride, searchPickup, searchDropoff string) ([]int, error) {
	return queryTakenSeats(ctx, ride, searchPickup, searchDropoff, primitive.NilObjectID)
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

	var bookings []models.Booking
	if err := decodeCursorAll(ctx, cursor, &bookings); err != nil {
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

	var bookings []models.Booking
	if err := decodeCursorAll(ctx, cursor, &bookings); err != nil {
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
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Search failed"})
		return
	}

	var matchedRides []models.Ride
	if err := decodeCursorAll(dbCtx, cursor, &matchedRides); err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to parse matching rides"})
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
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to check seat availability"})
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
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found"})
			return
		}
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to fetch ride details"})
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

	takenSeats, takenErr := getTakenSeats(dbCtx, ride, segPickup, segDropoff)
	if takenErr != nil {
		c.JSON(config.DBErrorHTTPStatus(takenErr), gin.H{"error": "Failed to verify seat availability"})
		return
	}
	response.TakenSeats = takenSeats
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

var nonDigitRegex = regexp.MustCompile(`[^0-9]`)

func sanitizePlainText(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	if maxLen > 0 && len(s) > maxLen {
		s = s[:maxLen]
	}
	return s
}

func validateAndSanitizeParcelBooking(
	parcelSize, recipientName, contactNumber, dropLocation, notes, photoUrl, price string,
) (cleanSize, cleanRecipient, cleanContact, cleanDropLoc, cleanNotes, cleanPhoto, cleanPrice, errMsg string) {
	cleanSize = sanitizePlainText(parcelSize, 50)
	if cleanSize == "" {
		return "", "", "", "", "", "", "", "Parcel size is required for parcel bookings"
	}

	cleanRecipient = sanitizePlainText(recipientName, 100)
	if len(cleanRecipient) < 2 {
		return "", "", "", "", "", "", "", "Recipient name must be at least 2 characters"
	}

	rawContact := strings.TrimSpace(contactNumber)
	if len(rawContact) > 25 {
		return "", "", "", "", "", "", "", "Contact number is too long"
	}
	digits := nonDigitRegex.ReplaceAllString(rawContact, "")
	if len(digits) < 7 || len(digits) > 15 {
		return "", "", "", "", "", "", "", "Valid recipient contact number is required"
	}
	cleanContact = sanitizePlainText(rawContact, 25)

	cleanDropLoc = sanitizePlainText(dropLocation, 200)
	cleanNotes = sanitizePlainText(notes, 500)

	cleanPhoto = strings.TrimSpace(photoUrl)
	if cleanPhoto != "" {
		lowerPhoto := strings.ToLower(cleanPhoto)
		if !strings.HasPrefix(lowerPhoto, "https://") &&
			!strings.HasPrefix(lowerPhoto, "http://") &&
			!strings.HasPrefix(lowerPhoto, "/uploads/") {
			return "", "", "", "", "", "", "", "Invalid parcel photo URL"
		}
	}

	priceStr := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(price, "₹", ""), ",", ""))
	parsedPrice, parseErr := strconv.ParseFloat(priceStr, 64)
	if parseErr != nil || math.IsNaN(parsedPrice) || math.IsInf(parsedPrice, 0) || parsedPrice <= 0 || parsedPrice > 100000 {
		return "", "", "", "", "", "", "", "Valid positive parcel price is required"
	}
	cleanPrice = fmt.Sprintf("%.0f", parsedPrice)

	return cleanSize, cleanRecipient, cleanContact, cleanDropLoc, cleanNotes, cleanPhoto, cleanPrice, ""
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

	var cleanSize, cleanRecipient, cleanContact, cleanDropLoc, cleanNotes, cleanPhoto, cleanParcelPrice string
	if bookingType == "parcel" {
		var pErr string
		cleanSize, cleanRecipient, cleanContact, cleanDropLoc, cleanNotes, cleanPhoto, cleanParcelPrice, pErr = validateAndSanitizeParcelBooking(
			body.ParcelSize, body.RecipientName, body.ContactNumber, body.DropLocation, body.Notes, body.PhotoUrl, body.Price,
		)
		if pErr != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": pErr})
			return
		}
	}

	// SECURITY: Prevent driver from booking their own ride
	checkCtx, checkCancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer checkCancel()
	var rideForCheck models.Ride
	if err := rideCollection.FindOne(checkCtx, bson.M{"_id": rideId}).Decode(&rideForCheck); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found"})
			return
		}
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to fetch ride"})
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

	bookingPrice := cleanParcelPrice
	if bookingType == "seat" {
		if computedTotalPrice > 0 {
			bookingPrice = fmt.Sprintf("%.0f", computedTotalPrice)
		} else {
			bookingPrice = strings.TrimSpace(body.Price)
		}
	}

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	newBookingID := primitive.NewObjectID()
	now := time.Now()
	booking := models.Booking{
		ID:             newBookingID,
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
		ParcelSize:    cleanSize,
		RecipientName: cleanRecipient,
		ContactNumber: cleanContact,
		DropLocation:  cleanDropLoc,
		Notes:         cleanNotes,
		PhotoUrl:      cleanPhoto,
		Price:         bookingPrice,
		Status:        "pending",
		CreatedAt:     now,
	}

	if err := config.RunInTransaction(dbCtx, func(txCtx context.Context) error {
		// Serialize concurrent bookings against the ride document so snapshot isolation
		// cannot allow two concurrent transactions to reserve the same seat.
		var lockedRide models.Ride
		lockErr := rideCollection.FindOneAndUpdate(
			txCtx,
			bson.M{"_id": rideId, "status": string(models.RideStatusAvailable)},
			bson.M{"$set": bson.M{"updatedAt": now}},
			options.FindOneAndUpdate().SetReturnDocument(options.After),
		).Decode(&lockedRide)
		if lockErr != nil {
			if errors.Is(lockErr, mongo.ErrNoDocuments) {
				var latest models.Ride
				if findErr := rideCollection.FindOne(txCtx, bson.M{"_id": rideId}).Decode(&latest); findErr != nil {
					if errors.Is(findErr, mongo.ErrNoDocuments) {
						return newTxErr(http.StatusNotFound, "Ride not found")
					}
					return findErr
				}
				return newTxErr(http.StatusConflict, "This ride is no longer accepting bookings")
			}
			return lockErr
		}

		if bookingType == "seat" {
			// SECURITY: Prevent same passenger from booking the same ride twice (seat rides only)
			existingCount, countErr := bookingCollection.CountDocuments(txCtx, bson.M{
				"rideId":      rideId,
				"passengerId": passengerId,
				"type":        "seat",
				"status":      bson.M{"$in": []string{"pending", "accepted"}},
			})
			if countErr != nil {
				return countErr
			}
			if existingCount > 0 {
				return newTxErr(http.StatusConflict, "You already have an active booking for this ride. Check your notifications for the status.")
			}

			// Verify that requested seats are not already taken or pending approval
			takenSeats, takenErr := getTakenSeats(txCtx, lockedRide, effectivePickup, effectiveDropoff)
			if takenErr != nil {
				return takenErr
			}
			takenMap := make(map[int]bool, len(takenSeats))
			for _, seat := range takenSeats {
				takenMap[seat] = true
			}
			for _, requestedSeat := range seatLayout {
				if takenMap[requestedSeat] {
					return newTxErr(http.StatusBadRequest, fmt.Sprintf("Seat %d is already taken or pending approval", requestedSeat))
				}
			}
		} else {
			// Double-submit guard for identical active parcel request
			dupParcelCount, dupErr := bookingCollection.CountDocuments(txCtx, bson.M{
				"rideId":        rideId,
				"passengerId":   passengerId,
				"type":          "parcel",
				"status":        bson.M{"$in": []string{"pending", "accepted"}},
				"recipientName": cleanRecipient,
				"contactNumber": cleanContact,
				"parcelSize":    cleanSize,
			})
			if dupErr != nil {
				return dupErr
			}
			if dupParcelCount > 0 {
				return newTxErr(http.StatusConflict, "An identical active parcel booking already exists for this ride")
			}
		}

		if _, err := bookingCollection.InsertOne(txCtx, booking); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return newTxErr(http.StatusConflict, "You already have an active booking for this ride. Check your notifications for the status.")
			}
			return err
		}

		// In non-transactional standalone dev mode, verify post-insert and compensate on conflict.
		if !config.InTransaction(txCtx) {
			conflictMsg, verifyErr := verifyNewBooking(txCtx, rideId, newBookingID, passengerId, bookingType, effectivePickup, effectiveDropoff, seatsCount, seatLayout)
			if verifyErr != nil || conflictMsg != "" {
				rbCtx, rbCancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, rbErr := bookingCollection.DeleteOne(rbCtx, bson.M{"_id": newBookingID})
				rbCancel()
				if rbErr != nil {
					log.Printf("[CRITICAL ROLLBACK FAILURE] BookRide failed to delete conflicting booking %s: %v", newBookingID.Hex(), rbErr)
					return fmt.Errorf("booking verification rollback failed: %w", rbErr)
				}
				if verifyErr != nil {
					return verifyErr
				}
				return newTxErr(http.StatusConflict, conflictMsg)
			}
		}
		return nil
	}); err != nil {
		respondTxError(c, err, "Failed to confirm booking. Please try again.")
		return
	}

	msg := "Booking request sent to driver"
	if bookingType == "parcel" {
		msg = "Parcel pickup request sent to driver"
	}

	// Notify the driver and admins via DB + FCM push (fire-and-forget)
	go func() {
		fcmCtx, fcmCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer fcmCancel()

		var passenger struct {
			Name        string `bson:"name"`
			PhoneNumber string `bson:"phone_number"`
		}
		if usersCollection != nil {
			_ = usersCollection.FindOne(fcmCtx, bson.M{"_id": passengerId}).Decode(&passenger)
		}

		actorLabel := strings.TrimSpace(passenger.Name)
		if actorLabel == "" {
			actorLabel = strings.TrimSpace(passenger.PhoneNumber)
		}
		if actorLabel == "" {
			actorLabel = "A user"
		}

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
				CreateNotification(ride.DriverID, title, body, "booking_request", newBookingID.Hex(), map[string]string{
					"bookingId": newBookingID.Hex(),
					"rideId":    rideId.Hex(),
					"pickup":    booking.Pickup,
					"dropoff":   booking.Dropoff,
					"url":       "/?openRequests=true",
				})
			}
		} else {
			log.Printf("[FCM] Ride lookup error for ride %s: %v", rideId.Hex(), err)
			ride = rideForCheck
		}

		if bookingType == "parcel" {
			NotifyAdminsAsync(AdminNotificationInput{
				Title:      "New Parcel Booking",
				Message:    fmt.Sprintf("%s booked a %s parcel (%s → %s) for %s.", actorLabel, cleanSize, booking.Pickup, booking.Dropoff, cleanRecipient),
				Type:       models.NotifTypeParcelCreated,
				Category:   models.NotifCategoryParcels,
				TargetView: models.AdminViewParcels,
				RelatedID:  newBookingID.Hex(),
				Metadata: map[string]string{
					"bookingId":     newBookingID.Hex(),
					"rideId":        rideId.Hex(),
					"driverId":      ride.DriverID.Hex(),
					"driverName":    ride.DriverName,
					"userId":        passengerId.Hex(),
					"actorName":     passenger.Name,
					"actorPhone":    passenger.PhoneNumber,
					"pickup":        booking.Pickup,
					"dropoff":       booking.Dropoff,
					"status":        string(models.BookingStatusPending),
					"bookingType":   "parcel",
					"parcelSize":    cleanSize,
					"recipientName": cleanRecipient,
					"contactNumber": cleanContact,
				},
			})
		} else {
			NotifyAdminsAsync(AdminNotificationInput{
				Title:      "New Seat Booking",
				Message:    fmt.Sprintf("%s requested %d seat(s): %s → %s.", actorLabel, seatsCount, booking.Pickup, booking.Dropoff),
				Type:       models.NotifTypeBookingCreated,
				Category:   models.NotifCategoryBookings,
				TargetView: models.AdminViewBookings,
				RelatedID:  newBookingID.Hex(),
				Metadata: map[string]string{
					"bookingId":      newBookingID.Hex(),
					"rideId":         rideId.Hex(),
					"driverId":       ride.DriverID.Hex(),
					"driverName":     ride.DriverName,
					"userId":         passengerId.Hex(),
					"actorName":      passenger.Name,
					"actorPhone":     passenger.PhoneNumber,
					"pickup":         booking.Pickup,
					"dropoff":        booking.Dropoff,
					"status":         string(models.BookingStatusPending),
					"bookingType":    "seat",
					"seatsRequested": fmt.Sprintf("%d", seatsCount),
				},
			})
		}
	}()

	c.JSON(http.StatusCreated, gin.H{"message": msg, "bookingId": newBookingID})
}

func GetDriverRequests(c *gin.Context) {
	driverId := c.MustGet("userId").(primitive.ObjectID)
	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	// Sub-query to get all rides created by this driver
	cursor, err := rideCollection.Find(dbCtx, bson.M{"driverId": driverId})
	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to fetch driver rides"})
		return
	}

	var rides []models.Ride
	if err := decodeCursorAll(dbCtx, cursor, &rides); err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to parse rides"})
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
	bCursor, err := bookingCollection.Find(dbCtx, bson.M{"rideId": bson.M{"$in": rideIds}}, opts)
	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to fetch requests"})
		return
	}

	var bookings []models.Booking
	if err := decodeCursorAll(dbCtx, bCursor, &bookings); err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to parse bookings"})
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
		if pErr != nil {
			c.JSON(config.DBErrorHTTPStatus(pErr), gin.H{"error": "Failed to fetch passenger details"})
			return
		}
		var passengers []models.User
		if err := decodeCursorAll(dbCtx, pCursor, &passengers); err != nil {
			c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to parse passenger details"})
			return
		}
		for _, p := range passengers {
			passengerMap[p.ID] = p
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
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to fetch bookings"})
		return
	}

	type aggregatedResult struct {
		models.Booking `bson:",inline"`
		RideDetails    []models.Ride `bson:"rideDetails"`
	}

	var results []aggregatedResult
	if err := decodeCursorAll(dbCtx, cursor, &results); err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to parse bookings"})
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
		c.JSON(config.DBErrorHTTPStatus(bErr), gin.H{"error": "Failed to fetch ride seat availability"})
		return
	}

	driverPhones := make(map[primitive.ObjectID]string, len(driverIDs))
	if len(driverIDs) > 0 {
		dCursor, dErr := usersCollection.Find(dbCtx,
			bson.M{"_id": bson.M{"$in": driverIDs}},
			options.Find().SetProjection(bson.M{"phone_number": 1}),
		)
		if dErr != nil {
			c.JSON(config.DBErrorHTTPStatus(dErr), gin.H{"error": "Failed to fetch driver contact details"})
			return
		}
		var drivers []struct {
			ID          primitive.ObjectID `bson:"_id"`
			PhoneNumber string             `bson:"phone_number"`
		}
		if err := decodeCursorAll(dbCtx, dCursor, &drivers); err != nil {
			c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to decode driver contact details"})
			return
		}
		for _, d := range drivers {
			driverPhones[d.ID] = d.PhoneNumber
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
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to save recent ride"})
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

	var booking models.Booking
	var ride models.Ride
	var generatedBID string

	if err := config.RunInTransaction(dbCtx, func(txCtx context.Context) error {
		// Get the booking to find the rideId and seatsRequested
		if err := bookingCollection.FindOne(txCtx, bson.M{"_id": bookingId}).Decode(&booking); err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				return newTxErr(http.StatusNotFound, "Booking not found")
			}
			return err
		}

		// Explicit booking FSM transition guard
		if !models.CanTransitionBookingStatus(models.BookingStatus(booking.Status), models.BookingStatus(body.Status)) {
			return newTxErr(http.StatusConflict, "Booking status already updated")
		}

		// Authorization check IDOR & ride status check
		ride = models.Ride{}
		if err := rideCollection.FindOne(txCtx, bson.M{"_id": booking.RideID}).Decode(&ride); err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				return newTxErr(http.StatusForbidden, "Not authorized")
			}
			return err
		}
		if ride.DriverID != driverId {
			return newTxErr(http.StatusForbidden, "Not authorized")
		}
		if ride.Status != string(models.RideStatusAvailable) {
			return newTxErr(http.StatusConflict, "Cannot update a booking for a ride that is "+ride.Status)
		}

		// Serialize concurrent mutations on this ride inside multi-document transactions.
		now := time.Now()
		rideCheckRes, rideCheckErr := rideCollection.UpdateOne(
			txCtx,
			bson.M{"_id": booking.RideID, "driverId": driverId, "status": string(models.RideStatusAvailable)},
			bson.M{"$set": bson.M{"updatedAt": now}},
		)
		if rideCheckErr != nil {
			return rideCheckErr
		}
		if rideCheckRes.MatchedCount == 0 {
			return newTxErr(http.StatusConflict, "Ride is no longer available")
		}

		if body.Status == string(models.BookingStatusAccepted) {
			// Fail closed on driver lookup error or unverified status
			var driver struct {
				VerificationStatus string `bson:"verification_status"`
			}
			if err := config.Database.Collection("users").FindOne(txCtx, bson.M{"_id": driverId}).Decode(&driver); err != nil {
				if errors.Is(err, mongo.ErrNoDocuments) {
					return newTxErr(http.StatusForbidden, "Only verified drivers can accept rides")
				}
				return err
			}
			if driver.VerificationStatus != string(models.VerificationStatusVerified) {
				return newTxErr(http.StatusForbidden, "Only verified drivers can accept rides")
			}

			// Segment-aware seat & manual-block conflict check
			if booking.Type == "seat" {
				acceptedCursor, accErr := bookingCollection.Find(txCtx, bson.M{
					"rideId": booking.RideID,
					"type":   "seat",
					"status": string(models.BookingStatusAccepted),
				})
				if accErr != nil {
					return accErr
				}
				var acceptedBookings []models.Booking
				decErr := acceptedCursor.All(txCtx, &acceptedBookings)
				curErr := acceptedCursor.Err()
				acceptedCursor.Close(txCtx)
				if decErr != nil {
					return decErr
				}
				if curErr != nil {
					return curErr
				}

				if conflict := checkAcceptBookingSeatConflict(ride, booking, acceptedBookings); conflict != "" {
					return newTxErr(http.StatusConflict, conflict)
				}
			}

			// Generate unique Booking ID (BID) outside tx lock (reused across tx retries)
			// so status and bookingId persist atomically together.
			if booking.BookingID == "" && generatedBID == "" {
				bidCtx, bidCancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
				newBID, errBID := utils.GenerateNextBookingID(bidCtx)
				bidCancel()
				if errBID != nil || newBID == "" {
					return fmt.Errorf("generate bookingId: %w", errBID)
				}
				generatedBID = newBID
			}
		}

		bookingUpdateFields := bson.M{"status": body.Status}
		if body.Status == string(models.BookingStatusAccepted) && booking.BookingID == "" && generatedBID != "" {
			bookingUpdateFields["bookingId"] = generatedBID
		}

		// CONCURRENCY: Only transition the booking if it is STILL pending.
		updateRes, err := bookingCollection.UpdateOne(
			txCtx,
			bson.M{"_id": bookingId, "status": string(models.BookingStatusPending)},
			bson.M{"$set": bookingUpdateFields},
		)
		if err != nil {
			return err
		}
		if updateRes.MatchedCount == 0 {
			return newTxErr(http.StatusConflict, "Booking status already updated")
		}

		if body.Status == string(models.BookingStatusAccepted) && booking.BookingID == "" {
			booking.BookingID = generatedBID
		}

		if body.Status == string(models.BookingStatusAccepted) {
			compensateStandalone := func() error {
				if config.InTransaction(txCtx) {
					return nil
				}
				rbCtx, rbCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer rbCancel()
				rbUpdate := bson.M{"$set": bson.M{"status": string(models.BookingStatusPending)}}
				if generatedBID != "" {
					rbUpdate["$unset"] = bson.M{"bookingId": ""}
				}
				if _, rbErr := bookingCollection.UpdateOne(
					rbCtx,
					bson.M{"_id": bookingId, "status": string(models.BookingStatusAccepted)},
					rbUpdate,
				); rbErr != nil {
					log.Printf("[CRITICAL ROLLBACK FAILURE] UpdateBookingStatus failed to revert booking %s: %v", bookingId.Hex(), rbErr)
					return fmt.Errorf("booking rollback failed: %w", rbErr)
				}
				return nil
			}

			// Re-verify ride status inside tx/standalone fallback
			var latestRide models.Ride
			if err := rideCollection.FindOne(txCtx, bson.M{"_id": booking.RideID}).Decode(&latestRide); err != nil || latestRide.Status != string(models.RideStatusAvailable) {
				if rbErr := compensateStandalone(); rbErr != nil {
					return rbErr
				}
				if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
					return err
				}
				return newTxErr(http.StatusConflict, "Ride is no longer available")
			}

			if booking.Type == "seat" {
				acceptedCursor, accErr := bookingCollection.Find(txCtx, bson.M{
					"rideId": booking.RideID,
					"type":   "seat",
					"status": string(models.BookingStatusAccepted),
				})
				if accErr != nil {
					if rbErr := compensateStandalone(); rbErr != nil {
						return rbErr
					}
					return accErr
				}
				var acceptedBookings []models.Booking
				decErr := acceptedCursor.All(txCtx, &acceptedBookings)
				curErr := acceptedCursor.Err()
				acceptedCursor.Close(txCtx)
				if decErr != nil {
					if rbErr := compensateStandalone(); rbErr != nil {
						return rbErr
					}
					return decErr
				}
				if curErr != nil {
					if rbErr := compensateStandalone(); rbErr != nil {
						return rbErr
					}
					return curErr
				}

				if conflict := checkAcceptBookingSeatConflict(latestRide, booking, acceptedBookings); conflict != "" {
					if rbErr := compensateStandalone(); rbErr != nil {
						return rbErr
					}
					return newTxErr(http.StatusConflict, conflict)
				}

				distinctSeats := countDistinctAcceptedSeats(acceptedBookings)
				seatUpdRes, seatUpdErr := rideCollection.UpdateOne(
					txCtx,
					bson.M{"_id": booking.RideID, "status": string(models.RideStatusAvailable)},
					bson.M{"$set": bson.M{"seatsBooked": distinctSeats}},
				)
				if seatUpdErr != nil {
					if rbErr := compensateStandalone(); rbErr != nil {
						return rbErr
					}
					return seatUpdErr
				}
				if seatUpdRes.MatchedCount == 0 {
					if rbErr := compensateStandalone(); rbErr != nil {
						return rbErr
					}
					return newTxErr(http.StatusConflict, "Ride is no longer available")
				}
			}
		}

		return nil
	}); err != nil {
		respondTxError(c, err, "Failed to update booking")
		return
	}

	// Capture status and immutable snapshots before goroutines so closures don't race
	capturedStatus := body.Status
	capturedBooking := booking
	capturedBooking.Status = capturedStatus
	if capturedBooking.BookingID == "" && generatedBID != "" {
		capturedBooking.BookingID = generatedBID
	}
	capturedRide := ride

	// Notify passenger of decision via DB notification + FCM Push (after commit)
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

	go func(b models.Booking, r models.Ride, status string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var passenger models.User
		if usersCol := getRideUsersCollection(); usersCol != nil && !b.PassengerID.IsZero() {
			_ = usersCol.FindOne(ctx, bson.M{"_id": b.PassengerID}, options.FindOne().SetProjection(bson.M{"name": 1, "phone_number": 1})).Decode(&passenger)
		}
		notifyAdminForBookingLifecycle(b, r, status, "", passenger.Name, passenger.PhoneNumber)
	}(capturedBooking, capturedRide, capturedStatus)

	c.JSON(http.StatusOK, gin.H{"message": "Booking status updated", "bookingId": booking.BookingID})
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
			c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to fetch rides"})
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
		if err := cursor.Err(); err != nil {
			c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to read rides"})
			return
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
		acceptedBookingsByRide := make(map[primitive.ObjectID][]models.Booking, len(rideIDs))
		if len(rideIDs) > 0 {
			bCursor, bErr := bookingCollection.Find(dbCtx, bson.M{"rideId": bson.M{"$in": rideIDs}})
			if bErr != nil {
				c.JSON(config.DBErrorHTTPStatus(bErr), gin.H{"error": "Failed to fetch bookings for rides"})
				return
			}
			var allBookings []models.Booking
			if err := decodeCursorAll(dbCtx, bCursor, &allBookings); err != nil {
				c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to decode bookings for rides"})
				return
			}
			for _, b := range allBookings {
				allBookingsByRide[b.RideID] = append(allBookingsByRide[b.RideID], b)
				if b.Status == string(models.BookingStatusPending) || b.Status == string(models.BookingStatusAccepted) {
					activeBookingsByRide[b.RideID] = append(activeBookingsByRide[b.RideID], b)
				}
				if b.Status == string(models.BookingStatusAccepted) || b.Status == string(models.BookingStatusCompleted) {
					acceptedBookingsByRide[b.RideID] = append(acceptedBookingsByRide[b.RideID], b)
				}
			}
		}

		var enrichedRides []EnrichedDriverRide
		for _, r := range rides {
			r.TakenSeats = computeTakenSeatsFromBookings(r, activeBookingsByRide[r.ID], "", "")
			r.SeatsBooked = countDistinctAcceptedSeats(acceptedBookingsByRide[r.ID])

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
	if bErr != nil {
		c.JSON(config.DBErrorHTTPStatus(bErr), gin.H{"error": "Failed to fetch recent rides"})
		return
	}
	if bCursor != nil {
		var results []struct {
			models.Booking `bson:",inline"`
			RideDetails    []models.Ride `bson:"rideDetails"`
		}
		if err := decodeCursorAll(dbCtx, bCursor, &results); err != nil {
			c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to parse recent rides"})
			return
		}
		if len(results) > 0 {
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
	}

	// Fallback for passengers with no bookings: return search history from `recent_routes`
	cursor, err := recentRoutesCollection.Find(
		dbCtx,
		bson.M{"userId": userId},
		opts,
	)
	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to fetch recent rides"})
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
	if err := cursor.Err(); err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to read recent rides"})
		return
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

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	var ride models.Ride
	var completedPassengerIDs []primitive.ObjectID
	var acceptedBookings []models.Booking
	var pendingBookings []models.Booking

	if err := config.RunInTransaction(dbCtx, func(txCtx context.Context) error {
		now := time.Now()
		completedPassengerIDs = nil
		acceptedBookings = nil
		pendingBookings = nil

		err := rideCollection.FindOneAndUpdate(
			txCtx,
			bson.M{
				"_id":      rideId,
				"driverId": userId,
				"status":   bson.M{"$in": []string{string(models.RideStatusAvailable), string(models.RideStatusOngoing)}},
			},
			bson.M{"$set": bson.M{"status": string(models.RideStatusCompleted), "completedAt": now}},
			options.FindOneAndUpdate().SetReturnDocument(options.After),
		).Decode(&ride)
		if err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				var existing models.Ride
				if findErr := rideCollection.FindOne(txCtx, bson.M{"_id": rideId}).Decode(&existing); findErr != nil {
					if errors.Is(findErr, mongo.ErrNoDocuments) {
						return newTxErr(http.StatusNotFound, "Ride not found")
					}
					return findErr
				}
				if existing.DriverID != userId {
					return newTxErr(http.StatusForbidden, "Only the driver can complete this ride")
				}
				if existing.Status == string(models.RideStatusCancelled) {
					return newTxErr(http.StatusConflict, "Cannot complete a cancelled ride")
				}
				if existing.Status == string(models.RideStatusCompleted) {
					// Idempotent retry reconciliation: if a previous attempt crashed/aborted
					// after marking the ride completed in non-tx mode, reconcile lingering open bookings.
					lingeringCount, cntErr := bookingCollection.CountDocuments(txCtx, bson.M{
						"rideId": rideId,
						"status": bson.M{"$in": []string{string(models.BookingStatusAccepted), string(models.BookingStatusPending)}},
					})
					if cntErr != nil {
						return cntErr
					}
					if lingeringCount == 0 {
						return newTxErr(http.StatusConflict, "Ride is already completed")
					}
					ride = existing
				} else {
					return newTxErr(http.StatusConflict, "Cannot complete a ride in status "+existing.Status)
				}
			} else {
				return err
			}
		}

		// Collect accepted & pending bookings BEFORE updating booking statuses
		pcursor, pcursorErr := bookingCollection.Find(
			txCtx,
			bson.M{"rideId": rideId, "status": bson.M{"$in": []string{
				string(models.BookingStatusAccepted),
				string(models.BookingStatusPending),
			}}},
		)
		if pcursorErr != nil {
			return fmt.Errorf("fetch bookings for completion: %w", pcursorErr)
		}
		var rideBookings []models.Booking
		decErr := pcursor.All(txCtx, &rideBookings)
		curErr := pcursor.Err()
		pcursor.Close(txCtx)
		if decErr != nil {
			return fmt.Errorf("decode bookings for completion: %w", decErr)
		}
		if curErr != nil {
			return fmt.Errorf("iterate bookings for completion: %w", curErr)
		}

		seen := make(map[primitive.ObjectID]bool, len(rideBookings))
		for _, row := range rideBookings {
			switch row.Status {
			case string(models.BookingStatusAccepted):
				acceptedBookings = append(acceptedBookings, row)
				if !row.PassengerID.IsZero() && !seen[row.PassengerID] {
					seen[row.PassengerID] = true
					completedPassengerIDs = append(completedPassengerIDs, row.PassengerID)
				}
			case string(models.BookingStatusPending):
				pendingBookings = append(pendingBookings, row)
			}
		}

		if _, err := bookingCollection.UpdateMany(
			txCtx,
			bson.M{"rideId": rideId, "status": string(models.BookingStatusAccepted)},
			bson.M{"$set": bson.M{"status": string(models.BookingStatusCompleted), "completedAt": now}},
		); err != nil {
			return fmt.Errorf("mark accepted bookings completed: %w", err)
		}

		if _, err := bookingCollection.UpdateMany(
			txCtx,
			bson.M{"rideId": rideId, "status": string(models.BookingStatusPending)},
			bson.M{"$set": bson.M{"status": string(models.BookingStatusCancelled), "cancelledAt": now}},
		); err != nil {
			return fmt.Errorf("cancel pending bookings: %w", err)
		}

		return nil
	}); err != nil {
		respondTxError(c, err, "Failed to complete ride")
		return
	}

	// Notify all accepted passengers AFTER transaction commits (DB notif + FCM push)
	for _, passengerID := range completedPassengerIDs {
		pid := passengerID // capture loop var
		go CreateNotification(pid, "🏁 Ride Completed!",
			"Your ride from "+ride.Pickup+" to "+ride.Dropoff+" is complete. Tap to rate your experience.",
			"ride_completed", rideId.Hex(), nil)
	}

	// Notify admins of completed ride and transitioned bookings/parcels
	NotifyAdminsAsync(AdminNotificationInput{
		Title:      "🏁 Ride Completed",
		Message:    fmt.Sprintf("Driver %s completed ride %s → %s (%d completed bookings, %d pending cancelled).", displayDriverLabel(ride.DriverName), ride.Pickup, ride.Dropoff, len(acceptedBookings), len(pendingBookings)),
		Type:       models.NotifTypeRideCompleted,
		Category:   models.NotifCategoryRides,
		TargetView: models.AdminViewCompletedRides,
		RelatedID:  rideId.Hex(),
		Metadata: map[string]string{
			"rideId":        rideId.Hex(),
			"driverId":      ride.DriverID.Hex(),
			"actorName":     ride.DriverName,
			"vehicleName":   ride.VehicleModel,
			"vehicleNumber": ride.VehicleNumber,
			"pickup":        ride.Pickup,
			"dropoff":       ride.Dropoff,
			"status":        string(models.RideStatusCompleted),
		},
	})
	if len(acceptedBookings) > 0 || len(pendingBookings) > 0 {
		go func(r models.Ride, accepted, pending []models.Booking) {
			passengerIDs := make([]primitive.ObjectID, 0, len(accepted)+len(pending))
			for _, b := range accepted {
				passengerIDs = append(passengerIDs, b.PassengerID)
			}
			for _, b := range pending {
				passengerIDs = append(passengerIDs, b.PassengerID)
			}
			passengers := lookupPassengerSummaries(passengerIDs)
			for _, ab := range accepted {
				ab.Status = string(models.BookingStatusCompleted)
				p := passengers[ab.PassengerID]
				notifyAdminForBookingLifecycle(ab, r, string(models.BookingStatusCompleted), "", p.Name, p.PhoneNumber)
			}
			for _, pb := range pending {
				pb.Status = string(models.BookingStatusCancelled)
				p := passengers[pb.PassengerID]
				notifyAdminForBookingLifecycle(pb, r, string(models.BookingStatusCancelled), "Ride completed before confirmation", p.Name, p.PhoneNumber)
			}
		}(ride, acceptedBookings, pendingBookings)
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
		if errors.Is(err, mongo.ErrNoDocuments) {
			var existing models.Ride
			if findErr := rideCollection.FindOne(dbCtx, bson.M{"_id": rideId}).Decode(&existing); findErr != nil {
				if errors.Is(findErr, mongo.ErrNoDocuments) {
					c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found"})
					return
				}
				c.JSON(config.DBErrorHTTPStatus(findErr), gin.H{"error": "Failed to verify ride"})
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
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to start ride"})
		return
	}

	// Notify all accepted passengers that the ride has started (DB notif + FCM push)
	go func(startedRideId primitive.ObjectID, ridePickup, rideDropoff string) {
		notifCtx, notifCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer notifCancel()
		cursor, err := bookingCollection.Find(notifCtx, bson.M{"rideId": startedRideId, "status": string(models.BookingStatusAccepted)})
		if err != nil {
			log.Printf("[StartRide] Failed to query passengers for ride %s: %v", startedRideId.Hex(), err)
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
		if err := cursor.Err(); err != nil {
			log.Printf("[StartRide] Cursor error notifying passengers for ride %s: %v", startedRideId.Hex(), err)
		}
	}(rideId, ride.Pickup, ride.Dropoff)

	NotifyAdminsAsync(AdminNotificationInput{
		Title:      "🚀 Ride Started",
		Message:    fmt.Sprintf("Driver %s started ride %s → %s.", displayDriverLabel(ride.DriverName), ride.Pickup, ride.Dropoff),
		Type:       models.NotifTypeRideStarted,
		Category:   models.NotifCategoryRides,
		TargetView: models.AdminViewActiveRides,
		RelatedID:  rideId.Hex(),
		Metadata: map[string]string{
			"rideId":        rideId.Hex(),
			"driverId":      ride.DriverID.Hex(),
			"actorName":     ride.DriverName,
			"vehicleName":   ride.VehicleModel,
			"vehicleNumber": ride.VehicleNumber,
			"pickup":        ride.Pickup,
			"dropoff":       ride.Dropoff,
			"status":        string(models.RideStatusOngoing),
		},
	})

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
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "User not found"})
			return
		}
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to load user profile"})
		return
	}
	role := userInfo.Role

	var filter bson.M
	var update bson.M

	if role == "driver" {
		// Driver sees requests for THEIR rides
		cursor, err := rideCollection.Find(dbCtx, bson.M{"driverId": userId}, options.Find().SetProjection(bson.M{"_id": 1}))
		if err != nil {
			c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to fetch driver rides"})
			return
		}
		defer cursor.Close(dbCtx)
		var rideIds []primitive.ObjectID
		for cursor.Next(dbCtx) {
			var r models.Ride
			if cursor.Decode(&r) == nil {
				rideIds = append(rideIds, r.ID)
			}
		}
		if err := cursor.Err(); err != nil {
			c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to read driver rides"})
			return
		}
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

	if _, err := bookingCollection.UpdateMany(dbCtx, filter, update); err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to mark notifications as viewed"})
		return
	}
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

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	var updatedRide models.Ride

	if err := config.RunInTransaction(dbCtx, func(txCtx context.Context) error {
		var ride models.Ride
		if err := rideCollection.FindOne(txCtx, bson.M{"_id": rideId}).Decode(&ride); err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				return newTxErr(http.StatusNotFound, "Ride not found")
			}
			return err
		}

		if ride.DriverID != userId {
			return newTxErr(http.StatusForbidden, "Only the driver can block seats")
		}
		if ride.Status != string(models.RideStatusAvailable) {
			return newTxErr(http.StatusConflict, "Seats can only be blocked or unblocked on available rides")
		}
		if body.SeatIndex < 1 || body.SeatIndex > ride.SeatsTotal {
			return newTxErr(http.StatusBadRequest, "Invalid seat index")
		}

		hasSeat := func(list []int, target int) bool {
			for _, s := range list {
				if s == target {
					return true
				}
			}
			return false
		}

		isBlocked := hasSeat(ride.ManualBlockedSeats, body.SeatIndex)

		if isBlocked {
			err := rideCollection.FindOneAndUpdate(
				txCtx,
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
				if errors.Is(err, mongo.ErrNoDocuments) {
					var latest models.Ride
					if findErr := rideCollection.FindOne(txCtx, bson.M{"_id": rideId}).Decode(&latest); findErr != nil {
						if errors.Is(findErr, mongo.ErrNoDocuments) {
							return newTxErr(http.StatusNotFound, "Ride not found")
						}
						return findErr
					}
					if latest.Status != string(models.RideStatusAvailable) {
						return newTxErr(http.StatusConflict, "Seats can only be blocked or unblocked on available rides")
					}
					if !hasSeat(latest.ManualBlockedSeats, body.SeatIndex) {
						// Already unblocked concurrently; safe idempotent return.
						updatedRide = latest
						return nil
					}
					return newTxErr(http.StatusConflict, "Seat block state changed concurrently. Please retry.")
				}
				return err
			}
			return nil
		}

		activeBookingFilter := bson.M{
			"rideId":     rideId,
			"type":       "seat",
			"status":     bson.M{"$in": []string{string(models.BookingStatusPending), string(models.BookingStatusAccepted)}},
			"seatLayout": body.SeatIndex,
		}
		bookedCount, countErr := bookingCollection.CountDocuments(txCtx, activeBookingFilter)
		if countErr != nil {
			return countErr
		}
		if bookedCount > 0 {
			return newTxErr(http.StatusConflict, fmt.Sprintf("Cannot block seat %d: seat is already booked or pending approval", body.SeatIndex))
		}

		err := rideCollection.FindOneAndUpdate(
			txCtx,
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
			if errors.Is(err, mongo.ErrNoDocuments) {
				var latest models.Ride
				if findErr := rideCollection.FindOne(txCtx, bson.M{"_id": rideId}).Decode(&latest); findErr != nil {
					if errors.Is(findErr, mongo.ErrNoDocuments) {
						return newTxErr(http.StatusNotFound, "Ride not found")
					}
					return findErr
				}
				if latest.Status != string(models.RideStatusAvailable) {
					return newTxErr(http.StatusConflict, "Seats can only be blocked or unblocked on available rides")
				}
				if hasSeat(latest.ManualBlockedSeats, body.SeatIndex) {
					updatedRide = latest
					return nil
				}
				return newTxErr(http.StatusConflict, "Seat block state changed concurrently. Please retry.")
			}
			return err
		}

		// Re-verify no booking raced in for this seat during $addToSet
		postCount, postErr := bookingCollection.CountDocuments(txCtx, activeBookingFilter)
		if postErr != nil || postCount > 0 {
			if !config.InTransaction(txCtx) {
				rbCtx, rbCancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, rbErr := rideCollection.UpdateOne(rbCtx, bson.M{"_id": rideId}, bson.M{"$pull": bson.M{"manualBlockedSeats": body.SeatIndex}})
				rbCancel()
				if rbErr != nil {
					log.Printf("[CRITICAL ROLLBACK FAILURE] ToggleBlockSeat failed to revert block on ride %s seat %d: %v", rideId.Hex(), body.SeatIndex, rbErr)
					return fmt.Errorf("seat block rollback failed: %w", rbErr)
				}
			}
			if postErr != nil {
				return postErr
			}
			return newTxErr(http.StatusConflict, fmt.Sprintf("Cannot block seat %d: seat was just booked by a passenger", body.SeatIndex))
		}
		return nil
	}); err != nil {
		respondTxError(c, err, "Failed to update seat block")
		return
	}

	if updatedRide.ManualBlockedSeats == nil {
		updatedRide.ManualBlockedSeats = []int{}
	}
	c.JSON(http.StatusOK, gin.H{
		"message":            "Seat status updated",
		"manualBlockedSeats": updatedRide.ManualBlockedSeats,
	})
}

type retiredBookingNotice struct {
	Booking models.Booking
	Ride    models.Ride
}

func sendRetiredRideNotices(notices []retiredBookingNotice) {
	for _, item := range notices {
		b := item.Booking
		r := item.Ride
		bookingHex := b.ID.Hex()
		go CreateNotification(b.PassengerID, "❌ Ride Cancelled",
			"Your booking from "+b.Pickup+" to "+b.Dropoff+" was cancelled because the driver replaced the ride ("+r.Pickup+" → "+r.Dropoff+"). Please book another ride.",
			"booking_status", bookingHex, map[string]string{
				"bookingId": bookingHex,
				"url":       "/?openTrips=true",
			})
	}
}

func getRideUsersCollection() *mongo.Collection {
	if usersCollection != nil {
		return usersCollection
	}
	if config.Database != nil {
		return config.Database.Collection("users")
	}
	return nil
}

func lookupPassengerSummaries(passengerIDs []primitive.ObjectID) map[primitive.ObjectID]models.User {
	result := make(map[primitive.ObjectID]models.User)
	usersCol := getRideUsersCollection()
	if usersCol == nil || len(passengerIDs) == 0 {
		return result
	}

	seen := make(map[primitive.ObjectID]bool, len(passengerIDs))
	uniqueIDs := make([]primitive.ObjectID, 0, len(passengerIDs))
	for _, id := range passengerIDs {
		if !id.IsZero() && !seen[id] {
			seen[id] = true
			uniqueIDs = append(uniqueIDs, id)
		}
	}
	if len(uniqueIDs) == 0 {
		return result
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cursor, err := usersCol.Find(
		ctx,
		bson.M{"_id": bson.M{"$in": uniqueIDs}},
		options.Find().SetProjection(bson.M{"name": 1, "phone_number": 1}),
	)
	if err != nil {
		return result
	}
	defer cursor.Close(ctx)

	var users []models.User
	if err := cursor.All(ctx, &users); err != nil {
		return result
	}
	for _, u := range users {
		result[u.ID] = u
	}
	return result
}

func displayDriverLabel(name string) string {
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return trimmed
	}
	return "a driver"
}

func buildBookingLifecycleAdminNotification(
	b models.Booking,
	r models.Ride,
	status string,
	reason string,
	passengerName string,
	passengerPhone string,
) (AdminNotificationInput, bool) {
	isParcel := strings.EqualFold(strings.TrimSpace(b.Type), "parcel")
	bookingHex := b.ID.Hex()
	bid := strings.TrimSpace(b.BookingID)
	bookingLabel := bid
	if bookingLabel == "" {
		bookingLabel = bookingHex
	}

	category := models.NotifCategoryBookings
	targetView := models.AdminViewBookings
	bookingType := "seat"
	if isParcel {
		category = models.NotifCategoryParcels
		targetView = models.AdminViewParcels
		bookingType = "parcel"
	}

	var notifType, title, message string
	switch status {
	case string(models.BookingStatusAccepted):
		if isParcel {
			notifType = models.NotifTypeParcelConfirmed
			title = "✅ Parcel Booking Confirmed"
			message = fmt.Sprintf("Driver %s confirmed parcel %s (%s → %s).", displayDriverLabel(r.DriverName), bookingLabel, b.Pickup, b.Dropoff)
		} else {
			notifType = models.NotifTypeBookingConfirmed
			title = "✅ Booking Confirmed"
			message = fmt.Sprintf("Driver %s confirmed booking %s (%d seats, %s → %s).", displayDriverLabel(r.DriverName), bookingLabel, maxInt(1, b.SeatsRequested), b.Pickup, b.Dropoff)
		}
	case string(models.BookingStatusRejected):
		if isParcel {
			notifType = models.NotifTypeParcelRejected
			title = "❌ Parcel Booking Declined"
			message = fmt.Sprintf("Driver %s declined parcel %s (%s → %s).", displayDriverLabel(r.DriverName), bookingLabel, b.Pickup, b.Dropoff)
		} else {
			notifType = models.NotifTypeBookingRejected
			title = "❌ Booking Declined"
			message = fmt.Sprintf("Driver %s declined booking %s (%d seats, %s → %s).", displayDriverLabel(r.DriverName), bookingLabel, maxInt(1, b.SeatsRequested), b.Pickup, b.Dropoff)
		}
	case string(models.BookingStatusCompleted):
		if isParcel {
			notifType = models.NotifTypeParcelCompleted
			title = "📦 Parcel Completed"
			message = fmt.Sprintf("Parcel %s (%s → %s) marked completed.", bookingLabel, b.Pickup, b.Dropoff)
		} else {
			notifType = models.NotifTypeBookingCompleted
			title = "🏁 Booking Completed"
			message = fmt.Sprintf("Booking %s (%d seats, %s → %s) marked completed.", bookingLabel, maxInt(1, b.SeatsRequested), b.Pickup, b.Dropoff)
		}
	case string(models.BookingStatusCancelled):
		reasonSuffix := ""
		if strings.TrimSpace(reason) != "" {
			reasonSuffix = " Reason: " + strings.TrimSpace(reason) + "."
		}
		if isParcel {
			notifType = models.NotifTypeParcelCancelled
			title = "❌ Parcel Cancelled"
			message = fmt.Sprintf("Parcel %s (%s → %s) was cancelled.%s", bookingLabel, b.Pickup, b.Dropoff, reasonSuffix)
		} else {
			notifType = models.NotifTypeBookingCancelled
			title = "❌ Booking Cancelled"
			message = fmt.Sprintf("Booking %s (%d seats, %s → %s) was cancelled.%s", bookingLabel, maxInt(1, b.SeatsRequested), b.Pickup, b.Dropoff, reasonSuffix)
		}
	default:
		return AdminNotificationInput{}, false
	}

	// Keep actorName / actorPhone strictly referring to the passenger (booker/sender),
	// matching BookRide, while driverName carries r.DriverName separately so UI context tags
	// and Parcel search filters never mix driver name with passenger phone.
	cleanPassengerName := strings.TrimSpace(passengerName)
	cleanPassengerPhone := strings.TrimSpace(passengerPhone)
	cleanDriverName := strings.TrimSpace(r.DriverName)

	meta := map[string]string{
		"bookingId":     bookingHex,
		"bid":           bid,
		"pickup":        b.Pickup,
		"dropoff":       b.Dropoff,
		"status":        status,
		"bookingType":   bookingType,
		"vehicleName":   r.VehicleModel,
		"vehicleNumber": r.VehicleNumber,
		"parcelSize":    b.ParcelSize,
		"recipientName": b.RecipientName,
		"contactNumber": b.ContactNumber,
		"reason":        strings.TrimSpace(reason),
	}
	if !r.ID.IsZero() {
		meta["rideId"] = r.ID.Hex()
	}
	if !r.DriverID.IsZero() {
		meta["driverId"] = r.DriverID.Hex()
	}
	if cleanDriverName != "" {
		meta["driverName"] = cleanDriverName
	}
	if !b.PassengerID.IsZero() {
		meta["userId"] = b.PassengerID.Hex()
	}
	if cleanPassengerName != "" {
		meta["actorName"] = cleanPassengerName
		meta["passengerName"] = cleanPassengerName
	}
	if cleanPassengerPhone != "" {
		meta["actorPhone"] = cleanPassengerPhone
	}

	return AdminNotificationInput{
		Title:      title,
		Message:    message,
		Type:       notifType,
		Category:   category,
		TargetView: targetView,
		RelatedID:  bookingHex,
		Metadata:   meta,
	}, true
}

func notifyAdminForBookingLifecycle(
	b models.Booking,
	r models.Ride,
	status string,
	reason string,
	passengerName string,
	passengerPhone string,
) {
	input, ok := buildBookingLifecycleAdminNotification(b, r, status, reason, passengerName, passengerPhone)
	if !ok {
		return
	}
	NotifyAdminsAsync(input)
}

func notifyAdminsForRetiredBookings(notices []retiredBookingNotice, reason string) {
	if len(notices) == 0 {
		return
	}
	copied := make([]retiredBookingNotice, len(notices))
	copy(copied, notices)

	go func(items []retiredBookingNotice, rawReason string) {
		seenRides := make(map[primitive.ObjectID]bool, len(items))
		trimmedReason := strings.TrimSpace(rawReason)
		passengerIDs := make([]primitive.ObjectID, 0, len(items))
		for _, item := range items {
			passengerIDs = append(passengerIDs, item.Booking.PassengerID)
		}
		passengers := lookupPassengerSummaries(passengerIDs)

		for _, item := range items {
			r := item.Ride
			if !r.ID.IsZero() && !seenRides[r.ID] {
				seenRides[r.ID] = true
				msg := fmt.Sprintf("Ride %s → %s (driver: %s) was cancelled.", r.Pickup, r.Dropoff, displayDriverLabel(r.DriverName))
				if trimmedReason != "" {
					msg = fmt.Sprintf("Ride %s → %s (driver: %s) was cancelled (%s).", r.Pickup, r.Dropoff, displayDriverLabel(r.DriverName), trimmedReason)
				}
				NotifyAdminsAsync(AdminNotificationInput{
					Title:      "❌ Ride Cancelled",
					Message:    msg,
					Type:       models.NotifTypeRideCancelled,
					Category:   models.NotifCategoryRides,
					TargetView: models.AdminViewCancelledRides,
					RelatedID:  r.ID.Hex(),
					Metadata: map[string]string{
						"rideId":        r.ID.Hex(),
						"driverId":      r.DriverID.Hex(),
						"driverName":    r.DriverName,
						"actorName":     r.DriverName,
						"vehicleName":   r.VehicleModel,
						"vehicleNumber": r.VehicleNumber,
						"pickup":        r.Pickup,
						"dropoff":       r.Dropoff,
						"status":        string(models.RideStatusCancelled),
						"reason":        trimmedReason,
					},
				})
			}

			b := item.Booking
			b.Status = string(models.BookingStatusCancelled)
			p := passengers[b.PassengerID]
			notifyAdminForBookingLifecycle(b, r, string(models.BookingStatusCancelled), trimmedReason, p.Name, p.PhoneNumber)
		}
	}(copied, reason)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// retireOpenRidesInTx soft-cancels all "available" rides belonging to driverID
// (never hard-deletes rides) and cancels any open ("pending"/"accepted") bookings
// on those rides, returning passenger notification records to dispatch after commit.
func retireOpenRidesInTx(ctx context.Context, driverID primitive.ObjectID) ([]retiredBookingNotice, error) {
	cursor, err := rideCollection.Find(ctx, bson.M{"driverId": driverID, "status": string(models.RideStatusAvailable)})
	if err != nil {
		return nil, err
	}
	var oldRides []models.Ride
	decErr := cursor.All(ctx, &oldRides)
	curErr := cursor.Err()
	cursor.Close(ctx)
	if decErr != nil {
		return nil, decErr
	}
	if curErr != nil {
		return nil, curErr
	}
	if len(oldRides) == 0 {
		return nil, nil
	}

	allIDs := make([]primitive.ObjectID, 0, len(oldRides))
	rideByID := make(map[primitive.ObjectID]models.Ride, len(oldRides))
	for _, r := range oldRides {
		allIDs = append(allIDs, r.ID)
		rideByID[r.ID] = r
	}

	now := time.Now()
	// Always soft-cancel available rides instead of hard-deleting them so audit history,
	// foreign-key references, and analytics remain intact.
	if _, err := rideCollection.UpdateMany(ctx,
		bson.M{"_id": bson.M{"$in": allIDs}, "status": string(models.RideStatusAvailable)},
		bson.M{"$set": bson.M{"status": string(models.RideStatusCancelled), "cancelledAt": now, "seatsBooked": 0}},
	); err != nil {
		return nil, err
	}

	activeStatuses := bson.M{"$in": []string{string(models.BookingStatusPending), string(models.BookingStatusAccepted)}}
	bCursor, err := bookingCollection.Find(ctx, bson.M{"rideId": bson.M{"$in": allIDs}, "status": activeStatuses})
	if err != nil {
		return nil, err
	}
	var affected []models.Booking
	bDecErr := bCursor.All(ctx, &affected)
	bCurErr := bCursor.Err()
	bCursor.Close(ctx)
	if bDecErr != nil {
		return nil, bDecErr
	}
	if bCurErr != nil {
		return nil, bCurErr
	}
	if len(affected) == 0 {
		log.Printf("[CreateRide] Driver %s: soft-cancelled %d open ride(s) with 0 open bookings",
			driverID.Hex(), len(allIDs))
		return nil, nil
	}

	affectedIDs := make([]primitive.ObjectID, 0, len(affected))
	notices := make([]retiredBookingNotice, 0, len(affected))
	for _, b := range affected {
		affectedIDs = append(affectedIDs, b.ID)
		notices = append(notices, retiredBookingNotice{
			Booking: b,
			Ride:    rideByID[b.RideID],
		})
	}
	if _, err := bookingCollection.UpdateMany(ctx,
		bson.M{"_id": bson.M{"$in": affectedIDs}, "status": activeStatuses},
		bson.M{"$set": bson.M{"status": string(models.BookingStatusCancelled), "cancelledAt": now}},
	); err != nil {
		return nil, err
	}

	log.Printf("[CreateRide] Driver %s: soft-cancelled %d open ride(s) and %d booking(s)",
		driverID.Hex(), len(allIDs), len(affected))
	return notices, nil
}

// retireOpenRides soft-cancels open rides and immediately dispatches passenger notifications.
func retireOpenRides(ctx context.Context, driverID primitive.ObjectID) error {
	notices, err := retireOpenRidesInTx(ctx, driverID)
	if err != nil {
		return err
	}
	sendRetiredRideNotices(notices)
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
		var inserted models.Booking
		if err := bookingCollection.FindOne(ctx, bson.M{"_id": bookingID}).Decode(&inserted); err != nil {
			return "", err
		}
		dupParcel, err := bookingCollection.CountDocuments(ctx, bson.M{
			"_id":           bson.M{"$ne": bookingID},
			"rideId":        rideID,
			"passengerId":   passengerID,
			"type":          "parcel",
			"status":        bson.M{"$in": []string{"pending", "accepted"}},
			"recipientName": inserted.RecipientName,
			"contactNumber": inserted.ContactNumber,
			"parcelSize":    inserted.ParcelSize,
		})
		if err != nil {
			return "", err
		}
		if dupParcel > 0 {
			return "An identical active parcel booking already exists for this ride", nil
		}
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
