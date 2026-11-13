package controllers

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	availableRides, _ := config.Database.Collection("rides").CountDocuments(ctx, bson.M{"status": string(models.RideStatusAvailable)})
	ongoingRides, _ := config.Database.Collection("rides").CountDocuments(ctx, bson.M{"status": string(models.RideStatusOngoing)})
	completedRides, _ := config.Database.Collection("rides").CountDocuments(ctx, bson.M{"status": string(models.RideStatusCompleted)})
	cancelledRides, _ := config.Database.Collection("rides").CountDocuments(ctx, bson.M{"status": string(models.RideStatusCancelled)})

	// --- Bookings ---
	totalBookings, _ := config.Database.Collection("bookings").CountDocuments(ctx, bson.M{})
	pendingBookings, _ := config.Database.Collection("bookings").CountDocuments(ctx, bson.M{"status": string(models.BookingStatusPending)})
	acceptedBookings, _ := config.Database.Collection("bookings").CountDocuments(ctx, bson.M{"status": string(models.BookingStatusAccepted)})
	rejectedBookings, _ := config.Database.Collection("bookings").CountDocuments(ctx, bson.M{"status": string(models.BookingStatusRejected)})
	completedBookings, _ := config.Database.Collection("bookings").CountDocuments(ctx, bson.M{"status": string(models.BookingStatusCompleted)})
	cancelledBookings, _ := config.Database.Collection("bookings").CountDocuments(ctx, bson.M{"status": string(models.BookingStatusCancelled)})

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
	if err == nil && routeCursor != nil {
		var routeResult []bson.M
		if allErr := routeCursor.All(ctx, &routeResult); allErr == nil && len(routeResult) > 0 {
			if v, ok := routeResult[0]["count"].(int32); ok {
				routeCount = int64(v)
			} else if v64, ok := routeResult[0]["count"].(int64); ok {
				routeCount = v64
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
			"ongoing":   ongoingRides,
			"completed": completedRides,
			"cancelled": cancelledRides,
		},
		"bookings": bson.M{
			"total":     totalBookings,
			"pending":   pendingBookings,
			"accepted":  acceptedBookings,
			"rejected":  rejectedBookings,
			"completed": completedBookings,
			"cancelled": cancelledBookings,
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
	if cursor != nil {
		if err := cursor.All(ctx, &parcels); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse parcels"})
			return
		}
	}

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

	// PERF: Load all senders in ONE query instead of one query per parcel (N+1).
	senderIDs := make([]primitive.ObjectID, 0, len(parcels))
	seenSender := make(map[primitive.ObjectID]bool, len(parcels))
	for _, p := range parcels {
		if !seenSender[p.PassengerID] {
			seenSender[p.PassengerID] = true
			senderIDs = append(senderIDs, p.PassengerID)
		}
	}
	senderNames := make(map[primitive.ObjectID]string, len(senderIDs))
	if len(senderIDs) > 0 {
		sCursor, sErr := config.Database.Collection("users").Find(ctx,
			bson.M{"_id": bson.M{"$in": senderIDs}},
			options.Find().SetProjection(bson.M{"name": 1}),
		)
		if sErr == nil && sCursor != nil {
			var senders []struct {
				ID   primitive.ObjectID `bson:"_id"`
				Name string             `bson:"name"`
			}
			if err := sCursor.All(ctx, &senders); err == nil {
				for _, s := range senders {
					senderNames[s.ID] = s.Name
				}
			}
		}
	}

	var result []ParcelRow
	for _, p := range parcels {
		sender := struct{ Name string }{Name: senderNames[p.PassengerID]}

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
	if cursor != nil {
		if err := cursor.All(ctx, &users); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse users"})
			return
		}
	}

	// PERF: Batch count driver rides and passenger bookings using Mongo aggregation
	// instead of executing N+1 queries in a loop.
	driverIDs := make([]primitive.ObjectID, 0)
	passengerIDs := make([]primitive.ObjectID, 0)
	for _, u := range users {
		if u.Role == "driver" {
			driverIDs = append(driverIDs, u.ID)
		} else {
			passengerIDs = append(passengerIDs, u.ID)
		}
	}

	driverRideCounts := make(map[primitive.ObjectID]int64)
	if len(driverIDs) > 0 {
		pipeline := mongo.Pipeline{
			{{Key: "$match", Value: bson.M{"driverId": bson.M{"$in": driverIDs}}}},
			{{Key: "$group", Value: bson.M{"_id": "$driverId", "count": bson.M{"$sum": 1}}}},
		}
		if dCursor, dErr := config.Database.Collection("rides").Aggregate(ctx, pipeline); dErr == nil && dCursor != nil {
			var dResults []struct {
				ID    primitive.ObjectID `bson:"_id"`
				Count int64              `bson:"count"`
			}
			if dCursor.All(ctx, &dResults) == nil {
				for _, res := range dResults {
					driverRideCounts[res.ID] = res.Count
				}
			}
		}
	}

	passengerBookingCounts := make(map[primitive.ObjectID]int64)
	if len(passengerIDs) > 0 {
		pipeline := mongo.Pipeline{
			{{Key: "$match", Value: bson.M{"passengerId": bson.M{"$in": passengerIDs}}}},
			{{Key: "$group", Value: bson.M{"_id": "$passengerId", "count": bson.M{"$sum": 1}}}},
		}
		if pCursor, pErr := config.Database.Collection("bookings").Aggregate(ctx, pipeline); pErr == nil && pCursor != nil {
			var pResults []struct {
				ID    primitive.ObjectID `bson:"_id"`
				Count int64              `bson:"count"`
			}
			if pCursor.All(ctx, &pResults) == nil {
				for _, res := range pResults {
					passengerBookingCounts[res.ID] = res.Count
				}
			}
		}
	}

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
		totalRides := int64(0)
		if u.Role == "driver" {
			totalRides = driverRideCounts[u.ID]
		} else {
			totalRides = passengerBookingCounts[u.ID]
		}

		result = append(result, UserRow{
			ID:         u.ID.Hex(),
			Name:       u.Name,
			Phone:      u.PhoneNumber,
			Role:       u.Role,
			JoinedAt:   u.ID.Timestamp().Format("Jan 2, 2006"),
			TotalRides: totalRides,
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

	switch reportType {
	case "daily_bookings":
		since := time.Now().AddDate(0, 0, -30)
		cursor, err := config.Database.Collection("bookings").Find(ctx, bson.M{"createdAt": bson.M{"$gte": since}})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch bookings report"})
			return
		}
		var bookings []models.Booking
		if cursor != nil {
			if err := cursor.All(ctx, &bookings); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse bookings report"})
				return
			}
		}
		c.Header("Content-Type", "text/csv")
		c.Header("Content-Disposition", `attachment; filename="daily_bookings.csv"`)
		c.String(http.StatusOK, "BookingID,Date,Route,Type,Status\n")
		for _, b := range bookings {
			routeStr := sanitizeCSV(fmt.Sprintf("%s → %s", b.Pickup, b.Dropoff))
			c.String(http.StatusOK, fmt.Sprintf("%s,%s,%s,%s,%s\n", b.ID.Hex(), b.CreatedAt.Format("2006-01-02"), routeStr, sanitizeCSV(b.Type), sanitizeCSV(b.Status)))
		}
	case "revenue":
		cursor, err := config.Database.Collection("bookings").Find(ctx, bson.M{
			"status": bson.M{"$in": []string{string(models.BookingStatusAccepted), string(models.BookingStatusCompleted)}},
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch revenue report"})
			return
		}
		var bookings []models.Booking
		if cursor != nil {
			if err := cursor.All(ctx, &bookings); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse revenue report"})
				return
			}
		}

		c.Header("Content-Type", "text/csv")
		c.Header("Content-Disposition", `attachment; filename="revenue.csv"`)
		c.String(http.StatusOK, "Route,Bookings,Revenue (₹)\n")

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
		cursor, err := config.Database.Collection("bookings").Find(ctx, bson.M{
			"status": bson.M{"$in": []string{string(models.BookingStatusAccepted), string(models.BookingStatusCompleted)}},
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch payouts report"})
			return
		}
		var bookings []models.Booking
		if cursor != nil {
			if err := cursor.All(ctx, &bookings); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse payouts report"})
				return
			}
		}

		c.Header("Content-Type", "text/csv")
		c.Header("Content-Disposition", `attachment; filename="payouts.csv"`)
		c.String(http.StatusOK, "RideID,Status,TotalPayout (₹)\n")

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
	cursor, err := config.Database.Collection("bookings").Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(100))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch bookings"})
		return
	}
	var bookings []models.Booking
	if cursor != nil {
		if err := cursor.All(ctx, &bookings); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse bookings"})
			return
		}
	}
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
	// PERF: Batch load passengers and rides in two $in queries instead of N+1 FindOne queries.
	passengerIDs := make([]primitive.ObjectID, 0, len(bookings))
	seenPassenger := make(map[primitive.ObjectID]bool, len(bookings))
	rideIDs := make([]primitive.ObjectID, 0, len(bookings))
	seenRide := make(map[primitive.ObjectID]bool, len(bookings))
	for _, b := range bookings {
		if !b.PassengerID.IsZero() && !seenPassenger[b.PassengerID] {
			seenPassenger[b.PassengerID] = true
			passengerIDs = append(passengerIDs, b.PassengerID)
		}
		if !b.RideID.IsZero() && !seenRide[b.RideID] {
			seenRide[b.RideID] = true
			rideIDs = append(rideIDs, b.RideID)
		}
	}

	passengerMap := make(map[primitive.ObjectID]models.User, len(passengerIDs))
	if len(passengerIDs) > 0 {
		uCursor, uErr := config.Database.Collection("users").Find(
			ctx,
			bson.M{"_id": bson.M{"$in": passengerIDs}},
			options.Find().SetProjection(bson.M{"name": 1, "phone_number": 1}),
		)
		if uErr == nil && uCursor != nil {
			var passengers []models.User
			if uCursor.All(ctx, &passengers) == nil {
				for _, p := range passengers {
					passengerMap[p.ID] = p
				}
			}
			uCursor.Close(ctx)
		}
	}

	rideMap := make(map[primitive.ObjectID]models.Ride, len(rideIDs))
	if len(rideIDs) > 0 {
		rCursor, rErr := config.Database.Collection("rides").Find(
			ctx,
			bson.M{"_id": bson.M{"$in": rideIDs}},
			options.Find().SetProjection(bson.M{"driverName": 1, "pickup": 1, "dropoff": 1}),
		)
		if rErr == nil && rCursor != nil {
			var rides []models.Ride
			if rCursor.All(ctx, &rides) == nil {
				for _, r := range rides {
					rideMap[r.ID] = r
				}
			}
			rCursor.Close(ctx)
		}
	}

	var result []BookingRow
	for _, b := range bookings {
		p := passengerMap[b.PassengerID]
		r := rideMap[b.RideID]
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
	cursor, err := config.Database.Collection("users").Find(ctx, bson.M{"role": "driver"}, options.Find().SetSort(bson.D{{Key: "submitted_at", Value: -1}}))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch drivers"})
		return
	}
	var drivers []models.User
	if cursor != nil {
		if err := cursor.All(ctx, &drivers); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse drivers"})
			return
		}
	}

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

	// PERF: Batch count driver rides in ONE aggregation instead of N+1 CountDocuments queries.
	driverIDs := make([]primitive.ObjectID, 0, len(drivers))
	for _, d := range drivers {
		driverIDs = append(driverIDs, d.ID)
	}
	driverRideCounts := make(map[primitive.ObjectID]int64, len(driverIDs))
	if len(driverIDs) > 0 {
		pipeline := mongo.Pipeline{
			{{Key: "$match", Value: bson.M{"driverId": bson.M{"$in": driverIDs}}}},
			{{Key: "$group", Value: bson.M{"_id": "$driverId", "count": bson.M{"$sum": 1}}}},
		}
		if dCursor, dErr := config.Database.Collection("rides").Aggregate(ctx, pipeline); dErr == nil && dCursor != nil {
			var counts []struct {
				ID    primitive.ObjectID `bson:"_id"`
				Count int64              `bson:"count"`
			}
			if dCursor.All(ctx, &counts) == nil {
				for _, c := range counts {
					driverRideCounts[c.ID] = c.Count
				}
			}
			dCursor.Close(ctx)
		}
	}

	var result []DriverRow
	for _, d := range drivers {
		totalRides := driverRideCounts[d.ID]

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

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	var driver models.User
	err = config.Database.Collection("users").FindOne(ctx, bson.M{"_id": driverId, "role": "driver"}).Decode(&driver)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			c.JSON(http.StatusNotFound, gin.H{"error": "Driver not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch driver"})
		return
	}

	fromStatus := models.VerificationStatus(driver.VerificationStatus)
	toStatus := models.VerificationStatus(body.Status)
	if !models.CanTransitionVerificationStatus(fromStatus, toStatus) {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Invalid driver verification transition from %q to %q", fromStatus, toStatus)})
		return
	}
	if fromStatus == models.VerificationStatusNone && toStatus == models.VerificationStatusVerified && driver.Vehicle == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot verify driver without submitted vehicle documents"})
		return
	}

	reasonToSet := strings.TrimSpace(body.Reason)
	if toStatus == models.VerificationStatusVerified {
		reasonToSet = ""
	}

	filter := bson.M{"_id": driverId, "role": "driver"}
	if driver.VerificationStatus == "" {
		filter["$or"] = []bson.M{
			{"verification_status": ""},
			{"verification_status": bson.M{"$exists": false}},
		}
	} else {
		filter["verification_status"] = driver.VerificationStatus
	}

	update := bson.M{
		"$set": bson.M{
			"verification_status": body.Status,
			"rejection_reason":    reasonToSet,
		},
	}

	res, err := config.Database.Collection("users").UpdateOne(ctx, filter, update)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update driver verification status"})
		return
	}
	if res.MatchedCount == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Driver verification status changed concurrently. Please refresh and try again."})
		return
	}

	if fromStatus == models.VerificationStatusVerified && toStatus != models.VerificationStatusVerified {
		if retireErr := retireOpenRides(ctx, driverId); retireErr != nil {
			log.Printf("[AdminVerifyDriver] Failed to retire open rides for demoted driver %s: %v", driverId.Hex(), retireErr)
		}
	}

	// Send Notification to Driver
	var title, msg string
	if body.Status == string(models.VerificationStatusVerified) {
		title = "Documents Approved"
		msg = "Your documents have been verified. You can now post rides and accept bookings!"
	} else if body.Status == string(models.VerificationStatusRejected) {
		title = "Documents Rejected"
		msg = "Your documents were rejected. Reason: " + reasonToSet
	}
	if title != "" {
		CreateNotification(driverId, title, msg, "document_verification", "", nil)
	}

	c.JSON(http.StatusOK, gin.H{"message": "Driver verification status updated", "status": body.Status})
}

// AdminRequestReKYC notifies a driver to re-upload their documents and resets verification status.
func AdminRequestReKYC(c *gin.Context) {
	driverIdHex := c.Param("driverId")
	driverId, err := primitive.ObjectIDFromHex(driverIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid driver ID"})
		return
	}

	var body struct {
		Reason string `json:"reason"`
	}
	if c.Request != nil && c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
			return
		}
	}

	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		reason = "Admin requested KYC re-verification. Please re-upload your driving license, vehicle RC, and other documents."
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	// Update verification status to 'rejected' so the driver app displays the
	// Action Required banner and the "Resubmit Documents" button.
	// Also clear old document URLs so the driver is prompted to upload fresh files.
	update := bson.M{
		"$set": bson.M{
			"verification_status":       "rejected",
			"rejection_reason":          reason,
			"vehicle.dl_url":            "",
			"vehicle.rc_url":            "",
			"vehicle.pollution_url":     "",
			"vehicle.vehicle_image_url": "",
			"vehicle.ownership_url":     "",
		},
	}

	res, err := config.Database.Collection("users").UpdateOne(ctx, bson.M{"_id": driverId, "role": "driver"}, update)
	if err != nil || res.MatchedCount == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update driver or driver not found"})
		return
	}

	if retireErr := retireOpenRides(ctx, driverId); retireErr != nil {
		log.Printf("[AdminRequestReKYC] Failed to retire open rides for driver %s: %v", driverId.Hex(), retireErr)
	}

	// Send Push & In-app Notification to Driver
	title := "Re-KYC Required"
	msg := reason
	CreateNotification(driverId, title, msg, "document_verification", "", map[string]string{
		"action": "rekyc",
	})

	c.JSON(http.StatusOK, gin.H{"message": "Re-KYC request and notification sent to driver successfully"})
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
	ridesCursor, err := config.Database.Collection("rides").Find(ctx, bson.M{"driverId": driverId}, options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch driver rides"})
		return
	}
	var rideIds []primitive.ObjectID
	if ridesCursor != nil {
		for ridesCursor.Next(ctx) {
			var r struct {
				ID primitive.ObjectID `bson:"_id"`
			}
			if ridesCursor.Decode(&r) == nil {
				rideIds = append(rideIds, r.ID)
			}
		}
		ridesCursor.Close(ctx)
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
	cursor, err := config.Database.Collection("rides").Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(50))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch rides"})
		return
	}
	var rides []models.Ride
	if cursor != nil {
		if err := cursor.All(ctx, &rides); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse rides"})
			return
		}
	}

	// Scope accepted bookings query to the fetched ride IDs instead of loading the entire collection.
	rideIDs := make([]primitive.ObjectID, 0, len(rides))
	for _, r := range rides {
		rideIDs = append(rideIDs, r.ID)
	}

	bookedSeatCount := make(map[primitive.ObjectID]int, len(rideIDs))
	if len(rideIDs) > 0 {
		bookingCursor, err := config.Database.Collection("bookings").Find(
			ctx,
			bson.M{"rideId": bson.M{"$in": rideIDs}, "status": "accepted"},
			options.Find().SetProjection(bson.M{"rideId": 1, "seatLayout": 1}),
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch bookings for rides"})
			return
		}
		var allBookings []models.Booking
		if bookingCursor != nil {
			if err := bookingCursor.All(ctx, &allBookings); err != nil {
				bookingCursor.Close(ctx)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse bookings for rides"})
				return
			}
			bookingCursor.Close(ctx)
		}

		// Build a map: rideId -> count of accepted booked seats (from actual SeatLayout)
		for _, b := range allBookings {
			bookedSeatCount[b.RideID] += len(b.SeatLayout)
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

func bsonInt32Val(v interface{}) int32 {
	switch n := v.(type) {
	case int32:
		return n
	case int64:
		return int32(n)
	case int:
		return int32(n)
	default:
		return 0
	}
}

func AdminRoutesAnalytics(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	pipeline := mongo.Pipeline{
		{{Key: "$group", Value: bson.M{
			"_id":           bson.M{"pickup": "$pickup", "dropoff": "$dropoff"},
			"totalRides":    bson.M{"$sum": 1},
			"cancellations": bson.M{"$sum": bson.M{"$cond": []interface{}{bson.M{"$eq": []interface{}{"$status", "cancelled"}}, 1, 0}}},
			"topDriver":     bson.M{"$first": "$driverName"},
			"avgPrice":      bson.M{"$avg": "$pricePerSeat"},
		}}},
	}
	cursor, err := config.Database.Collection("rides").Aggregate(ctx, pipeline)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch route analytics"})
		return
	}
	var raw []bson.M
	if cursor != nil {
		if err := cursor.All(ctx, &raw); err != nil {
			cursor.Close(ctx)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse route analytics"})
			return
		}
		cursor.Close(ctx)
	}
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
		idMap, ok := r["_id"].(primitive.M)
		if !ok {
			continue
		}
		pickup, _ := idMap["pickup"].(string)
		dropoff, _ := idMap["dropoff"].(string)
		totalRides := bsonInt32Val(r["totalRides"])
		cancelledCount := bsonInt32Val(r["cancellations"])
		topDriver, _ := r["topDriver"].(string)
		avgPrice, _ := r["avgPrice"].(float64)

		result = append(result, RouteRow{
			ID:            "RT-" + primitive.NewObjectID().Hex()[:4],
			Route:         pickup + " ⇄ " + dropoff,
			Bookings:      totalRides,
			Cancellations: cancelledCount,
			TopDriver:     topDriver,
			AvgPrice:      avgPrice,
			Status:        "Active",
		})
	}
	if result == nil {
		result = []RouteRow{}
	}
	c.JSON(http.StatusOK, result)
}
