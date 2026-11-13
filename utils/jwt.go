package utils

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"raahi-backend/config"
)

func getJwtSecret() []byte {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		if config.IsProductionEnv(os.Getenv("APP_ENV")) {
			// CRITICAL SECURITY FIX: Never use a fallback secret in production.
			// The application must crash if the secret is missing to prevent token forging.
			panic("CRITICAL ERROR: JWT_SECRET environment variable is not set in production!")
		}
		// Use a fixed dev secret only in development
		secret = "raahi-dev-secret-do-not-use-in-production-32chars"
	}
	return []byte(secret)
}

func GenerateJWT(userId primitive.ObjectID, tokenVersion int) (string, error) {
	claims := jwt.MapClaims{
		"userId":  userId.Hex(),
		"version": tokenVersion,
		"type":    "access",
		"exp":     time.Now().Add(24 * time.Hour).Unix(), // 24 hours expiry
		"iat":     time.Now().Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(getJwtSecret())
}

func GenerateRefreshToken(userId primitive.ObjectID, tokenVersion int) (string, error) {
	claims := jwt.MapClaims{
		"userId":  userId.Hex(),
		"version": tokenVersion,
		"type":    "refresh",
		"exp":     time.Now().Add(30 * 24 * time.Hour).Unix(), // 30 days refresh token expiry
		"iat":     time.Now().Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(getJwtSecret())
}

func ValidateJWT(tokenString string) (primitive.ObjectID, int, error) {
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		// SECURITY: Reject any algorithm that is not HMAC (HS256/HS384/HS512).
		// Prevents "none" algorithm bypass and RS256-to-HS256 key confusion attacks.
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return getJwtSecret(), nil
	})
	if err != nil || token == nil || !token.Valid {
		return primitive.NilObjectID, 0, errors.New("invalid token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return primitive.NilObjectID, 0, errors.New("invalid claims")
	}

	// SECURITY: Prevent token type confusion — require explicit "access" token type
	// so refresh tokens or untyped legacy tokens can never be used as Bearer access tokens.
	tokenType, _ := claims["type"].(string)
	if tokenType != "access" {
		return primitive.NilObjectID, 0, errors.New("invalid token type: expected access token")
	}

	userIdStr, ok := claims["userId"].(string)
	if !ok || userIdStr == "" {
		return primitive.NilObjectID, 0, errors.New("missing or invalid userId in claims")
	}

	userId, err := primitive.ObjectIDFromHex(userIdStr)
	if err != nil {
		return primitive.NilObjectID, 0, err
	}

	versionFloat, ok := claims["version"].(float64)
	if !ok {
		return primitive.NilObjectID, 0, errors.New("missing or invalid version in claims")
	}

	return userId, int(versionFloat), nil
}

// ValidateRefreshToken verifies the refresh token signature, expiry, token type, and claims.
// SECURITY: Never fall back to ValidateJWTIgnoreExpiry, which would allow expired tokens
// (or expired access tokens) to mint new sessions indefinitely.
func ValidateRefreshToken(tokenString string) (primitive.ObjectID, int, error) {
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return getJwtSecret(), nil
	})
	if err != nil || token == nil || !token.Valid {
		return primitive.NilObjectID, 0, errors.New("invalid or expired refresh token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return primitive.NilObjectID, 0, errors.New("invalid claims")
	}

	// Require explicit "refresh" token type unconditionally.
	tokenType, _ := claims["type"].(string)
	if tokenType != "refresh" {
		return primitive.NilObjectID, 0, errors.New("invalid token type: expected refresh token")
	}

	userIdStr, ok := claims["userId"].(string)
	if !ok || userIdStr == "" {
		return primitive.NilObjectID, 0, errors.New("missing or invalid userId in claims")
	}

	userId, err := primitive.ObjectIDFromHex(userIdStr)
	if err != nil {
		return primitive.NilObjectID, 0, err
	}

	versionFloat, ok := claims["version"].(float64)
	if !ok {
		return primitive.NilObjectID, 0, errors.New("missing or invalid version in claims")
	}

	return userId, int(versionFloat), nil
}

// ValidateJWTIgnoreExpiry verifies token signature and extracts claims even if the token is expired.
func ValidateJWTIgnoreExpiry(tokenString string) (primitive.ObjectID, int, error) {
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return getJwtSecret(), nil
	}, jwt.WithoutClaimsValidation())

	if err != nil || token == nil {
		return primitive.NilObjectID, 0, errors.New("invalid token format or signature")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return primitive.NilObjectID, 0, errors.New("invalid claims")
	}

	userIdStr, ok := claims["userId"].(string)
	if !ok || userIdStr == "" {
		return primitive.NilObjectID, 0, errors.New("missing userId in claims")
	}

	userId, err := primitive.ObjectIDFromHex(userIdStr)
	if err != nil {
		return primitive.NilObjectID, 0, err
	}

	versionFloat, ok := claims["version"].(float64)
	if !ok {
		return primitive.NilObjectID, 0, errors.New("missing version in claims")
	}

	return userId, int(versionFloat), nil
}

