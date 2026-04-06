package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type VehicleDetails struct {
	VehicleName   string `bson:"vehicle_name"`
	VehicleType   string `bson:"vehicle_type"`
	Seats         int    `bson:"seats"`
	SeatingLayout string `bson:"seating_layout"`
	VehicleNumber string `bson:"vehicle_number"`
}

type Ride struct {
	ID            primitive.ObjectID `bson:"_id,omitempty"`
	DriverID      primitive.ObjectID `bson:"driverId"`
	Pickup        string             `bson:"pickup"`
	Dropoff       string             `bson:"dropoff"`
	Date          string             `bson:"date"`
	DepartureTime string             `bson:"departureTime"`
	VehicleModel  string             `bson:"vehicleModel"`
	VehicleNumber string             `bson:"vehicleNumber"`
	SeatsTotal    int                `bson:"seatsTotal"`
	SeatsBooked   int                `bson:"seatsBooked"`
	SeatingLayout string             `bson:"seatingLayout"`
	PricePerSeat  int                `bson:"pricePerSeat"`
	Status        string             `bson:"status"`
	CreatedAt     time.Time          `bson:"createdAt"`
}

func main() {
	client, err := mongo.Connect(context.Background(), options.Client().ApplyURI("mongodb://localhost:27017"))
	if err != nil {
		log.Fatal(err)
	}
	defer client.Disconnect(context.Background())

	db := client.Database("Raahi")
	userColl := db.Collection("users")
	rideColl := db.Collection("rides")

	// Find a driver (2222222222)
	var driver struct {
		ID      primitive.ObjectID `bson:"_id"`
		Vehicle *VehicleDetails    `bson:"vehicle"`
	}
	err = userColl.FindOne(context.Background(), bson.M{"phone_number": "2222222222"}).Decode(&driver)
	if err != nil {
		log.Fatal("Could not find driver:", err)
	}

	today := time.Now().Format("02/01/2006")

	// Create a new ride for today
	newRide := Ride{
		DriverID:      driver.ID,
		Pickup:        "Almora",
		Dropoff:       "Bageshwar",
		Date:          today,
		DepartureTime: "05:00 PM",
		VehicleModel:  driver.Vehicle.VehicleName,
		VehicleNumber: driver.Vehicle.VehicleNumber,
		SeatsTotal:    driver.Vehicle.Seats,
		SeatsBooked:   0,
		SeatingLayout: driver.Vehicle.SeatingLayout,
		PricePerSeat:  450,
		Status:        "available",
		CreatedAt:     time.Now(),
	}

	res, err := rideColl.InsertOne(context.Background(), newRide)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Created test ride for today (%s). Ride ID: %v\n", today, res.InsertedID)
}
