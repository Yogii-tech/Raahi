package controllers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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

const (
	defaultUserNotificationRetention  = 24 * time.Hour
	defaultAdminNotificationRetention = 30 * 24 * time.Hour
	notificationDedupWindow           = 5 * time.Second
	defaultNotificationLimit          = 100
	maxNotificationLimit              = 200
)

var notificationCollection *mongo.Collection

func InitializeNotificationCollection() {
	notificationCollection = config.Database.Collection("notifications")
}

func getNotificationCollection() *mongo.Collection {
	if notificationCollection != nil {
		return notificationCollection
	}
	if config.Database != nil {
		return config.Database.Collection("notifications")
	}
	return nil
}

func getNotificationUsersCollection() *mongo.Collection {
	if userCollection != nil {
		return userCollection
	}
	if config.Database != nil {
		return config.Database.Collection("users")
	}
	return nil
}

// buildAdminNotificationURL constructs a safe relative deep-link URL for admin push clicks.
func buildAdminNotificationURL(targetView, relatedID, notifType, notificationID string) string {
	q := url.Values{}
	if v := strings.TrimSpace(targetView); v != "" {
		q.Set("adminView", v)
	}
	if v := strings.TrimSpace(relatedID); v != "" {
		q.Set("relatedId", v)
	}
	if v := strings.TrimSpace(notifType); v != "" {
		q.Set("type", v)
	}
	if v := strings.TrimSpace(notificationID); v != "" {
		q.Set("notificationId", v)
	}
	encoded := q.Encode()
	if encoded == "" {
		return "/"
	}
	return "/?" + encoded
}

// appendNotificationIDToURL appends notificationId query param to a relative or absolute URL
// without duplicating it if already present.
func appendNotificationIDToURL(rawURL, notificationID string) string {
	rawURL = strings.TrimSpace(rawURL)
	notificationID = strings.TrimSpace(notificationID)
	if rawURL == "" || notificationID == "" {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := parsed.Query()
	if q.Get("notificationId") == "" {
		q.Set("notificationId", notificationID)
		parsed.RawQuery = q.Encode()
	}
	return parsed.String()
}

func sanitizeNotificationMetadata(extraData map[string]string) map[string]string {
	if len(extraData) == 0 {
		return nil
	}
	out := make(map[string]string, len(extraData))
	for k, v := range extraData {
		k = strings.TrimSpace(k)
		if k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func buildFCMNotificationData(
	notifID primitive.ObjectID,
	title, message, notifType, category, targetView, relatedID string,
	metadata map[string]string,
) map[string]string {
	data := make(map[string]string, len(metadata)+8)
	for k, v := range metadata {
		if k != "" && v != "" {
			data[k] = v
		}
	}
	if title != "" {
		data["title"] = title
	}
	if message != "" {
		data["body"] = message
	}
	if notifType != "" {
		data["type"] = notifType
	}
	if relatedID != "" {
		data["relatedId"] = relatedID
	}
	if category != "" {
		data["category"] = category
	}
	if targetView != "" {
		data["targetView"] = targetView
	}
	if !notifID.IsZero() {
		data["notificationId"] = notifID.Hex()
		if existingURL := data["url"]; existingURL != "" {
			data["url"] = appendNotificationIDToURL(existingURL, notifID.Hex())
		}
	}
	return data
}

// CreateNotification stores a notification in DB and sends an FCM push to the user's device.
// extraData is an optional map of additional key-value pairs stored in Metadata (when relevant)
// and forwarded into the FCM data payload (e.g. bookingId, pickup, dropoff, rideId, url).
func CreateNotification(userId primitive.ObjectID, title, message, notifType, relatedId string, extraData map[string]string) error {
	notifCol := getNotificationCollection()
	if notifCol == nil {
		return errors.New("notification collection is not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Deduplication safeguard: prevent duplicate notifications for the same user & entity within 5 seconds
	dupFilter := bson.M{
		"userId":    userId,
		"title":     title,
		"message":   message,
		"type":      notifType,
		"createdAt": bson.M{"$gte": time.Now().Add(-notificationDedupWindow)},
	}
	if relatedId != "" {
		dupFilter["relatedId"] = relatedId
	}

	dupCheckCtx, dupCancel := context.WithTimeout(ctx, 2*time.Second)
	defer dupCancel()
	recentCount, err := notifCol.CountDocuments(dupCheckCtx, dupFilter)
	if err != nil {
		log.Printf("[NOTIF_WARN] deduplication check failed for user %s: %v", userId.Hex(), err)
	} else if recentCount > 0 {
		return nil
	}

	metadata := sanitizeNotificationMetadata(extraData)
	category := ""
	targetView := ""
	if metadata != nil {
		category = metadata["category"]
		targetView = metadata["targetView"]
	}

	notifID := primitive.NewObjectID()
	notif := models.Notification{
		ID:         notifID,
		UserID:     userId,
		Title:      title,
		Message:    message,
		Type:       notifType,
		Category:   category,
		TargetView: targetView,
		RelatedId:  relatedId,
		Metadata:   metadata,
		Read:       false,
		CreatedAt:  time.Now(),
	}
	if _, err = notifCol.InsertOne(ctx, notif); err != nil {
		log.Printf("[NOTIF_ERR] failed to persist notification for user %s (type=%s): %v", userId.Hex(), notifType, err)
		return err
	}

	// Fetch user's FCM tokens and send push notification to all user devices (fire-and-forget)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[NOTIF_PANIC] recovered in CreateNotification push goroutine: %v", r)
			}
		}()

		usersCol := getNotificationUsersCollection()
		if usersCol == nil {
			return
		}
		tokenCtx, tokenCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer tokenCancel()

		var user struct {
			FCMToken  string   `bson:"fcm_token"`
			FCMTokens []string `bson:"fcm_tokens"`
		}
		if e := usersCol.FindOne(tokenCtx, bson.M{"_id": userId}).Decode(&user); e != nil {
			if !errors.Is(e, mongo.ErrNoDocuments) {
				log.Printf("[NOTIF_WARN] failed to load FCM tokens for user %s: %v", userId.Hex(), e)
			}
			return
		}
		tokens := utils.CollectTokens(user.FCMToken, user.FCMTokens)
		if len(tokens) > 0 {
			data := buildFCMNotificationData(notifID, title, message, notifType, category, targetView, relatedId, metadata)
			if pushErr := utils.SendPushToTokensErr(tokens, title, message, data); pushErr != nil {
				log.Printf("[NOTIF_WARN] FCM delivery error for user %s: %v", userId.Hex(), pushErr)
			}
		}
	}()

	return nil
}

// AdminNotificationInput describes a platform-wide admin alert to persist and push.
type AdminNotificationInput struct {
	Title          string
	Message        string
	Type           string
	Category       string
	TargetView     string
	RelatedID      string
	ExcludeAdminID primitive.ObjectID
	Metadata       map[string]string
}

type adminRecipient struct {
	ID     primitive.ObjectID
	Tokens []string
}

type adminNotifyDeps struct {
	findAdmins  func(ctx context.Context) ([]adminRecipient, error)
	isDuplicate func(ctx context.Context, adminID primitive.ObjectID, input AdminNotificationInput, since time.Time) (bool, error)
	insertNotif func(ctx context.Context, notif models.Notification) error
	sendPush    func(tokens []string, title, body string, data map[string]string) error
	now         func() time.Time
	newObjectID func() primitive.ObjectID
}

func defaultAdminNotifyDeps() adminNotifyDeps {
	return adminNotifyDeps{
		findAdmins: func(ctx context.Context) ([]adminRecipient, error) {
			usersCol := getNotificationUsersCollection()
			if usersCol == nil {
				return nil, nil
			}
			cursor, err := usersCol.Find(
				ctx,
				bson.M{"role": "admin"},
				options.Find().SetProjection(bson.M{"_id": 1, "fcm_token": 1, "fcm_tokens": 1}),
			)
			if err != nil {
				return nil, err
			}
			defer cursor.Close(ctx)

			var rows []struct {
				ID        primitive.ObjectID `bson:"_id"`
				FCMToken  string             `bson:"fcm_token"`
				FCMTokens []string           `bson:"fcm_tokens"`
			}
			if err := cursor.All(ctx, &rows); err != nil {
				return nil, err
			}
			if err := cursor.Err(); err != nil {
				return nil, err
			}

			recipients := make([]adminRecipient, 0, len(rows))
			for _, r := range rows {
				recipients = append(recipients, adminRecipient{
					ID:     r.ID,
					Tokens: utils.CollectTokens(r.FCMToken, r.FCMTokens),
				})
			}
			return recipients, nil
		},
		isDuplicate: func(ctx context.Context, adminID primitive.ObjectID, input AdminNotificationInput, since time.Time) (bool, error) {
			notifCol := getNotificationCollection()
			if notifCol == nil {
				return false, nil
			}
			filter := bson.M{
				"userId":    adminID,
				"title":     input.Title,
				"message":   input.Message,
				"type":      input.Type,
				"createdAt": bson.M{"$gte": since},
			}
			if input.RelatedID != "" {
				filter["relatedId"] = input.RelatedID
			}
			count, err := notifCol.CountDocuments(ctx, filter)
			if err != nil {
				return false, err
			}
			return count > 0, nil
		},
		insertNotif: func(ctx context.Context, notif models.Notification) error {
			notifCol := getNotificationCollection()
			if notifCol == nil {
				return errors.New("notification collection is not initialized")
			}
			_, err := notifCol.InsertOne(ctx, notif)
			return err
		},
		sendPush:    utils.SendPushToTokensErr,
		now:         time.Now,
		newObjectID: primitive.NewObjectID,
	}
}

func buildAdminMetadata(input AdminNotificationInput) map[string]string {
	meta := sanitizeNotificationMetadata(input.Metadata)
	if meta == nil {
		meta = make(map[string]string)
	}
	meta["audience"] = "admin"
	if input.Category != "" {
		meta["category"] = input.Category
	}
	if input.TargetView != "" {
		meta["targetView"] = input.TargetView
	}
	if input.RelatedID != "" {
		meta["relatedId"] = input.RelatedID
	}
	if input.Type != "" {
		meta["type"] = input.Type
	}
	if meta["url"] == "" && input.TargetView != "" {
		meta["url"] = buildAdminNotificationURL(input.TargetView, input.RelatedID, input.Type, "")
	}
	return meta
}

func notifyAdminsSync(ctx context.Context, input AdminNotificationInput, deps adminNotifyDeps) {
	if deps.findAdmins == nil || deps.insertNotif == nil {
		return
	}
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.newObjectID == nil {
		deps.newObjectID = primitive.NewObjectID
	}

	admins, err := deps.findAdmins(ctx)
	if err != nil {
		log.Printf("[NOTIF_ERR] failed to query admins for %s: %v", input.Type, err)
		return
	}
	if len(admins) == 0 {
		return
	}

	baseMeta := buildAdminMetadata(input)
	now := deps.now()
	dedupSince := now.Add(-notificationDedupWindow)
	// Deduplicate tokens across multiple admin accounts so a browser/device holding tokens
	// from multiple admin sessions never receives duplicate pushes for the same event.
	dispatchedTokens := make(map[string]bool)

	for _, admin := range admins {
		if admin.ID.IsZero() {
			continue
		}
		if !input.ExcludeAdminID.IsZero() && admin.ID == input.ExcludeAdminID {
			continue
		}

		if deps.isDuplicate != nil {
			dup, dupErr := deps.isDuplicate(ctx, admin.ID, input, dedupSince)
			if dupErr != nil {
				log.Printf("[NOTIF_WARN] admin dedup check failed for %s (type=%s): %v", admin.ID.Hex(), input.Type, dupErr)
			} else if dup {
				continue
			}
		}

		notifID := deps.newObjectID()
		storedMeta := make(map[string]string, len(baseMeta))
		for k, v := range baseMeta {
			storedMeta[k] = v
		}
		if storedMeta["url"] != "" {
			storedMeta["url"] = appendNotificationIDToURL(storedMeta["url"], notifID.Hex())
		}

		notif := models.Notification{
			ID:         notifID,
			UserID:     admin.ID,
			Title:      input.Title,
			Message:    input.Message,
			Type:       input.Type,
			Category:   input.Category,
			TargetView: input.TargetView,
			RelatedId:  input.RelatedID,
			Metadata:   storedMeta,
			Read:       false,
			CreatedAt:  now,
		}

		if err := deps.insertNotif(ctx, notif); err != nil {
			log.Printf("[NOTIF_WARN] failed to persist admin notification for %s (type=%s): %v", admin.ID.Hex(), input.Type, err)
			continue
		}

		if deps.sendPush == nil || len(admin.Tokens) == 0 {
			continue
		}

		uniqueTokens := make([]string, 0, len(admin.Tokens))
		for _, tok := range admin.Tokens {
			tok = strings.TrimSpace(tok)
			if tok == "" || dispatchedTokens[tok] {
				continue
			}
			dispatchedTokens[tok] = true
			uniqueTokens = append(uniqueTokens, tok)
		}
		if len(uniqueTokens) == 0 {
			continue
		}

		fcmData := buildFCMNotificationData(
			notifID,
			input.Title,
			input.Message,
			input.Type,
			input.Category,
			input.TargetView,
			input.RelatedID,
			storedMeta,
		)
		if pushErr := deps.sendPush(uniqueTokens, input.Title, input.Message, fcmData); pushErr != nil {
			log.Printf("[NOTIF_WARN] admin FCM delivery error for %s (type=%s): %v", admin.ID.Hex(), input.Type, pushErr)
		}
	}
}

// NotifyAdminsAsync persists DB notifications and sends FCM pushes to all admin users
// in a non-blocking background goroutine so caller HTTP requests never slow down or fail.
func NotifyAdminsAsync(input AdminNotificationInput) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[NOTIF_PANIC] recovered in NotifyAdminsAsync (%s): %v", input.Type, r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		notifyAdminsSync(ctx, input, defaultAdminNotifyDeps())
	}()
}

// NotifyAdminsWithData sends a rich notification to all users with the "admin" role via DB + FCM push.
func NotifyAdminsWithData(title, message, notifType, relatedId string, extraData map[string]string) {
	category := ""
	targetView := ""
	if extraData != nil {
		category = extraData["category"]
		targetView = extraData["targetView"]
	}
	NotifyAdminsAsync(AdminNotificationInput{
		Title:      title,
		Message:    message,
		Type:       notifType,
		Category:   category,
		TargetView: targetView,
		RelatedID:  relatedId,
		Metadata:   extraData,
	})
}

// NotifyAdmins sends a notification to all users with the "admin" role via DB + FCM push.
func NotifyAdmins(title, message, notifType string) {
	NotifyAdminsWithData(title, message, notifType, "", nil)
}

func resolveCallerRole(c *gin.Context, userId primitive.ObjectID) string {
	if role := strings.TrimSpace(c.GetString("userRole")); role != "" {
		return role
	}
	usersCol := getNotificationUsersCollection()
	if usersCol == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	var user struct {
		Role string `bson:"role"`
	}
	if err := usersCol.FindOne(ctx, bson.M{"_id": userId}, options.FindOne().SetProjection(bson.M{"role": 1})).Decode(&user); err != nil {
		return ""
	}
	return strings.TrimSpace(user.Role)
}

func resolveNotificationRetention(role, daysParam string) time.Duration {
	if role == "admin" {
		if dStr := strings.TrimSpace(daysParam); dStr != "" {
			if days, err := strconv.Atoi(dStr); err == nil && days >= 1 && days <= 30 {
				return time.Duration(days) * 24 * time.Hour
			}
		}
		return defaultAdminNotificationRetention
	}
	return defaultUserNotificationRetention
}

func buildNotificationQueryFilter(
	userId primitive.ObjectID,
	cutoff time.Time,
	unreadOnly bool,
	category, notifType string,
) bson.M {
	filter := bson.M{
		"userId":    userId,
		"createdAt": bson.M{"$gte": cutoff},
	}
	if unreadOnly {
		filter["read"] = false
	}
	if cat := strings.TrimSpace(category); cat != "" && cat != "all" {
		filter["category"] = cat
	}
	if t := strings.TrimSpace(notifType); t != "" {
		filter["type"] = t
	}
	return filter
}

func parseNotificationsPagination(limitStr, offsetStr, skipStr string) (limit int64, offset int64) {
	limit = defaultNotificationLimit
	if vStr := strings.TrimSpace(limitStr); vStr != "" {
		if v, err := strconv.Atoi(vStr); err == nil && v >= 1 {
			if v > maxNotificationLimit {
				v = maxNotificationLimit
			}
			limit = int64(v)
		}
	}

	rawOffset := strings.TrimSpace(offsetStr)
	if rawOffset == "" {
		rawOffset = strings.TrimSpace(skipStr)
	}
	if rawOffset != "" {
		if v, err := strconv.Atoi(rawOffset); err == nil && v > 0 {
			if v > 10000 {
				v = 10000
			}
			offset = int64(v)
		}
	}
	return limit, offset
}

func GetMyNotifications(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	notifCol := getNotificationCollection()
	if notifCol == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Notification service unavailable"})
		return
	}

	role := resolveCallerRole(c, userId)
	retention := resolveNotificationRetention(role, c.Query("days"))
	cutoff := time.Now().Add(-retention)

	unreadStr := strings.ToLower(strings.TrimSpace(c.Query("unreadOnly")))
	unreadOnly := unreadStr == "true" || unreadStr == "1"
	filter := buildNotificationQueryFilter(userId, cutoff, unreadOnly, c.Query("category"), c.Query("type"))
	limit, offset := parseNotificationsPagination(c.Query("limit"), c.Query("offset"), c.Query("skip"))

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	opts := options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}).
		SetLimit(limit).
		SetSkip(offset)

	cursor, err := notifCol.Find(ctx, filter, opts)
	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to fetch notifications"})
		return
	}
	defer cursor.Close(ctx)

	var notifications []models.Notification
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

// GetUnreadNotificationCount returns the caller's unread notification count using role-aware retention.
func GetUnreadNotificationCount(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	notifCol := getNotificationCollection()
	if notifCol == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Notification service unavailable"})
		return
	}

	role := resolveCallerRole(c, userId)
	retention := resolveNotificationRetention(role, c.Query("days"))
	cutoff := time.Now().Add(-retention)
	filter := buildNotificationQueryFilter(userId, cutoff, true, c.Query("category"), c.Query("type"))

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	count, err := notifCol.CountDocuments(ctx, filter)
	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to count unread notifications"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"unreadCount": count})
}

func MarkNotificationRead(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	notifIdHex := c.Param("id")
	notifId, err := primitive.ObjectIDFromHex(notifIdHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid notification ID"})
		return
	}

	notifCol := getNotificationCollection()
	if notifCol == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Notification service unavailable"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	res, err := notifCol.UpdateOne(
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

// MarkAllNotificationsRead marks all unread notifications for the caller as read
// (optionally filtered by ?category=... or ?type=...).
func MarkAllNotificationsRead(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	notifCol := getNotificationCollection()
	if notifCol == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Notification service unavailable"})
		return
	}

	filter := bson.M{
		"userId": userId,
		"read":   false,
	}
	if cat := strings.TrimSpace(c.Query("category")); cat != "" && cat != "all" {
		filter["category"] = cat
	}
	if t := strings.TrimSpace(c.Query("type")); t != "" {
		filter["type"] = t
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	res, err := notifCol.UpdateMany(ctx, filter, bson.M{"$set": bson.M{"read": true}})
	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to mark all notifications as read"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":       "All notifications marked as read",
		"modifiedCount": res.ModifiedCount,
	})
}

func ClearAllNotifications(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	notifCol := getNotificationCollection()
	if notifCol == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Notification service unavailable"})
		return
	}

	filter := bson.M{"userId": userId}
	if cat := strings.TrimSpace(c.Query("category")); cat != "" && cat != "all" {
		filter["category"] = cat
	}
	if t := strings.TrimSpace(c.Query("type")); t != "" {
		filter["type"] = t
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	res, err := notifCol.DeleteMany(ctx, filter)
	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to clear notifications"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      "All notifications cleared",
		"deletedCount": res.DeletedCount,
	})
}

// TestPushNotification triggers a test FCM push notification to all logged in devices of the user
func TestPushNotification(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)
	usersCol := getNotificationUsersCollection()
	if usersCol == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Notification service unavailable"})
		return
	}

	tokenCtx, tokenCancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer tokenCancel()

	var user struct {
		Role      string   `bson:"role"`
		FCMToken  string   `bson:"fcm_token"`
		FCMTokens []string `bson:"fcm_tokens"`
	}
	err := usersCol.FindOne(tokenCtx, bson.M{"_id": userId}).Decode(&user)
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

	payloadData := map[string]string{
		"type": "test",
	}
	if user.Role == "admin" {
		payloadData["audience"] = "admin"
		payloadData["category"] = models.NotifCategorySystem
		payloadData["targetView"] = models.AdminViewNotifications
		payloadData["url"] = buildAdminNotificationURL(models.AdminViewNotifications, "", "test", "")
	}

	if pushErr := utils.SendPushToTokensErr(tokens, "🔔 GoRaahi Test Notification", "If you see this banner, FCM Push Notifications are working 100%!", payloadData); pushErr != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to deliver test push notification: " + pushErr.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Test push notification sent successfully!"})
}
