package controllers

import (
	"context"
	"net/http"
	"time"

	"raahi-backend/config"
	"raahi-backend/models"
	"raahi-backend/utils"

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

// CreateNotification stores a notification in DB and sends an FCM push to the user's device.
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

	// Fetch user's FCM token and send push notification (fire-and-forget)
	go func() {
		tokenCtx, tokenCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer tokenCancel()

		var user struct {
			FCMToken string `bson:"fcm_token"`
		}
		if e := config.Database.Collection("users").FindOne(tokenCtx, bson.M{"_id": userId}).Decode(&user); e == nil && user.FCMToken != "" {
			utils.SendPushNotification(user.FCMToken, title, message, map[string]string{
				"type": notifType,
			})
		}
	}()

	return err
}


// NotifyAdmins sends a notification to all users with the "admin" role via DB + FCM push.
func NotifyAdmins(title, message, notifType string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userColl := config.Database.Collection("users")
	cursor, err := userColl.Find(ctx, bson.M{"role": "admin"})
	if err != nil {
		return
	}
	defer cursor.Close(ctx)

	var fcmTokens []string
	for cursor.Next(ctx) {
		var admin struct {
			ID       primitive.ObjectID `bson:"_id"`
			FCMToken string             `bson:"fcm_token"`
		}
		if err := cursor.Decode(&admin); err == nil {
			CreateNotification(admin.ID, title, message, notifType)
			if admin.FCMToken != "" {
				fcmTokens = append(fcmTokens, admin.FCMToken)
			}
		}
	}

	// Send FCM multicast to all admins at once
	if len(fcmTokens) > 0 {
		utils.SendMulticastPush(fcmTokens, title, message, map[string]string{"type": notifType})
	}
}

func GetMyNotifications(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	// Auto-expire: only return notifications from the last 24 hours
	cutoff := time.Now().Add(-24 * time.Hour)

	var notifications []models.Notification
	opts := options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}})
	
	cursor, err := notificationCollection.Find(ctx, bson.M{
		"userId": userId,
		"createdAt": bson.M{"$gte": cutoff},
	}, opts)
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

func ClearAllNotifications(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	_, err := notificationCollection.DeleteMany(ctx, bson.M{"userId": userId})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to clear notifications"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "All notifications cleared"})
}
