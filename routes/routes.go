package routes

import (
	"raahi-backend/controllers"
	"raahi-backend/middleware"
	"raahi-backend/models"

	"github.com/gin-gonic/gin"
)

func RegisterRoutes(r *gin.Engine) {
	api := r.Group("/api")

	auth := api.Group("/auth")
	{
		auth.POST("/otp/send", controllers.SendOTP)
		auth.POST("/otp/verify", controllers.VerifyOTP)
		auth.POST("/promote-admin", middleware.AuthMiddleware(), controllers.PromoteToAdmin)
	}

	admin := api.Group("/admin")
	admin.Use(middleware.AuthMiddleware(), middleware.RequireAdmin())
	{
		admin.GET("/stats", controllers.GetAdminStats)
		admin.GET("/bookings", controllers.GetAllAdminBookings)
		admin.GET("/drivers", controllers.GetAllDrivers)
		admin.GET("/reports/:type", controllers.DownloadReport)
	}

	rides := api.Group("/rides")
	rides.Use(middleware.AuthMiddleware())
	{
		// Driver-only actions
		rides.POST("/create", middleware.RequireRole(models.RoleDriver), controllers.CreateRide)
		rides.GET("/requests", middleware.RequireRole(models.RoleDriver), controllers.GetDriverRequests)
		rides.PUT("/bookings/:bookingId", middleware.RequireRole(models.RoleDriver), controllers.UpdateBookingStatus)
		rides.POST("/recent", middleware.RequireRole(models.RoleDriver), controllers.SaveRecentRide)
		rides.GET("/recent", middleware.RequireRole(models.RoleDriver), controllers.GetRecentRides)

		// Passenger-only actions
		rides.POST("/:rideId/book", middleware.RequireRole(models.RolePassenger), controllers.BookRide)
		rides.GET("/bookings", middleware.RequireRole(models.RolePassenger), controllers.GetPassengerBookings)

		// Publicly accessible actions (but still require Auth)
		rides.GET("/available", controllers.GetAvailableRides)
		rides.GET("/:rideId", controllers.GetRideDetails)
		rides.POST("/viewed", controllers.MarkNotificationsViewed)
	}

	user := api.Group("/user")
	user.Use(middleware.AuthMiddleware())
	{
		user.GET("/profile", controllers.GetProfile)
		user.PUT("/profile", controllers.UpdateProfile)
		user.GET("/trusted-contacts", controllers.GetTrustedContacts)
		user.PUT("/trusted-contacts", controllers.UpdateTrustedContacts)
	}

	api.POST("/upload", middleware.AuthMiddleware(), controllers.UploadFile)
}
