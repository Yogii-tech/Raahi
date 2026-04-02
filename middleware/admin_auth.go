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

func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		userIdVal, exists := c.Get("userId")
		if !exists {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized user"})
			return
		}

		userId := userIdVal.(primitive.ObjectID)
		var user models.User
		err := config.Database.Collection("users").FindOne(context.Background(), bson.M{"_id": userId}).Decode(&user)
		if err != nil || user.Role != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Admin access required"})
			return
		}

		c.Next()
	}
}
