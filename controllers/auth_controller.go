package controllers

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"sync"
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

var (
	userCollection *mongo.Collection
	otpLimitsMu    sync.Mutex
	phoneOTPLimits = make(map[string]*rateLimitEntry)
	ipOTPLimits    = make(map[string]*rateLimitEntry)
	checkCount     int
)

type rateLimitEntry struct {
	Count     int
	ResetTime time.Time
}

func checkRateLimit(phone string, ip string) error {
	otpLimitsMu.Lock()
	defer otpLimitsMu.Unlock()

	now := time.Now()

	// Clean up old entries to prevent memory growth
	checkCount++
	if checkCount%100 == 0 {
		for k, v := range ipOTPLimits {
			if now.After(v.ResetTime) {
				delete(ipOTPLimits, k)
			}
		}
		for k, v := range phoneOTPLimits {
			if now.After(v.ResetTime) {
				delete(phoneOTPLimits, k)
			}
		}
	}

	// 1. Check IP rate limit
	if entry, exists := ipOTPLimits[ip]; exists {
		if now.Before(entry.ResetTime) {
			if entry.Count >= 5 {
				return fmt.Errorf("too many requests from this IP. Please try again later")
			}
			entry.Count++
		} else {
			entry.Count = 1
			entry.ResetTime = now.Add(5 * time.Minute)
		}
	} else {
		ipOTPLimits[ip] = &rateLimitEntry{
			Count:     1,
			ResetTime: now.Add(5 * time.Minute),
		}
	}

	// 2. Check Phone number rate limit
	if entry, exists := phoneOTPLimits[phone]; exists {
		if now.Before(entry.ResetTime) {
			if entry.Count >= 3 {
				return fmt.Errorf("too many OTP requests for this phone number. Please try again in 5 minutes")
			}
			entry.Count++
		} else {
			entry.Count = 1
			entry.ResetTime = now.Add(5 * time.Minute)
		}
	} else {
		phoneOTPLimits[phone] = &rateLimitEntry{
			Count:     1,
			ResetTime: now.Add(5 * time.Minute),
		}
	}

	return nil
}

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

	// Apply Rate Limiting
	clientIP := c.ClientIP()
	if err := checkRateLimit(body.PhoneNumber, clientIP); err != nil {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
		return
	}

	appEnv := os.Getenv("APP_ENV")
	otp := "123456"
	if appEnv != "development" && appEnv != "" {
		otp = generateRandomOTP()
	}

	hashedOTP, err := bcrypt.GenerateFromPassword([]byte(otp), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process OTP"})
		return
	}

	// Set OTP with 10-minute expiry
	otpExpiry := time.Now().Add(10 * time.Minute)
	_, err = userCollection.UpdateOne(
		context.Background(),
		bson.M{"phone_number": body.PhoneNumber},
		bson.M{"$set": bson.M{"otp": string(hashedOTP), "otp_expiry": otpExpiry}},
		nil,
	)

	// Send OTP via MSG91 SMS
	smsSent := false
	if smsErr := utils.SendOTPviaMSG91(body.PhoneNumber, otp); smsErr != nil {
		log.Printf("[WARN] Failed to send SMS: %v", smsErr)
	} else {
		smsSent = true
	}

	// If user doesn't exist, create it
	if err == nil {
		var user models.User
		err = userCollection.FindOne(context.Background(), bson.M{"phone_number": body.PhoneNumber}).Decode(&user)
		if err != nil {
			if err == mongo.ErrNoDocuments {
				newUser := models.User{
					ID:          primitive.NewObjectID(),
					PhoneNumber: body.PhoneNumber,
					OTP:         string(hashedOTP),
					OTPExpiry:   otpExpiry,
				}
				userCollection.InsertOne(context.Background(), newUser)
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
				return
			}
		}
	} else {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}

	// In dev (SMS not configured), return OTP in response for testing.
	// In production, OTP is delivered only via SMS — never exposed in API.
	if !smsSent && (appEnv == "development" || appEnv == "") {
		c.JSON(http.StatusOK, gin.H{"message": "OTP sent", "otp": otp})
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
	err := userCollection.FindOne(
		context.Background(),
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

	expectedKey := os.Getenv("ADMIN_SECRET_KEY")
	if expectedKey == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Admin promotion is not configured"})
		return
	}

	if body.SecretKey != expectedKey {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid admin secret key"})
		return
	}

	_, err := userCollection.UpdateOne(
		context.Background(),
		bson.M{"_id": userId},
		bson.M{"$set": bson.M{"role": "admin"}},
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to promote user to admin"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Successfully promoted to admin"})
}
