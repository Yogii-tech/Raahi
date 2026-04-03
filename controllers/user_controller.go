package controllers

import (
	"context"
	"net/http"

	"raahi-backend/config"
	"raahi-backend/models"
	"raahi-backend/utils"

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
	err := userProfileCollection.FindOne(context.Background(), bson.M{"_id": userId}).Decode(&user)
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

	// Validation: All contacts must have valid 10-digit phone numbers
	for _, contact := range contacts {
		if len(contact.Phone) != 10 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Trusted contacts must have valid 10-digit phone numbers"})
			return
		}
	}

	// Explicit limit of 2 contacts (No silent truncation)
	if len(contacts) > 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Maximum 2 trusted contacts allowed"})
		return
	}

	_, err := userProfileCollection.UpdateOne(
		context.Background(),
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
		Name     string                 `json:"name"`
		Role     string                 `json:"role"`
		Language string                 `json:"language"`
		Vehicle  *models.VehicleDetails `json:"vehicle"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// Validation: Name length and sanitization
	sanitizedName := utils.SanitizeString(body.Name)
	if len(sanitizedName) < 2 || len(sanitizedName) > 50 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Name must be between 2 and 50 characters"})
		return
	}

	// Fetch current user role
	var currentUser models.User
	userProfileCollection.FindOne(context.Background(), bson.M{"_id": userId}).Decode(&currentUser)

	updateFields := bson.M{
		"name": sanitizedName,
	}

	// Only update role if user is NOT already an admin.
	// This allows normal users to switch between driver <-> passenger
	// but prevents them from promoting themselves to admin.
	if currentUser.Role != models.RoleAdmin && (body.Role == models.RoleDriver || body.Role == models.RolePassenger) {
		updateFields["role"] = body.Role
	}

	if body.Language != "" {
		updateFields["language"] = body.Language
	}

	if body.Vehicle != nil {
		updateFields["vehicle"] = body.Vehicle
	}

	update := bson.M{
		"$set": updateFields,
	}

	_, err := userProfileCollection.UpdateOne(
		context.Background(),
		bson.M{"_id": userId},
		update,
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update profile"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Profile updated successfully"})
}

func GetProfile(c *gin.Context) {
	userId := c.MustGet("userId").(primitive.ObjectID)

	var user models.User
	err := userProfileCollection.FindOne(context.Background(), bson.M{"_id": userId}).Decode(&user)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	c.JSON(http.StatusOK, user)
}
