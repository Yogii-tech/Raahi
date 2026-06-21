package middleware

import (
	"net/http"
	"strings"

	"raahi-backend/config"
	"raahi-backend/utils"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
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
