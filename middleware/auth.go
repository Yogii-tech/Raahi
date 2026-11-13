package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"raahi-backend/config"
	"raahi-backend/utils"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type authUserState struct {
	TokenVersion int    `bson:"token_version"`
	Role         string `bson:"role"`
}

var fetchAuthUser = func(ctx context.Context, userID primitive.ObjectID) (authUserState, error) {
	var user authUserState
	if config.Database == nil {
		return user, errors.New("database is not initialized")
	}
	dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := config.Database.Collection("users").FindOne(dbCtx, bson.M{"_id": userID}).Decode(&user)
	return user, err
}

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

		// Session Revocation Check: Verify version against DB.
		// Operational DB errors return 503 so clients are not erroneously logged out during failovers.
		user, err := fetchAuthUser(c.Request.Context(), userId)
		if err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Session revoked or expired"})
			} else {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Authentication service temporarily unavailable"})
			}
			c.Abort()
			return
		}
		if user.TokenVersion != tokenVersion {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Session revoked or expired"})
			c.Abort()
			return
		}

		c.Set("userId", userId)
		c.Set("userRole", user.Role)
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

		var role string
		if roleVal, hasRole := c.Get("userRole"); hasRole {
			if r, ok := roleVal.(string); ok {
				role = r
			}
		} else {
			user, err := fetchAuthUser(c.Request.Context(), userId)
			if err != nil {
				if errors.Is(err, mongo.ErrNoDocuments) {
					c.JSON(http.StatusUnauthorized, gin.H{"error": "User not found"})
				} else {
					c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Authentication service temporarily unavailable"})
				}
				c.Abort()
				return
			}
			role = user.Role
		}

		if role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Forbidden: Requires Admin role"})
			c.Abort()
			return
		}

		c.Next()
	}
}
