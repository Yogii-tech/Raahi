package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
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

	fmt.Println("--- DEBUG: RUNNING FIXED PIPELINE ---")
	pickup := "Haldwani"
	dropoff := "takula"
	date := "30/05/2026"

	matchStage := bson.M{"status": "available", "date": date}

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: matchStage}},
		{{Key: "$match", Value: bson.M{"route": bson.M{"$regex": primitive.Regex{Pattern: "(?i)" + pickup, Options: ""}}}}},
		{{Key: "$match", Value: bson.M{"route": bson.M{"$regex": primitive.Regex{Pattern: "(?i)" + dropoff, Options: ""}}}}},
		{{Key: "$addFields", Value: bson.M{
			"pickupIndex": bson.M{
				"$indexOfArray": []interface{}{
					bson.M{"$map": bson.M{
						"input": "$route",
						"as":    "r",
						"in": bson.M{"$regexMatch": bson.M{
							"input": "$$r",
							"regex": "(?i)" + pickup,
						}},
					}},
					true,
				},
			},
			"dropoffIndex": bson.M{
				"$indexOfArray": []interface{}{
					bson.M{"$map": bson.M{
						"input": "$route",
						"as":    "r",
						"in": bson.M{"$regexMatch": bson.M{
							"input": "$$r",
							"regex": "(?i)" + dropoff,
						}},
					}},
					true,
				},
			},
		}}},
		{{Key: "$match", Value: bson.M{
			"$expr": bson.M{"$and": []interface{}{
				bson.M{"$ne": []interface{}{"$pickupIndex", -1}},
				bson.M{"$ne": []interface{}{"$dropoffIndex", -1}},
				bson.M{"$lt": []interface{}{"$pickupIndex", "$dropoffIndex"}},
			}},
		}}},
	}

	cursor, err := collection.Aggregate(context.Background(), pipeline)
	if err != nil {
		log.Fatal(err)
	}

	found := false
	for cursor.Next(context.Background()) {
		found = true
		var ride bson.M
		cursor.Decode(&ride)
		fmt.Printf("PIPELINE MATCH: ID: %v | PickupIdx: %v | DropoffIdx: %v\n", ride["_id"], ride["pickupIndex"], ride["dropoffIndex"])
	}

	if !found {
		fmt.Println("PIPELINE MATCH FAILED.")
	}

	os.Exit(0)
}
