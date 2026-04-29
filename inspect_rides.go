//go:build ignore
// +build ignore

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	godotenv.Load()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		uri = "mongodb://localhost:27017"
	}
	client, err := mongo.Connect(context.Background(), options.Client().ApplyURI(uri))
	if err != nil {
		log.Fatal(err)
	}
	defer client.Disconnect(context.Background())

	db := client.Database("raahi")
	collection := db.Collection("rides")

	cursor, err := collection.Find(context.Background(), bson.M{"status": "available"})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Available Rides:")
	for cursor.Next(context.Background()) {
		var ride bson.M
		cursor.Decode(&ride)
		fmt.Printf("ID: %v, Pickup: %v, Dropoff: %v, Date: %v, Time: %v\n", 
			ride["_id"], ride["pickup"], ride["dropoff"], ride["date"], ride["departureTime"])
	}

	now := time.Now()
	fmt.Printf("\nCurrent Server Time: %s (Hour: %d, Min: %d)\n", now.Format("15:04:05"), now.Hour(), now.Minute())
}
