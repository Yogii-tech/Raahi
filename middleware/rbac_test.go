package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"raahi-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestRequireRole(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("Forbidden when role does not match", func(t *testing.T) {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			// Mocking a passenger user
			c.Set("userId", primitive.NewObjectID())
			// This mimics skipping the DB fetch for testing middleware logic
			c.Set("user", models.User{Role: models.RolePassenger})
		})

		// This endpoint requires "driver"
		r.GET("/test", RequireRole(models.RoleDriver), func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("Success when role matches", func(t *testing.T) {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("userId", primitive.NewObjectID())
			c.Set("user", models.User{Role: models.RoleDriver})
		})

		r.GET("/test", RequireRole(models.RoleDriver), func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})
	t.Run("Unauthorized when userID is missing", func(t *testing.T) {
		r := gin.New()
		r.GET("/test", RequireRole(models.RoleDriver), func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("Success when any of multiple roles match", func(t *testing.T) {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("userId", primitive.NewObjectID())
			c.Set("user", models.User{Role: models.RoleAdmin})
		})

		// If Admin or Driver
		r.GET("/test", RequireRole(models.RoleAdmin, models.RoleDriver), func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})
}
