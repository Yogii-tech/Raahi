package main

import (
	"context"
	"fmt"
	"log"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	clientOptions := options.Client().ApplyURI("mongodb://localhost:27017")
	client, err := mongo.Connect(context.TODO(), clientOptions)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Disconnect(context.TODO())

	collection := client.Database("Raahi").Collection("rides")
	cursor, err := collection.Find(context.TODO(), bson.M{})
	if err != nil {
		log.Fatal(err)
	}
	defer cursor.Close(context.TODO())

	fmt.Println("--- START RIDE LIST ---")
	for cursor.Next(context.TODO()) {
		var ride bson.M
		if err := cursor.Decode(&ride); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("P: %v -> D: %v (Status: %v)\n",
			ride["pickup"], ride["dropoff"], ride["status"])
	}
	fmt.Println("--- END RIDE LIST ---")
}
