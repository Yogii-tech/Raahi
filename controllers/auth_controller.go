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
		PhoneNumber string `json:"phone_number"`
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
