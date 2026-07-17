package controllers

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"time"

	"raahi-backend/config"
	"raahi-backend/models"
	"raahi-backend/utils"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/crypto/bcrypt"
)

var userCollection *mongo.Collection

func generateRandomOTP() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(900000))
	return fmt.Sprintf("%06d", n.Int64()+100000)
}

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
	otp := generateRandomOTP()

	hashedOTP, err := bcrypt.GenerateFromPassword([]byte(otp), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process OTP"})
		return
	}

	// Set OTP with 10-minute expiry
	otpExpiry := time.Now().Add(10 * time.Minute)
	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	_, err = userCollection.UpdateOne(
		dbCtx,
		bson.M{"phone_number": body.PhoneNumber},
		bson.M{"$set": bson.M{"otp": string(hashedOTP), "otp_expiry": otpExpiry}},
		nil,
	)

	// Send OTP via MSG91 (falls back to console log if not configured)
	if smsErr := utils.SendOTPviaMSG91(body.PhoneNumber, otp); smsErr != nil {
		fmt.Printf("[WARN] Failed to send SMS: %v\n", smsErr)
	}

	// If user doesn't exist, create it
	if err == nil {
		var user models.User
		dbCtx2, cancel2 := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel2()
		err = userCollection.FindOne(dbCtx2, bson.M{"phone_number": body.PhoneNumber}).Decode(&user)
		if err != nil {
			if err == mongo.ErrNoDocuments {
				newUser := models.User{
					ID:          primitive.NewObjectID(),
					PhoneNumber: body.PhoneNumber,
					OTP:         string(hashedOTP),
					OTPExpiry:   otpExpiry,
				}
				dbCtx3, cancel3 := context.WithTimeout(c.Request.Context(), 5*time.Second)
				defer cancel3()
				userCollection.InsertOne(dbCtx3, newUser)
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
				return
			}
		}
	} else {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "OTP sent"})
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
	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	err := userCollection.FindOne(
		dbCtx,
		bson.M{"phone_number": body.PhoneNumber},
	).Decode(&user)

	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid phone number or OTP"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.OTP), []byte(body.OTP)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid OTP"})
		return
	}

	// Check OTP expiry (10 minutes)
	if !user.OTPExpiry.IsZero() && time.Now().After(user.OTPExpiry) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "OTP has expired. Please request a new one."})
		return
	}

	// Create JWT token
	token, _ := utils.GenerateJWT(user.ID, user.TokenVersion)

	// Option to clear OTP after verification
	dbCtx2, cancel2 := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel2()
	userCollection.UpdateOne(
		dbCtx2,
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

	expectedKey := os.Getenv("ADMIN_SECRET_KEY")
	if expectedKey == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Admin promotion is currently unavailable"})
		return
	}

	if body.SecretKey != expectedKey {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid admin secret key"})
		return
	}

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	_, err := userCollection.UpdateOne(
		dbCtx,
		bson.M{"_id": userId},
		bson.M{"$set": bson.M{"role": "admin"}},
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to promote user to admin"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Successfully promoted to admin"})
}
