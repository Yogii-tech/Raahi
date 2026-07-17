package controllers

import (
	"context"
	"fmt"
	"net/http"
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
		"routes": routeCount,
		"trends": monthlyTrend,
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

		pipeline := mongo.Pipeline{
			{{Key: "$match", Value: bson.M{"status": "accepted"}}},
			{{Key: "$group", Value: bson.M{
				"_id":   bson.M{"pickup": "$pickup", "dropoff": "$dropoff"},
				"count": bson.M{"$sum": 1},
				"total": bson.M{"$sum": bson.M{"$convert": bson.M{"input": "$price", "to": "double", "onError": 0, "onNull": 0}}},
			}}},
		}
		cursor, _ := config.Database.Collection("bookings").Aggregate(ctx, pipeline)
		var results []bson.M
		cursor.All(ctx, &results)
		for _, r := range results {
			idMap := r["_id"].(primitive.M)
			c.String(http.StatusOK, fmt.Sprintf("%s → %s,%v,%v\n", idMap["pickup"], idMap["dropoff"], r["count"], r["total"]))
		}
	case "payouts":
		c.Header("Content-Disposition", `attachment; filename="payouts.csv"`)
		c.String(http.StatusOK, "DriverID,Status,TotalPayout (₹)\n")

		pipeline := mongo.Pipeline{
			{{Key: "$match", Value: bson.M{"status": "accepted"}}},
			{{Key: "$group", Value: bson.M{
				"_id":   "$rideId",
				"total": bson.M{"$sum": bson.M{"$convert": bson.M{"input": "$price", "to": "double", "onError": 0, "onNull": 0}}},
			}}},
		}
		cursor, _ := config.Database.Collection("bookings").Aggregate(ctx, pipeline)
		var results []bson.M
		cursor.All(ctx, &results)
		for _, r := range results {
			c.String(http.StatusOK, fmt.Sprintf("%s,Pending,%v\n", r["_id"].(primitive.ObjectID).Hex(), r["total"]))
		}
	default:
		c.String(http.StatusBadRequest, "Unknown report type")
	}
}

// SanitizeCSV and other helpers
func sanitizeCSV(s string) string { return "\"" + s + "\"" }
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
		result = append(result, BookingRow{ID: b.ID.Hex(), PassengerName: p.Name, PassengerPhone: p.PhoneNumber, DriverName: r.DriverName, Ride: r.Pickup + " → " + r.Dropoff, Status: b.Status, Seats: b.SeatsRequested, CreatedAt: b.CreatedAt.Format("Jan 2, 2006")})
	}
	if result == nil {
		result = []BookingRow{}
	}
	c.JSON(http.StatusOK, result)
}

func AdminDrivers(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	cursor, _ := config.Database.Collection("users").Find(ctx, bson.M{"role": "driver"})
	var drivers []models.User
	cursor.All(ctx, &drivers)
	type DriverRow struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Phone         string `json:"phone"`
		VehicleName   string `json:"vehicleName"`
		VehicleNumber string `json:"vehicleNumber"`
		VehicleType   string `json:"vehicleType"`
		Seats         int    `json:"seats"`
		TotalRides    int64  `json:"totalRides"`
		CurrentRide   string `json:"currentRide"`
	}
	var result []DriverRow
	for _, d := range drivers {
		totalRides, _ := config.Database.Collection("rides").CountDocuments(ctx, bson.M{"driverId": d.ID})
		row := DriverRow{ID: d.ID.Hex(), Name: d.Name, Phone: d.PhoneNumber, TotalRides: totalRides}
		if d.Vehicle != nil {
			row.VehicleName, row.VehicleNumber, row.VehicleType, row.Seats = d.Vehicle.VehicleName, d.Vehicle.VehicleNumber, d.Vehicle.VehicleType, d.Vehicle.Seats
		}
		result = append(result, row)
	}
	if result == nil {
		result = []DriverRow{}
	}
	c.JSON(http.StatusOK, result)
}

func AdminRidesList(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	cursor, _ := config.Database.Collection("rides").Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(50))
	var rides []models.Ride
	cursor.All(ctx, &rides)
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
		result = append(result, RideRow{ID: r.ID.Hex(), Driver: r.DriverName, Route: r.Pickup + " → " + r.Dropoff, Date: r.Date, Time: r.DepartureTime, Seats: r.SeatsTotal, Booked: r.SeatsBooked, Price: r.PricePerSeat, Status: r.Status, DistanceKm: r.TotalDistanceM / 1000})
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
		ID        string  `json:"id"`
		Route     string  `json:"route"`
		Bookings  int32   `json:"bookings"`
		TopDriver string  `json:"topDriver"`
		AvgPrice  float64 `json:"avgPrice"`
		Status    string  `json:"status"`
	}
	var result []RouteRow
	for _, r := range raw {
		idMap, _ := r["_id"].(primitive.M)
		result = append(result, RouteRow{ID: "RT-" + primitive.NewObjectID().Hex()[:4], Route: idMap["pickup"].(string) + " ⇄ " + idMap["dropoff"].(string), Bookings: r["totalRides"].(int32), TopDriver: r["topDriver"].(string), AvgPrice: r["avgPrice"].(float64), Status: "Active"})
	}
	if result == nil {
		result = []RouteRow{}
	}
	c.JSON(http.StatusOK, result)
}
