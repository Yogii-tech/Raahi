package controllers

import (
	"net/http"
	"os"

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
		PhoneNumber string `json:"phone_number" binding:"required,numeric,len=10"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid phone number format. Expected 10 digits."})
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

	_, err := userCollection.UpdateOne(c.Request.Context(), filter, update, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate OTP"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "OTP sent"})
}

func VerifyOTP(c *gin.Context) {
	var body struct {
		PhoneNumber string `json:"phone_number" binding:"required,numeric,len=10"`
		OTP         string `json:"otp" binding:"required,numeric,len=6"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid phone number or OTP format"})
		return
	}

	var user models.User
	err := userCollection.FindOne(
		c.Request.Context(),
		bson.M{"phone_number": body.PhoneNumber, "otp": body.OTP},
	).Decode(&user)

	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid OTP"})
		return
	}

	// Create both Access and Refresh tokens
	accessToken, _ := utils.GenerateJWT(user.ID)
	refreshToken, _ := utils.GenerateRefreshToken(user.ID)

	// Save refresh token in DB and clear OTP
	_, err = userCollection.UpdateOne(
		c.Request.Context(),
		bson.M{"_id": user.ID},
		bson.M{
			"$set": bson.M{
				"otp":           "",
				"refresh_token": refreshToken,
			},
		},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to login"})
		return
	}

	// Set HttpOnly cookies for Web clients
	isSecure := os.Getenv("GIN_MODE") == "release"
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("auth_token", accessToken, 3600, "/", "", isSecure, true)
	c.SetCookie("auth_refresh_token", refreshToken, 30*24*3600, "/", "", isSecure, true)

	c.JSON(http.StatusOK, gin.H{
		"token":         accessToken,
		"refresh_token": refreshToken,
		"user": gin.H{
			"id":           user.ID,
			"phone_number": user.PhoneNumber,
			"name":         user.Name,
			"role":         user.Role,
			"language":     user.Language,
			"vehicle":      user.Vehicle,
		},
	})
}

func RefreshToken(c *gin.Context) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	c.ShouldBindJSON(&body)

	refreshToken := body.RefreshToken
	if refreshToken == "" {
		// Fallback to cookie
		if cookie, err := c.Cookie("auth_refresh_token"); err == nil {
			refreshToken = cookie
		}
	}

	if refreshToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Refresh token is required"})
		return
	}

	// Validate JWT structure/expiry
	userId, err := utils.ValidateRefreshToken(refreshToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired refresh token"})
		return
	}

	// Check if this token exists in our DB for this user
	var user models.User
	err = userCollection.FindOne(c.Request.Context(), bson.M{
		"_id":           userId,
		"refresh_token": refreshToken,
	}).Decode(&user)

	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Token revoked or mismatch"})
		return
	}

	// Token is valid and matches DB, issue NEW access token
	newAccessToken, _ := utils.GenerateJWT(userId)

	// Set HttpOnly cookie for new token
	isSecure := os.Getenv("GIN_MODE") == "release"
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("auth_token", newAccessToken, 3600, "/", "", isSecure, true)

	c.JSON(http.StatusOK, gin.H{
		"token": newAccessToken,
	})
}

func Logout(c *gin.Context) {
	c.SetCookie("auth_token", "", -1, "/", "", false, true)
	c.SetCookie("auth_refresh_token", "", -1, "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{"message": "Logged out successfully"})
}

func PromoteToAdmin(c *gin.Context) {
	userIdVal, exists := c.Get("userId")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized user"})
		return
	}
	userId := userIdVal.(primitive.ObjectID)

	var body struct {
		SecretKey string `json:"secret_key" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Secret key is required"})
		return
	}

	adminKey := os.Getenv("ADMIN_PROMOTION_KEY")
	if adminKey == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Admin promotion is not configured on this server"})
		return
	}

	if body.SecretKey != adminKey {
		c.JSON(http.StatusForbidden, gin.H{"error": "Invalid secret key"})
		return
	}

	_, err := userCollection.UpdateOne(
		c.Request.Context(),
		bson.M{"_id": userId},
		bson.M{"$set": bson.M{"role": "admin"}},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update role"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Promoted to admin successfully", "role": "admin"})
}
