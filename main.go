package main

import (
	"fmt"
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
	// Initialize Sentry
	sentryDsn := os.Getenv("SENTRY_DSN")
	appEnv := os.Getenv("APP_ENV")
	tracesSampleRate := 1.0
	if appEnv == "production" {
		tracesSampleRate = 0.1
	}

	err := sentry.Init(sentry.ClientOptions{
		Dsn:              sentryDsn,
		EnableTracing:    true,
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

	if appEnv == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.Default()

	// Sentry middleware to capture panics and errors
	r.Use(sentrygin.New(sentrygin.Options{
		Repanic: true,
	}))

	// Add CORS middleware
	corsConfig := cors.DefaultConfig()
	corsConfig.AllowOrigins = []string{
		"http://localhost:3000",
		"http://127.0.0.1:3000",
		"http://localhost:5173",
		"http://192.168.0.107:3000",
		"https://goraahi.in",
		"https://raahi-web-v2.web.app",
	}
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
