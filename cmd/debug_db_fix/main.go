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

	// Set all rides to available status
	res, err := collection.UpdateMany(context.TODO(), bson.M{}, bson.M{
		"$set": bson.M{"status": "available"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Fixed %v rides in the database.\n", res.ModifiedCount)
}
