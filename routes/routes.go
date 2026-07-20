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
		auth.POST("/otp/send", middleware.OTPRateLimiter(), controllers.SendOTP)
		auth.POST("/otp/verify", controllers.VerifyOTP)
		auth.POST("/promote-admin", middleware.AuthMiddleware(), controllers.PromoteAdmin)
	}

	rides := api.Group("/rides")
	rides.Use(middleware.AuthMiddleware())
	{
		rides.POST("/create", controllers.CreateRide)
		rides.GET("/available", controllers.GetAvailableRides)
		rides.GET("/route-preview", controllers.RoutePreview)
		rides.GET("/:rideId", controllers.GetRideDetails)
		rides.POST("/:rideId/book", controllers.BookRide)
		rides.GET("/requests", controllers.GetDriverRequests)
		rides.GET("/bookings", controllers.GetPassengerBookings)
		rides.PUT("/bookings/:bookingId", controllers.UpdateBookingStatus)
		rides.POST("/recent", controllers.SaveRecentRide)
		rides.GET("/recent", controllers.GetRecentRides)
		rides.POST("/viewed", controllers.MarkNotificationsViewed)
		rides.POST("/:rideId/block-seat", controllers.ToggleBlockSeat)
	}

	user := api.Group("/user")
	user.Use(middleware.AuthMiddleware())
	{
		user.GET("/profile", controllers.GetProfile)
		user.PUT("/profile", controllers.UpdateProfile)
		user.GET("/trusted-contacts", controllers.GetTrustedContacts)
		user.PUT("/trusted-contacts", controllers.UpdateTrustedContacts)
		user.POST("/logout", controllers.Logout)
	}

	location := api.Group("/location")
	location.Use(middleware.AuthMiddleware())
	{
		location.GET("/landmarks", controllers.GetNearbyLandmarks)
		location.GET("/search", controllers.SearchLocations)
	}

	api.POST("/upload", middleware.AuthMiddleware(), controllers.UploadFile)

	admin := api.Group("/admin")
	admin.Use(middleware.AuthMiddleware(), middleware.AdminOnlyMiddleware())
	{
		admin.GET("/stats", controllers.AdminStats)
		admin.GET("/bookings", controllers.AdminBookings)
		admin.GET("/drivers", controllers.AdminDrivers)
		admin.GET("/rides", controllers.AdminRidesList)
		admin.GET("/routes", controllers.AdminRoutesAnalytics)
		admin.GET("/parcels", controllers.AdminParcels)
		admin.GET("/users", controllers.AdminUsersList)
		admin.GET("/reports/:type", controllers.AdminReports)
	}
}
