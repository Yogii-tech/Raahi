package main

import (
	"context"
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

	_, err = collection.DeleteMany(context.Background(), bson.M{})
	if err != nil {
		log.Fatal(err)
	}

	log.Println("✅ Rides collection cleared for fresh testing")
	os.Exit(0)
}
