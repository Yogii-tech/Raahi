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
	client, err := mongo.Connect(context.Background(), options.Client().ApplyURI("mongodb://localhost:27017"))
	if err != nil {
		log.Fatal(err)
	}
	defer client.Disconnect(context.Background())

	collection := client.Database("Raahi").Collection("users")
	res, err := collection.UpdateOne(
		context.Background(),
		bson.M{"phone_number": "9045277600"},
		bson.M{"$set": bson.M{"role": "passenger"}},
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Updated %v user(s)\n", res.ModifiedCount)
}
