package utils

import (
	"errors"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

var (
	jwtSecret     []byte
	jwtSecretOnce sync.Once
)

func getJWTSecret() []byte {
	jwtSecretOnce.Do(func() {
		secret := os.Getenv("JWT_SECRET")
		if secret == "" {
			secret = "raahi_default_secret_key"
		}
		jwtSecret = []byte(secret)
	})
	return jwtSecret
}

// GenerateJWT creates a short-lived access token (1 hour)
func GenerateJWT(userId primitive.ObjectID) (string, error) {
	claims := jwt.MapClaims{
		"userId": userId.Hex(),
		"exp":    time.Now().Add(1 * time.Hour).Unix(),
		"type":   "access",
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(getJWTSecret())
}

// GenerateRefreshToken creates a long-lived refresh token (30 days)
func GenerateRefreshToken(userId primitive.ObjectID) (string, error) {
	claims := jwt.MapClaims{
		"userId": userId.Hex(),
		"exp":    time.Now().Add(30 * 24 * time.Hour).Unix(),
		"type":   "refresh",
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(getJWTSecret())
}

// ValidateJWT validates an access token
func ValidateJWT(tokenString string) (primitive.ObjectID, error) {
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		return getJWTSecret(), nil
	})
	if err != nil || !token.Valid {
		return primitive.NilObjectID, errors.New("invalid token")
	}

	claims := token.Claims.(jwt.MapClaims)
	if claims["type"] != "access" {
		return primitive.NilObjectID, errors.New("invalid token type")
	}
	return primitive.ObjectIDFromHex(claims["userId"].(string))
}

// ValidateRefreshToken validates a refresh token and returns the userId
func ValidateRefreshToken(tokenString string) (primitive.ObjectID, error) {
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		return getJWTSecret(), nil
	})
	if err != nil || !token.Valid {
		return primitive.NilObjectID, errors.New("invalid refresh token")
	}

	claims := token.Claims.(jwt.MapClaims)
	if claims["type"] != "refresh" {
		return primitive.NilObjectID, errors.New("invalid token type")
	}
	return primitive.ObjectIDFromHex(claims["userId"].(string))
}

