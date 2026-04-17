package utils

import (
	"testing"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestJWTGenerationAndValidation(t *testing.T) {
	userId := primitive.NewObjectID()

	// Test Access Token
	accessToken, err := GenerateJWT(userId)
	if err != nil {
		t.Fatalf("Failed to generate access token: %v", err)
	}

	decodedId, err := ValidateJWT(accessToken)
	if err != nil {
		t.Fatalf("Failed to validate access token: %v", err)
	}
	if decodedId != userId {
		t.Errorf("Expected userId %v, got %v", userId, decodedId)
	}

	// Test Refresh Token
	refreshToken, err := GenerateRefreshToken(userId)
	if err != nil {
		t.Fatalf("Failed to generate refresh token: %v", err)
	}

	decodedRefreshId, err := ValidateRefreshToken(refreshToken)
	if err != nil {
		t.Fatalf("Failed to validate refresh token: %v", err)
	}
	if decodedRefreshId != userId {
		t.Errorf("Expected userId %v, got %v", userId, decodedRefreshId)
	}
}

func TestInvalidTokens(t *testing.T) {
	userId := primitive.NewObjectID()
	accessToken, _ := GenerateJWT(userId)
	refreshToken, _ := GenerateRefreshToken(userId)

	// Validate Access Token using Refresh Token (Should Fail)
	_, err := ValidateJWT(refreshToken)
	if err == nil {
		t.Error("Expected error when validating refresh token as access token")
	}

	// Validate Refresh Token using Access Token (Should Fail)
	_, err = ValidateRefreshToken(accessToken)
	if err == nil {
		t.Error("Expected error when validating access token as refresh token")
	}

	// Completely invalid token string
	_, err = ValidateJWT("this.is.notatoken")
	if err == nil {
		t.Error("Expected error for complete garbage token")
	}
}
