package controllers

import (
	"context"
	"encoding/csv"
	"net/http"

	"raahi-backend/models"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
)

func GetAdminStats(c *gin.Context) {
	ridesCount, _ := rideCollection.CountDocuments(context.Background(), bson.M{})
	driversCount, _ := userCollection.CountDocuments(context.Background(), bson.M{"role": "driver"})
	pendingCount, _ := bookingCollection.CountDocuments(context.Background(), bson.M{"status": "pending"})
	confirmedCount, _ := bookingCollection.CountDocuments(context.Background(), bson.M{"status": "accepted"})
	canceledCount, _ := bookingCollection.CountDocuments(context.Background(), bson.M{"status": "rejected"})

	// Since we might not have timestamps sorted neatly without options, we'll just take simple counts
	c.JSON(http.StatusOK, gin.H{
		"totalRides": ridesCount,
		"pending":    pendingCount,
		"confirmed":  confirmedCount,
		"drivers":    driversCount,
		"canceled":   canceledCount,
		"activities": []gin.H{
			{"name": "Amit Sharma", "amount": 700},
			{"name": "Priya Verma", "amount": 350},
			{"name": "Rohan Gupta", "amount": 1050},
			{"name": "Suresh Negi", "amount": 1400},
		}, // Mocked latest activity for UI demonstration as per image
	})
}

func GetAllAdminBookings(c *gin.Context) {
	// Let's get all bookings
	cursor, err := bookingCollection.Find(context.Background(), bson.M{})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch bookings"})
		return
	}
	var bookings []models.Booking
	cursor.All(context.Background(), &bookings)

	type BookingAdmin struct {
		ID            string `json:"id"`
		PassengerName string `json:"passengerName"`
		Status        string `json:"status"`
		DriverName    string `json:"driverName"`
	}

	var response []BookingAdmin
	for _, b := range bookings {
		var passenger models.User
		var ride models.Ride
		userCollection.FindOne(context.Background(), bson.M{"_id": b.PassengerID}).Decode(&passenger)
		rideCollection.FindOne(context.Background(), bson.M{"_id": b.RideID}).Decode(&ride)

		status := b.Status
		if status == "accepted" {
			status = "CONFIRMED"
		} else if status == "pending" {
			status = "PENDING"
		} else if status == "rejected" {
			status = "CANCELLED"
		} else {
			status = "CANCELLED" // Default to canceled if empty just in case
		}

		response = append(response, BookingAdmin{
			ID:            b.ID.Hex(),
			PassengerName: passenger.Name,
			Status:        status,
			DriverName:    ride.DriverName,
		})
	}

	c.JSON(http.StatusOK, response)
}

func DownloadReport(c *gin.Context) {
	reportType := c.Param("type")

	c.Writer.Header().Set("Content-Type", "text/csv")
	c.Writer.Header().Set("Content-Disposition", "attachment;filename="+reportType+".csv")

	writer := csv.NewWriter(c.Writer)
	
	if reportType == "daily_bookings" {
		writer.Write([]string{"Date", "Bookings", "Revenue"})
		writer.Write([]string{"2026-04-01", "25", "12500"})
		writer.Write([]string{"2026-04-02", "30", "15000"})
	} else if reportType == "revenue" {
		writer.Write([]string{"Month", "Revenue", "Platform Fee"})
		writer.Write([]string{"Jan", "450000", "45000"})
		writer.Write([]string{"Feb", "500000", "50000"})
	} else {
		writer.Write([]string{"Driver Name", "Payout Amount", "Status"})
		writer.Write([]string{"Vikram Negi", "5000", "Paid"})
		writer.Write([]string{"Sanjay Rawat", "4200", "Pending"})
	}

	writer.Flush()
}

func GetAllDrivers(c *gin.Context) {
	cursor, err := userCollection.Find(context.Background(), bson.M{"role": "driver"})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch drivers"})
		return
	}
	var drivers []models.User
	cursor.All(context.Background(), &drivers)

	type DriverAdmin struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Phone         string `json:"phone"`
		VehicleName   string `json:"vehicleName"`
		VehicleNumber string `json:"vehicleNumber"`
		VehicleType   string `json:"vehicleType"`
		Seats         int    `json:"seats"`
		TotalRides    int64  `json:"totalRides"`
	}

	var response []DriverAdmin
	for _, d := range drivers {
		rideCount, _ := rideCollection.CountDocuments(context.Background(), bson.M{"driverId": d.ID})
		da := DriverAdmin{
			ID:         d.ID.Hex(),
			Name:       d.Name,
			Phone:      d.PhoneNumber,
			TotalRides: rideCount,
		}
		if d.Vehicle != nil {
			da.VehicleName = d.Vehicle.VehicleName
			da.VehicleNumber = d.Vehicle.VehicleNumber
			da.VehicleType = d.Vehicle.VehicleType
			da.Seats = d.Vehicle.Seats
		}
		response = append(response, da)
	}

	if response == nil {
		response = []DriverAdmin{}
	}

	c.JSON(http.StatusOK, response)
}
