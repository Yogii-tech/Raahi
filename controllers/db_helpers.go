package controllers

import (
	"context"
	"errors"
	"log"

	"raahi-backend/config"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// txHTTPError carries an explicit HTTP status code and client-safe message out of
// a RunInTransaction callback so business/state-machine rejections trigger a clean
// transaction abort while preserving 4xx status semantics.
type txHTTPError struct {
	Status  int
	Message string
	Extra   gin.H
}

func (e *txHTTPError) Error() string {
	return e.Message
}

func newTxErr(status int, message string) error {
	return &txHTTPError{Status: status, Message: message}
}

func newTxErrWithExtra(status int, message string, extra gin.H) error {
	return &txHTTPError{Status: status, Message: message, Extra: extra}
}

func respondTxError(c *gin.Context, err error, fallbackMsg string) {
	var txErr *txHTTPError
	if errors.As(err, &txErr) {
		payload := gin.H{"error": txErr.Message}
		for k, v := range txErr.Extra {
			payload[k] = v
		}
		c.JSON(txErr.Status, payload)
		return
	}
	log.Printf("[DB/TX Error] %s: %v", fallbackMsg, err)
	c.JSON(config.DBErrorHTTPStatus(err), gin.H{"error": fallbackMsg})
}

func countCollectionDocs(ctx context.Context, collectionName string, filter bson.M) (int64, error) {
	return config.Database.Collection(collectionName).CountDocuments(ctx, filter)
}

func applyVerificationStatusCAS(filter bson.M, currentStatus string) {
	if currentStatus == "" {
		existingAnd, _ := filter["$and"].([]bson.M)
		filter["$and"] = append(existingAnd, bson.M{
			"$or": []bson.M{
				{"verification_status": ""},
				{"verification_status": bson.M{"$exists": false}},
			},
		})
		return
	}
	filter["verification_status"] = currentStatus
}

// decodeCursorAll safely closes cursor, decodes all documents into out, and checks
// cursor.Err() so mid-stream DB/network failures never return truncated results.
func decodeCursorAll[T any](ctx context.Context, cursor *mongo.Cursor, out *[]T) error {
	if cursor == nil {
		return nil
	}
	defer cursor.Close(ctx)
	if err := cursor.All(ctx, out); err != nil {
		return err
	}
	return cursor.Err()
}
