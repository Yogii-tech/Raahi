package main

import (
	"fmt"
	"log"
	"os"
	"raahi-backend/config"
	"raahi-backend/controllers"
	"raahi-backend/routes"

	"github.com/getsentry/sentry-go"
	sentrygin "github.com/getsentry/sentry-go/gin"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func main() {
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
	controllers.InitializeAuthCollection()
	controllers.InitializeRideCollection()
	controllers.InitializeUserController()
	controllers.InitializeChatCollection()

	// Always run in release mode unless explicitly in development
	if isDev {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.Default()

	// Sentry middleware to capture panics and errors
	r.Use(sentrygin.New(sentrygin.Options{
		Repanic: true,
	}))

	// CORS — only allow known production and local dev origins
	allowedOrigins := []string{
		"http://localhost:3000",
		"http://127.0.0.1:3000",
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
	r.Use(cors.New(corsConfig))

	routes.RegisterRoutes(r)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	r.Run(":" + port)
}
