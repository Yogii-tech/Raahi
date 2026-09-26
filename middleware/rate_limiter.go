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

// SECURITY: Cleanup goroutine to evict expired rate limiter entries (prevents memory leak)
func init() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			limiterStore.mu.Lock()
			cutoff := time.Now().Add(-10 * time.Minute)
			for k, times := range limiterStore.phoneLimits {
				var active []time.Time
				for _, t := range times {
					if t.After(cutoff) {
						active = append(active, t)
					}
				}
				if len(active) == 0 {
					delete(limiterStore.phoneLimits, k)
				} else {
					limiterStore.phoneLimits[k] = active
				}
			}
			for k, times := range limiterStore.ipLimits {
				var active []time.Time
				for _, t := range times {
					if t.After(cutoff) {
						active = append(active, t)
					}
				}
				if len(active) == 0 {
					delete(limiterStore.ipLimits, k)
				} else {
					limiterStore.ipLimits[k] = active
				}
			}
			limiterStore.mu.Unlock()
		}
	}()
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

// ──────────────────────────────────────────────────────────────────────
// SECURITY: Global per-user rate limiter for authenticated API endpoints
// ──────────────────────────────────────────────────────────────────────

type userRateLimiterStore struct {
	mu      sync.Mutex
	buckets map[string][]time.Time
}

var globalLimiterStore = &userRateLimiterStore{
	buckets: make(map[string][]time.Time),
}

// Cleanup goroutine for the global limiter
func init() {
	go func() {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			globalLimiterStore.mu.Lock()
			cutoff := time.Now().Add(-1 * time.Minute)
			for k, times := range globalLimiterStore.buckets {
				var active []time.Time
				for _, t := range times {
					if t.After(cutoff) {
						active = append(active, t)
					}
				}
				if len(active) == 0 {
					delete(globalLimiterStore.buckets, k)
				} else {
					globalLimiterStore.buckets[k] = active
				}
			}
			globalLimiterStore.mu.Unlock()
		}
	}()
}

// GlobalRateLimiter limits authenticated users to maxRequests per window.
// It keys on the "userId" set by AuthMiddleware. If userId is not set
// (unauthenticated route), it falls back to client IP.
// label is an optional suffix appended to the bucket key so that multiple
// stacked GlobalRateLimiter instances on the same route don't share a bucket.
// Pass an empty string "" to use the default key (backward compatible).
func GlobalRateLimiter(maxRequests int, window time.Duration, label ...string) gin.HandlerFunc {
	suffix := ""
	if len(label) > 0 && label[0] != "" {
		suffix = ":" + label[0]
	}
	return func(c *gin.Context) {
		// Prefer userId (set by AuthMiddleware); fall back to IP
		base := c.ClientIP()
		if uid, exists := c.Get("userId"); exists {
			base = "user:" + uid.(interface{ Hex() string }).Hex()
		}
		key := base + suffix

		now := time.Now()
		cutoff := now.Add(-window)

		globalLimiterStore.mu.Lock()
		requests := globalLimiterStore.buckets[key]
		var active []time.Time
		for _, t := range requests {
			if t.After(cutoff) {
				active = append(active, t)
			}
		}

		if len(active) >= maxRequests {
			globalLimiterStore.mu.Unlock()
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":       "Rate limit exceeded. Please slow down.",
				"retry_after": int(window.Seconds()),
			})
			c.Abort()
			return
		}

		active = append(active, now)
		globalLimiterStore.buckets[key] = active
		globalLimiterStore.mu.Unlock()

		c.Next()
	}
}
