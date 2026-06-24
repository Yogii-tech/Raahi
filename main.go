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
	err := sentry.Init(sentry.ClientOptions{
		// Replace with your actual Sentry DSN
		Dsn:              "https://08643806a6b5a3818e9508d0b2849b38@o4508492061245440.ingest.us.sentry.io/4508492067799040",
		EnableTracing:    true,
		TracesSampleRate: 1.0,
		Environment:      os.Getenv("APP_ENV"),
	})
	if err != nil {
		fmt.Printf("Sentry initialization failed: %v\n", err)
	}

	config.ConnectDB()
	controllers.InitializeAuthCollection()
	controllers.InitializeRideCollection()
	controllers.InitializeUserController()
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

	r.Static("/uploads", "./uploads")
	routes.RegisterRoutes(r)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	r.Run(":" + port)
}
