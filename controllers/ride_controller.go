package controllers

import (
	"context"
	"net/http"
	"regexp"
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

// escapeRegex escapes special regex characters to prevent ReDoS injection
var regexMetaChars = regexp.MustCompile(`[\\^$.|?*+()\[\]{}]`)

func escapeRegex(s string) string {
	return regexMetaChars.ReplaceAllString(s, `\$0`)
}

var rideCollection *mongo.Collection
var bookingCollection *mongo.Collection

func InitializeRideCollection() {
	rideCollection = config.Database.Collection("rides")
	bookingCollection = config.Database.Collection("bookings")

	// Create TTL index to auto-delete rides and bookings after 7 days (604800 seconds)
	ttlIndex := mongo.IndexModel{
		Keys:    bson.M{"createdAt": 1},
		Options: options.Index().SetExpireAfterSeconds(604800),
	}
	// Using a timeout context for background indexing as it's not request-specific
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rideCollection.Indexes().CreateOne(ctx, ttlIndex)
	bookingCollection.Indexes().CreateOne(ctx, ttlIndex)
}

func CreateRide(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	var body struct {
		Pickup        string  `json:"pickup"`
		Dropoff       string  `json:"dropoff"`
		VehicleModel  string  `json:"vehicleModel"`
		VehicleNumber string  `json:"vehicleNumber"`
		DepartureTime string  `json:"departureTime"`
		Date          string  `json:"date"`
		SeatsTotal    int     `json:"seatsTotal"`
		SeatingLayout string  `json:"seatingLayout"`
		PricePerSeat  float64 `json:"pricePerSeat"`
	}

	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input formatting"})
		return
	}

	// Validation: Location sanitization and length
	pickup := utils.SanitizeString(body.Pickup)
	dropoff := utils.SanitizeString(body.Dropoff)
	if len(pickup) < 2 || len(pickup) > 100 || len(dropoff) < 2 || len(dropoff) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Pickup and Dropoff locations must be between 2 and 100 characters"})
		return
	}

	// Validation: Positive seats and price
	if body.SeatsTotal <= 0 || body.PricePerSeat < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid seat count or price. Must be positive."})
		return
	}

	// Fetch driver Name
	var driver struct {
		Name string `bson:"name"`
	}
	errFetch := config.Database.Collection("users").FindOne(c.Request.Context(), bson.M{"_id": userId}).Decode(&driver)
	if errFetch != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Driver user record not found"})
		return
	}

	// We no longer delete existing 'available' rides here so that a driver
	// logged into multiple devices (e.g., laptop and mobile) posting different
	// trips simultaneously does not experience silent deletions causing out-of-sync states.

	// Ensure date is never empty - use current date if not provided
	rideDate := body.Date
	if rideDate == "" {
		rideDate = time.Now().Format("02/01/2006")
	} else {
		parsedDate, err := time.Parse("02/01/2006", rideDate)
		if err == nil {
			now := time.Now()
			today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
			if parsedDate.Before(today) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot post a ride for a past date"})
				return
			} else if parsedDate.Equal(today) && body.DepartureTime != "" {
	
				// Provide multiple formats for parsing the departure time
				formats := []string{"03:04 PM", "3:04 PM", "03:04PM", "3:04PM", "15:04"}
				var parsedTime time.Time
				var errTime error
				for _, f := range formats {
					parsedTime, errTime = time.Parse(f, body.DepartureTime)
					if errTime == nil {
						break
					}
				}
				
				if errTime == nil {
					if parsedTime.Hour() < now.Hour() || (parsedTime.Hour() == now.Hour() && parsedTime.Minute() < now.Minute()) {
						c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot post a ride for a past time today"})
						return
					}
				}
			}
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid date format, expected DD/MM/YYYY"})
			return
		}
	}

	ride := models.Ride{
		DriverID:      userId,
		DriverName:    driver.Name,
		Pickup:        pickup,
		Dropoff:       dropoff,
		Date:          rideDate,
		VehicleModel:  body.VehicleModel,
		VehicleNumber: body.VehicleNumber,
		DepartureTime: body.DepartureTime,
		SeatsTotal:    body.SeatsTotal,
		SeatingLayout: body.SeatingLayout,
		SeatsBooked:   0,
		PricePerSeat:  body.PricePerSeat,
		Status:        "available",
		CreatedAt:     time.Now(),
	}

	_, errInsert := rideCollection.InsertOne(c.Request.Context(), ride)
	if errInsert != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create ride"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Ride created successfully"})
}

func getTakenSeats(ctx context.Context, rideId primitive.ObjectID) []int {
	cursor, err := bookingCollection.Find(ctx, bson.M{
		"rideId": rideId,
		"status": bson.M{"$in": []string{"pending", "accepted"}},
	})
	if err != nil {
		return []int{}
	}
	var bookings []models.Booking
	cursor.All(ctx, &bookings)

	taken := []int{}
	for _, b := range bookings {
		taken = append(taken, b.SeatLayout...)
	}
	return taken
}

// backfillDate ensures every ride has a human-readable date string.
// If the driver already set one, it is kept. Otherwise the ride's
// creation timestamp is formatted as DD/MM/YYYY.
func backfillDate(rides []models.Ride) {
	for i := range rides {
		if rides[i].Date == "" && !rides[i].CreatedAt.IsZero() {
			rides[i].Date = rides[i].CreatedAt.Format("02/01/2006")
		}
	}
}

func GetAvailableRides(c *gin.Context) {
	filter := bson.M{"status": "available"}

	pickup := c.Query("pickup")
	if pickup != "" {
		filter["pickup"] = primitive.Regex{Pattern: escapeRegex(pickup), Options: "i"}
	}

	dropoff := c.Query("dropoff")
	if dropoff != "" {
		filter["dropoff"] = primitive.Regex{Pattern: escapeRegex(dropoff), Options: "i"}
	}

	cursor, err := rideCollection.Find(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch rides"})
		return
	}

	var rides []models.Ride
	if err := cursor.All(c.Request.Context(), &rides); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse rides"})
		return
	}

	var validRides []models.Ride
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	for i := range rides {
		if rides[i].Date != "" {
			parsedDate, err := time.Parse("02/01/2006", rides[i].Date)
			if err == nil && parsedDate.Before(today) {
				// Ride is in the past, auto-expire it
				rideCollection.UpdateOne(c.Request.Context(), bson.M{"_id": rides[i].ID}, bson.M{"$set": bson.M{"status": "expired"}})
				continue
			}
		}

		rides[i].TakenSeats = getTakenSeats(c.Request.Context(), rides[i].ID)
		rides[i].SeatsBooked = len(rides[i].TakenSeats)
		validRides = append(validRides, rides[i])
	}
	backfillDate(validRides)

	c.JSON(http.StatusOK, validRides)
}

func GetRideDetails(c *gin.Context) {
	rideIdHex := c.Param("rideId")
	rideId, err := primitive.ObjectIDFromHex(rideIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ride ID format"})
		return
	}

	var ride models.Ride
	err = rideCollection.FindOne(c.Request.Context(), bson.M{"_id": rideId}).Decode(&ride)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found"})
		return
	}

	ride.TakenSeats = getTakenSeats(c.Request.Context(), ride.ID)
	ride.SeatsBooked = len(ride.TakenSeats)
	if ride.Date == "" && !ride.CreatedAt.IsZero() {
		ride.Date = ride.CreatedAt.Format("02/01/2006")
	}
	c.JSON(http.StatusOK, ride)
}

func BookRide(c *gin.Context) {
	passengerId := c.MustGet("userId").(primitive.ObjectID)
	rideIdHex := c.Param("rideId")
	rideId, err := primitive.ObjectIDFromHex(rideIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ride ID format"})
		return
	}

	var body struct {
		SeatsRequested int   `json:"seatsRequested"`
		SeatLayout     []int `json:"seatLayout"`
		RoofCarrier    bool  `json:"roofCarrier"`
		MotionSickness bool  `json:"motionSickness"`
	}

	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	// Basic Validation
	if body.SeatsRequested <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "You must request at least 1 seat"})
		return
	}

	// Fetch the ride to verify existence and capacity
	var ride models.Ride
	err = rideCollection.FindOne(c.Request.Context(), bson.M{"_id": rideId}).Decode(&ride)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found or unavailable"})
		return
	}

	// Security Check: Cannot book your own ride
	if ride.DriverID == passengerId {
		c.JSON(http.StatusForbidden, gin.H{"error": "You cannot book your own ride"})
		return
	}

	// Logic Check: Overbooking Prevention
	takenCount := len(getTakenSeats(c.Request.Context(), rideId))
	if takenCount+body.SeatsRequested > ride.SeatsTotal {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Not enough seats available"})
		return
	}

	booking := models.Booking{
		RideID:         rideId,
		PassengerID:    passengerId,
		SeatsRequested: body.SeatsRequested,
		SeatLayout:     body.SeatLayout,
		RoofCarrier:    body.RoofCarrier,
		MotionSickness: body.MotionSickness,
		Status:         "pending",
		CreatedAt:      time.Now(),
	}

	_, err = bookingCollection.InsertOne(c.Request.Context(), booking)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to book ride"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Booking request sent to driver"})
}

func GetDriverRequests(c *gin.Context) {
	driverId := c.MustGet("userId").(primitive.ObjectID)

	// Sub-query to get all rides created by this driver
	cursor, err := rideCollection.Find(c.Request.Context(), bson.M{"driverId": driverId})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch driver rides"})
		return
	}

	var rides []models.Ride
	if err := cursor.All(c.Request.Context(), &rides); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse rides"})
		return
	}

	// If driver has no rides, return empty array immediately
	if len(rides) == 0 {
		c.JSON(http.StatusOK, []struct{}{})
		return
	}

	rideIds := make([]primitive.ObjectID, 0, len(rides))
	for _, ride := range rides {
		rideIds = append(rideIds, ride.ID)
	}

	// Filter bookings for those rideIds, sorted by newest first
	opts := options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}})
	cursor, err = bookingCollection.Find(c.Request.Context(), bson.M{"rideId": bson.M{"$in": rideIds}}, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch requests"})
		return
	}

	var bookings []models.Booking
	if err := cursor.All(c.Request.Context(), &bookings); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse bookings"})
		return
	}

	type BookingResponse struct {
		models.Booking
		Ride            models.Ride `json:"ride"`
		UnreadChatCount int64       `json:"unreadChatCount"`
	}

	response := make([]BookingResponse, 0)
	for _, b := range bookings {
		var ride models.Ride
		errRide := rideCollection.FindOne(c.Request.Context(), bson.M{"_id": b.RideID}).Decode(&ride)
		if errRide != nil {
			continue
		}
		// Backfill date if empty
		if ride.Date == "" && !ride.CreatedAt.IsZero() {
			ride.Date = ride.CreatedAt.Format("02/01/2006")
		}
		response = append(response, BookingResponse{
			Booking:         b,
			Ride:            ride,
			UnreadChatCount: GetUnreadMessageCount(b.ID, "passenger"),
		})
	}

	c.JSON(http.StatusOK, response)
}

func GetPassengerBookings(c *gin.Context) {
	passengerId := c.MustGet("userId").(primitive.ObjectID)

	opts := options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}})
	cursor, err := bookingCollection.Find(c.Request.Context(), bson.M{"passengerId": passengerId}, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch bookings"})
		return
	}

	var bookings []models.Booking
	if err := cursor.All(c.Request.Context(), &bookings); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse bookings"})
		return
	}

	type BookingResponse struct {
		models.Booking
		Ride            models.Ride `json:"ride"`
		UnreadChatCount int64       `json:"unreadChatCount"`
		CompletedSeats  []int       `json:"completedSeats"`
	}

	response := make([]BookingResponse, 0)
	for _, b := range bookings {
		var ride models.Ride
		errRide := rideCollection.FindOne(c.Request.Context(), bson.M{"_id": b.RideID}).Decode(&ride)
		if errRide != nil {
			continue
		}
		ride.TakenSeats = getTakenSeats(c.Request.Context(), ride.ID) // Populate real-time taken seats
		
		// Map completed seats from other bookings on this ride
		completedSeats := []int{}
		bCursor, _ := bookingCollection.Find(c.Request.Context(), bson.M{"rideId": ride.ID, "status": "completed"})
		var bList []models.Booking
		bCursor.All(c.Request.Context(), &bList)
		for _, ob := range bList {
			completedSeats = append(completedSeats, ob.SeatLayout...)
		}

		// Backfill date if empty
		if ride.Date == "" && !ride.CreatedAt.IsZero() {
			ride.Date = ride.CreatedAt.Format("02/01/2006")
		}
		response = append(response, BookingResponse{
			Booking:         b,
			Ride:            ride,
			UnreadChatCount: GetUnreadMessageCount(b.ID, "driver"),
			CompletedSeats:  completedSeats,
		})
	}

	c.JSON(http.StatusOK, response)
}

func SaveRecentRide(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	var body struct {
		Pickup   string `json:"pickup"`
		Dropoff  string `json:"dropoff"`
		RideType string `json:"rideType"`
	}

	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	ride := models.Ride{
		DriverID:  userId, // Using DriverID because unified Ride model
		Pickup:    body.Pickup,
		Dropoff:   body.Dropoff,
		Date:      time.Now().Format("02/01/2006"), // Add current date
		CreatedAt: time.Now(),
	}

	_, err := rideCollection.InsertOne(c.Request.Context(), ride)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save ride"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Recent ride saved"})
}

func UpdateBookingStatus(c *gin.Context) {
	bookingIdHex := c.Param("bookingId")
	bookingId, err := primitive.ObjectIDFromHex(bookingIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid booking ID format"})
		return
	}

	var body struct {
		Status string `json:"status"` // "accepted" or "rejected"
	}

	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// Get the booking to find the rideId and seatsRequested
	var booking models.Booking
	err = bookingCollection.FindOne(c.Request.Context(), bson.M{"_id": bookingId}).Decode(&booking)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Booking not found"})
		return
	}

	// Ownership check: Is this booking for a ride owned by this driver?
	userId := c.MustGet("userId").(primitive.ObjectID)
	var ride models.Ride
	err = rideCollection.FindOne(c.Request.Context(), bson.M{"_id": booking.RideID}).Decode(&ride)
	if err != nil || ride.DriverID != userId {
		c.JSON(http.StatusForbidden, gin.H{"error": "Forbidden: You do not own this ride's booking requests"})
		return
	}

	// Update status
	_, err = bookingCollection.UpdateOne(
		c.Request.Context(),
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
			c.Request.Context(),
			bson.M{"_id": booking.RideID},
			bson.M{"$inc": bson.M{"seatsBooked": booking.SeatsRequested}},
		)
	}

	c.JSON(http.StatusOK, gin.H{"message": "Booking status updated"})
}

func GetRecentRides(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	opts := options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}})

	cursor, err := rideCollection.Find(
		c.Request.Context(),
		bson.M{"driverId": userId},
		opts,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch rides"})
		return
	}

	rides := []models.Ride{}
	if err := cursor.All(c.Request.Context(), &rides); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse rides"})
		return
	}

	type RideResponse struct {
		models.Ride
		AcceptedSeats  []int            `json:"acceptedSeats"`
		PendingSeats   []int            `json:"pendingSeats"`
		CompletedSeats []int            `json:"completedSeats"`
		Bookings       []models.Booking `json:"bookings"`
	}

	var response []RideResponse
	for i := range rides {
		rides[i].TakenSeats = getTakenSeats(c.Request.Context(), rides[i].ID)
		rides[i].SeatsBooked = len(rides[i].TakenSeats)
		if rides[i].Date == "" && !rides[i].CreatedAt.IsZero() {
			rides[i].Date = rides[i].CreatedAt.Format("02/01/2006")
		}

		// Separate accepted, pending, and completed seat layouts, newest first
		bOpts := options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}})
		bCursor, _ := bookingCollection.Find(c.Request.Context(), bson.M{"rideId": rides[i].ID}, bOpts)
		var bList []models.Booking
		bCursor.All(c.Request.Context(), &bList)
		
		acceptedSeats := []int{}
		pendingSeats := []int{}
		completedSeats := []int{}
		
		for _, b := range bList {
			if b.Status == "accepted" {
				acceptedSeats = append(acceptedSeats, b.SeatLayout...)
			} else if b.Status == "pending" {
				pendingSeats = append(pendingSeats, b.SeatLayout...)
			} else if b.Status == "completed" {
				completedSeats = append(completedSeats, b.SeatLayout...)
			}
		}

		response = append(response, RideResponse{
			Ride:           rides[i],
			AcceptedSeats:  acceptedSeats,
			PendingSeats:   pendingSeats,
			CompletedSeats: completedSeats,
			Bookings:       bList,
		})
	}
	backfillDate(rides)

	if response == nil {
		response = []RideResponse{}
	}
	c.JSON(http.StatusOK, response)
}

func MarkNotificationsViewed(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	role := c.Query("role") // "driver" or "passenger"

	// Validate the role parameter is one of the expected values
	if role != "driver" && role != "passenger" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid role parameter. Use 'driver' or 'passenger'."})
		return
	}

	var filter bson.M
	var update bson.M

	if role == "driver" {
		// Driver sees requests for THEIR rides
		cursor, _ := rideCollection.Find(c.Request.Context(), bson.M{"driverId": userId})
		var rideIds []primitive.ObjectID
		for cursor.Next(c.Request.Context()) {
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

	bookingCollection.UpdateMany(c.Request.Context(), filter, update)
	c.JSON(http.StatusOK, gin.H{"message": "Notifications marked as viewed"})
}

func CompleteRide(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	rideIdHex := c.Param("rideId")
	rideId, err := primitive.ObjectIDFromHex(rideIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ride ID"})
		return
	}

	// Verify driver ownership
	var ride models.Ride
	err = rideCollection.FindOne(c.Request.Context(), bson.M{"_id": rideId, "driverId": userId}).Decode(&ride)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Forbidden: You do not own this ride or it does not exist"})
		return
	}

	if ride.Status == "completed" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ride is already completed"})
		return
	}

	// Update ride status
	now := time.Now()
	_, err = rideCollection.UpdateOne(
		c.Request.Context(),
		bson.M{"_id": rideId},
		bson.M{"$set": bson.M{"status": "completed", "completedAt": now}},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update ride"})
		return
	}

	// Also complete all accepted bookings for this ride
	bookingCollection.UpdateMany(
		c.Request.Context(),
		bson.M{"rideId": rideId, "status": "accepted"},
		bson.M{"$set": bson.M{"status": "completed", "completedAt": now}},
	)

	// Insert Database level alert for Admin
	adminNotificationsCollection := config.Database.Collection("admin_notifications")
	notification := bson.M{
		"type":      "trip_completed",
		"rideId":    rideId,
		"driverId":  userId,
		"message":   "Trip has been completed by driver " + ride.DriverName + " for Ride " + ride.Pickup + " to " + ride.Dropoff,
		"viewed":    false,
		"createdAt": now,
	}
	adminNotificationsCollection.InsertOne(c.Request.Context(), notification)

	c.JSON(http.StatusOK, gin.H{"message": "Trip marked as completed"})
}

func CompleteBooking(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	bookingIdHex := c.Param("bookingId")
	bookingId, err := primitive.ObjectIDFromHex(bookingIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid booking ID"})
		return
	}

	// Get the booking
	var booking models.Booking
	err = bookingCollection.FindOne(c.Request.Context(), bson.M{"_id": bookingId}).Decode(&booking)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Booking not found"})
		return
	}

	// Verify driver ownership of the ride this booking belongs to
	var ride models.Ride
	err = rideCollection.FindOne(c.Request.Context(), bson.M{"_id": booking.RideID, "driverId": userId}).Decode(&ride)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Forbidden: You do not own this ride's bookings"})
		return
	}

	if booking.Status == "completed" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Booking is already completed"})
		return
	}

	// Update booking status
	now := time.Now()
	_, err = bookingCollection.UpdateOne(
		c.Request.Context(),
		bson.M{"_id": bookingId},
		bson.M{"$set": bson.M{"status": "completed", "completedAt": now}},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update booking"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Booking marked as completed"})
}
