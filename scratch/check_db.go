//go:build ignore
// +build ignore

// This is a standalone utility script for debugging.
// Run with: go run scratch/check_db.go

package main

import (
	"context"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	client, err := mongo.Connect(context.TODO(), options.Client().ApplyURI("mongodb://localhost:27017"))
	if err != nil {
		fmt.Println("Connect error:", err)
		return
	}
	defer client.Disconnect(context.Background())

	collection := client.Database("Raahi").Collection("rides")
	cursor, err := collection.Find(context.Background(), bson.M{})
	if err != nil {
		fmt.Println("Find error:", err)
		return
	}

	var results []bson.M
	cursor.All(context.Background(), &results)
	fmt.Printf("Total rides in DB: %d\n", len(results))
	for i, r := range results {
		if i >= 10 { // Just print the first 10
			break
		}
		fmt.Printf("Driver: %v, Pickup: %v, Dropoff: %v, Status: %v, CreatedAt: %v\n", r["driverName"], r["pickup"], r["dropoff"], r["status"], r["createdAt"])
	}
}
