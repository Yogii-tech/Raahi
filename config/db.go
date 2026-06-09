package config

import (
	"context"
	"log"
	"os"
	"time"

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
		log.Fatal("MongoDB connection error:", err)
	}

	// Ping DB
	if err = client.Ping(ctx, nil); err != nil {
		log.Fatal("MongoDB ping failed:", err)
	}

	DB = client
	Database = client.Database("Raahi")
	log.Println("✅ MongoDB connected successfully")
}
