package middleware

import (
	"context"
	"net/http"
	"raahi-backend/config"
	"raahi-backend/models"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func RequireRole(allowedRoles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIdVal, exists := c.Get("userId")
		if !exists {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: No userID found"})
			return
		}

		userId := userIdVal.(primitive.ObjectID)
		var user models.User

		// Check if user is already in context (from a previous middleware or mock)
		if userVal, exists := c.Get("user"); exists {
			user = userVal.(models.User)
		} else {
			err := config.Database.Collection("users").FindOne(context.Background(), bson.M{"_id": userId}).Decode(&user)
			if err != nil {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "User no longer exists"})
				return
			}
		}

		// Check if user's role is in the allowed list
		isAllowed := false
		for _, role := range allowedRoles {
			if user.Role == role {
				isAllowed = true
				break
			}
		}

		if !isAllowed {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Permission denied: access restricted for your role"})
			return
		}

		// Optionally store the user object in the context for downstream reuse
		c.Set("user", user)
		c.Next()
	}
}
