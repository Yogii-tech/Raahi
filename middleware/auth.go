package middleware

import (
	"net/http"
	"strings"

	"raahi-backend/config"
	"raahi-backend/utils"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header missing"})
			c.Abort()
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization format"})
			c.Abort()
			return
		}

		userId, tokenVersion, err := utils.ValidateJWT(parts[1])
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			c.Abort()
			return
		}

		// Session Revocation Check: Verify version against DB
		var user struct {
			TokenVersion int `bson:"token_version"`
		}
		err = config.Database.Collection("users").FindOne(c.Request.Context(), bson.M{"_id": userId}).Decode(&user)
		if err != nil || user.TokenVersion != tokenVersion {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Session revoked or expired"})
			c.Abort()
			return
		}

		c.Set("userId", userId)
		c.Next()
	}
}

func AdminOnlyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		userIdVal, exists := c.Get("userId")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			c.Abort()
			return
		}
		userId, ok := userIdVal.(primitive.ObjectID)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			c.Abort()
			return
		}

		var user struct {
			Role string `bson:"role"`
		}
		err := config.Database.Collection("users").FindOne(c.Request.Context(), bson.M{"_id": userId}).Decode(&user)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "User not found"})
			c.Abort()
			return
		}

		if user.Role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Forbidden: Requires Admin role"})
			c.Abort()
			return
		}

		c.Next()
	}
}
