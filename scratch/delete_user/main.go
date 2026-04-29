package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	clientOptions := options.Client().ApplyURI("mongodb://localhost:27017/")
	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		log.Fatal(err)
	}

	db := client.Database("Raahi")
	collection := db.Collection("users")

	phoneNumber := "8809228888"
	filter := bson.M{"phone_number": phoneNumber}

	result, err := collection.DeleteOne(ctx, filter)
	if err != nil {
		log.Fatal(err)
	}

	if result.DeletedCount > 0 {
		fmt.Printf("✅ User with phone %s deleted successfully.\n", phoneNumber)
	} else {
		fmt.Printf("❌ No user found with phone %s.\n", phoneNumber)
	}

	client.Disconnect(ctx)
}
