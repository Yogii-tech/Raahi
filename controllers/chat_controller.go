package controllers

import (
	"context"
	"errors"
	"log"
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

var chatCollection *mongo.Collection

func InitializeChatCollection() {
	chatCollection = config.Database.Collection("chats")
}

// isAuthorizedForBookingChat returns true if callerID is either the booking's passenger or the ride's driver.
func isAuthorizedForBookingChat(callerID, passengerID, driverID primitive.ObjectID) bool {
	if callerID.IsZero() {
		return false
	}
	return callerID == passengerID || callerID == driverID
}

func loadAuthorizedBookingChat(
	ctx context.Context,
	callerID, bookingID primitive.ObjectID,
) (booking models.Booking, ride models.Ride, role string, errStatus int, errMsg string) {
	if err := bookingCollection.FindOne(ctx, bson.M{"_id": bookingID}).Decode(&booking); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return booking, ride, "", http.StatusNotFound, "Booking not found"
		}
		return booking, ride, "", config.DBErrorHTTPStatus(err), "Database error fetching booking"
	}

	if err := rideCollection.FindOne(ctx, bson.M{"_id": booking.RideID}).Decode(&ride); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return booking, ride, "", http.StatusNotFound, "Ride not found"
		}
		return booking, ride, "", config.DBErrorHTTPStatus(err), "Database error fetching ride"
	}

	if callerID == ride.DriverID {
		role = "driver"
	} else if callerID == booking.PassengerID {
		role = "passenger"
	}
	if !isAuthorizedForBookingChat(callerID, booking.PassengerID, ride.DriverID) || role == "" {
		return booking, ride, "", http.StatusForbidden, "Not authorized for this chat"
	}

	return booking, ride, role, 0, ""
}

// GetMessages returns recent messages for a booking, validating the caller is party to the booking.
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

	_, _, _, errStatus, errMsg := loadAuthorizedBookingChat(dbCtx, callerID, bookingID)
	if errStatus != 0 {
		c.JSON(errStatus, gin.H{"error": errMsg})
		return
	}

	opts := options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(200)
	cursor, err := chatCollection.Find(dbCtx, bson.M{"bookingId": bookingID}, opts)
	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to fetch messages"})
		return
	}
	defer cursor.Close(dbCtx)

	var messages []models.ChatMessage
	if err := cursor.All(dbCtx, &messages); err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to parse messages"})
		return
	}
	if err := cursor.Err(); err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to read messages"})
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

	booking, ride, role, errStatus, errMsg := loadAuthorizedBookingChat(dbCtx, callerID, bookingID)
	if errStatus != 0 {
		c.JSON(errStatus, gin.H{"error": errMsg})
		return
	}

	if booking.Status == string(models.BookingStatusRejected) || booking.Status == string(models.BookingStatusCancelled) {
		c.JSON(http.StatusConflict, gin.H{"error": "Cannot send messages for a " + booking.Status + " booking"})
		return
	}
	if ride.Status == string(models.RideStatusCancelled) {
		c.JSON(http.StatusConflict, gin.H{"error": "Cannot send messages for a cancelled ride"})
		return
	}

	// SECURITY: Sanitize HTML to prevent stored XSS
	sanitizedText := strings.ReplaceAll(body.Text, "<", "&lt;")
	sanitizedText = strings.ReplaceAll(sanitizedText, ">", "&gt;")

	msg := models.ChatMessage{
		BookingID: bookingID,
		SenderID:  callerID,
		Role:      role,
		Text:      sanitizedText,
		CreatedAt: time.Now(),
	}

	result, err := chatCollection.InsertOne(dbCtx, msg)
	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to send message"})
		return
	}

	msg.ID = result.InsertedID.(primitive.ObjectID)

	// Send notification to recipient asynchronously
	var recipientID primitive.ObjectID
	if role == "driver" {
		recipientID = booking.PassengerID
	} else {
		recipientID = ride.DriverID
	}
	notifText := body.Text
	if len(notifText) > 40 {
		notifText = notifText[:37] + "..."
	}
	go func(rID primitive.ObjectID, preview, bHex string) {
		if nErr := CreateNotification(rID, "New Message", "💬 "+preview, "chat", bHex, nil); nErr != nil {
			log.Printf("[Chat] Failed to send chat notification to %s: %v", rID.Hex(), nErr)
		}
	}(recipientID, notifText, bookingIDHex)

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

	_, _, _, errStatus, errMsg := loadAuthorizedBookingChat(dbCtx, callerID, bookingID)
	if errStatus != 0 {
		c.JSON(errStatus, gin.H{"error": errMsg})
		return
	}

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
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to mark messages as read"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Messages marked as read"})
}
