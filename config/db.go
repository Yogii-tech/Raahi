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

	// Connection pooling: limits connections to save on free tier
	maxPool := uint64(10)
	minPool := uint64(2)
	clientOptions.SetMaxPoolSize(maxPool)
	clientOptions.SetMinPoolSize(minPool)

	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		log.Fatalf("❌ MongoDB connection error: %v", err)
	}

	// Ping DB
	if err = client.Ping(ctx, nil); err != nil {
		log.Println("⚠️ MongoDB ping warning (container will remain running):", err)
	} else {
		log.Println("✅ MongoDB connected successfully")
	}

	DB = client
	Database = client.Database("Raahi")
	if Database == nil {
		log.Fatal("❌ MongoDB database handle is nil")
	}

	if err == nil {
		InitializeIndexes()
	}
}

func ensureCollectionIndexes(ctx context.Context, collectionName string, models []mongo.IndexModel) {
	if _, err := Database.Collection(collectionName).Indexes().CreateMany(ctx, models); err != nil {
		log.Printf("⚠️ Failed to create %s indexes: %v", collectionName, err)
	}
}

func InitializeIndexes() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Users: Phone number unique, role search, and admin verification queue
	ensureCollectionIndexes(ctx, "users", []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "phone_number", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{Keys: bson.D{{Key: "role", Value: 1}}},
		{Keys: bson.D{{Key: "verification_status", Value: 1}, {Key: "submitted_at", Value: -1}}},
	})

	// Rides: Searchable by route, status/date, driver, and time
	ensureCollectionIndexes(ctx, "rides", []mongo.IndexModel{
		{Keys: bson.D{{Key: "driverId", Value: 1}}},
		{Keys: bson.D{{Key: "status", Value: 1}}},
		{Keys: bson.D{{Key: "pickup", Value: 1}, {Key: "dropoff", Value: 1}}},
		{Keys: bson.D{{Key: "createdAt", Value: -1}}},
		{Keys: bson.D{{Key: "status", Value: 1}, {Key: "date", Value: 1}}},
		{Keys: bson.D{{Key: "driverId", Value: 1}, {Key: "createdAt", Value: -1}}},
		{Keys: bson.D{{Key: "driverId", Value: 1}, {Key: "status", Value: 1}}},
	})

	// Bookings: Relational links, status filters, and passenger history
	ensureCollectionIndexes(ctx, "bookings", []mongo.IndexModel{
		{Keys: bson.D{{Key: "rideId", Value: 1}}},
		{Keys: bson.D{{Key: "passengerId", Value: 1}}},
		{Keys: bson.D{{Key: "type", Value: 1}, {Key: "status", Value: 1}}},
		{Keys: bson.D{{Key: "createdAt", Value: -1}}},
		{Keys: bson.D{{Key: "rideId", Value: 1}, {Key: "status", Value: 1}}},
		{Keys: bson.D{{Key: "passengerId", Value: 1}, {Key: "createdAt", Value: -1}}},
	})

	// Chats: Chronological lookup per booking
	ensureCollectionIndexes(ctx, "chats", []mongo.IndexModel{
		{Keys: bson.D{{Key: "bookingId", Value: 1}, {Key: "createdAt", Value: 1}}},
	})

	// Notifications: Per-user feed + 30-day TTL auto-expiration
	ensureCollectionIndexes(ctx, "notifications", []mongo.IndexModel{
		{Keys: bson.D{{Key: "userId", Value: 1}, {Key: "createdAt", Value: -1}}},
		{
			Keys:    bson.D{{Key: "createdAt", Value: 1}},
			Options: options.Index().SetExpireAfterSeconds(30 * 24 * 3600),
		},
	})

	// Reviews: One review per passenger per ride + driver review feed
	ensureCollectionIndexes(ctx, "reviews", []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "rideId", Value: 1}, {Key: "passengerId", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{Keys: bson.D{{Key: "driverId", Value: 1}, {Key: "createdAt", Value: -1}}},
	})

	// Recent routes: Per-user search history
	ensureCollectionIndexes(ctx, "recent_routes", []mongo.IndexModel{
		{Keys: bson.D{{Key: "userId", Value: 1}, {Key: "createdAt", Value: -1}}},
	})

	log.Println("⚡ Database indexes initialized")
}
