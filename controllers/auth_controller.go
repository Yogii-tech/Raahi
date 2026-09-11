package controllers

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"raahi-backend/config"
	"raahi-backend/models"
	"raahi-backend/utils"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

var (
	userCollection *mongo.Collection
)

func InitializeAuthCollection() {
	userCollection = config.Database.Collection("users")
}

func VerifyFirebaseToken(c *gin.Context) {
	var body struct {
		IDToken     string `json:"id_token"`
		PhoneNumber string `json:"phone_number"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	phoneNum := strings.TrimSpace(body.PhoneNumber)
	fbAuth := utils.GetFirebaseAuth()

	if fbAuth == nil {
		log.Println("[Firebase Auth] Warning: Firebase Auth client is not initialized")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Firebase Auth client not initialized on server"})
		return
	}

	if body.IDToken == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Firebase ID Token is required for verification"})
		return
	}

	clientIP := c.ClientIP()
	token, err := fbAuth.VerifyIDToken(c.Request.Context(), body.IDToken)
	if err != nil {
		log.Printf("[Firebase Auth] Token verification failed for IP %s: %v", clientIP, err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired Firebase verification token"})
		return
	}
	log.Printf("[Firebase Auth SUCCESS] IP: %s | Verified token for phone: %s", clientIP, phoneNum)

	if claimPhone, ok := token.Claims["phone_number"].(string); ok && claimPhone != "" {
		phoneNum = claimPhone
	}

	cleanPhone := strings.TrimPrefix(phoneNum, "+91")
	cleanPhone = strings.TrimPrefix(cleanPhone, "+")
	if len(cleanPhone) > 10 && strings.HasPrefix(cleanPhone, "91") {
		cleanPhone = cleanPhone[2:]
	}

	if cleanPhone == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Phone number missing or invalid"})
		return
	}

	dbCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	var user models.User
	err = userCollection.FindOne(dbCtx, bson.M{
		"$or": []bson.M{
			{"phone_number": cleanPhone},
			{"phone_number": phoneNum},
			{"phone_number": "+91" + cleanPhone},
		},
	}).Decode(&user)

	if err != nil {
		if err == mongo.ErrNoDocuments {
			newUser := models.User{
				ID:          primitive.NewObjectID(),
				PhoneNumber: cleanPhone,
				SubmittedAt: time.Now(),
			}
			dbCtx2, cancel2 := context.WithTimeout(c.Request.Context(), 5*time.Second)
			defer cancel2()
			_, insertErr := userCollection.InsertOne(dbCtx2, newUser)
			if insertErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create user record"})
				return
			}
			user = newUser
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database query error"})
			return
		}
	}

	jwtToken, err := utils.GenerateJWT(user.ID, user.TokenVersion)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate session token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token": jwtToken,
		"user":  user,
	})
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

	providedKey := strings.Trim(strings.TrimSpace(body.SecretKey), "\"'")
	expectedEnvKey := strings.Trim(strings.TrimSpace(os.Getenv("ADMIN_SECRET_KEY")), "\"'")
	defaultKey := "RAAHI_ADMIN_2026"
	isProd := os.Getenv("APP_ENV") == "production"

	// Only allow the fallback default key if we are NOT in production.
	// In production, the environment variable MUST be set and match exactly.
	isValid := false
	if isProd {
		isValid = (expectedEnvKey != "" && providedKey == expectedEnvKey)
	} else {
		isValid = (providedKey == defaultKey) || (expectedEnvKey != "" && providedKey == expectedEnvKey)
	}

	if !isValid {
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
