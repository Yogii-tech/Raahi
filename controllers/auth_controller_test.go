package controllers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestAuthValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("SendOTP rejects non-numeric phone number", func(t *testing.T) {
		r := gin.New()
		r.POST("/otp/send", SendOTP)

		body := bytes.NewBufferString(`{"phone_number": "99999abcde"}`)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/otp/send", body)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "Invalid phone number format")
	})

	t.Run("SendOTP rejects short phone number", func(t *testing.T) {
		r := gin.New()
		r.POST("/otp/send", SendOTP)

		body := bytes.NewBufferString(`{"phone_number": "123456"}`)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/otp/send", body)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("VerifyOTP rejects short OTP", func(t *testing.T) {
		r := gin.New()
		r.POST("/otp/verify", VerifyOTP)

		body := bytes.NewBufferString(`{"phone_number": "9999999999", "otp": "123"}`)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/otp/verify", body)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "Invalid phone number or OTP format")
	})

	t.Run("PromoteToAdmin rejects missing secret key", func(t *testing.T) {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("userId", primitive.NewObjectID())
		})
		r.POST("/promote-admin", PromoteToAdmin)

		body := bytes.NewBufferString(`{}`) // Empty JSON
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/promote-admin", body)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "Secret key is required")
	})
}
