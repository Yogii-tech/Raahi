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

	// Delete rides from the test driver "123456" or containing test markers
	res, err := collection.DeleteMany(context.TODO(), bson.M{
		"$or": []bson.M{
			{"driverName": "123456"},
			{"driverName": "COMMUNITY DRIVER"},
			{"vehicleNumber": "UK 02A5847"},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Deleted %v test clutter rides.\n", res.DeletedCount)
}
