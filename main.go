package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"raahi-backend/config"
	"raahi-backend/controllers"
	"raahi-backend/routes"
	"raahi-backend/utils"

	"github.com/getsentry/sentry-go"
	sentrygin "github.com/getsentry/sentry-go/gin"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env file for local development (ignored in production where vars are injected by Cloud Run)
	if err := godotenv.Load(); err != nil {
		log.Println("[INFO] No .env file found — using system environment variables")
	}

	appEnv := os.Getenv("APP_ENV")
	isDev := appEnv == "development" || appEnv == ""

	// Initialize Sentry — DSN must be set as environment variable
	sentryDsn := os.Getenv("SENTRY_DSN")
	if sentryDsn == "" {
		log.Println("[WARN] SENTRY_DSN not set — error tracking disabled")
	}
	tracesSampleRate := 0.1 // 10% sampling in production
	if isDev {
		tracesSampleRate = 1.0 // 100% in dev
	}
	err := sentry.Init(sentry.ClientOptions{
		Dsn:              sentryDsn,
		EnableTracing:    sentryDsn != "",
		TracesSampleRate: tracesSampleRate,
		Environment:      appEnv,
	})
	if err != nil {
		fmt.Printf("Sentry initialization failed: %v\n", err)
	}

	config.ConnectDB()
	utils.InitFCM()
	controllers.InitializeAuthCollection()
	controllers.InitializeRideCollection()
	controllers.InitializeUserController()
	controllers.InitializeChatCollection()
	controllers.InitializeNotificationCollection()

	// Always run in release mode unless explicitly in development
	if isDev {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.Default()

	// SECURITY: Set request body size limit (1MB for JSON, uploads have their own limit)
	r.MaxMultipartMemory = 10 << 20 // 10 MB for file uploads

	// SECURITY: Global security headers middleware
	r.Use(func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-XSS-Protection", "1; mode=block")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		if !isDev {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	})

	// SECURITY: Global request body size limiter (1MB for non-upload JSON endpoints)
	r.Use(func(c *gin.Context) {
		// Skip size limit for upload endpoint (it has its own 10MB limit)
		if c.Request.URL.Path == "/api/upload" {
			c.Next()
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20) // 1MB
		c.Next()
	})

	// Sentry middleware to capture panics and errors
	r.Use(sentrygin.New(sentrygin.Options{
		Repanic: true,
	}))

	// CORS — only allow known production and local dev origins
	allowedOrigins := []string{
		"http://localhost:3000",
		"https://localhost:3000",
		"http://127.0.0.1:3000",
		"https://127.0.0.1:3000",
		"http://localhost:5173",
		"https://goraahi.in",
		"https://www.goraahi.in",
		"https://raahi-web-v2.web.app",
	}
	// Allow LAN IP only during development
	if isDev {
		if lanIP := os.Getenv("DEV_LAN_ORIGIN"); lanIP != "" {
			allowedOrigins = append(allowedOrigins, lanIP)
		}
	}
	corsConfig := cors.DefaultConfig()
	corsConfig.AllowOrigins = allowedOrigins
	corsConfig.AllowCredentials = true
	corsConfig.AllowHeaders = append(corsConfig.AllowHeaders, "Authorization", "Content-Type")
	corsConfig.AllowMethods = append(corsConfig.AllowMethods, "GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS")
	// In development the webpack-dev-server proxy forwards requests to the backend
	// without an Origin header (or with Origin: null). gin-cors rejects these as 403.
	// We allow no-origin requests locally so proxied API calls work correctly.
	// In production this flag is false so the origin whitelist is strictly enforced.
	corsConfig.AllowBrowserExtensions = isDev
	r.Use(cors.New(corsConfig))

	// SECURITY: Serve uploads with security headers and no directory listing.
	// Files are served as attachments to prevent inline script execution.
	r.GET("/uploads/:filename", func(c *gin.Context) {
		filename := c.Param("filename")
		// Prevent path traversal
		if filename == "" || filename[0] == '.' || len(filename) > 255 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid filename"})
			return
		}
		c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"")
		c.Header("X-Content-Type-Options", "nosniff")
		c.File("./uploads/" + filename)
	})

	routes.RegisterRoutes(r)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	r.Run(":" + port)
}
