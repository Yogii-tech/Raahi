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
	"go.mongodb.org/mongo-driver/mongo/options"
)

var userCollection *mongo.Collection

func InitializeAuthCollection() {
	userCollection = config.Database.Collection("users")
}

func SendOTP(c *gin.Context) {
	var body struct {
		PhoneNumber string `json:"phone_number"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// In a real app, generate a 6-digit random OTP and send it via SMS
	otp := "123456"

	// Upsert user: Update if exists, create if not
	opts := options.Update().SetUpsert(true)
	filter := bson.M{"phone_number": body.PhoneNumber}
	update := bson.M{
		"$set":         bson.M{"otp": otp},
		"$setOnInsert": bson.M{"_id": primitive.NewObjectID(), "language": "en"},
	}

	_, err := userCollection.UpdateOne(context.Background(), filter, update, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate OTP"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "OTP sent", "otp": otp}) // For dev, returning OTP in response
}

func VerifyOTP(c *gin.Context) {
	var body struct {
		PhoneNumber string `json:"phone_number"`
		OTP         string `json:"otp"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	var user models.User
	err := userCollection.FindOne(
		context.Background(),
		bson.M{"phone_number": body.PhoneNumber, "otp": body.OTP},
	).Decode(&user)

	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid OTP"})
		return
	}

	// Create JWT token
	token, _ := utils.GenerateJWT(user.ID)

	// Optionally clear OTP after verification
	userCollection.UpdateOne(
		context.Background(),
		bson.M{"_id": user.ID},
		bson.M{"$set": bson.M{"otp": ""}},
	)

	c.JSON(http.StatusOK, gin.H{"token": token, "user": user})
}

func PromoteToAdmin(c *gin.Context) {
	userIdVal, exists := c.Get("userId")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized user"})
		return
	}
	userId := userIdVal.(primitive.ObjectID)

	var body struct {
		SecretKey string `json:"secret_key"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	if body.SecretKey != "admin_secret_123" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Invalid secret key"})
		return
	}

	_, err := userCollection.UpdateOne(
		context.Background(),
		bson.M{"_id": userId},
		bson.M{"$set": bson.M{"role": "admin"}},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update role"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Promoted to admin successfully", "role": "admin"})
}
