package utils

import (
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TestJWTGenerationAndValidation tests positive and negative JWT scenarios
func TestJWTGenerationAndValidation(t *testing.T) {
	os.Setenv("JWT_SECRET", "test-secret-key-1234567890123456")
	userID := primitive.NewObjectID()
	tokenVersion := 1

	// Positive Scenario: Valid Token Generation & Parsing
	tokenStr, err := GenerateJWT(userID, tokenVersion)
	if err != nil {
		t.Fatalf("Failed to generate JWT: %v", err)
	}

	parsedID, parsedVersion, err := ValidateJWT(tokenStr)
	if err != nil {
		t.Fatalf("Validation failed for valid JWT: %v", err)
	}
	if parsedID != userID {
		t.Errorf("Expected UserID %s, got %s", userID.Hex(), parsedID.Hex())
	}
	if parsedVersion != tokenVersion {
		t.Errorf("Expected TokenVersion %d, got %d", tokenVersion, parsedVersion)
	}

	// Positive Scenario: Valid Refresh Token Generation & Validation
	refreshStr, err := GenerateRefreshToken(userID, tokenVersion)
	if err != nil {
		t.Fatalf("Failed to generate Refresh Token: %v", err)
	}

	parsedRefID, parsedRefVersion, err := ValidateRefreshToken(refreshStr)
	if err != nil {
		t.Fatalf("Validation failed for valid Refresh Token: %v", err)
	}
	if parsedRefID != userID {
		t.Errorf("Refresh Token: Expected UserID %s, got %s", userID.Hex(), parsedRefID.Hex())
	}
	if parsedRefVersion != tokenVersion {
		t.Errorf("Refresh Token: Expected TokenVersion %d, got %d", tokenVersion, parsedRefVersion)
	}

	// Negative Scenario 1: Malformed JWT String
	_, _, err = ValidateJWT("invalid.jwt.token")
	if err == nil {
		t.Errorf("Expected error for malformed token, got nil")
	}

	// Negative Scenario 2: Token signed with different key
	fakeToken := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"userId":  userID.Hex(),
		"version": tokenVersion,
		"exp":     time.Now().Add(1 * time.Hour).Unix(),
	})
	fakeTokenStr, _ := fakeToken.SignedString([]byte("wrong-secret-key-1234567890123456"))

	_, _, err = ValidateJWT(fakeTokenStr)
	if err == nil {
		t.Errorf("Expected signature validation failure for fake secret key")
	}

	_, _, err = ValidateRefreshToken(fakeTokenStr)
	if err == nil {
		t.Errorf("Expected signature validation failure for fake secret key on refresh")
	}
}

// TestCalculateSegmentPrice tests proportional segment pricing
func TestCalculateSegmentPrice(t *testing.T) {
	tests := []struct {
		name         string
		totalPrice   float64
		totalDistM   float64
		pickupDistM  float64
		dropoffDistM float64
		expected     float64
	}{
		{
			name:         "Full Route (100% distance)",
			totalPrice:   500,
			totalDistM:   100000, // 100km
			pickupDistM:  0,
			dropoffDistM: 100000,
			expected:     500,
		},
		{
			name:         "Half Route (50% distance)",
			totalPrice:   500,
			totalDistM:   100000,
			pickupDistM:  0,
			dropoffDistM: 50000,
			expected:     250,
		},
		{
			name:         "Quarter Route with Rounding to 5",
			totalPrice:   350,
			totalDistM:   100000,
			pickupDistM:  25000,
			dropoffDistM: 50000,
			expected:     90, // 350 * 0.25 = 87.5 -> rounded to 90
		},
		{
			name:         "Zero Total Distance Fallback",
			totalPrice:   300,
			totalDistM:   0,
			pickupDistM:  0,
			dropoffDistM: 0,
			expected:     300,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateSegmentPrice(tt.totalPrice, tt.totalDistM, tt.pickupDistM, tt.dropoffDistM)
			if got != tt.expected {
				t.Errorf("%s: Expected price %.2f, got %.2f", tt.name, tt.expected, got)
			}
		})
	}
}

// TestFuzzyMatch tests string matching logic for route stops
func TestFuzzyMatch(t *testing.T) {
	if !FuzzyMatch("Kotdwar", "kotdwar") {
		t.Errorf("Expected case-insensitive match")
	}
	if !FuzzyMatch("Lansdowne Market", "Lansdowne") {
		t.Errorf("Expected substring match")
	}
	if FuzzyMatch("Pauri", "Kotdwar") {
		t.Errorf("Expected non-match for completely different stop names")
	}
	if FuzzyMatch("", "Kotdwar") {
		t.Errorf("Expected false when matching against empty string")
	}
}
