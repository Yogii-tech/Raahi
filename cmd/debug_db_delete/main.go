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

	// Delete the specific Silchar test ride
	res, err := collection.DeleteMany(context.TODO(), bson.M{
		"pickup": bson.M{"$regex": "Silchar", "$options": "i"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Deleted %v irrelevant test rides.\n", res.DeletedCount)
}
