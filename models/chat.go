package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type ChatMessage struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	BookingID primitive.ObjectID `bson:"booking_id" json:"bookingId"`
	SenderID  primitive.ObjectID `bson:"sender_id" json:"senderId"`
	Role      string             `bson:"role" json:"role"` // "driver" or "passenger"
	Text      string             `bson:"text" json:"text"`
	CreatedAt time.Time          `bson:"created_at" json:"createdAt"`
	IsRead    bool               `bson:"is_read" json:"isRead"`
}
