package config

import (
	"context"
	"log"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var DB *mongo.Client
var Database *mongo.Database

func ConnectDB() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		uri = "mongodb://localhost:27017/"
	}

	clientOptions := options.Client().ApplyURI(uri)

	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		log.Fatal("MongoDB connection error:", err)
	}

	// Ping DB
	if err = client.Ping(ctx, nil); err != nil {
		log.Fatal("MongoDB ping failed:", err)
	}

	DB = client
	Database = client.Database("Raahi")
	log.Println("✅ MongoDB connected successfully")

	InitializeIndexes()
}

func InitializeIndexes() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Users: Phone number unique + Role search
	Database.Collection("users").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "phoneNumber", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{Keys: bson.D{{Key: "role", Value: 1}}},
	})

	// Rides: Searchable by route, status, driver and time
	Database.Collection("rides").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "driverId", Value: 1}}},
		{Keys: bson.D{{Key: "status", Value: 1}}},
		{Keys: bson.D{{Key: "pickup", Value: 1}, {Key: "dropoff", Value: 1}}},
		{Keys: bson.D{{Key: "createdAt", Value: -1}}},
	})

	// Bookings: Relational links and type filters
	Database.Collection("bookings").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "rideId", Value: 1}}},
		{Keys: bson.D{{Key: "passengerId", Value: 1}}},
		{Keys: bson.D{{Key: "type", Value: 1}, {Key: "status", Value: 1}}},
		{Keys: bson.D{{Key: "createdAt", Value: -1}}},
	})

	log.Println("⚡ Database indexes initialized")
}
