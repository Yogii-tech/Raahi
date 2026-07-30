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
	chatCollection = config.Database.Collection("chats")
}

// GetMessages returns all messages for a booking, validating the caller is party to the booking.
func GetMessages(c *gin.Context) {
	callerID := c.MustGet("userId").(primitive.ObjectID)
	bookingIDHex := c.Param("bookingId")
	bookingID, err := primitive.ObjectIDFromHex(bookingIDHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid booking ID"})
		return
	}

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	// Authorization: caller must be passenger OR driver of this booking's ride
	var booking models.Booking
	err = bookingCollection.FindOne(dbCtx, bson.M{"_id": bookingID}).Decode(&booking)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Booking not found"})
		return
	}

	// Load ride to get driverId
	var ride models.Ride
	err = rideCollection.FindOne(dbCtx, bson.M{"_id": booking.RideID}).Decode(&ride)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found"})
		return
	}

	if callerID != booking.PassengerID && callerID != ride.DriverID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized to view this chat"})
		return
	}

	opts := options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}})
	cursor, err := chatCollection.Find(dbCtx, bson.M{"bookingId": bookingID}, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch messages"})
		return
	}

	var messages []models.ChatMessage
	if err := cursor.All(dbCtx, &messages); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse messages"})
		return
	}

	if messages == nil {
		messages = []models.ChatMessage{}
	}
	c.JSON(http.StatusOK, messages)
}

// SendMessage stores a new chat message for a booking.
func SendMessage(c *gin.Context) {
	callerID := c.MustGet("userId").(primitive.ObjectID)
	bookingIDHex := c.Param("bookingId")
	bookingID, err := primitive.ObjectIDFromHex(bookingIDHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid booking ID"})
		return
	}

	var body struct {
		Text string `json:"text" binding:"required,min=1,max=1000"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Message text is required"})
		return
	}

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	// Authorization: caller must be passenger OR driver of this booking's ride
	var booking models.Booking
	err = bookingCollection.FindOne(dbCtx, bson.M{"_id": bookingID}).Decode(&booking)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Booking not found"})
		return
	}

	var ride models.Ride
	err = rideCollection.FindOne(dbCtx, bson.M{"_id": booking.RideID}).Decode(&ride)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Ride not found"})
		return
	}

	var role string
	if callerID == ride.DriverID {
		role = "driver"
	} else if callerID == booking.PassengerID {
		role = "passenger"
	} else {
		c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized to send messages in this chat"})
		return
	}

	msg := models.ChatMessage{
		BookingID: bookingID,
		SenderID:  callerID,
		Role:      role,
		Text:      body.Text,
		CreatedAt: time.Now(),
	}

	result, err := chatCollection.InsertOne(dbCtx, msg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to send message"})
		return
	}

	msg.ID = result.InsertedID.(primitive.ObjectID)
	c.JSON(http.StatusCreated, msg)
}

// MarkMessagesRead marks all unread messages in a chat as read for the calling user.
func MarkMessagesRead(c *gin.Context) {
	callerID := c.MustGet("userId").(primitive.ObjectID)
	bookingIDHex := c.Param("bookingId")
	bookingID, err := primitive.ObjectIDFromHex(bookingIDHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid booking ID"})
		return
	}

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	now := time.Now()
	// Mark all messages NOT sent by the caller as read (i.e. messages the other person sent)
	_, err = chatCollection.UpdateMany(
		dbCtx,
		bson.M{
			"bookingId": bookingID,
			"senderId":  bson.M{"$ne": callerID},
			"readAt":    nil,
		},
		bson.M{"$set": bson.M{"readAt": now}},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to mark messages as read"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Messages marked as read"})
}
