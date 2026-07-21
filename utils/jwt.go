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
		// Use a fixed dev secret — in production JWT_SECRET must be set
		secret = "raahi-dev-secret-do-not-use-in-production-32chars"
	}
	return []byte(secret)
}

func GenerateJWT(userId primitive.ObjectID, tokenVersion int) (string, error) {
	claims := jwt.MapClaims{
		"userId":  userId.Hex(),
		"version": tokenVersion,
		"exp":     time.Now().Add(24 * time.Hour).Unix(), // 24 hours expiry
		"iat":     time.Now().Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(getJwtSecret())
}

func ValidateJWT(tokenString string) (primitive.ObjectID, int, error) {
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		return getJwtSecret(), nil
	})
	if err != nil || !token.Valid {
		return primitive.NilObjectID, 0, errors.New("invalid token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return primitive.NilObjectID, 0, errors.New("invalid claims")
	}

	userId, err := primitive.ObjectIDFromHex(claims["userId"].(string))
	if err != nil {
		return primitive.NilObjectID, 0, err
	}

	version := int(claims["version"].(float64))
	return userId, version, nil
}
