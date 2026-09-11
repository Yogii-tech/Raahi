package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type OTPRateLimiterStore struct {
	mu          sync.Mutex
	phoneLimits map[string][]time.Time
	ipLimits    map[string][]time.Time
}

var limiterStore = &OTPRateLimiterStore{
	phoneLimits: make(map[string][]time.Time),
	ipLimits:    make(map[string][]time.Time),
}

func maskPhoneNumber(phone string) string {
	clean := strings.TrimSpace(phone)
	if len(clean) <= 4 {
		return "****"
	}
	return clean[:2] + "******" + clean[len(clean)-2:]
}

func OTPRateLimiter() gin.HandlerFunc {
	return func(c *gin.Context) {
		clientIP := c.ClientIP()

		// Read body to extract phone number if present
		var bodyBytes []byte
		if c.Request.Body != nil {
			bodyBytes, _ = io.ReadAll(c.Request.Body)
			// Restore body so downstream handlers can read it
			c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		}

		var req struct {
			PhoneNumber string `json:"phone_number"`
		}
		if len(bodyBytes) > 0 {
			_ = json.Unmarshal(bodyBytes, &req)
		}

		phone := strings.TrimSpace(req.PhoneNumber)
		now := time.Now()
		tenMinsAgo := now.Add(-10 * time.Minute)

		limiterStore.mu.Lock()
		defer limiterStore.mu.Unlock()

		// 1. IP Address Rate Limiting (max 5 requests per 10 minutes)
		ipRequests := limiterStore.ipLimits[clientIP]
		var activeIPRequests []time.Time
		for _, t := range ipRequests {
			if t.After(tenMinsAgo) {
				activeIPRequests = append(activeIPRequests, t)
			}
		}

		if len(activeIPRequests) >= 5 {
			log.Printf("[OTP RATE LIMIT EXCEEDED] IP: %s | Path: %s | Exceeded IP limit (5 per 10 mins)", clientIP, c.Request.URL.Path)
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":       "Too many OTP requests from your IP address. Please try again after 10 minutes.",
				"retry_after": 600,
			})
			c.Abort()
			return
		}

		// 2. Phone Number Rate Limiting (max 3 requests per 10 minutes)
		if phone != "" {
			phoneRequests := limiterStore.phoneLimits[phone]
			var activePhoneRequests []time.Time
			for _, t := range phoneRequests {
				if t.After(tenMinsAgo) {
					activePhoneRequests = append(activePhoneRequests, t)
				}
			}

			if len(activePhoneRequests) >= 3 {
				masked := maskPhoneNumber(phone)
				log.Printf("[OTP RATE LIMIT EXCEEDED] IP: %s | Phone: %s | Path: %s | Exceeded Phone limit (3 per 10 mins)", clientIP, masked, c.Request.URL.Path)
				c.JSON(http.StatusTooManyRequests, gin.H{
					"error":       "Too many OTP requests for this phone number. Please try again after 10 minutes.",
					"retry_after": 600,
				})
				c.Abort()
				return
			}

			activePhoneRequests = append(activePhoneRequests, now)
			limiterStore.phoneLimits[phone] = activePhoneRequests
		}

		activeIPRequests = append(activeIPRequests, now)
		limiterStore.ipLimits[clientIP] = activeIPRequests

		// Audit Log
		masked := "N/A"
		if phone != "" {
			masked = maskPhoneNumber(phone)
		}
		log.Printf("[OTP API LOG] Time: %s | IP: %s | Phone: %s | Path: %s | UserAgent: %s",
			now.Format("2006-01-02 15:04:05"), clientIP, masked, c.Request.URL.Path, c.Request.UserAgent())

		c.Next()
	}
}
