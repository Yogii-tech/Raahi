package controllers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"raahi-backend/config"
	"raahi-backend/models"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

var userProfileCollection *mongo.Collection

func InitializeUserController() {
	userProfileCollection = config.Database.Collection("users")
}

func GetTrustedContacts(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	var user models.User
	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	err := userProfileCollection.FindOne(dbCtx, bson.M{"_id": userId}).Decode(&user)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	contacts := user.TrustedContacts
	if contacts == nil {
		contacts = []models.Contact{}
	}

	c.JSON(http.StatusOK, contacts)
}

func UpdateTrustedContacts(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	var contacts []models.Contact
	if err := c.BindJSON(&contacts); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// Limit to 2 contacts and sanitize fields
	if len(contacts) > 2 {
		contacts = contacts[:2]
	}
	for i := range contacts {
		contacts[i].Name = strings.TrimSpace(contacts[i].Name)
		contacts[i].Name = strings.ReplaceAll(contacts[i].Name, "<", "&lt;")
		contacts[i].Name = strings.ReplaceAll(contacts[i].Name, ">", "&gt;")
		if len(contacts[i].Name) > 100 {
			contacts[i].Name = contacts[i].Name[:100]
		}
		contacts[i].Phone = strings.TrimSpace(contacts[i].Phone)
		if len(contacts[i].Phone) > 20 {
			contacts[i].Phone = contacts[i].Phone[:20]
		}
	}

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	_, err := userProfileCollection.UpdateOne(
		dbCtx,
		bson.M{"_id": userId},
		bson.M{"$set": bson.M{"trusted_contacts": contacts}},
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update contacts"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Trusted contacts updated successfully", "contacts": contacts})
}

// isAllowedUserProfileRole strictly enforces that user profile updates can only assign
// standard non-privileged roles ("passenger", "driver", "parceller") and NEVER "admin".
func isAllowedUserProfileRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "passenger", "driver", "parceller":
		return true
	default:
		return false
	}
}

// hasDriverVehicleChanged checks whether KYC-critical vehicle specs or document URLs changed.
// Note: RatePerKm is intentionally excluded so verified drivers can adjust per-km pricing
// in PricingSettingsScreen without triggering KYC re-verification.
func hasDriverVehicleChanged(oldV, newV *models.Vehicle) bool {
	if oldV == nil || newV == nil {
		return oldV != newV
	}
	return oldV.Seats != newV.Seats ||
		strings.TrimSpace(oldV.VehicleName) != strings.TrimSpace(newV.VehicleName) ||
		strings.TrimSpace(oldV.VehicleType) != strings.TrimSpace(newV.VehicleType) ||
		strings.TrimSpace(oldV.SeatingLayout) != strings.TrimSpace(newV.SeatingLayout) ||
		strings.TrimSpace(oldV.VehicleNumber) != strings.TrimSpace(newV.VehicleNumber) ||
		strings.TrimSpace(oldV.DLUrl) != strings.TrimSpace(newV.DLUrl) ||
		strings.TrimSpace(oldV.RCUrl) != strings.TrimSpace(newV.RCUrl) ||
		strings.TrimSpace(oldV.PollutionUrl) != strings.TrimSpace(newV.PollutionUrl) ||
		strings.TrimSpace(oldV.VehicleImageUrl) != strings.TrimSpace(newV.VehicleImageUrl) ||
		strings.TrimSpace(oldV.OwnershipUrl) != strings.TrimSpace(newV.OwnershipUrl)
}

func validateDriverVehicleKYC(v *models.Vehicle) string {
	if v == nil {
		return ""
	}
	if v.Seats < 1 {
		return "Vehicle seats must be at least 1"
	}
	if strings.TrimSpace(v.DLUrl) == "" ||
		strings.TrimSpace(v.RCUrl) == "" ||
		strings.TrimSpace(v.PollutionUrl) == "" ||
		strings.TrimSpace(v.VehicleImageUrl) == "" {
		return "All required vehicle documents (DL, RC, Pollution, Vehicle Image) must be uploaded"
	}
	return ""
}

func normalizeLanguage(lang string) (string, bool) {
	clean := strings.ToLower(strings.TrimSpace(lang))
	switch clean {
	case "en", "hi":
		return clean, true
	default:
		return "", false
	}
}

func UpdateProfile(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	var body struct {
		Name     string          `json:"name" binding:"required,min=2,max=100"`
		Role     string          `json:"role" binding:"required,oneof=passenger driver parceller"` // admin role only via /promote-admin
		Language string          `json:"language"`
		Vehicle  *models.Vehicle `json:"vehicle"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	body.Name = strings.TrimSpace(body.Name)
	body.Name = strings.ReplaceAll(body.Name, "<", "&lt;")
	body.Name = strings.ReplaceAll(body.Name, ">", "&gt;")
	body.Role = strings.ToLower(strings.TrimSpace(body.Role))

	// SECURITY: Explicit defense-in-depth check to prevent privilege escalation to "admin"
	if !isAllowedUserProfileRole(body.Role) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid role. Allowed roles: passenger, driver, parceller"})
		return
	}

	var normalizedLang string
	if strings.TrimSpace(body.Language) != "" {
		var ok bool
		normalizedLang, ok = normalizeLanguage(body.Language)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid language. Allowed values: en, hi"})
			return
		}
	}

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	var currentUser models.User
	err := userProfileCollection.FindOne(dbCtx, bson.M{"_id": userId}).Decode(&currentUser)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	// Prevent role change if it's already set to something else
	// (Except if admin is somehow updating profile, in which case keep role as "admin")
	if currentUser.Role != "" && currentUser.Role != "admin" && currentUser.Role != body.Role {
		c.JSON(http.StatusForbidden, gin.H{"error": "Roles cannot be changed after registering it one time"})
		return
	}

	effectiveRole := body.Role
	if currentUser.Role == "admin" {
		effectiveRole = "admin"
	}

	if effectiveRole == "driver" && body.Vehicle != nil {
		if kycErr := validateDriverVehicleKYC(body.Vehicle); kycErr != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": kycErr})
			return
		}
	}

	setMap := bson.M{
		"name": body.Name,
		"role": effectiveRole,
	}

	if normalizedLang != "" {
		setMap["language"] = normalizedLang
	}

	if body.Vehicle != nil {
		setMap["vehicle"] = body.Vehicle
	}

	isFirstSubmission := false
	isResubmission := false

	if effectiveRole == "driver" && body.Vehicle != nil {
		currentStatus := models.VerificationStatus(currentUser.VerificationStatus)
		if currentStatus == models.VerificationStatusNone {
			setMap["verification_status"] = string(models.VerificationStatusPending)
			setMap["submitted_at"] = time.Now()
			setMap["rejection_reason"] = ""
			isFirstSubmission = true
		} else if currentStatus == models.VerificationStatusRejected || (currentStatus == models.VerificationStatusVerified && hasDriverVehicleChanged(currentUser.Vehicle, body.Vehicle)) {
			if models.CanTransitionVerificationStatus(currentStatus, models.VerificationStatusPending) {
				setMap["verification_status"] = string(models.VerificationStatusPending)
				setMap["submitted_at"] = time.Now()
				setMap["rejection_reason"] = ""
				isResubmission = true
			}
		}
		// If verification_status is already "pending", or "verified" with unchanged KYC vehicle docs, keep it as-is.
	}

	filter := bson.M{"_id": userId}
	if isFirstSubmission {
		filter["$or"] = []bson.M{
			{"verification_status": ""},
			{"verification_status": bson.M{"$exists": false}},
		}
	} else if isResubmission {
		filter["verification_status"] = currentUser.VerificationStatus
	}

	update := bson.M{
		"$set": setMap,
	}

	res, err := userProfileCollection.UpdateOne(
		dbCtx,
		filter,
		update,
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update profile"})
		return
	}
	if res.MatchedCount == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Profile verification state changed concurrently. Please refresh and try again."})
		return
	}

	if isFirstSubmission {
		go CreateNotification(
			userId,
			"Documents Submitted Successfully",
			"Your vehicle documents have been submitted and are under review. You will be notified once the admin approves or rejects your application.",
			"document_verification",
			"",
			nil,
		)
		
		go NotifyAdmins(
			"New Driver Application",
			"A new driver ("+body.Name+") has submitted their documents for review.",
			"admin_alert",
		)
	} else if isResubmission {
		go CreateNotification(
			userId,
			"Documents Resubmitted",
			"Your vehicle documents have been resubmitted and are under review.",
			"document_verification",
			"",
			nil,
		)
		
		go NotifyAdmins(
			"Driver Resubmitted Documents",
			"Driver ("+body.Name+") has resubmitted their documents for review after a previous rejection.",
			"admin_alert",
		)
	}

	c.JSON(http.StatusOK, gin.H{"message": "Profile updated successfully"})
}

func GetProfile(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	var user models.User
	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	err := userProfileCollection.FindOne(dbCtx, bson.M{"_id": userId}).Decode(&user)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	c.JSON(http.StatusOK, user)
}
func Logout(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	// Increment TokenVersion to invalidate all existing tokens
	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	_, err := userProfileCollection.UpdateOne(
		dbCtx,
		bson.M{"_id": userId},
		bson.M{"$inc": bson.M{"token_version": 1}},
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to logout"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Logged out and all sessions revoked successfully"})
}

// UpdateFCMToken stores or refreshes the user's FCM device push token in the database.
// Called by the app after login and whenever the FCM token is refreshed.
// Uses $addToSet to maintain all active device tokens for multi-device push.
func UpdateFCMToken(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	var body struct {
		FCMToken string `json:"fcmToken" binding:"required"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "fcmToken is required"})
		return
	}

	token := strings.TrimSpace(body.FCMToken)
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "fcmToken cannot be empty"})
		return
	}

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	_, err := userProfileCollection.UpdateOne(
		dbCtx,
		bson.M{"_id": userId},
		bson.M{
			"$set":      bson.M{"fcm_token": token},
			"$addToSet": bson.M{"fcm_tokens": token},
		},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update FCM token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "FCM token updated"})
}

// RemoveFCMToken removes a specific FCM device push token (e.g. on logout).
func RemoveFCMToken(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	var body struct {
		FCMToken string `json:"fcmToken"`
	}
	if c.Request != nil && c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
			return
		}
	}

	token := strings.TrimSpace(body.FCMToken)

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	if token != "" {
		if _, err := userProfileCollection.UpdateOne(
			dbCtx,
			bson.M{"_id": userId},
			bson.M{"$pull": bson.M{"fcm_tokens": token}},
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove FCM token"})
			return
		}
		if _, err := userProfileCollection.UpdateOne(
			dbCtx,
			bson.M{"_id": userId, "fcm_token": token},
			bson.M{"$unset": bson.M{"fcm_token": ""}},
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove FCM token"})
			return
		}
	} else {
		_, err := userProfileCollection.UpdateOne(
			dbCtx,
			bson.M{"_id": userId},
			bson.M{"$unset": bson.M{"fcm_token": "", "fcm_tokens": ""}},
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove FCM token"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "FCM token removed"})
}


