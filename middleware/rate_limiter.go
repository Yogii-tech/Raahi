package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type PhoneLimiter struct {
	mu     sync.Mutex
	limits map[string][]time.Time
}

var otpLimiter = &PhoneLimiter{
	limits: make(map[string][]time.Time),
}

func OTPRateLimiter() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Read body to extract phone number
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
			c.Abort()
			return
		}
		
		// Restore body so next handlers can read it
		c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

		var req struct {
			PhoneNumber string `json:"phone_number"`
		}
		
		if err := json.Unmarshal(bodyBytes, &req); err != nil || req.PhoneNumber == "" {
			c.Next()
			return
		}

		phone := req.PhoneNumber
		now := time.Now()
		tenMinsAgo := now.Add(-10 * time.Minute)

		otpLimiter.mu.Lock()
		requests := otpLimiter.limits[phone]
		
		// Filter out requests older than 10 minutes
		var activeRequests []time.Time
		for _, t := range requests {
			if t.After(tenMinsAgo) {
				activeRequests = append(activeRequests, t)
			}
		}

		if len(activeRequests) >= 3 {
			otpLimiter.mu.Unlock()
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many OTP requests. Please try again after 10 minutes."})
			c.Abort()
			return
		}

		activeRequests = append(activeRequests, now)
		otpLimiter.limits[phone] = activeRequests
		otpLimiter.mu.Unlock()

		c.Next()
	}
}
