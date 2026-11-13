package utils

import (
	"math"
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

	// Negative Scenario 3: Token Type Confusion — Refresh token MUST be rejected by ValidateJWT
	_, _, err = ValidateJWT(refreshStr)
	if err == nil {
		t.Errorf("Expected ValidateJWT to reject a refresh token (type=refresh), got nil")
	}

	// Negative Scenario 4: Token Type Confusion — Access token MUST be rejected by ValidateRefreshToken
	_, _, err = ValidateRefreshToken(tokenStr)
	if err == nil {
		t.Errorf("Expected ValidateRefreshToken to reject an access token (type=access), got nil")
	}

	// Negative Scenario 4b: Missing or empty "type" claim MUST be rejected by both validators
	for _, typeVal := range []interface{}{nil, ""} {
		claims := jwt.MapClaims{
			"userId":  userID.Hex(),
			"version": tokenVersion,
			"exp":     time.Now().Add(1 * time.Hour).Unix(),
		}
		if typeVal != nil {
			claims["type"] = typeVal
		}
		untypedTok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		untypedStr, _ := untypedTok.SignedString(getJwtSecret())
		if _, _, err := ValidateJWT(untypedStr); err == nil {
			t.Errorf("Expected ValidateJWT to reject token with type=%v", typeVal)
		}
		if _, _, err := ValidateRefreshToken(untypedStr); err == nil {
			t.Errorf("Expected ValidateRefreshToken to reject token with type=%v", typeVal)
		}
	}

	// Negative Scenario 5: Expired Refresh Token MUST be rejected by ValidateRefreshToken
	expiredRefresh := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"userId":  userID.Hex(),
		"version": tokenVersion,
		"type":    "refresh",
		"exp":     time.Now().Add(-1 * time.Hour).Unix(),
	})
	expiredRefreshStr, _ := expiredRefresh.SignedString(getJwtSecret())
	if _, _, err := ValidateRefreshToken(expiredRefreshStr); err == nil {
		t.Errorf("Expected ValidateRefreshToken to reject expired refresh token, got nil")
	}

	// Negative Scenario 6: Malformed claims (missing userId/version or wrong types) must NOT panic in ValidateJWT
	malformedClaimsCases := []jwt.MapClaims{
		{"version": tokenVersion, "exp": time.Now().Add(1 * time.Hour).Unix()},                         // missing userId
		{"userId": 12345, "version": tokenVersion, "exp": time.Now().Add(1 * time.Hour).Unix()},        // non-string userId
		{"userId": userID.Hex(), "exp": time.Now().Add(1 * time.Hour).Unix()},                          // missing version
		{"userId": userID.Hex(), "version": "not-an-int", "exp": time.Now().Add(1 * time.Hour).Unix()}, // non-numeric version
	}
	for i, mc := range malformedClaimsCases {
		tok := jwt.NewWithClaims(jwt.SigningMethodHS256, mc)
		signed, _ := tok.SignedString(getJwtSecret())
		if _, _, err := ValidateJWT(signed); err == nil {
			t.Errorf("Case %d: expected ValidateJWT error for malformed claims, got nil", i)
		}
		if _, _, err := ValidateRefreshToken(signed); err == nil {
			t.Errorf("Case %d: expected ValidateRefreshToken error for malformed claims, got nil", i)
		}
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

func TestDecodePolyline(t *testing.T) {
	// Valid Google-encoded polyline: (38.5, -120.2), (40.7, -120.95), (43.252, -126.453)
	validEncoded := "_p~iF~ps|U_ulLnnqC_mqNvxq`@"
	coords := decodePolyline(validEncoded)
	expected := [][2]float64{
		{38.5, -120.2},
		{40.7, -120.95},
		{43.252, -126.453},
	}
	if len(coords) != len(expected) {
		t.Fatalf("decodePolyline(%q) returned %d coords, want %d", validEncoded, len(coords), len(expected))
	}
	for i := range expected {
		if math.Abs(coords[i][0]-expected[i][0]) > 1e-5 || math.Abs(coords[i][1]-expected[i][1]) > 1e-5 {
			t.Errorf("coord[%d] = %v, want %v", i, coords[i], expected[i])
		}
	}

	// Empty polyline returns empty slice without panic
	if emptyCoords := decodePolyline(""); len(emptyCoords) != 0 {
		t.Errorf("expected empty slice for empty polyline, got %v", emptyCoords)
	}

	// Truncated and malformed polylines must NOT panic
	malformedCases := []struct {
		name      string
		encoded   string
		wantCount int
	}{
		{"Truncated mid-latitude varint", "_p~i", 0},
		{"Truncated right after latitude (no longitude)", "_p~iF", 0},
		{"Truncated mid-longitude varint on 3rd point", "_p~iF~ps|U_ulLnnqC_mqNvxq", 2},
		{"All continuation bits", "~~~~~~~~", 0},
		{"Single continuation byte", "~", 0},
	}
	for _, tc := range malformedCases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("decodePolyline(%q) panicked: %v", tc.encoded, r)
				}
			}()
			got := decodePolyline(tc.encoded)
			if len(got) != tc.wantCount {
				t.Errorf("decodePolyline(%q) returned %d points, want %d", tc.encoded, len(got), tc.wantCount)
			}
		})
	}
}

func TestGetJwtSecret_ProductionGuard(t *testing.T) {
	prodEnvs := []string{"prod", "staging", "PRODUCTION", "production"}
	for _, env := range prodEnvs {
		t.Run("panics in "+env+" when JWT_SECRET empty", func(t *testing.T) {
			t.Setenv("APP_ENV", env)
			t.Setenv("JWT_SECRET", "")

			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("expected getJwtSecret() to panic when APP_ENV=%q and JWT_SECRET is empty", env)
				}
			}()
			_ = getJwtSecret()
		})
	}

	t.Run("allows dev fallback when APP_ENV=dev", func(t *testing.T) {
		t.Setenv("APP_ENV", "dev")
		t.Setenv("JWT_SECRET", "")

		got := getJwtSecret()
		if len(got) == 0 {
			t.Fatal("expected non-empty development fallback secret")
		}
	})
}

func TestCollectTokens(t *testing.T) {
	got := CollectTokens("tok-1", []string{"", "tok-1", "tok-2", "tok-3", "tok-2", ""})
	expected := []string{"tok-1", "tok-2", "tok-3"}
	if len(got) != len(expected) {
		t.Fatalf("CollectTokens len = %d (%v), want %d (%v)", len(got), got, len(expected), expected)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], expected[i])
		}
	}

	if empty := CollectTokens("", nil); len(empty) != 0 {
		t.Errorf("expected 0 tokens for empty input, got %v", empty)
	}
}

func TestAbsURL(t *testing.T) {
	t.Setenv("WEB_APP_URL", "https://admin.goraahi.in/")

	if got := absURL("/logo192.png"); got != "https://admin.goraahi.in/logo192.png" {
		t.Errorf("absURL(/logo192.png) = %q", got)
	}
	if got := absURL("/?adminView=bookings&relatedId=123"); got != "https://admin.goraahi.in/?adminView=bookings&relatedId=123" {
		t.Errorf("absURL(query) = %q", got)
	}
	if got := absURL("https://other.example.com/a"); got != "https://other.example.com/a" {
		t.Errorf("absURL(absolute) = %q", got)
	}
}

func TestCloneFCMData(t *testing.T) {
	original := map[string]string{
		"type":      "ride_created",
		"relatedId": "r1",
		"":          "drop-me",
	}
	cloned := cloneFCMData("New Ride", "Rishikesh -> Delhi", original)

	if cloned["title"] != "New Ride" || cloned["body"] != "Rishikesh -> Delhi" {
		t.Errorf("title/body fallback missing: %#v", cloned)
	}
	if _, hasEmpty := cloned[""]; hasEmpty {
		t.Errorf("empty key should be skipped")
	}
	if _, mutated := original["title"]; mutated {
		t.Errorf("caller map should not be mutated")
	}

	// Caller-supplied title/body should be preserved
	withExplicit := cloneFCMData("Ignored", "IgnoredBody", map[string]string{
		"title": "ExplicitTitle",
		"body":  "ExplicitBody",
	})
	if withExplicit["title"] != "ExplicitTitle" || withExplicit["body"] != "ExplicitBody" {
		t.Errorf("explicit title/body overwritten: %#v", withExplicit)
	}
}

func TestBuildWebpushTag(t *testing.T) {
	if got := buildWebpushTag(nil); got != "" {
		t.Errorf("nil map = %q, want empty", got)
	}
	if got := buildWebpushTag(map[string]string{"type": "ride_created", "relatedId": "r1", "bookingId": "b1"}); got != "ride_created-r1" {
		t.Errorf("relatedId precedence failed: %q", got)
	}
	if got := buildWebpushTag(map[string]string{"type": "booking_status", "bookingId": "b1", "notificationId": "n1"}); got != "booking_status-b1" {
		t.Errorf("bookingId fallback failed: %q", got)
	}
	if got := buildWebpushTag(map[string]string{"notificationId": "n1"}); got != "general-n1" {
		t.Errorf("notificationId + default type fallback failed: %q", got)
	}
}

func TestBuildFCMPlatformConfigs(t *testing.T) {
	t.Setenv("WEB_APP_URL", "https://goraahi.in")
	data := map[string]string{
		"type":      "driver_kyc_submitted",
		"relatedId": "drv-42",
		"url":       "/?adminView=driver_verification&relatedId=drv-42&notificationId=n1",
	}

	androidCfg, apnsCfg, webpushCfg := buildFCMPlatformConfigs("KYC Submitted", "Driver submitted KYC", data)

	if androidCfg == nil || androidCfg.Notification == nil || androidCfg.Notification.ChannelID != "raahi_high_importance" {
		t.Fatalf("unexpected android config: %#v", androidCfg)
	}
	if apnsCfg == nil || apnsCfg.Payload == nil || apnsCfg.Payload.Aps == nil || apnsCfg.Payload.Aps.Alert == nil {
		t.Fatalf("unexpected apns config: %#v", apnsCfg)
	}
	if apnsCfg.Payload.Aps.Alert.Title != "KYC Submitted" || apnsCfg.Payload.Aps.Alert.Body != "Driver submitted KYC" {
		t.Errorf("unexpected apns alert: %#v", apnsCfg.Payload.Aps.Alert)
	}

	if webpushCfg == nil || webpushCfg.Notification == nil || webpushCfg.FCMOptions == nil {
		t.Fatalf("unexpected webpush config: %#v", webpushCfg)
	}
	wantLink := "https://goraahi.in/?adminView=driver_verification&relatedId=drv-42&notificationId=n1"
	if webpushCfg.FCMOptions.Link != wantLink {
		t.Errorf("webpush Link = %q, want %q", webpushCfg.FCMOptions.Link, wantLink)
	}
	if webpushCfg.Notification.Icon != "https://goraahi.in/logo192.png" || webpushCfg.Notification.Badge != "https://goraahi.in/logo192.png" {
		t.Errorf("webpush icon/badge must be absolute HTTPS URLs: %#v", webpushCfg.Notification)
	}
	if !webpushCfg.Notification.RequireInteraction {
		t.Errorf("expected webpush RequireInteraction=true")
	}
	if webpushCfg.Notification.Tag != "driver_kyc_submitted-drv-42" {
		t.Errorf("webpush Tag = %q, want driver_kyc_submitted-drv-42", webpushCfg.Notification.Tag)
	}
}
