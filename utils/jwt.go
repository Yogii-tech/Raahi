package utils

import (
	"errors"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func getJwtSecret() []byte {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		panic("JWT_SECRET environment variable is not set")
	}
	return []byte(secret)
}

func GenerateJWT(userId primitive.ObjectID) (string, error) {
	claims := jwt.MapClaims{
		"userId": userId.Hex(),
		"exp":    time.Now().Add(7 * 24 * time.Hour).Unix(), // 7 days expiration
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(getJwtSecret())
}

func ValidateJWT(tokenString string) (primitive.ObjectID, error) {
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		return getJwtSecret(), nil
	})
	if err != nil || !token.Valid {
		return primitive.NilObjectID, errors.New("invalid token")
	}

	claims := token.Claims.(jwt.MapClaims)
	return primitive.ObjectIDFromHex(claims["userId"].(string))
}
