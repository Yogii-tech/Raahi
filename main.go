package main

import (
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
	corsConfig.AllowAllOrigins = true
	corsConfig.AllowHeaders = append(corsConfig.AllowHeaders, "Authorization")
	corsConfig.AllowMethods = append(corsConfig.AllowMethods, "PUT", "DELETE", "PATCH")
	r.Use(cors.New(corsConfig))

	routes.RegisterRoutes(r)
	r.Run(":8081")
}
