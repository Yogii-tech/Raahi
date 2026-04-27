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

	collection := client.Database("Raahi").Collection("users")
	
	result, err := collection.UpdateOne(
		context.Background(),
		bson.M{"phone_number": "8809228888"},
		bson.M{"$set": bson.M{"otp": "121212", "name": "Driver Chaman", "role": "driver", "vehicle": bson.M{"vehicle_number": "UK07-AX-4421", "seats": 5, "seating_layout": "suv"}}},
	)
	if err != nil {
		fmt.Println("Update error:", err)
		return
	}

	fmt.Printf("Updated %d user(s) as test driver.\n", result.ModifiedCount)
}
