package controllers

import (
	"context"
	"net/http"
	"raahi-backend/config"
	"raahi-backend/models"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var locationCollection *mongo.Collection

func InitializeLocationCollection() {
	locationCollection = config.Database.Collection("locations")
	// Create index on DisplayName for searching and Lat/Lon for uniqueness
	_, _ = locationCollection.Indexes().CreateMany(context.Background(), []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "displayName", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{
			Keys: bson.D{{Key: "useCount", Value: -1}},
		},
	})
}

// RecordLocation handles storing/updating a location when used
func RecordLocation(c *gin.Context) {
	var input struct {
		DisplayName string `json:"displayName" binding:"required"`
		Lat         string `json:"lat" binding:"required"`
		Lon         string `json:"lon" binding:"required"`
		Type        string `json:"type"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	filter := bson.M{"displayName": input.DisplayName}
	update := bson.M{
		"$set": bson.M{
			"lat":        input.Lat,
			"lon":        input.Lon,
			"type":       input.Type,
			"lastUsedAt": time.Now(),
		},
		"$inc": bson.M{"useCount": 1},
	}

	opts := options.Update().SetUpsert(true)
	_, err := locationCollection.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record location"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Location recorded"})
}

// GetLocationSuggestions returns popular locations matching the query
func GetLocationSuggestions(c *gin.Context) {
	query := c.Query("q")
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var filter bson.M
	if query != "" {
		filter = bson.M{
			"displayName": bson.M{
				"$regex":   query,
				"$options": "i",
			},
		}
	} else {
		filter = bson.M{}
	}

	findOptions := options.Find()
	findOptions.SetSort(bson.D{{Key: "useCount", Value: -1}})
	findOptions.SetLimit(10)

	cursor, err := locationCollection.Find(ctx, filter, findOptions)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch suggestions"})
		return
	}
	defer cursor.Close(ctx)

	var suggestions []models.LocationSuggestion
	if err = cursor.All(ctx, &suggestions); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to decode suggestions"})
		return
	}

	c.JSON(http.StatusOK, suggestions)
}
