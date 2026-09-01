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

	// Limit to 2 contacts
	if len(contacts) > 2 {
		contacts = contacts[:2]
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

func UpdateProfile(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	var body struct {
		Name    string          `json:"name" binding:"required,min=2,max=100"`
		Role    string          `json:"role" binding:"required,oneof=passenger driver parceller"` // admin role only via /promote-admin
		Vehicle *models.Vehicle `json:"vehicle"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
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
	// (Except if admin is somehow updating profile, but /profile is standard user endpoint)
	if currentUser.Role != "" && currentUser.Role != "admin" && currentUser.Role != body.Role {
		c.JSON(http.StatusForbidden, gin.H{"error": "Roles cannot be changed after registering it one time"})
		return
	}

	setMap := bson.M{
		"name": body.Name,
		"role": body.Role,
	}

	if body.Vehicle != nil {
		setMap["vehicle"] = body.Vehicle
	}

	isFirstSubmission := false
	isResubmission := false

	if body.Role == "driver" && body.Vehicle != nil {
		if currentUser.VerificationStatus == "" {
			setMap["verification_status"] = "pending"
			setMap["submitted_at"] = time.Now()
			isFirstSubmission = true
		} else if currentUser.VerificationStatus == "rejected" {
			setMap["verification_status"] = "pending"
			setMap["submitted_at"] = time.Now()
			isResubmission = true
		}
		// If verification_status is already "pending" or "verified", we do not reset it just because they update profile.
	}

	update := bson.M{
		"$set": setMap,
	}

	_, err = userProfileCollection.UpdateOne(
		dbCtx,
		bson.M{"_id": userId},
		update,
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update profile"})
		return
	}

	if isFirstSubmission {
		go CreateNotification(
			userId,
			"Documents Submitted Successfully",
			"Your vehicle documents have been submitted and are under review. You will be notified once the admin approves or rejects your application.",
			"document_verification",
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
