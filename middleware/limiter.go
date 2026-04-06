package middleware

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ulule/limiter/v3"
	mgin "github.com/ulule/limiter/v3/drivers/middleware/gin"
	"github.com/ulule/limiter/v3/drivers/store/memory"
)

// AuthRateLimiter limits the number of requests to authentication endpoints.
func AuthRateLimiter() gin.HandlerFunc {
	// Define the rate: 5 requests per minute
	rate := limiter.Rate{
		Period: 1 * time.Minute,
		Limit:  5,
	}

	// Use an in-memory store for now
	store := memory.NewStore()

	// Create the rate limiter instance
	instance := limiter.New(store, rate)

	// Create the middleware
	return mgin.NewMiddleware(instance, mgin.WithLimitReachedHandler(func(c *gin.Context) {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": "Too many requests. Please try again later.",
		})
		c.Abort()
	}))
}

// GlobalRateLimiter sets a more generous limit for general API usage.
func GlobalRateLimiter() gin.HandlerFunc {
	rate := limiter.Rate{
		Period: 1 * time.Second,
		Limit:  10, // 10 requests per second
	}
	store := memory.NewStore()
	instance := limiter.New(store, rate)

	return mgin.NewMiddleware(instance, mgin.WithLimitReachedHandler(func(c *gin.Context) {
		log.Printf("Global rate limit reached for IP: %s", c.ClientIP())
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": "Global rate limit exceeded.",
		})
		c.Abort()
	}))
}
