package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type Notification struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID    primitive.ObjectID `bson:"userId" json:"userId"`
	Title     string             `bson:"title" json:"title"`
	Message   string             `bson:"message" json:"message"`
	Type      string             `bson:"type" json:"type"` // e.g. "document_verification"
	Read      bool               `bson:"read" json:"read"`
	CreatedAt time.Time          `bson:"createdAt" json:"createdAt"`
}
