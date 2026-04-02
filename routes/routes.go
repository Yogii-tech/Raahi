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
		rides.POST("/create", controllers.CreateRide)
		rides.GET("/available", controllers.GetAvailableRides)
		rides.GET("/:rideId", controllers.GetRideDetails)
		rides.POST("/:rideId/book", controllers.BookRide)
		rides.GET("/requests", controllers.GetDriverRequests)
		rides.GET("/bookings", controllers.GetPassengerBookings)
		rides.PUT("/bookings/:bookingId", controllers.UpdateBookingStatus)
		rides.POST("/recent", controllers.SaveRecentRide)
		rides.GET("/recent", controllers.GetRecentRides)
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
