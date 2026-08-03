package controllers

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"regexp"
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

func InitializeRideCollection() {
	rideCollection = config.Database.Collection("rides")
	bookingCollection = config.Database.Collection("bookings")
	recentRoutesCollection = config.Database.Collection("recent_routes")
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

	// Fetch driver Name
	var driver struct {
		Name string `bson:"name"`
	}
	err = config.Database.Collection("users").FindOne(findCtx, bson.M{"_id": userId}).Decode(&driver)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch driver info"})
		return
	}

	delCtx, delCancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer delCancel()

	// Delete existing rides for this driver to keep it clean
	_, err = rideCollection.DeleteMany(delCtx, bson.M{"driverId": userId, "status": "available"})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset available rides"})
		return
	}

	// --- AUTO-DISCOVER INTERMEDIATE STOPS FROM MAPS ---
	var discoveredStops []models.StopInfo
	var routeCoords [][]float64
	var totalDistM float64
	var routeNames []string

	// Geocode pickup and dropoff in parallel
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

	pRes, dRes := <-pChan, <-dChan
	startLat, startLon, geoErr1 := pRes.lat, pRes.lon, pRes.err
	endLat, endLon, geoErr2 := dRes.lat, dRes.lon, dRes.err

	if geoErr1 == nil && geoErr2 == nil {
		// Discover intermediate villages/towns from maps
		log.Printf("Discovering intermediate stops for: %s → %s", body.Pickup, body.Dropoff)
		stops, coords, dist, discErr := utils.DiscoverIntermediateStops(startLat, startLon, endLat, endLon, 3.0)
		if discErr == nil && len(stops) > 0 {
			discoveredStops = stops
			routeCoords = make([][]float64, len(coords))
			for i, c := range coords {
				routeCoords[i] = []float64{c[0], c[1]}
			}
			totalDistM = dist
			// Build route names from discovered stops
			for _, s := range stops {
				routeNames = append(routeNames, s.Name)
			}
			log.Printf("Discovered %d intermediate stops: %v", len(stops), routeNames)
		} else {
			log.Printf("Stop discovery warning: %v (found %d stops)", discErr, len(stops))
		}
	} else {
		log.Printf("Geocoding failed for ride: pickup=%v, dropoff=%v", geoErr1, geoErr2)
	}

	// Build final route array for matching
	route := []string{body.Pickup} // Always start with what the user typed
	for _, s := range routeNames {
		// Avoid exact duplicates (case-insensitive) if discovery found the same name
		sClean := strings.ToLower(strings.TrimSpace(s))
		if sClean != strings.ToLower(strings.TrimSpace(body.Pickup)) &&
			sClean != strings.ToLower(strings.TrimSpace(body.Dropoff)) &&
			sClean != "" {
			route = append(route, s)
		}
	}
	route = append(route, body.Dropoff) // Always end with what the user typed

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
		Status:          "available",
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


func getStopIndex(route []string, stop string) int {
	for i, s := range route {
		if utils.FuzzyMatch(s, stop) {
			return i
		}
	}
	return -1
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
	cursor, err := bookingCollection.Find(ctx, bson.M{
		"rideId": ride.ID,
		"status": bson.M{"$in": []string{"pending", "accepted"}},
	})
	if err != nil {
		return []int{}
	}
	var bookings []models.Booking
	cursor.All(ctx, &bookings)

	takenMap := make(map[int]bool)
	for _, b := range bookings {
		// If searching for specific segment, only include overlapping bookings
		if searchPickup != "" && searchDropoff != "" {
			if segmentsOverlap(searchPickup, searchDropoff, b.Pickup, b.Dropoff, ride.Route) {
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

	taken := []int{}
	for s := range takenMap {
		taken = append(taken, s)
	}

	// MERGE manual blocked seats by driver (Offline bookings)
	for _, s := range ride.ManualBlockedSeats {
		found := false
		for _, t := range taken {
			if t == s {
				found = true
				break
			}
		}
		if !found {
			taken = append(taken, s)
		}
	}

	return taken
}

func GetAvailableRides(c *gin.Context) {
	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	pickup := strings.TrimSpace(c.Query("pickup"))
	dropoff := strings.TrimSpace(c.Query("dropoff"))
	date := strings.TrimSpace(c.Query("date"))

	// Validate user-controlled inputs before using them in DB query construction
	const maxLocationLen = 100
	locationPattern := regexp.MustCompile(`^[a-zA-Z0-9\s\-,.]+$`)

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

	// Build DB filter: always filter available rides, optionally by date
	matchStage := bson.M{"status": "available"}
	if date != "" {
		matchStage["date"] = date
	}

	// Use MongoDB aggregation for efficient multi-stop matching
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: matchStage}},
	}

	// If pickup/dropoff are provided, filter by route sequence
	if pickup != "" && dropoff != "" {
		// --- SECURITY HARDENING: Validation ---
		if len(pickup) > 100 || len(dropoff) > 100 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Search query too long"})
			return
		}

		// Only allow alphanumeric, spaces, commas, and hyphens to prevent regex injection attacks
		safeRegex := regexp.MustCompile(`^[a-zA-Z0-9\s,\-]+$`)
		if !safeRegex.MatchString(pickup) || !safeRegex.MatchString(dropoff) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid characters in search"})
			return
		}

		quotedPickup := regexp.QuoteMeta(pickup)
		quotedDropoff := regexp.QuoteMeta(dropoff)

		// We use multiple $match stages because $all with $regex is not supported in many MongoDB versions/drivers
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: bson.M{
			"route": bson.M{"$regex": primitive.Regex{Pattern: "(?i)" + quotedPickup, Options: ""}},
		}}})
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: bson.M{
			"route": bson.M{"$regex": primitive.Regex{Pattern: "(?i)" + quotedDropoff, Options: ""}},
		}}})

		// Add fields for indexes to check sequence
		// We use $map with $regexMatch to handle fuzzy/partial matches for indices
		pipeline = append(pipeline, bson.D{{Key: "$addFields", Value: bson.M{
			"pickupIndex": bson.M{
				"$indexOfArray": []interface{}{
					bson.M{"$map": bson.M{
						"input": "$route",
						"as":    "r",
						"in": bson.M{"$regexMatch": bson.M{
							"input": "$$r",
							"regex": "(?i)" + quotedPickup,
						}},
					}},
					true,
				},
			},
			"dropoffIndex": bson.M{
				"$indexOfArray": []interface{}{
					bson.M{"$map": bson.M{
						"input": "$route",
						"as":    "r",
						"in": bson.M{"$regexMatch": bson.M{
							"input": "$$r",
							"regex": "(?i)" + quotedDropoff,
						}},
					}},
					true,
				},
			},
		}}})

		// Filter: pickup must come before dropoff
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: bson.M{
			"$expr": bson.M{"$and": []interface{}{
				bson.M{"$ne": []interface{}{"$pickupIndex", -1}},
				bson.M{"$ne": []interface{}{"$dropoffIndex", -1}},
				bson.M{"$lt": []interface{}{"$pickupIndex", "$dropoffIndex"}},
			}},
		}}})
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

	type RideResponse struct {
		models.Ride
		SegmentPricePerSeat float64 `json:"segmentPricePerSeat,omitempty"`
		SegmentDistanceKm   float64 `json:"segmentDistanceKm,omitempty"`
		MatchedPickup       string  `json:"matchedPickup,omitempty"`
		MatchedDropoff      string  `json:"matchedDropoff,omitempty"`
	}

	var results []RideResponse
	for _, ride := range matchedRides {
		ride.TakenSeats = getTakenSeats(dbCtx, ride, pickup, dropoff)
		response := RideResponse{Ride: ride}

		if pickup != "" && dropoff != "" {
			// Find actual stop names for the response
			var pStop, dStop models.StopInfo
			pIdx, dIdx := -1, -1

			for i, s := range ride.DiscoveredStops {
				if utils.FuzzyMatch(s.Name, pickup) {
					pIdx = i
					pStop = s
				}
				if utils.FuzzyMatch(s.Name, dropoff) {
					dIdx = i
					dStop = s
				}
			}

			if pIdx != -1 && dIdx != -1 {
				response.SegmentDistanceKm = (dStop.DistanceM - pStop.DistanceM) / 1000
				response.SegmentPricePerSeat = utils.CalculateSegmentPrice(ride.PricePerSeat, ride.TotalDistanceM, pStop.DistanceM, dStop.DistanceM)
				response.MatchedPickup = pStop.Name
				response.MatchedDropoff = dStop.Name
			} else {
				// Fallback to full route info if stops not detailed
				response.SegmentPricePerSeat = ride.PricePerSeat
				response.SegmentDistanceKm = ride.TotalDistanceM / 1000
				response.MatchedPickup = pickup
				response.MatchedDropoff = dropoff
			}
		}
		results = append(results, response)
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
	rideId, _ := primitive.ObjectIDFromHex(rideIdHex)
	pickup := c.Query("pickup")
	dropoff := c.Query("dropoff")

	var ride models.Ride
	err := rideCollection.FindOne(dbCtx, bson.M{"_id": rideId}).Decode(&ride)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found"})
		return
	}

	ride.TakenSeats = getTakenSeats(dbCtx, ride, pickup, dropoff)
	c.JSON(http.StatusOK, ride)
}

func BookRide(c *gin.Context) {
	passengerId := c.MustGet("userId").(primitive.ObjectID)
	rideIdHex := c.Param("rideId")
	rideId, _ := primitive.ObjectIDFromHex(rideIdHex)

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

	booking := models.Booking{
		RideID:         rideId,
		PassengerID:    passengerId,
		Type:           bookingType,
		Pickup:         body.Pickup,
		Dropoff:        body.Dropoff,
		SeatsRequested: body.SeatsRequested,
		SeatLayout:     body.SeatLayout,
		RoofCarrier:    body.RoofCarrier,
		MotionSickness: body.MotionSickness,
		// Parcel fields
		ParcelSize:    body.ParcelSize,
		RecipientName: body.RecipientName,
		ContactNumber: body.ContactNumber,
		DropLocation:  body.DropLocation,
		Notes:         body.Notes,
		PhotoUrl:      body.PhotoUrl,
		Price:         body.Price,
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

	msg := "Booking request sent to driver"
	if bookingType == "parcel" {
		msg = "Parcel pickup request sent to driver"
	}

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

	var rideIds []primitive.ObjectID
	for _, ride := range rides {
		rideIds = append(rideIds, ride.ID)
	}

	// Filter bookings for those rideIds
	cursor, err = bookingCollection.Find(dbCtx, bson.M{"rideId": bson.M{"$in": rideIds}})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch requests"})
		return
	}

	var bookings []models.Booking
	if err := cursor.All(dbCtx, &bookings); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse bookings"})
		return
	}

	c.JSON(http.StatusOK, bookings)
}

func GetPassengerBookings(c *gin.Context) {
	passengerId := c.MustGet("userId").(primitive.ObjectID)
	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	pipeline := []bson.M{
		{"$match": bson.M{"passengerId": passengerId}},
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
		Ride models.Ride `json:"ride"`
	}

	var response []BookingResponse
	for _, res := range results {
		ride := models.Ride{}
		if len(res.RideDetails) > 0 {
			ride = res.RideDetails[0]
			// Populate real-time taken seats for THIS booking's segment
			ride.TakenSeats = getTakenSeats(dbCtx, ride, res.Booking.Pickup, res.Booking.Dropoff)
		}
		response = append(response, BookingResponse{
			Booking: res.Booking,
			Ride:    ride,
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

func UpdateBookingStatus(c *gin.Context) {
	bookingIdHex := c.Param("bookingId")
	bookingId, _ := primitive.ObjectIDFromHex(bookingIdHex)

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
	err := bookingCollection.FindOne(dbCtx, bson.M{"_id": bookingId}).Decode(&booking)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Booking not found"})
		return
	}

	// Double check race condition
	if booking.Status != "pending" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Booking status already updated"})
		return
	}

	// Authorization check IDOR
	var ride models.Ride
	err = rideCollection.FindOne(dbCtx, bson.M{"_id": booking.RideID}).Decode(&ride)
	if err != nil || ride.DriverID != driverId {
		c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized"})
		return
	}

	// Update status
	_, err = bookingCollection.UpdateOne(
		dbCtx,
		bson.M{"_id": bookingId},
		bson.M{"$set": bson.M{"status": body.Status}},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update booking"})
		return
	}

	// If accepted, update ride's seatsBooked
	if body.Status == "accepted" {
		rideCollection.UpdateOne(
			dbCtx,
			bson.M{"_id": booking.RideID},
			bson.M{"$inc": bson.M{"seatsBooked": booking.SeatsRequested}},
		)
	}

	c.JSON(http.StatusOK, gin.H{"message": "Booking status updated"})
}

func GetRecentRides(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	role := c.Query("role") // "driver" or "passenger"

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	opts := options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}}).
		SetLimit(5)

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
		rides := []models.Ride{}
		if err := cursor.All(dbCtx, &rides); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse rides"})
			return
		}
		for i := range rides {
			rides[i].TakenSeats = getTakenSeats(dbCtx, rides[i], "", "")
		}
		c.JSON(http.StatusOK, rides)
		return
	}

	// Passengers (default): return search history from `recent_routes`
	cursor, err := recentRoutesCollection.Find(
		dbCtx,
		bson.M{"userId": userId},
		opts,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch recent rides"})
		return
	}

	var recent []bson.M
	if err := cursor.All(dbCtx, &recent); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse recent rides"})
		return
	}

	if recent == nil {
		recent = []bson.M{}
	}

	c.JSON(http.StatusOK, recent)
}

func MarkNotificationsViewed(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	role := c.Query("role") // "driver" or "passenger"

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	var filter bson.M
	var update bson.M

	if role == "driver" {
		// Driver sees requests for THEIR rides
		cursor, _ := rideCollection.Find(dbCtx, bson.M{"driverId": userId})
		var rideIds []primitive.ObjectID
		for cursor.Next(dbCtx) {
			var r models.Ride
			cursor.Decode(&r)
			rideIds = append(rideIds, r.ID)
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
	rideId, _ := primitive.ObjectIDFromHex(rideIdHex)

	var body struct {
		SeatIndex int `json:"seatIndex"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// 1. Fetch the ride and verify ownership
	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	var ride models.Ride
	err := rideCollection.FindOne(dbCtx, bson.M{"_id": rideId}).Decode(&ride)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found"})
		return
	}

	if ride.DriverID != userId {
		c.JSON(http.StatusForbidden, gin.H{"error": "Only the driver can block seats"})
		return
	}

	// 2. Add or Remove from ManualBlockedSeats
	newBlocked := []int{}
	found := false
	for _, s := range ride.ManualBlockedSeats {
		if s == body.SeatIndex {
			found = true
			continue // remove it
		}
		newBlocked = append(newBlocked, s)
	}

	if !found {
		// Verify seat index is valid
		if body.SeatIndex < 1 || body.SeatIndex > ride.SeatsTotal {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid seat index"})
			return
		}
		newBlocked = append(newBlocked, body.SeatIndex)
	}

	// 3. Update DB
	_, err = rideCollection.UpdateOne(
		dbCtx,
		bson.M{"_id": rideId},
		bson.M{"$set": bson.M{"manualBlockedSeats": newBlocked}},
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update seat block"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":            "Seat status updated",
		"manualBlockedSeats": newBlocked,
	})
}
