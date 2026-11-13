package controllers

import (
	"context"
	"errors"
	"log"
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
// extraData is an optional map of additional key-value pairs forwarded into the FCM data payload
// (e.g. bookingId, pickup, dropoff, rideId). Pass nil if not needed.
func CreateNotification(userId primitive.ObjectID, title, message, notifType, relatedId string, extraData map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Deduplication safeguard: prevent duplicate notifications for the same user within 3 seconds
	dupCheckCtx, dupCancel := context.WithTimeout(ctx, 2*time.Second)
	defer dupCancel()
	recentCount, err := notificationCollection.CountDocuments(dupCheckCtx, bson.M{
		"userId":    userId,
		"title":     title,
		"message":   message,
		"type":      notifType,
		"createdAt": bson.M{"$gte": time.Now().Add(-3 * time.Second)},
	})
	if err != nil {
		log.Printf("[NOTIF_WARN] deduplication check failed for user %s: %v", userId.Hex(), err)
	} else if recentCount > 0 {
		return nil
	}

	notif := models.Notification{
		UserID:    userId,
		Title:     title,
		Message:   message,
		Type:      notifType,
		RelatedId: relatedId,
		Read:      false,
		CreatedAt: time.Now(),
	}
	if _, err = notificationCollection.InsertOne(ctx, notif); err != nil {
		log.Printf("[NOTIF_ERR] failed to persist notification for user %s (type=%s): %v", userId.Hex(), notifType, err)
		return err
	}

	// Fetch user's FCM tokens and send push notification to all user devices (fire-and-forget)
	go func() {
		tokenCtx, tokenCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer tokenCancel()

		var user struct {
			FCMToken  string   `bson:"fcm_token"`
			FCMTokens []string `bson:"fcm_tokens"`
		}
		if e := config.Database.Collection("users").FindOne(tokenCtx, bson.M{"_id": userId}).Decode(&user); e != nil {
			if !errors.Is(e, mongo.ErrNoDocuments) {
				log.Printf("[NOTIF_WARN] failed to load FCM tokens for user %s: %v", userId.Hex(), e)
			}
			return
		}
		tokens := utils.CollectTokens(user.FCMToken, user.FCMTokens)
		if len(tokens) > 0 {
			// Build data payload: start with base fields, then overlay extraData
			data := map[string]string{
				"type":      notifType,
				"relatedId": relatedId,
			}
			for k, v := range extraData {
				data[k] = v
			}
			if pushErr := utils.SendPushToTokensErr(tokens, title, message, data); pushErr != nil {
				log.Printf("[NOTIF_WARN] FCM delivery error for user %s: %v", userId.Hex(), pushErr)
			}
		}
	}()

	return nil
}

// NotifyAdmins sends a notification to all users with the "admin" role via DB + FCM push.
func NotifyAdmins(title, message, notifType string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userColl := config.Database.Collection("users")
	cursor, err := userColl.Find(ctx, bson.M{"role": "admin"})
	if err != nil {
		log.Printf("[NOTIF_ERR] failed to query admins for %s: %v", notifType, err)
		return
	}
	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		var admin struct {
			ID primitive.ObjectID `bson:"_id"`
		}
		if err := cursor.Decode(&admin); err != nil {
			log.Printf("[NOTIF_WARN] failed to decode admin doc: %v", err)
			continue
		}
		// CreateNotification stores the DB record AND fires an FCM push internally.
		// Do NOT also call SendMulticastPush here — that would send a duplicate notification.
		if err := CreateNotification(admin.ID, title, message, notifType, "", nil); err != nil {
			log.Printf("[NOTIF_WARN] failed to notify admin %s: %v", admin.ID.Hex(), err)
		}
	}
	if err := cursor.Err(); err != nil {
		log.Printf("[NOTIF_WARN] admin cursor error during %s: %v", notifType, err)
	}
}

func GetMyNotifications(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	// Auto-expire: only return notifications from the last 24 hours
	cutoff := time.Now().Add(-24 * time.Hour)

	var notifications []models.Notification
	opts := options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(100)

	cursor, err := notificationCollection.Find(ctx, bson.M{
		"userId":    userId,
		"createdAt": bson.M{"$gte": cutoff},
	}, opts)
	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to fetch notifications"})
		return
	}
	defer cursor.Close(ctx)

	if err = cursor.All(ctx, &notifications); err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to parse notifications"})
		return
	}
	if err = cursor.Err(); err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Notifications stream interrupted"})
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

	res, err := notificationCollection.UpdateOne(
		ctx,
		bson.M{"_id": notifId, "userId": userId},
		bson.M{"$set": bson.M{"read": true}},
	)
	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to mark read"})
		return
	}
	if res.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Notification not found"})
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
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to clear notifications"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "All notifications cleared"})
}

// TestPushNotification triggers a test FCM push notification to all logged in devices of the user
func TestPushNotification(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	tokenCtx, tokenCancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer tokenCancel()

	var user struct {
		FCMToken  string   `bson:"fcm_token"`
		FCMTokens []string `bson:"fcm_tokens"`
	}
	err := config.Database.Collection("users").FindOne(tokenCtx, bson.M{"_id": userId}).Decode(&user)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to load user FCM tokens"})
		return
	}

	tokens := utils.CollectTokens(user.FCMToken, user.FCMTokens)
	if len(tokens) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No FCM token stored for this user in DB. Log out and log back in."})
		return
	}

	if pushErr := utils.SendPushToTokensErr(tokens, "🔔 GoRaahi Test Notification", "If you see this banner, FCM Push Notifications are working 100%!", map[string]string{
		"type": "test",
	}); pushErr != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to deliver test push notification: " + pushErr.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Test push notification sent successfully!"})
}
