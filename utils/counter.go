package utils

import (
	"context"
	"fmt"
	"time"

	"raahi-backend/config"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// GenerateNextBookingID atomically increments and returns the next Booking ID in format Go-0001, Go-0002...
func GenerateNextBookingID(ctx context.Context) (string, error) {
	dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	countersCol := config.Database.Collection("counters")
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)

	var result struct {
		Seq int64 `bson:"seq"`
	}

	err := countersCol.FindOneAndUpdate(
		dbCtx,
		bson.M{"_id": "booking_id"},
		bson.M{"$inc": bson.M{"seq": 1}},
		opts,
	).Decode(&result)

	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Go-%04d", result.Seq), nil
}
