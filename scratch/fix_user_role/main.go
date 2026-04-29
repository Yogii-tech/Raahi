package main

import (
	"context"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func main() {
	client, err := mongo.Connect(context.TODO(), options.Client().ApplyURI("mongodb://localhost:27017"))
	if err != nil {
		fmt.Println("Connect error:", err)
		return
	}
	defer client.Disconnect(context.Background())

	collection := client.Database("Raahi").Collection("users")
	
	userID, _ := primitive.ObjectIDFromHex("69e2761ab3cf2c6a04441de6")
	
	// Set role to passenger for this user
	result, err := collection.UpdateOne(
		context.Background(),
		bson.M{"_id": userID},
		bson.M{"$set": bson.M{"role": "passenger"}},
	)
	if err != nil {
		fmt.Println("Update error:", err)
		return
	}

	fmt.Printf("Updated %d user(s) to 'passenger' role.\n", result.ModifiedCount)
}
