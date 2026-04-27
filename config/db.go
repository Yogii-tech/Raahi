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

	mongoURI := os.Getenv("MONGODB_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://localhost:27017/"
		log.Println("⚠️  MONGODB_URI not set, using default: mongodb://localhost:27017/")
	}

	clientOptions := options.Client().ApplyURI(mongoURI)

	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		log.Fatal("MongoDB connection error:", err)
	}

	// Ping DB
	if err = client.Ping(ctx, nil); err != nil {
		log.Fatal("MongoDB ping failed:", err)
	}

	dbName := os.Getenv("DATABASE_NAME")
	if dbName == "" {
		dbName = "Raahi"
		log.Println("⚠️  DATABASE_NAME not set, using default: Raahi")
	}

	DB = client
	Database = client.Database(dbName)
	log.Println("✅ MongoDB connected successfully to", dbName)
}
