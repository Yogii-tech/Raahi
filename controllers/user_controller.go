package controllers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
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

const maxFCMTokensPerUser = 20

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
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Database error fetching contacts"})
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

	res, err := userProfileCollection.UpdateOne(
		dbCtx,
		bson.M{"_id": userId},
		bson.M{"$set": bson.M{"trusted_contacts": contacts}},
	)

	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to update contacts"})
		return
	}
	if res.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
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
		return "Vehicle details and required documents must be submitted"
	}
	if strings.TrimSpace(v.VehicleName) == "" || strings.TrimSpace(v.VehicleNumber) == "" {
		return "Vehicle name and registration number are required"
	}
	if v.Seats < 1 || v.Seats > 60 {
		return "Vehicle seats must be between 1 and 60"
	}
	if math.IsNaN(v.RatePerKm) || math.IsInf(v.RatePerKm, 0) || v.RatePerKm < 0 {
		return "Vehicle rate per km must be a valid non-negative number"
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

// buildDriverProfileUpdateCAS constructs the $set map and Compare-And-Swap filter
// for UpdateProfile so concurrent admin verification/role changes cannot race with
// profile or document submissions, and pending-review docs cannot be swapped silently.
func buildDriverProfileUpdateCAS(
	userId primitive.ObjectID,
	currentUser models.User,
	name string,
	effectiveRole string,
	normalizedLang string,
	bodyVehicle *models.Vehicle,
	now time.Time,
) (setMap bson.M, filter bson.M, isFirstSubmission bool, isResubmission bool, conflictMsg string) {
	setMap = bson.M{
		"name": name,
		"role": effectiveRole,
	}
	if normalizedLang != "" {
		setMap["language"] = normalizedLang
	}

	filter = bson.M{"_id": userId}
	if currentUser.Role == "" {
		filter["$and"] = appendFilterCondition(filter, bson.M{
			"$or": []bson.M{
				{"role": ""},
				{"role": bson.M{"$exists": false}},
			},
		})
	} else {
		filter["role"] = currentUser.Role
	}

	if effectiveRole != "driver" {
		if bodyVehicle != nil {
			setMap["vehicle"] = bodyVehicle
		}
		return setMap, filter, false, false, ""
	}

	currentStatus := models.VerificationStatus(currentUser.VerificationStatus)

	if bodyVehicle == nil {
		if currentStatus == models.VerificationStatusNone {
			filter["$and"] = appendFilterCondition(filter, bson.M{
				"$or": []bson.M{
					{"verification_status": ""},
					{"verification_status": bson.M{"$exists": false}},
				},
			})
		} else {
			filter["verification_status"] = currentUser.VerificationStatus
		}
		return setMap, filter, false, false, ""
	}

	switch currentStatus {
	case models.VerificationStatusNone:
		setMap["vehicle"] = bodyVehicle
		setMap["verification_status"] = string(models.VerificationStatusPending)
		setMap["submitted_at"] = now
		setMap["rejection_reason"] = ""
		isFirstSubmission = true
		filter["$and"] = appendFilterCondition(filter, bson.M{
			"$or": []bson.M{
				{"verification_status": ""},
				{"verification_status": bson.M{"$exists": false}},
			},
		})
	case models.VerificationStatusPending:
		if hasDriverVehicleChanged(currentUser.Vehicle, bodyVehicle) {
			return nil, nil, false, false, "Vehicle details and documents cannot be modified while verification is pending review"
		}
		// Only allow safe non-KYC rate update while keeping verification_status locked via CAS.
		if bodyVehicle.RatePerKm != 0 || (currentUser.Vehicle != nil && currentUser.Vehicle.RatePerKm != bodyVehicle.RatePerKm) {
			setMap["vehicle.rate_per_km"] = bodyVehicle.RatePerKm
		}
		filter["verification_status"] = string(models.VerificationStatusPending)
	case models.VerificationStatusVerified:
		if hasDriverVehicleChanged(currentUser.Vehicle, bodyVehicle) {
			if models.CanTransitionVerificationStatus(currentStatus, models.VerificationStatusPending) {
				setMap["vehicle"] = bodyVehicle
				setMap["verification_status"] = string(models.VerificationStatusPending)
				setMap["submitted_at"] = now
				setMap["rejection_reason"] = ""
				isResubmission = true
			}
		} else {
			// Verified driver updating per-km rate or profile name/language without altering KYC vehicle docs.
			setMap["vehicle.rate_per_km"] = bodyVehicle.RatePerKm
		}
		filter["verification_status"] = string(models.VerificationStatusVerified)
	case models.VerificationStatusRejected:
		if models.CanTransitionVerificationStatus(currentStatus, models.VerificationStatusPending) {
			setMap["vehicle"] = bodyVehicle
			setMap["verification_status"] = string(models.VerificationStatusPending)
			setMap["submitted_at"] = now
			setMap["rejection_reason"] = ""
			isResubmission = true
		}
		filter["verification_status"] = string(models.VerificationStatusRejected)
	default:
		filter["verification_status"] = currentUser.VerificationStatus
	}

	return setMap, filter, isFirstSubmission, isResubmission, ""
}

func appendFilterCondition(filter bson.M, cond bson.M) []bson.M {
	if existing, ok := filter["$and"].([]bson.M); ok {
		return append(existing, cond)
	}
	return []bson.M{cond}
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
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Database error fetching profile"})
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

	setMap, filter, isFirstSubmission, isResubmission, conflictMsg := buildDriverProfileUpdateCAS(
		userId,
		currentUser,
		body.Name,
		effectiveRole,
		normalizedLang,
		body.Vehicle,
		time.Now(),
	)
	if conflictMsg != "" {
		c.JSON(http.StatusConflict, gin.H{"error": conflictMsg})
		return
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
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to update profile"})
		return
	}
	if res.MatchedCount == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Profile verification state changed concurrently. Please refresh and try again."})
		return
	}

	isNewProfileRegistration := currentUser.Role == "" && effectiveRole != ""
	vehicleAddedOrUpdated := (currentUser.Vehicle == nil && body.Vehicle != nil) ||
		(effectiveRole == "driver" && body.Vehicle != nil && currentUser.Vehicle != nil && currentUser.Vehicle.RatePerKm != body.Vehicle.RatePerKm)

	if isFirstSubmission {
		go CreateNotification(
			userId,
			"Documents Submitted Successfully",
			"Your vehicle documents have been submitted and are under review. You will be notified once the admin approves or rejects your application.",
			"document_verification",
			"",
			nil,
		)

		meta := map[string]string{
			"driverId":   userId.Hex(),
			"userId":     userId.Hex(),
			"actorName":  body.Name,
			"actorPhone": currentUser.PhoneNumber,
			"status":     string(models.VerificationStatusPending),
		}
		if body.Vehicle != nil {
			meta["vehicleName"] = body.Vehicle.VehicleName
			meta["vehicleNumber"] = body.Vehicle.VehicleNumber
		}
		NotifyAdminsAsync(AdminNotificationInput{
			Title:      "New Driver Application",
			Message:    fmt.Sprintf("New driver %s (%s) submitted KYC documents for review.", body.Name, currentUser.PhoneNumber),
			Type:       models.NotifTypeDriverKYCSubmitted,
			Category:   models.NotifCategoryDrivers,
			TargetView: models.AdminViewDriverVerification,
			RelatedID:  userId.Hex(),
			Metadata:   meta,
		})
	} else if isResubmission {
		go CreateNotification(
			userId,
			"Documents Resubmitted",
			"Your vehicle documents have been resubmitted and are under review.",
			"document_verification",
			"",
			nil,
		)

		meta := map[string]string{
			"driverId":   userId.Hex(),
			"userId":     userId.Hex(),
			"actorName":  body.Name,
			"actorPhone": currentUser.PhoneNumber,
			"status":     string(models.VerificationStatusPending),
		}
		if body.Vehicle != nil {
			meta["vehicleName"] = body.Vehicle.VehicleName
			meta["vehicleNumber"] = body.Vehicle.VehicleNumber
		}
		NotifyAdminsAsync(AdminNotificationInput{
			Title:      "Driver Resubmitted Documents",
			Message:    fmt.Sprintf("Driver %s (%s) resubmitted KYC documents for review.", body.Name, currentUser.PhoneNumber),
			Type:       models.NotifTypeDriverKYCResubmitted,
			Category:   models.NotifCategoryDrivers,
			TargetView: models.AdminViewDriverVerification,
			RelatedID:  userId.Hex(),
			Metadata:   meta,
		})
	} else {
		if isNewProfileRegistration {
			NotifyAdminsAsync(AdminNotificationInput{
				Title:      "New User Registered",
				Message:    fmt.Sprintf("%s joined GoRaahi as %s (%s).", body.Name, effectiveRole, currentUser.PhoneNumber),
				Type:       models.NotifTypeUserRegistered,
				Category:   models.NotifCategoryUsers,
				TargetView: models.AdminViewAdminUsers,
				RelatedID:  userId.Hex(),
				Metadata: map[string]string{
					"userId":     userId.Hex(),
					"actorName":  body.Name,
					"actorPhone": currentUser.PhoneNumber,
					"role":       effectiveRole,
				},
			})
		}
		if vehicleAddedOrUpdated && body.Vehicle != nil {
			NotifyAdminsAsync(AdminNotificationInput{
				Title:      "Vehicle Details Updated",
				Message:    fmt.Sprintf("%s (%s) updated vehicle %s (%s) — ₹%.0f/km.", body.Name, currentUser.PhoneNumber, body.Vehicle.VehicleName, body.Vehicle.VehicleNumber, body.Vehicle.RatePerKm),
				Type:       models.NotifTypeVehicleUpdated,
				Category:   models.NotifCategoryDrivers,
				TargetView: models.AdminViewVehicles,
				RelatedID:  userId.Hex(),
				Metadata: map[string]string{
					"driverId":      userId.Hex(),
					"userId":        userId.Hex(),
					"actorName":     body.Name,
					"actorPhone":    currentUser.PhoneNumber,
					"vehicleName":   body.Vehicle.VehicleName,
					"vehicleNumber": body.Vehicle.VehicleNumber,
					"ratePerKm":     fmt.Sprintf("%.2f", body.Vehicle.RatePerKm),
				},
			})
		}
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
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Database error fetching profile"})
		return
	}

	c.JSON(http.StatusOK, user)
}

func removeUserFCMToken(ctx context.Context, userId primitive.ObjectID, token string) error {
	if _, err := userProfileCollection.UpdateOne(
		ctx,
		bson.M{"_id": userId},
		bson.M{"$pull": bson.M{"fcm_tokens": token}},
	); err != nil {
		return err
	}
	if _, err := userProfileCollection.UpdateOne(
		ctx,
		bson.M{"_id": userId, "fcm_token": token},
		bson.M{"$unset": bson.M{"fcm_token": ""}},
	); err != nil {
		return err
	}
	return nil
}

func Logout(c *gin.Context) {
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

	// Increment TokenVersion to invalidate all existing tokens
	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	res, err := userProfileCollection.UpdateOne(
		dbCtx,
		bson.M{"_id": userId},
		bson.M{"$inc": bson.M{"token_version": 1}},
	)
	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to logout"})
		return
	}
	if res.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	if token := strings.TrimSpace(body.FCMToken); token != "" {
		if fcmErr := removeUserFCMToken(dbCtx, userId, token); fcmErr != nil {
			c.JSON(config.DBErrorHTTPStatus(fcmErr), gin.H{"error": "Session revoked, but failed to clear FCM token"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "Logged out and all sessions revoked successfully"})
}

// UpdateFCMToken stores or refreshes the user's FCM device push token in the database.
// Called by the app after login and whenever the FCM token is refreshed.
// Maintains a deduplicated, bounded list (max 20) of active device tokens.
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

	if _, err := userProfileCollection.UpdateOne(
		dbCtx,
		bson.M{"_id": userId},
		bson.M{"$pull": bson.M{"fcm_tokens": token}},
	); err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to update FCM token"})
		return
	}

	res, err := userProfileCollection.UpdateOne(
		dbCtx,
		bson.M{"_id": userId},
		bson.M{
			"$set": bson.M{"fcm_token": token},
			"$push": bson.M{
				"fcm_tokens": bson.M{
					"$each":  []string{token},
					"$slice": -maxFCMTokensPerUser,
				},
			},
		},
	)
	if err != nil {
		c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to update FCM token"})
		return
	}
	if res.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
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
		if err := removeUserFCMToken(dbCtx, userId, token); err != nil {
			c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to remove FCM token"})
			return
		}
	} else {
		_, err := userProfileCollection.UpdateOne(
			dbCtx,
			bson.M{"_id": userId},
			bson.M{"$unset": bson.M{"fcm_token": "", "fcm_tokens": ""}},
		)
		if err != nil {
			c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": "Failed to remove FCM token"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "FCM token removed"})
}


