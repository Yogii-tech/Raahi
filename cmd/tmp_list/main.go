//go:build ignore

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		uri = "mongodb://localhost:27017/"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		log.Fatal(err)
	}

	db := client.Database("Raahi")
	coll := db.Collection("rides")

	cursor, err := coll.Find(ctx, bson.M{})
	if err != nil {
		log.Fatal(err)
	}

	for cursor.Next(ctx) {
		var r bson.M
		cursor.Decode(&r)
		fmt.Printf("ID: %v, Pick: %v, Drop: %v, Dist: %v\n", r["_id"], r["pickup"], r["dropoff"], r["totalDistanceM"])
	}
}
