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

	db := client.Database("Raahi")
	userColl := db.Collection("users")

	cursor, err := userColl.Find(context.Background(), bson.M{"role": "driver", "vehicle": bson.M{"$ne": nil}})
	if err != nil {
		log.Fatal(err)
	}

	var users []struct {
		Phone   string `bson:"phone_number"`
		Role    string `bson:"role"`
		Vehicle *struct {
			Name string `bson:"vehicle_name"`
		} `bson:"vehicle"`
	}
	cursor.All(context.Background(), &users)

	for _, u := range users {
		fmt.Printf("Driver: %s, Vehicle: %v\n", u.Phone, u.Vehicle.Name)
	}
}
