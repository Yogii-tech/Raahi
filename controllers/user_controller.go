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

	setMap := bson.M{
		"name":    body.Name,
		"role":    body.Role,
		"vehicle": body.Vehicle,
	}

	if body.Role == "driver" {
		setMap["verification_status"] = "pending"
		setMap["submitted_at"] = time.Now()
	}

	update := bson.M{
		"$set": setMap,
	}

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	_, err := userProfileCollection.UpdateOne(
		dbCtx,
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
