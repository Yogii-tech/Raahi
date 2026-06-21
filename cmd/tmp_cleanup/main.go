//go:build ignore

package main

import (
	"context"
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

	// Delete rides with impossible distances (> 1000km for this specific app's context)
	res, err := coll.DeleteMany(ctx, bson.M{"totalDistanceM": bson.M{"$gt": 1000000}})
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Cleaned up %d rides with impossible distances.", res.DeletedCount)
}
