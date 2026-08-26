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

var notificationCollection *mongo.Collection

func InitializeNotificationCollection() {
	notificationCollection = config.Database.Collection("notifications")
}

// CreateNotification is a helper to be used internally by other controllers
func CreateNotification(userId primitive.ObjectID, title, message, notifType string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	notif := models.Notification{
		UserID:    userId,
		Title:     title,
		Message:   message,
		Type:      notifType,
		Read:      false,
		CreatedAt: time.Now(),
	}
	_, err := notificationCollection.InsertOne(ctx, notif)
	return err
}

func GetMyNotifications(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	var notifications []models.Notification
	opts := options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}})
	
	cursor, err := notificationCollection.Find(ctx, bson.M{"userId": userId}, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch notifications"})
		return
	}
	defer cursor.Close(ctx)

	if err = cursor.All(ctx, &notifications); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse notifications"})
		return
	}

	if notifications == nil {
		notifications = []models.Notification{}
	}

	c.JSON(http.StatusOK, notifications)
}

func MarkNotificationRead(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	notifIdHex := c.Param("id")
	notifId, err := primitive.ObjectIDFromHex(notifIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid notification ID"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	_, err = notificationCollection.UpdateOne(
		ctx,
		bson.M{"_id": notifId, "userId": userId},
		bson.M{"$set": bson.M{"read": true}},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to mark read"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Marked read"})
}
