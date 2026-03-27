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
	r := gin.Default()

	// Add CORS middleware
	r.Use(cors.Default())

	routes.RegisterRoutes(r)
	r.Run(":8081")
}
