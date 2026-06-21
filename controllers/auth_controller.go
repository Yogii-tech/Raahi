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

var userCollection *mongo.Collection

func InitializeAuthCollection() {
	userCollection = config.Database.Collection("users")
}

func SendOTP(c *gin.Context) {
	var body struct {
		PhoneNumber string `json:"phone_number" binding:"required,min=10,max=15"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// In a real app, generate a 6-digit random OTP and send it via SMS
	otp := "123456"

	_, err := userCollection.UpdateOne(
		context.Background(),
		bson.M{"phone_number": body.PhoneNumber},
		bson.M{"$set": bson.M{"otp": otp}},
		nil,
	)

	// If user doesn't exist, create it
	if err == nil {
		var user models.User
		err = userCollection.FindOne(context.Background(), bson.M{"phone_number": body.PhoneNumber}).Decode(&user)
		if err != nil {
			newUser := models.User{
				ID:          primitive.NewObjectID(),
				PhoneNumber: body.PhoneNumber,
				OTP:         otp,
			}
			userCollection.InsertOne(context.Background(), newUser)
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "OTP sent", "otp": otp}) // For dev, returning OTP in response
}

func VerifyOTP(c *gin.Context) {
	var body struct {
		PhoneNumber string `json:"phone_number" binding:"required,min=10,max=15"`
		OTP         string `json:"otp" binding:"required,len=6"`
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
	token, _ := utils.GenerateJWT(user.ID, user.TokenVersion)

	// Optionally clear OTP after verification
	userCollection.UpdateOne(
		context.Background(),
		bson.M{"_id": user.ID},
		bson.M{"$set": bson.M{"otp": ""}},
	)

	c.JSON(http.StatusOK, gin.H{"token": token, "user": user})
}

func PromoteAdmin(c *gin.Context) {
	// Must be protected by generic AuthMiddleware so we know WHO to promote
	userId := c.MustGet("userId").(primitive.ObjectID)

	var body struct {
		SecretKey string `json:"secret_key"`
	}

	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// In a real app this should be in .env, using simple hardcoded for Raahi demo
	if body.SecretKey != "RAAHI_ADMIN_2026" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid admin secret key"})
		return
	}

	_, err := userCollection.UpdateOne(
		context.Background(),
		bson.M{"_id": userId},
		bson.M{"$set": bson.M{"role": "admin"}},
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to promote to admin"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Promoted to admin successfully"})
}
