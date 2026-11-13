package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

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
	if config.Database == nil {
		log.Fatal("FATAL: MongoDB database is not initialized")
	}
	utils.InitFCM()
	controllers.InitializeAuthCollection()
	controllers.InitializeRideCollection()
	controllers.InitializeUserController()
	controllers.InitializeChatCollection()
	controllers.InitializeNotificationCollection()
	controllers.InitializeReviewCollection()
	if err := controllers.InitializeStorage(); err != nil {
		if !isDev {
			log.Fatalf("FATAL: Storage initialization failed in non-development environment (APP_ENV=%q): %v", appEnv, err)
		}
		log.Printf("⚠️ Storage initialization warning (dev mode): %v", err)
	}

	// Always run in release mode unless explicitly in development
	if isDev {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.Default()

	// SECURITY: Set request body size limit (1MB for JSON, uploads have their own 5MB limit)
	r.MaxMultipartMemory = 5 << 20 // 5 MB for file uploads

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

	// SECURITY: Global request body size limiter (1MB for non-upload JSON endpoints, 5MB+64KB for uploads)
	r.Use(func(c *gin.Context) {
		if c.Request.URL.Path == "/api/upload" {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, (5<<20)+(64<<10))
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
	// In non-development environments (e.g. production Cloud Run), local ./uploads
	// disk serving is disabled because all uploads live in Cloud Storage.
	// Images are served inline so they can be rendered by browser <img> tags and
	// the React Native Web <Image> component. Non-image files (PDFs etc.) are
	// served as attachments to prevent inline script execution.
	r.GET("/uploads/:filename", func(c *gin.Context) {
		if !isDev {
			c.JSON(http.StatusNotFound, gin.H{"error": "Local disk file serving is disabled in production"})
			return
		}
		filename := c.Param("filename")
		// Prevent path traversal (including Windows backslashes)
		if filename == "" || filename[0] == '.' || len(filename) > 255 || filename != filepath.Base(filename) || strings.ContainsAny(filename, "/\\") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid filename"})
			return
		}
		ext := strings.ToLower(filepath.Ext(filename))
		imageExts := map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true}
		if imageExts[ext] {
			// Inline disposition lets the browser render the image directly
			// (required for the admin Document Preview modal).
			c.Header("Content-Disposition", "inline; filename=\""+filename+"\"")
		} else {
			// PDFs and other files: force download to prevent XSS via inline execution.
			c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"")
		}
		c.Header("X-Content-Type-Options", "nosniff")
		c.File("./uploads/" + filename)
	})

	routes.RegisterRoutes(r)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("Server listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Server listen error: %v", err)
		}
	}()

	<-sigCtx.Done()
	log.Println("Shutdown signal received — shutting down gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server forced to shutdown: %v", err)
	}

	if config.DB != nil {
		if err := config.DB.Disconnect(shutdownCtx); err != nil {
			log.Printf("MongoDB disconnect error: %v", err)
		}
	}

	if err := controllers.CloseStorage(); err != nil {
		log.Printf("GCS storage close error: %v", err)
	}

	sentry.Flush(5 * time.Second)
	log.Println("Server exited cleanly")
}
