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

func InitializeRideCollection() {
	rideCollection = config.Database.Collection("rides")
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
		UserID:    userId,
		Pickup:    body.Pickup,
		Dropoff:   body.Dropoff,
		RideType:  body.RideType,
		CreatedAt: time.Now(),
	}

	_, err := rideCollection.InsertOne(context.Background(), ride)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save ride"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Recent ride saved"})
}

func GetRecentRides(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	opts := options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}}).
		SetLimit(5)

	cursor, err := rideCollection.Find(
		context.Background(),
		bson.M{"userId": userId},
		opts,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch rides"})
		return
	}

	var rides []models.Ride
	if err := cursor.All(context.Background(), &rides); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse rides"})
		return
	}

	c.JSON(http.StatusOK, rides)
}
