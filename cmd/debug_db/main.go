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

	collection := client.Database("raahi").Collection("rides")

	cursor, err := collection.Find(context.Background(), bson.M{})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("--- DEBUG: ALL RIDES IN DATABASE ---")
	for cursor.Next(context.Background()) {
		var ride bson.M
		cursor.Decode(&ride)
		fmt.Printf("ID: %v | %v -> %v | Route: %v\n", ride["_id"], ride["pickup"], ride["dropoff"], ride["route"])
	}

	os.Exit(0)
}
