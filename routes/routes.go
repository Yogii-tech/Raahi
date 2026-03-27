package routes

import (
	"raahi-backend/controllers"
	"raahi-backend/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterRoutes(r *gin.Engine) {
	api := r.Group("/api")

	auth := api.Group("/auth")
	{
		auth.POST("/register", controllers.Register)
		auth.POST("/login", controllers.Login)
		auth.DELETE("/user/:id", controllers.DeleteUser) 
	}

	rides := api.Group("/rides")
	rides.Use(middleware.AuthMiddleware())
	{
		rides.POST("/recent", controllers.SaveRecentRide)
		rides.GET("/recent", controllers.GetRecentRides)
	}
}
