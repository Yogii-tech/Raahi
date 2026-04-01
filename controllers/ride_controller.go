package controllers

import (
	"context"
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

var rideCollection *mongo.Collection
var bookingCollection *mongo.Collection

func InitializeRideCollection() {
	rideCollection = config.Database.Collection("rides")
	bookingCollection = config.Database.Collection("bookings")
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

	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// Fetch driver Name
	var driver struct {
		Name string `bson:"name"`
	}
	config.Database.Collection("users").FindOne(context.Background(), bson.M{"_id": userId}).Decode(&driver)

	// Delete existing rides for this driver to keep it clean
	rideCollection.DeleteMany(context.Background(), bson.M{"driverId": userId, "status": "available"})

	ride := models.Ride{
		DriverID:      userId,
		DriverName:    driver.Name,
		Pickup:        body.Pickup,
		Dropoff:       body.Dropoff,
		Date:          body.Date,
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

	_, err := rideCollection.InsertOne(context.Background(), ride)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create ride"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Ride created successfully"})
}

func getTakenSeats(rideId primitive.ObjectID) []int {
	cursor, err := bookingCollection.Find(context.Background(), bson.M{
		"rideId": rideId,
		"status": bson.M{"$in": []string{"pending", "accepted"}},
	})
	if err != nil {
		return []int{}
	}
	var bookings []models.Booking
	cursor.All(context.Background(), &bookings)

	taken := []int{}
	for _, b := range bookings {
		taken = append(taken, b.SeatLayout...)
	}
	return taken
}

func GetAvailableRides(c *gin.Context) {
	cursor, err := rideCollection.Find(context.Background(), bson.M{"status": "available"})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch rides"})
		return
	}

	var rides []models.Ride
	if err := cursor.All(context.Background(), &rides); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse rides"})
		return
	}

	for i := range rides {
		rides[i].TakenSeats = getTakenSeats(rides[i].ID)
		rides[i].SeatsBooked = len(rides[i].TakenSeats)
	}

	c.JSON(http.StatusOK, rides)
}

func GetRideDetails(c *gin.Context) {
	rideIdHex := c.Param("rideId")
	rideId, _ := primitive.ObjectIDFromHex(rideIdHex)

	var ride models.Ride
	err := rideCollection.FindOne(context.Background(), bson.M{"_id": rideId}).Decode(&ride)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found"})
		return
	}

	ride.TakenSeats = getTakenSeats(ride.ID)
	ride.SeatsBooked = len(ride.TakenSeats)
	c.JSON(http.StatusOK, ride)
}

func BookRide(c *gin.Context) {
	passengerId := c.MustGet("userId").(primitive.ObjectID)
	rideIdHex := c.Param("rideId")
	rideId, _ := primitive.ObjectIDFromHex(rideIdHex)

	var body struct {
		SeatsRequested int   `json:"seatsRequested"`
		SeatLayout     []int `json:"seatLayout"`
		RoofCarrier    bool  `json:"roofCarrier"`
		MotionSickness bool  `json:"motionSickness"`
	}

	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
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

	_, err := bookingCollection.InsertOne(context.Background(), booking)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to book ride"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Booking request sent to driver"})
}

func GetDriverRequests(c *gin.Context) {
	driverId := c.MustGet("userId").(primitive.ObjectID)

	// Sub-query to get all rides created by this driver
	cursor, err := rideCollection.Find(context.Background(), bson.M{"driverId": driverId})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch driver rides"})
		return
	}

	var rides []models.Ride
	if err := cursor.All(context.Background(), &rides); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse rides"})
		return
	}

	var rideIds []primitive.ObjectID
	for _, ride := range rides {
		rideIds = append(rideIds, ride.ID)
	}

	// Filter bookings for those rideIds
	cursor, err = bookingCollection.Find(context.Background(), bson.M{"rideId": bson.M{"$in": rideIds}})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch requests"})
		return
	}

	var bookings []models.Booking
	if err := cursor.All(context.Background(), &bookings); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse bookings"})
		return
	}

	type BookingResponse struct {
		models.Booking
		Ride models.Ride `json:"ride"`
	}

	var response []BookingResponse
	for _, b := range bookings {
		var ride models.Ride
		rideCollection.FindOne(context.Background(), bson.M{"_id": b.RideID}).Decode(&ride)
		response = append(response, BookingResponse{
			Booking: b,
			Ride:    ride,
		})
	}

	c.JSON(http.StatusOK, response)
}

func GetPassengerBookings(c *gin.Context) {
	passengerId := c.MustGet("userId").(primitive.ObjectID)

	cursor, err := bookingCollection.Find(context.Background(), bson.M{"passengerId": passengerId})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch bookings"})
		return
	}

	var bookings []models.Booking
	if err := cursor.All(context.Background(), &bookings); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse bookings"})
		return
	}

	type BookingResponse struct {
		models.Booking
		Ride models.Ride `json:"ride"`
	}

	var response []BookingResponse
	for _, b := range bookings {
		var ride models.Ride
		rideCollection.FindOne(context.Background(), bson.M{"_id": b.RideID}).Decode(&ride)
		ride.TakenSeats = getTakenSeats(ride.ID) // Populate real-time taken seats
		response = append(response, BookingResponse{
			Booking: b,
			Ride:    ride,
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
		CreatedAt: time.Now(),
	}

	_, err := rideCollection.InsertOne(context.Background(), ride)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save ride"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Recent ride saved"})
}

func UpdateBookingStatus(c *gin.Context) {
	bookingIdHex := c.Param("bookingId")
	bookingId, _ := primitive.ObjectIDFromHex(bookingIdHex)

	var body struct {
		Status string `json:"status"` // "accepted" or "rejected"
	}

	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// Get the booking to find the rideId and seatsRequested
	var booking models.Booking
	err := bookingCollection.FindOne(context.Background(), bson.M{"_id": bookingId}).Decode(&booking)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Booking not found"})
		return
	}

	// Update status
	_, err = bookingCollection.UpdateOne(
		context.Background(),
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
			context.Background(),
			bson.M{"_id": booking.RideID},
			bson.M{"$inc": bson.M{"seatsBooked": booking.SeatsRequested}},
		)
	}

	c.JSON(http.StatusOK, gin.H{"message": "Booking status updated"})
}

func GetRecentRides(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	opts := options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}}).
		SetLimit(5)

	cursor, err := rideCollection.Find(
		context.Background(),
		bson.M{"driverId": userId},
		opts,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch rides"})
		return
	}

	rides := []models.Ride{}
	if err := cursor.All(context.Background(), &rides); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse rides"})
		return
	}

	for i := range rides {
		rides[i].TakenSeats = getTakenSeats(rides[i].ID)
		rides[i].SeatsBooked = len(rides[i].TakenSeats)
	}

	c.JSON(http.StatusOK, rides)
}

func MarkNotificationsViewed(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	role := c.Query("role") // "driver" or "passenger"

	var filter bson.M
	var update bson.M

	if role == "driver" {
		// Driver sees requests for THEIR rides
		cursor, _ := rideCollection.Find(context.Background(), bson.M{"driverId": userId})
		var rideIds []primitive.ObjectID
		for cursor.Next(context.Background()) {
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

	bookingCollection.UpdateMany(context.Background(), filter, update)
	c.JSON(http.StatusOK, gin.H{"message": "Notifications marked as viewed"})
}
