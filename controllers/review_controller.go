package controllers

import (
	"context"
	"errors"
	"math"
	"net/http"
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

var reviewCollection *mongo.Collection

func InitializeReviewCollection() {
	reviewCollection = config.Database.Collection("reviews")
}

// SubmitReview — POST /api/reviews (passenger submits a review for the driver of a completed ride)
func SubmitReview(c *gin.Context) {
	passengerID := c.MustGet("userId").(primitive.ObjectID)

	var body struct {
		RideID  string `json:"rideId" binding:"required"`
		Rating  int    `json:"rating" binding:"required,min=1,max=5"`
		Comment string `json:"comment" binding:"max=1000"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input. Rating must be 1-5 and comment up to 1000 characters."})
		return
	}

	sanitizedComment := strings.TrimSpace(body.Comment)
	if len(sanitizedComment) > 1000 {
		sanitizedComment = sanitizedComment[:1000]
	}
	sanitizedComment = strings.ReplaceAll(sanitizedComment, "<", "&lt;")
	sanitizedComment = strings.ReplaceAll(sanitizedComment, ">", "&gt;")

	rideId, err := primitive.ObjectIDFromHex(body.RideID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ride ID"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	// Verify the ride exists and is completed
	var ride models.Ride
	if err := config.Database.Collection("rides").FindOne(ctx, bson.M{"_id": rideId}).Decode(&ride); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found"})
			return
		}
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to verify ride"})
		return
	}
	if passengerID == ride.DriverID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Drivers cannot review their own ride"})
		return
	}
	if ride.Status != string(models.RideStatusCompleted) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ride is not completed yet"})
		return
	}

	// Verify the passenger had an accepted/completed booking for this ride
	var booking models.Booking
	if err := config.Database.Collection("bookings").FindOne(ctx, bson.M{
		"rideId":      rideId,
		"passengerId": passengerID,
		"status":      bson.M{"$in": []string{string(models.BookingStatusAccepted), string(models.BookingStatusCompleted)}},
	}).Decode(&booking); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusForbidden, gin.H{"error": "You can only review rides you were part of"})
			return
		}
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to verify booking eligibility"})
		return
	}

	// Prevent duplicate review for same ride by same passenger
	existing, err := reviewCollection.CountDocuments(ctx, bson.M{
		"rideId":      rideId,
		"passengerId": passengerID,
	})
	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to check existing reviews"})
		return
	}
	if existing > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "You have already reviewed this ride"})
		return
	}

	// Get passenger name
	var passenger struct {
		Name string `bson:"name"`
	}
	if err := config.Database.Collection("users").FindOne(ctx, bson.M{"_id": passengerID}).Decode(&passenger); err != nil {
		if !errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to load reviewer profile"})
			return
		}
	}

	review := models.Review{
		DriverID:      ride.DriverID,
		PassengerID:   passengerID,
		RideID:        rideId,
		Rating:        body.Rating,
		Comment:       sanitizedComment,
		PassengerName: passenger.Name,
		CreatedAt:     time.Now(),
	}

	if _, err := reviewCollection.InsertOne(ctx, review); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "You have already reviewed this ride"})
			return
		}
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to save review"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Review submitted successfully"})
}

// GetDriverReviews — GET /api/reviews/driver/:driverId (public — anyone can view driver reviews)
func GetDriverReviews(c *gin.Context) {
	driverIdHex := c.Param("driverId")
	driverID, err := primitive.ObjectIDFromHex(driverIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid driver ID"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	cursor, err := reviewCollection.Find(ctx, bson.M{"driverId": driverID},
		options.Find().SetSort(bson.M{"createdAt": -1}).SetLimit(50))
	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to fetch reviews"})
		return
	}
	defer cursor.Close(ctx)

	var reviews []models.Review
	if err := cursor.All(ctx, &reviews); err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to decode reviews"})
		return
	}
	if err := cursor.Err(); err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Review list stream interrupted"})
		return
	}
	if reviews == nil {
		reviews = []models.Review{}
	}

	// Compute average rating and total review count across ALL reviews for this driver.
	aggPipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: bson.M{"driverId": driverID}}},
		bson.D{{Key: "$group", Value: bson.M{
			"_id":          nil,
			"totalReviews": bson.M{"$sum": 1},
			"avgRating":    bson.M{"$avg": "$rating"},
		}}},
	}
	aggCursor, err := reviewCollection.Aggregate(ctx, aggPipeline)
	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to compute review statistics"})
		return
	}
	defer aggCursor.Close(ctx)

	var aggStats []struct {
		TotalReviews int     `bson:"totalReviews"`
		AvgRating    float64 `bson:"avgRating"`
	}
	if err := aggCursor.All(ctx, &aggStats); err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to decode review statistics"})
		return
	}
	if err := aggCursor.Err(); err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Review statistics stream interrupted"})
		return
	}

	var avgRating float64
	totalReviews := 0
	if len(aggStats) > 0 {
		totalReviews = aggStats[0].TotalReviews
		avgRating = math.Round(aggStats[0].AvgRating*10) / 10
	}

	c.JSON(http.StatusOK, gin.H{
		"reviews":       reviews,
		"averageRating": avgRating,
		"totalReviews":  totalReviews,
	})
}

// CheckCanReview — GET /api/reviews/can-review/:rideId (can this passenger review this ride?)
func CheckCanReview(c *gin.Context) {
	passengerID := c.MustGet("userId").(primitive.ObjectID)
	rideIdHex := c.Param("rideId")
	rideId, err := primitive.ObjectIDFromHex(rideIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ride ID"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	// Check if already reviewed
	existing, err := reviewCollection.CountDocuments(ctx, bson.M{
		"rideId":      rideId,
		"passengerId": passengerID,
	})
	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to check existing review"})
		return
	}
	if existing > 0 {
		c.JSON(http.StatusOK, gin.H{"canReview": false, "reason": "already_reviewed"})
		return
	}

	// Check if passenger was part of this ride
	var ride models.Ride
	if err := config.Database.Collection("rides").FindOne(ctx, bson.M{"_id": rideId}).Decode(&ride); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusOK, gin.H{"canReview": false, "reason": "ride_not_found"})
			return
		}
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to verify ride"})
		return
	}
	if passengerID == ride.DriverID {
		c.JSON(http.StatusOK, gin.H{"canReview": false, "reason": "own_ride"})
		return
	}
	if ride.Status != string(models.RideStatusCompleted) {
		c.JSON(http.StatusOK, gin.H{"canReview": false, "reason": "ride_not_completed"})
		return
	}

	var booking models.Booking
	if err := config.Database.Collection("bookings").FindOne(ctx, bson.M{
		"rideId":      rideId,
		"passengerId": passengerID,
		"status":      bson.M{"$in": []string{string(models.BookingStatusAccepted), string(models.BookingStatusCompleted)}},
	}).Decode(&booking); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusOK, gin.H{"canReview": false, "reason": "no_booking"})
			return
		}
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to verify booking"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"canReview": true, "driverId": ride.DriverID.Hex(), "driverName": ride.DriverName})
}
