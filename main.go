package main

import (
	"log"
	"os"
	"raahi-backend/config"
	"raahi-backend/controllers"
	"raahi-backend/routes"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func main() {
	// Load environment variables
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using defaults")
	}

	config.ConnectDB()
	controllers.InitializeAuthCollection()
	controllers.InitializeRideCollection()
	controllers.InitializeUserController()
	controllers.InitializeChatCollection() // For RaahiChat
	r := gin.Default()
	r.SetTrustedProxies(nil)

	// Add CORS middleware
	corsConfig := cors.DefaultConfig()
	// To allow cookies (Credentials), we cannot use AllowAllOrigins = true.
	// We must list origins or use AllowOriginFunc.
	corsConfig.AllowCredentials = true
	corsConfig.AllowOriginFunc = func(origin string) bool {
		// Allow our local development origins
		return true // In production, replace this with strict origin checks
	}
	corsConfig.AllowHeaders = append(corsConfig.AllowHeaders, "Authorization", "Content-Type", "Accept")
	corsConfig.AllowMethods = append(corsConfig.AllowMethods, "GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS")
	r.Use(cors.New(corsConfig))

	// Create uploads directory if it doesn't exist
	if _, err := os.Stat("uploads"); os.IsNotExist(err) {
		if err := os.MkdirAll("uploads", 0755); err != nil {
			log.Fatal("Failed to create uploads directory:", err)
		}
	}
	r.Static("/uploads", "./uploads")

	// Get port from env
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	routes.RegisterRoutes(r)
	log.Printf("🚀 Server starting on port %s", port)
	r.Run(":" + port)
}
