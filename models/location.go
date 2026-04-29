package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type LocationSuggestion struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	DisplayName string             `bson:"displayName" json:"displayName"`
	Lat         string             `bson:"lat" json:"lat"`
	Lon         string             `bson:"lon" json:"lon"`
	Type        string             `bson:"type" json:"type"`
	UseCount    int                `bson:"useCount" json:"useCount"`
	LastUsedAt  time.Time          `bson:"lastUsedAt" json:"lastUsedAt"`
}
