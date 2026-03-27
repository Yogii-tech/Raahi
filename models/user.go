package models

import "go.mongodb.org/mongo-driver/bson/primitive"

type Contact struct {
	Name  string `bson:"name" json:"name"`
	Phone string `bson:"phone" json:"phone"`
}

type User struct {
	ID              primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	PhoneNumber     string             `bson:"phone_number" json:"phone_number"`
	Name            string             `bson:"name" json:"name"`
	OTP             string             `bson:"otp" json:"otp"`
	TrustedContacts []Contact          `bson:"trusted_contacts" json:"trusted_contacts"`
	Role            string             `bson:"role" json:"role"`
}
