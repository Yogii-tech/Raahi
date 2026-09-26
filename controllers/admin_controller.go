package controllers

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"raahi-backend/config"
	"raahi-backend/models"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// AdminStats returns aggregated dashboard KPIs computed from real collections.
func AdminStats(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	// --- Rides ---
	totalRides, _ := config.Database.Collection("rides").CountDocuments(ctx, bson.M{})
	availableRides, _ := config.Database.Collection("rides").CountDocuments(ctx, bson.M{"status": "available"})
	completedRides, _ := config.Database.Collection("rides").CountDocuments(ctx, bson.M{"status": "completed"})
	cancelledRides, _ := config.Database.Collection("rides").CountDocuments(ctx, bson.M{"status": "cancelled"})

	// --- Bookings ---
	totalBookings, _ := config.Database.Collection("bookings").CountDocuments(ctx, bson.M{})
	pendingBookings, _ := config.Database.Collection("bookings").CountDocuments(ctx, bson.M{"status": "pending"})
	acceptedBookings, _ := config.Database.Collection("bookings").CountDocuments(ctx, bson.M{"status": "accepted"})
	rejectedBookings, _ := config.Database.Collection("bookings").CountDocuments(ctx, bson.M{"status": "rejected"})

	// --- Users ---
	totalUsers, _ := config.Database.Collection("users").CountDocuments(ctx, bson.M{})
	totalDrivers, _ := config.Database.Collection("users").CountDocuments(ctx, bson.M{"role": "driver"})
	totalPassengers, _ := config.Database.Collection("users").CountDocuments(ctx, bson.M{"role": "passenger"})

	// --- Routes (unique pickup-dropoff pairs from rides) ---
	routePipeline := mongo.Pipeline{
		{{Key: "$group", Value: bson.M{"_id": bson.M{"pickup": "$pickup", "dropoff": "$dropoff"}}}},
		{{Key: "$count", Value: "count"}},
	}
	routeCursor, err := config.Database.Collection("rides").Aggregate(ctx, routePipeline)
	routeCount := int64(0)
	if err == nil {
		var routeResult []bson.M
		routeCursor.All(ctx, &routeResult)
		if len(routeResult) > 0 {
			if v, ok := routeResult[0]["count"].(int32); ok {
				routeCount = int64(v)
			}
		}
	}

	// --- Parcels (Bookings with type "parcel") ---
	totalParcels, _ := config.Database.Collection("bookings").CountDocuments(ctx, bson.M{"type": "parcel"})
	pendingParcels, _ := config.Database.Collection("bookings").CountDocuments(ctx, bson.M{"type": "parcel", "status": "pending"})
	shippedParcels, _ := config.Database.Collection("bookings").CountDocuments(ctx, bson.M{"type": "parcel", "status": "accepted"})

	// --- Monthly ride trend for the past 6 months ---
	now := time.Now()
	monthlyTrend := make([]map[string]interface{}, 6)
	for i := 5; i >= 0; i-- {
		monthStart := time.Date(now.Year(), now.Month()-time.Month(i), 1, 0, 0, 0, 0, time.UTC)
		monthEnd := monthStart.AddDate(0, 1, 0).Add(-time.Nanosecond)

		count, _ := config.Database.Collection("rides").CountDocuments(ctx, bson.M{
			"createdAt": bson.M{"$gte": monthStart, "$lte": monthEnd},
		})

		monthlyTrend[5-i] = map[string]interface{}{
			"month": monthStart.Format("Jan"),
			"count": count,
			"rides": count,
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"rides": bson.M{
			"total":     totalRides,
			"available": availableRides,
			"completed": completedRides,
			"cancelled": cancelledRides,
		},
		"bookings": bson.M{
			"total":    totalBookings,
			"pending":  pendingBookings,
			"accepted": acceptedBookings,
			"rejected": rejectedBookings,
		},
		"parcels": bson.M{
			"total":   totalParcels,
			"pending": pendingParcels,
			"shipped": shippedParcels,
		},
		"users": bson.M{
			"total":      totalUsers,
			"drivers":    totalDrivers,
			"passengers": totalPassengers,
		},
		"routes":       routeCount,
		"trends":       monthlyTrend,
		"monthlyTrend": monthlyTrend,
	})
}

// AdminParcels returns a list of all parcel bookings for the admin dashboard.
func AdminParcels(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	cursor, err := config.Database.Collection("bookings").Find(
		ctx,
		bson.M{"type": "parcel"},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(100),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch parcels"})
		return
	}

	var parcels []models.Booking
	cursor.All(ctx, &parcels)

	type ParcelRow struct {
		ID            string `json:"id"`
		Type          string `json:"type"`
		Sender        string `json:"sender"`
		Recipient     string `json:"recipient"`
		Pickup        string `json:"pickup"`
		Dropoff       string `json:"dropoff"`
		Status        string `json:"status"`
		Price         string `json:"price"`
		Date          string `json:"date"`
		ContactNumber string `json:"contactNumber"`
	}

	var result []ParcelRow
	for _, p := range parcels {
		var sender struct {
			Name string `bson:"name"`
		}
		config.Database.Collection("users").FindOne(ctx, bson.M{"_id": p.PassengerID}).Decode(&sender)

		result = append(result, ParcelRow{
			ID:            "RA-P-" + p.ID.Hex()[len(p.ID.Hex())-4:],
			Type:          p.ParcelSize,
			Sender:        sender.Name,
			Recipient:     p.RecipientName,
			Pickup:        p.Pickup,
			Dropoff:       p.Dropoff,
			Status:        p.Status,
			Price:         p.Price,
			Date:          p.CreatedAt.Format("Jan 2, 2006"),
			ContactNumber: p.ContactNumber,
		})
	}

	if result == nil {
		result = []ParcelRow{}
	}
	c.JSON(http.StatusOK, result)
}

// AdminUsersList returns all registered users with role, join date and ride count.
func AdminUsersList(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	cursor, err := config.Database.Collection("users").Find(
		ctx,
		bson.M{},
		options.Find().SetSort(bson.D{{Key: "_id", Value: -1}}).SetLimit(200),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch users"})
		return
	}

	var users []models.User
	cursor.All(ctx, &users)

	type UserRow struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Phone      string `json:"phone"`
		Role       string `json:"role"`
		JoinedAt   string `json:"joinedAt"`
		TotalRides int64  `json:"totalRides"`
	}

	var result []UserRow
	for _, u := range users {
		rideField := "passengerId"
		if u.Role == "driver" {
			rideField = "driverId"
		}
		rideCount, _ := config.Database.Collection("rides").CountDocuments(ctx, bson.M{rideField: u.ID})

		result = append(result, UserRow{
			ID:         u.ID.Hex(),
			Name:       u.Name,
			Phone:      u.PhoneNumber,
			Role:       u.Role,
			JoinedAt:   u.ID.Timestamp().Format("Jan 2, 2006"),
			TotalRides: rideCount,
		})
	}

	if result == nil {
		result = []UserRow{}
	}
	c.JSON(http.StatusOK, result)
}

// AdminCleanupIncompleteUsers deletes abandoned registrations (missing name or role older than 24 hours).
func AdminCleanupIncompleteUsers(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	cutoff := time.Now().Add(-24 * time.Hour)
	force := c.Query("force") == "true"

	var filter bson.M
	if force {
		filter = bson.M{
			"$or": []bson.M{
				{"name": ""},
				{"name": bson.M{"$exists": false}},
				{"role": ""},
				{"role": bson.M{"$exists": false}},
			},
		}
	} else {
		filter = bson.M{
			"$or": []bson.M{
				{"name": "", "submitted_at": bson.M{"$lt": cutoff}},
				{"name": bson.M{"$exists": false}, "submitted_at": bson.M{"$lt": cutoff}},
				{"role": "", "submitted_at": bson.M{"$lt": cutoff}},
				{"role": bson.M{"$exists": false}, "submitted_at": bson.M{"$lt": cutoff}},
			},
		}
	}

	result, err := config.Database.Collection("users").DeleteMany(ctx, filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete incomplete user registrations"})
		return
	}

	log.Printf("[ADMIN CLEANUP] Deleted %d incomplete user records (force=%v)", result.DeletedCount, force)
	c.JSON(http.StatusOK, gin.H{
		"message":      "Incomplete registrations cleaned up successfully",
		"deletedCount": result.DeletedCount,
	})
}

// AdminReports generates and streams a CSV report for the given type.
func AdminReports(c *gin.Context) {
	reportType := c.Param("type")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	c.Header("Content-Type", "text/csv")

	switch reportType {
	case "daily_bookings":
		c.Header("Content-Disposition", `attachment; filename="daily_bookings.csv"`)
		since := time.Now().AddDate(0, 0, -30)
		cursor, _ := config.Database.Collection("bookings").Find(ctx, bson.M{"createdAt": bson.M{"$gte": since}})
		var bookings []models.Booking
		cursor.All(ctx, &bookings)
		c.String(http.StatusOK, "BookingID,Date,Route,Type,Status\n")
		for _, b := range bookings {
			c.String(http.StatusOK, fmt.Sprintf("%s,%s,%s → %s,%s,%s\n", b.ID.Hex(), b.CreatedAt.Format("2006-01-02"), b.Pickup, b.Dropoff, b.Type, b.Status))
		}
	case "revenue":
		c.Header("Content-Disposition", `attachment; filename="revenue.csv"`)
		c.String(http.StatusOK, "Route,Bookings,Revenue (₹)\n")

		cursor, _ := config.Database.Collection("bookings").Find(ctx, bson.M{"status": "accepted"})
		var bookings []models.Booking
		cursor.All(ctx, &bookings)

		type routeAgg struct {
			count int
			total float64
		}
		routeMap := make(map[string]*routeAgg)
		for _, b := range bookings {
			routeKey := fmt.Sprintf("%s → %s", b.Pickup, b.Dropoff)
			if _, exists := routeMap[routeKey]; !exists {
				routeMap[routeKey] = &routeAgg{}
			}
			routeMap[routeKey].count++
			routeMap[routeKey].total += parsePriceFloat(b.Price)
		}

		for routeKey, agg := range routeMap {
			c.String(http.StatusOK, fmt.Sprintf("%s,%d,%.2f\n", sanitizeCSV(routeKey), agg.count, agg.total))
		}
	case "payouts":
		c.Header("Content-Disposition", `attachment; filename="payouts.csv"`)
		c.String(http.StatusOK, "DriverID,Status,TotalPayout (₹)\n")

		cursor, _ := config.Database.Collection("bookings").Find(ctx, bson.M{"status": "accepted"})
		var bookings []models.Booking
		cursor.All(ctx, &bookings)

		payoutMap := make(map[primitive.ObjectID]float64)
		for _, b := range bookings {
			payoutMap[b.RideID] += parsePriceFloat(b.Price)
		}

		for rideID, total := range payoutMap {
			c.String(http.StatusOK, fmt.Sprintf("%s,Pending,%.2f\n", rideID.Hex(), total))
		}
	default:
		c.String(http.StatusBadRequest, "Unknown report type")
	}
}

var priceRegex = regexp.MustCompile(`[0-9]+(\.[0-9]+)?`)

func parsePriceFloat(s string) float64 {
	match := priceRegex.FindString(s)
	if match == "" {
		return 0
	}
	v, _ := strconv.ParseFloat(match, 64)
	return v
}


// sanitizeCSV prevents CSV injection attacks by escaping dangerous cell prefixes.
// If a cell starts with =, +, -, @, tab, or carriage return, Excel may interpret
// it as a formula — prepend a single quote to neutralise it.
func sanitizeCSV(s string) string {
	if len(s) > 0 {
		switch s[0] {
		case '=', '+', '-', '@', '\t', '\r':
			s = "'" + s
		}
	}
	return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
}
func itoa(v int) string           { return fmt.Sprintf("%d", v) }
func fmtFloat(f float64) string   { return fmt.Sprintf("%.2f", f) }

// Restoring the missing functions
func AdminBookings(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	cursor, _ := config.Database.Collection("bookings").Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(100))
	var bookings []models.Booking
	cursor.All(ctx, &bookings)
	type BookingRow struct {
		ID             string `json:"id"`
		BookingID      string `json:"bookingId"`
		PassengerName  string `json:"passengerName"`
		PassengerPhone string `json:"passengerPhone"`
		DriverName     string `json:"driverName"`
		Ride           string `json:"ride"`
		Status         string `json:"status"`
		Seats          int    `json:"seats"`
		CreatedAt      string `json:"createdAt"`
	}
	var result []BookingRow
	for _, b := range bookings {
		var p models.User
		config.Database.Collection("users").FindOne(ctx, bson.M{"_id": b.PassengerID}).Decode(&p)
		var r models.Ride
		config.Database.Collection("rides").FindOne(ctx, bson.M{"_id": b.RideID}).Decode(&r)
		result = append(result, BookingRow{
			ID:             b.ID.Hex(),
			BookingID:      b.BookingID,
			PassengerName:  p.Name,
			PassengerPhone: p.PhoneNumber,
			DriverName:     r.DriverName,
			Ride:           r.Pickup + " → " + r.Dropoff,
			Status:         b.Status,
			Seats:          b.SeatsRequested,
			CreatedAt:      b.CreatedAt.Format("Jan 2, 2006"),
		})
	}
	if result == nil {
		result = []BookingRow{}
	}
	c.JSON(http.StatusOK, result)
}

// AdminGetBookingDetails fetches complete details for a booking by BID (e.g. Go-0047) or Mongo ID.
func AdminGetBookingDetails(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	queryVal := strings.TrimSpace(c.Query("bid"))
	if queryVal == "" {
		queryVal = strings.TrimSpace(c.Query("id"))
	}

	if queryVal == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Booking ID or BID is required"})
		return
	}

	var filter bson.M
	if objID, err := primitive.ObjectIDFromHex(queryVal); err == nil {
		filter = bson.M{
			"$or": []bson.M{
				{"_id": objID},
				{"bookingId": queryVal},
			},
		}
	} else {
		normalizedBID := queryVal
		cleanNum := strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(queryVal), "go-"), "go"), "0")
		if numVal, parseErr := strconv.Atoi(cleanNum); parseErr == nil && numVal > 0 {
			normalizedBID = fmt.Sprintf("Go-%04d", numVal)
		}

		filter = bson.M{
			"$or": []bson.M{
				{"bookingId": queryVal},
				{"bookingId": normalizedBID},
				{"bookingId": bson.M{"$regex": primitive.Regex{Pattern: "(?i)^" + regexp.QuoteMeta(queryVal) + "$", Options: ""}}},
			},
		}
	}

	var b models.Booking
	err := config.Database.Collection("bookings").FindOne(ctx, filter).Decode(&b)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Booking not found with specified BID or ID"})
		return
	}

	// Fetch Passenger
	var p models.User
	_ = config.Database.Collection("users").FindOne(ctx, bson.M{"_id": b.PassengerID}).Decode(&p)

	// Fetch Ride
	var r models.Ride
	_ = config.Database.Collection("rides").FindOne(ctx, bson.M{"_id": b.RideID}).Decode(&r)

	// Fetch Driver
	var d models.User
	if !r.DriverID.IsZero() {
		_ = config.Database.Collection("users").FindOne(ctx, bson.M{"_id": r.DriverID}).Decode(&d)
	}

	c.JSON(http.StatusOK, gin.H{
		"id":             b.ID.Hex(),
		"bookingId":      b.BookingID,
		"type":           b.Type,
		"pickup":         b.Pickup,
		"dropoff":        b.Dropoff,
		"seatsRequested": b.SeatsRequested,
		"seatLayout":     b.SeatLayout,
		"roofCarrier":    b.RoofCarrier,
		"motionSickness": b.MotionSickness,
		"price":          b.Price,
		"status":         b.Status,
		"createdAt":      b.CreatedAt,
		"completedAt":    b.CompletedAt,

		"passenger": gin.H{
			"id":    p.ID.Hex(),
			"name":  p.Name,
			"phone": p.PhoneNumber,
		},
		"driver": gin.H{
			"id":            d.ID.Hex(),
			"name":          r.DriverName,
			"phone":         d.PhoneNumber,
			"vehicleModel":  r.VehicleModel,
			"vehicleNumber": r.VehicleNumber,
		},
		"ride": gin.H{
			"id":            r.ID.Hex(),
			"date":          r.Date,
			"departureTime": r.DepartureTime,
			"pickup":        r.Pickup,
			"dropoff":       r.Dropoff,
			"pricePerSeat":  r.PricePerSeat,
			"status":        r.Status,
		},
		"parcel": gin.H{
			"parcelSize":    b.ParcelSize,
			"recipientName": b.RecipientName,
			"contactNumber": b.ContactNumber,
			"dropLocation":  b.DropLocation,
			"notes":         b.Notes,
			"photoUrl":      b.PhotoUrl,
		},
	})
}

func AdminDrivers(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	cursor, _ := config.Database.Collection("users").Find(ctx, bson.M{"role": "driver"}, options.Find().SetSort(bson.D{{Key: "submitted_at", Value: -1}}))
	var drivers []models.User
	cursor.All(ctx, &drivers)

	type DriverRow struct {
		ID                 string `json:"id"`
		Name               string `json:"name"`
		Phone              string `json:"phone"`
		Location           string `json:"location"`
		VerificationStatus string `json:"verificationStatus"`
		RejectionReason    string `json:"rejectionReason"`
		SubmittedAt        string `json:"submittedAt"`
		VehicleName        string `json:"vehicleName"`
		VehicleNumber      string `json:"vehicleNumber"`
		VehicleType        string `json:"vehicleType"`
		Seats              int    `json:"seats"`
		SeatingLayout      string `json:"seatingLayout"`
		DLUrl              string `json:"dlUrl"`
		RCUrl              string `json:"rcUrl"`
		PollutionUrl       string `json:"pollutionUrl"`
		VehicleImageUrl    string `json:"vehicleImageUrl"`
		OwnershipUrl       string `json:"ownershipUrl"`
		TotalRides         int64  `json:"totalRides"`
		CurrentRide        string `json:"currentRide"`
	}

	var result []DriverRow
	for _, d := range drivers {
		totalRides, _ := config.Database.Collection("rides").CountDocuments(ctx, bson.M{"driverId": d.ID})

		status := d.VerificationStatus
		if status == "" {
			status = "pending"
		}

		location := d.Location
		if location == "" {
			location = "Bageshwar"
		}

		submittedAtStr := ""
		if !d.SubmittedAt.IsZero() {
			submittedAtStr = d.SubmittedAt.Format("02 Jan 2006, 03:04 PM")
		} else {
			submittedAtStr = "Recent"
		}

		row := DriverRow{
			ID:                 d.ID.Hex(),
			Name:               d.Name,
			Phone:              d.PhoneNumber,
			Location:           location,
			VerificationStatus: status,
			RejectionReason:    d.RejectionReason,
			SubmittedAt:        submittedAtStr,
			TotalRides:         totalRides,
		}

		if d.Vehicle != nil {
			row.VehicleName = d.Vehicle.VehicleName
			row.VehicleNumber = d.Vehicle.VehicleNumber
			row.VehicleType = d.Vehicle.VehicleType
			row.Seats = d.Vehicle.Seats
			row.SeatingLayout = d.Vehicle.SeatingLayout
			row.DLUrl = d.Vehicle.DLUrl
			row.RCUrl = d.Vehicle.RCUrl
			row.PollutionUrl = d.Vehicle.PollutionUrl
			row.VehicleImageUrl = d.Vehicle.VehicleImageUrl
			row.OwnershipUrl = d.Vehicle.OwnershipUrl
		}

		result = append(result, row)
	}

	if result == nil {
		result = []DriverRow{}
	}
	c.JSON(http.StatusOK, result)
}

func AdminVerifyDriver(c *gin.Context) {
	driverIdHex := c.Param("driverId")
	driverId, err := primitive.ObjectIDFromHex(driverIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid driver ID"})
		return
	}

	var body struct {
		Status string `json:"status" binding:"required,oneof=verified rejected pending"`
		Reason string `json:"reason"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid verification status"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	update := bson.M{
		"$set": bson.M{
			"verification_status": body.Status,
			"rejection_reason":    body.Reason,
		},
	}

	res, err := config.Database.Collection("users").UpdateOne(ctx, bson.M{"_id": driverId}, update)
	if err != nil || res.MatchedCount == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update driver verification status"})
		return
	}

	// Send Notification to Driver
	var title, msg string
	if body.Status == "verified" {
		title = "Documents Approved"
		msg = "Your documents have been verified. You can now post rides and accept bookings!"
	} else if body.Status == "rejected" {
		title = "Documents Rejected"
		msg = "Your documents were rejected. Reason: " + body.Reason
	}
	if title != "" {
		CreateNotification(driverId, title, msg, "document_verification", "", nil)
	}

	c.JSON(http.StatusOK, gin.H{"message": "Driver verification status updated", "status": body.Status})
}

// AdminDeleteDriver permanently deletes a driver and all associated data (rides, bookings).
func AdminDeleteDriver(c *gin.Context) {
	driverIdHex := c.Param("driverId")
	driverId, err := primitive.ObjectIDFromHex(driverIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid driver ID"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	// Verify the user exists and is actually a driver
	var driver models.User
	err = config.Database.Collection("users").FindOne(ctx, bson.M{"_id": driverId, "role": "driver"}).Decode(&driver)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Driver not found"})
		return
	}

	// Delete all bookings associated with rides by this driver
	ridesCursor, _ := config.Database.Collection("rides").Find(ctx, bson.M{"driverId": driverId}, options.Find().SetProjection(bson.M{"_id": 1}))
	var rideIds []primitive.ObjectID
	for ridesCursor.Next(ctx) {
		var r struct {
			ID primitive.ObjectID `bson:"_id"`
		}
		ridesCursor.Decode(&r)
		rideIds = append(rideIds, r.ID)
	}

	deletedBookings := int64(0)
	if len(rideIds) > 0 {
		bookingResult, _ := config.Database.Collection("bookings").DeleteMany(ctx, bson.M{"rideId": bson.M{"$in": rideIds}})
		if bookingResult != nil {
			deletedBookings = bookingResult.DeletedCount
		}
	}

	// Delete all rides by this driver
	rideResult, _ := config.Database.Collection("rides").DeleteMany(ctx, bson.M{"driverId": driverId})
	deletedRides := int64(0)
	if rideResult != nil {
		deletedRides = rideResult.DeletedCount
	}

	// Delete the driver user document
	_, err = config.Database.Collection("users").DeleteOne(ctx, bson.M{"_id": driverId})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete driver"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":         fmt.Sprintf("Driver %s deleted successfully", driver.Name),
		"deletedRides":    deletedRides,
		"deletedBookings": deletedBookings,
	})
}

func AdminRidesList(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	cursor, _ := config.Database.Collection("rides").Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(50))
	var rides []models.Ride
	cursor.All(ctx, &rides)

	// Fetch all accepted bookings in one query for efficiency
	bookingCursor, _ := config.Database.Collection("bookings").Find(ctx, bson.M{"status": "accepted"})
	var allBookings []models.Booking
	bookingCursor.All(ctx, &allBookings)

	// Build a map: rideId -> count of accepted booked seats (from actual SeatLayout)
	bookedSeatCount := make(map[primitive.ObjectID]int)
	for _, b := range allBookings {
		for range b.SeatLayout {
			bookedSeatCount[b.RideID]++
		}
	}

	type RideRow struct {
		ID         string  `json:"id"`
		Driver     string  `json:"driver"`
		Route      string  `json:"route"`
		Date       string  `json:"date"`
		Time       string  `json:"departureTime"`
		Seats      int     `json:"seatsTotal"`
		Booked     int     `json:"seatsBooked"`
		Price      float64 `json:"pricePerSeat"`
		Status     string  `json:"status"`
		DistanceKm float64 `json:"distanceKm"`
	}
	var result []RideRow
	for _, r := range rides {
		// Use live count from bookings instead of the potentially stale seatsBooked field
		liveBooked := bookedSeatCount[r.ID]
		result = append(result, RideRow{ID: r.ID.Hex(), Driver: r.DriverName, Route: r.Pickup + " → " + r.Dropoff, Date: r.Date, Time: r.DepartureTime, Seats: r.SeatsTotal, Booked: liveBooked, Price: r.PricePerSeat, Status: r.Status, DistanceKm: r.TotalDistanceM / 1000})
	}
	if result == nil {
		result = []RideRow{}
	}
	c.JSON(http.StatusOK, result)
}

func AdminRoutesAnalytics(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	pipeline := mongo.Pipeline{{{Key: "$group", Value: bson.M{"_id": bson.M{"pickup": "$pickup", "dropoff": "$dropoff"}, "totalRides": bson.M{"$sum": 1}, "topDriver": bson.M{"$first": "$driverName"}, "avgPrice": bson.M{"$avg": "$pricePerSeat"}}}}}
	cursor, _ := config.Database.Collection("rides").Aggregate(ctx, pipeline)
	var raw []bson.M
	cursor.All(ctx, &raw)
	type RouteRow struct {
		ID            string  `json:"id"`
		Route         string  `json:"route"`
		Bookings      int32   `json:"bookings"`
		Cancellations int32   `json:"cancellations"`
		TopDriver     string  `json:"topDriver"`
		AvgPrice      float64 `json:"avgPrice"`
		Status        string  `json:"status"`
	}
	var result []RouteRow
	for _, r := range raw {
		idMap, _ := r["_id"].(primitive.M)
		pickup := idMap["pickup"].(string)
		dropoff := idMap["dropoff"].(string)

		cancelledCount, _ := config.Database.Collection("rides").CountDocuments(ctx, bson.M{
			"pickup":  pickup,
			"dropoff": dropoff,
			"status":  "cancelled",
		})

		result = append(result, RouteRow{
			ID:            "RT-" + primitive.NewObjectID().Hex()[:4],
			Route:         pickup + " ⇄ " + dropoff,
			Bookings:      r["totalRides"].(int32),
			Cancellations: int32(cancelledCount),
			TopDriver:     r["topDriver"].(string),
			AvgPrice:      r["avgPrice"].(float64),
			Status:        "Active",
		})
	}
	if result == nil {
		result = []RouteRow{}
	}
	c.JSON(http.StatusOK, result)
}
