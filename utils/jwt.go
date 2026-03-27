package utils

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

var jwtSecret = []byte("raahi_secret_key")

func GenerateJWT(userId primitive.ObjectID) (string, error) {
	claims := jwt.MapClaims{
		"userId": userId.Hex(),
		"exp":    time.Now().Add(365 * 24 * time.Hour).Unix(), // 1 year for dev
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

func ValidateJWT(tokenString string) (primitive.ObjectID, error) {
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return primitive.NilObjectID, errors.New("invalid token")
	}

	claims := token.Claims.(jwt.MapClaims)
	return primitive.ObjectIDFromHex(claims["userId"].(string))
}
