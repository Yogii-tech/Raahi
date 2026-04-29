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

	collection := client.Database("raahi").Collection("users")
	
	userID, _ := primitive.ObjectIDFromHex("69e2761ab3cf2c6a04441de6")
	var user bson.M
	err = collection.FindOne(context.Background(), bson.M{"_id": userID}).Decode(&user)
	if err != nil {
		fmt.Println("FindOne error:", err)
		// Try finding by ID as string if it's not a proper ObjectID
		err = collection.FindOne(context.Background(), bson.M{"_id": "69e2761ab3cf2c6a04441de6"}).Decode(&user)
		if err != nil {
			fmt.Println("FindOne by string ID error:", err)
			return
		}
	}

	fmt.Printf("User Found: %+v\n", user)
}
