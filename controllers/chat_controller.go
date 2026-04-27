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

var chatCollection *mongo.Collection

func InitializeChatCollection() {
	chatCollection = config.Database.Collection("chat")

	// Create TTL index to auto-delete chat messages after 7 days (604800 seconds)
	ttlIndex := mongo.IndexModel{
		Keys:    bson.M{"created_at": 1},
		Options: options.Index().SetExpireAfterSeconds(604800),
	}
	chatCollection.Indexes().CreateOne(context.Background(), ttlIndex)
}

func SendMessage(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	bookingIdHex := c.Param("bookingId")
	bookingId, err := primitive.ObjectIDFromHex(bookingIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid booking ID format"})
		return
	}

	var msgInput struct {
		Text string `json:"text" binding:"required"`
	}

	if err := c.ShouldBindJSON(&msgInput); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Verify the user is part of this booking
	var booking models.Booking
	err = bookingCollection.FindOne(context.Background(), bson.M{"_id": bookingId}).Decode(&booking)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Booking not found"})
		return
	}

	// Get DriverID from Ride
	var ride models.Ride
	err = rideCollection.FindOne(context.Background(), bson.M{"_id": booking.RideID}).Decode(&ride)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found"})
		return
	}

	role := ""
	if ride.DriverID == userId {
		role = "driver"
	} else if booking.PassengerID == userId {
		role = "passenger"
	}

	if role == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not part of this booking"})
		return
	}

	newMessage := models.ChatMessage{
		ID:        primitive.NewObjectID(),
		BookingID: bookingId,
		SenderID:  userId,
		Role:      role,
		Text:      msgInput.Text,
		CreatedAt: time.Now(),
		IsRead:    false,
	}

	_, err = chatCollection.InsertOne(context.Background(), newMessage)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to send message"})
		return
	}

	c.JSON(http.StatusOK, newMessage)
}

func GetMessages(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	bookingIdHex := c.Param("bookingId")
	bookingId, err := primitive.ObjectIDFromHex(bookingIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid booking ID format"})
		return
	}

	// Verify ownership
	var booking models.Booking
	err = bookingCollection.FindOne(context.Background(), bson.M{"_id": bookingId}).Decode(&booking)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Booking not found"})
		return
	}

	var ride models.Ride
	err = rideCollection.FindOne(context.Background(), bson.M{"_id": booking.RideID}).Decode(&ride)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found"})
		return
	}

	if userId != ride.DriverID && userId != booking.PassengerID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	// Fetch messages ordered by creation time
	findOptions := options.Find()
	findOptions.SetSort(bson.M{"created_at": 1})

	cursor, err := chatCollection.Find(context.Background(), bson.M{"booking_id": bookingId}, findOptions)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch messages"})
		return
	}
	defer cursor.Close(context.Background())

	var messages []models.ChatMessage
	if err = cursor.All(context.Background(), &messages); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to decode messages"})
		return
	}

	c.JSON(http.StatusOK, messages)
}

func GetUnreadMessageCount(bookingID primitive.ObjectID, otherRole string) int64 {
	count, err := chatCollection.CountDocuments(context.Background(), bson.M{
		"booking_id": bookingID,
		"role":       otherRole,
		"is_read":    bson.M{"$ne": true},
	})
	if err != nil {
		return 0
	}
	return count
}

func MarkMessagesAsRead(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	bookingIdHex := c.Param("bookingId")
	bookingId, err := primitive.ObjectIDFromHex(bookingIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid booking ID format"})
		return
	}

	// If user is driver, mark passenger messages as read, and vice versa
	var booking models.Booking
	bookingCollection.FindOne(context.Background(), bson.M{"_id": bookingId}).Decode(&booking)
	var ride models.Ride
	rideCollection.FindOne(context.Background(), bson.M{"_id": booking.RideID}).Decode(&ride)

	otherRole := "passenger"
	if ride.DriverID == userId {
		otherRole = "passenger"
	} else {
		otherRole = "driver"
	}

	_, err = chatCollection.UpdateMany(
		context.Background(),
		bson.M{
			"booking_id": bookingId,
			"role":       otherRole,
			"is_read":    bson.M{"$ne": true},
		},
		bson.M{"$set": bson.M{"is_read": true}},
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to mark messages as read"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Messages marked as read"})
}
