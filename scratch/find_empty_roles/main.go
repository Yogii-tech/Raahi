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
	
	cursor, err := collection.Find(context.Background(), bson.M{"role": ""})
	if err != nil {
		fmt.Println("Find error:", err)
		return
	}

	var users []bson.M
	if err = cursor.All(context.Background(), &users); err != nil {
		fmt.Println("Cursor error:", err)
		return
	}

	fmt.Printf("Found %d users with empty role:\n", len(users))
	for _, u := range users {
		fmt.Printf("ID: %v, Name: %v, Phone: %v\n", u["_id"], u["name"], u["phone_number"])
	}

    // Also check for missing role field
    cursor, err = collection.Find(context.Background(), bson.M{"role": bson.M{"$exists": false}})
	if err == nil {
        var missingUsers []bson.M
        cursor.All(context.Background(), &missingUsers)
        fmt.Printf("Found %d users with missing role field:\n", len(missingUsers))
        for _, u := range missingUsers {
		    fmt.Printf("ID: %v, Name: %v, Phone: %v\n", u["_id"], u["name"], u["phone_number"])
	    }
    }

    // Specifically check the ID from the logs
    idHex := "69e2761ab3cf2c6a04441de6"
    objID, _ := primitive.ObjectIDFromHex(idHex)
    var specificUser bson.M
    err = collection.FindOne(context.Background(), bson.M{"_id": objID}).Decode(&specificUser)
    if err == nil {
        fmt.Printf("Specific User Found: %+v\n", specificUser)
    } else {
        fmt.Printf("Specific User %s NOT Found in Raahi.users\n", idHex)
    }
}
