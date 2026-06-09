package main

import (
	"os"
	"raahi-backend/config"
	"raahi-backend/controllers"
	"raahi-backend/routes"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func main() {
	config.ConnectDB()
	controllers.InitializeAuthCollection()
	controllers.InitializeRideCollection()
	controllers.InitializeUserController()
	r := gin.Default()

	// Add CORS middleware
	corsConfig := cors.DefaultConfig()
	corsConfig.AllowOrigins = []string{
		"https://goraahi.in",
		"https://raahi-web-v2.web.app",
		"http://localhost:5173", // Still allow local dev
		"http://localhost:3000",
	}
	corsConfig.AllowHeaders = append(corsConfig.AllowHeaders, "Authorization")
	corsConfig.AllowMethods = append(corsConfig.AllowMethods, "PUT", "DELETE", "PATCH", "OPTIONS")
	corsConfig.AllowCredentials = true
	r.Use(cors.New(corsConfig))

	routes.RegisterRoutes(r)
	
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	r.Run(":" + port)
}
