package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type ChatMessage struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	BookingID primitive.ObjectID `bson:"bookingId" json:"bookingId"`
	SenderID  primitive.ObjectID `bson:"senderId" json:"senderId"`
	Role      string             `bson:"role" json:"role"` // "driver" or "passenger"
	Text      string             `bson:"text" json:"text"`
	ReadAt    *time.Time         `bson:"readAt,omitempty" json:"readAt,omitempty"`
	CreatedAt time.Time          `bson:"createdAt" json:"createdAt"`
}
