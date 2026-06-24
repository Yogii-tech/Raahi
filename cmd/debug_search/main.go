package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	clientOptions := options.Client().ApplyURI("mongodb://localhost:27017")
	client, err := mongo.Connect(context.Background(), clientOptions)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Disconnect(context.Background())

	collection := client.Database("Raahi").Collection("rides")

	fmt.Println("--- DEBUG: SEARCHING FOR RIDES ---")
	pickup := "Haldwani"
	dropoff := "takula"
	date := "30/05/2026"

	fmt.Printf("Criteria: pickup='%s', dropoff='%s', date='%s'\n", pickup, dropoff, date)

	cursor, err := collection.Find(context.Background(), bson.M{
		"status": "available",
		"date":   date,
	})
	if err != nil {
		log.Fatal(err)
	}

	found := false
	for cursor.Next(context.Background()) {
		found = true
		var ride bson.M
		cursor.Decode(&ride)
		fmt.Printf("MATCH FOUND: ID: %v | %v -> %v | Route: %v\n", ride["_id"], ride["pickup"], ride["dropoff"], ride["route"])
	}

	if !found {
		fmt.Println("No rides found with basic status/date filter.")
	}

	os.Exit(0)
}
