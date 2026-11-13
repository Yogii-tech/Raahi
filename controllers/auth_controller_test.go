package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"raahi-backend/models"
	"raahi-backend/utils"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

func TestPromoteUserToAdminSync(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-key-1234567890123456")

	t.Run("brand-new phone user gets role=admin, default name='Admin', fresh tokens, and admin alert", func(t *testing.T) {
		uid := primitive.NewObjectID()
		var capturedSet bson.M
		notifyCalls := 0

		deps := promoteAdminDeps{
			findUserByID: func(ctx context.Context, userID primitive.ObjectID) (models.User, error) {
				return models.User{
					ID:           uid,
					PhoneNumber:  "9876543210",
					Name:         "",
					Role:         "",
					TokenVersion: 2,
				}, nil
			},
			updateAndFetchAdmin: func(ctx context.Context, userID primitive.ObjectID, setFields bson.M) (models.User, error) {
				capturedSet = setFields
				return models.User{
					ID:           uid,
					PhoneNumber:  "9876543210",
					Name:         setFields["name"].(string),
					Role:         setFields["role"].(string),
					TokenVersion: 2,
				}, nil
			},
			notifyNewAdmin: func(promoted models.User) {
				notifyCalls++
			},
		}

		res := promoteUserToAdminSync(context.Background(), uid, "", deps)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d (%s), want 200", res.StatusCode, res.ErrorMsg)
		}
		if capturedSet["role"] != "admin" || capturedSet["name"] != "Admin" {
			t.Fatalf("unexpected $set fields: %#v", capturedSet)
		}
		if res.User.Role != "admin" || res.User.Name != "Admin" {
			t.Fatalf("unexpected returned user: %#v", res.User)
		}
		if notifyCalls != 1 {
			t.Fatalf("notifyCalls = %d, want 1", notifyCalls)
		}

		parsedID, parsedVer, err := utils.ValidateJWT(res.Token)
		if err != nil || parsedID != uid || parsedVer != 2 {
			t.Fatalf("invalid returned JWT: id=%v ver=%d err=%v", parsedID, parsedVer, err)
		}
		rID, rVer, err := utils.ValidateRefreshToken(res.RefreshToken)
		if err != nil || rID != uid || rVer != 2 {
			t.Fatalf("invalid returned refresh token: id=%v ver=%d err=%v", rID, rVer, err)
		}
	})

	t.Run("existing named user keeps existing name when promoted", func(t *testing.T) {
		uid := primitive.NewObjectID()
		var capturedSet bson.M
		notifyCalls := 0

		deps := promoteAdminDeps{
			findUserByID: func(ctx context.Context, userID primitive.ObjectID) (models.User, error) {
				return models.User{
					ID:          uid,
					PhoneNumber: "9811112222",
					Name:        "Yogesh",
					Role:        "passenger",
				}, nil
			},
			updateAndFetchAdmin: func(ctx context.Context, userID primitive.ObjectID, setFields bson.M) (models.User, error) {
				capturedSet = setFields
				return models.User{
					ID:          uid,
					PhoneNumber: "9811112222",
					Name:        "Yogesh",
					Role:        "admin",
				}, nil
			},
			notifyNewAdmin: func(promoted models.User) {
				notifyCalls++
			},
		}

		res := promoteUserToAdminSync(context.Background(), uid, "IgnoredName", deps)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", res.StatusCode)
		}
		if _, hasName := capturedSet["name"]; hasName {
			t.Fatalf("existing user name must not be overwritten: %#v", capturedSet)
		}
		if res.User.Name != "Yogesh" || res.User.Role != "admin" {
			t.Fatalf("unexpected user: %#v", res.User)
		}
		if notifyCalls != 1 {
			t.Fatalf("notifyCalls = %d, want 1", notifyCalls)
		}
	})

	t.Run("existing admin heals empty name idempotently without duplicate admin-promoted alert", func(t *testing.T) {
		uid := primitive.NewObjectID()
		var capturedSet bson.M
		notifyCalls := 0

		deps := promoteAdminDeps{
			findUserByID: func(ctx context.Context, userID primitive.ObjectID) (models.User, error) {
				return models.User{
					ID:          uid,
					PhoneNumber: "9900001111",
					Name:        "",
					Role:        "admin",
				}, nil
			},
			updateAndFetchAdmin: func(ctx context.Context, userID primitive.ObjectID, setFields bson.M) (models.User, error) {
				capturedSet = setFields
				return models.User{
					ID:          uid,
					PhoneNumber: "9900001111",
					Name:        setFields["name"].(string),
					Role:        "admin",
				}, nil
			},
			notifyNewAdmin: func(promoted models.User) {
				notifyCalls++
			},
		}

		res := promoteUserToAdminSync(context.Background(), uid, "", deps)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", res.StatusCode)
		}
		if capturedSet["name"] != "Admin" {
			t.Fatalf("expected legacy stuck admin name to be healed to Admin, got %#v", capturedSet)
		}
		if res.User.Name != "Admin" || res.User.Role != "admin" || res.Token == "" || res.RefreshToken == "" {
			t.Fatalf("expected full healed user + fresh tokens, got %#v", res)
		}
		if notifyCalls != 0 {
			t.Fatalf("notifyCalls = %d, want 0 for already-admin account", notifyCalls)
		}
	})

	t.Run("handles not found and db errors cleanly", func(t *testing.T) {
		uid := primitive.NewObjectID()
		notFound := promoteUserToAdminSync(context.Background(), uid, "", promoteAdminDeps{
			findUserByID: func(ctx context.Context, userID primitive.ObjectID) (models.User, error) {
				return models.User{}, mongo.ErrNoDocuments
			},
			updateAndFetchAdmin: func(ctx context.Context, userID primitive.ObjectID, setFields bson.M) (models.User, error) {
				return models.User{}, nil
			},
		})
		if notFound.StatusCode != http.StatusNotFound {
			t.Fatalf("notFound status = %d, want 404", notFound.StatusCode)
		}

		dbDown := promoteUserToAdminSync(context.Background(), uid, "", promoteAdminDeps{
			findUserByID: func(ctx context.Context, userID primitive.ObjectID) (models.User, error) {
				return models.User{}, errors.New("connection refused")
			},
			updateAndFetchAdmin: func(ctx context.Context, userID primitive.ObjectID, setFields bson.M) (models.User, error) {
				return models.User{}, nil
			},
		})
		if dbDown.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("dbDown status = %d, want 503", dbDown.StatusCode)
		}
	})
}

func TestPromoteAdminHandler_HTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret-key-1234567890123456")

	origDeps := defaultPromoteAdminDeps
	defer func() { defaultPromoteAdminDeps = origDeps }()

	t.Run("missing ADMIN_SECRET_KEY returns 403", func(t *testing.T) {
		t.Setenv("ADMIN_SECRET_KEY", "")
		uid := primitive.NewObjectID()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/auth/promote-admin", bytes.NewBufferString(`{"secret_key":"any"}`))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("userId", uid)

		PromoteAdmin(c)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("wrong secret key returns 401", func(t *testing.T) {
		t.Setenv("ADMIN_SECRET_KEY", "super-secret-key")
		uid := primitive.NewObjectID()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/auth/promote-admin", bytes.NewBufferString(`{"secret_key":"wrong-key"}`))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("userId", uid)

		PromoteAdmin(c)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("valid secret key returns updated admin user, name, token, and refresh_token", func(t *testing.T) {
		t.Setenv("ADMIN_SECRET_KEY", "super-secret-key")
		uid := primitive.NewObjectID()
		defaultPromoteAdminDeps = func() promoteAdminDeps {
			return promoteAdminDeps{
				findUserByID: func(ctx context.Context, userID primitive.ObjectID) (models.User, error) {
					return models.User{ID: uid, PhoneNumber: "9876543210", Name: "", Role: ""}, nil
				},
				updateAndFetchAdmin: func(ctx context.Context, userID primitive.ObjectID, setFields bson.M) (models.User, error) {
					return models.User{
						ID:          uid,
						PhoneNumber: "9876543210",
						Name:        setFields["name"].(string),
						Role:        "admin",
					}, nil
				},
			}
		}

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/auth/promote-admin", bytes.NewBufferString(`{"secret_key":"  super-secret-key  "}`))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("userId", uid)

		PromoteAdmin(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
		}

		var resp struct {
			Message      string      `json:"message"`
			Token        string      `json:"token"`
			RefreshToken string      `json:"refresh_token"`
			User         models.User `json:"user"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		if resp.Token == "" || resp.RefreshToken == "" {
			t.Fatalf("expected non-empty token & refresh_token in response: %s", w.Body.String())
		}
		if resp.User.ID != uid || resp.User.Role != "admin" || resp.User.Name != "Admin" {
			t.Fatalf("unexpected promoted user in response: %#v", resp.User)
		}
	})
}
