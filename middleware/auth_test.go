package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"raahi-backend/utils"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

func TestAuthMiddleware_DBErrorMappingAndRolePropagation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-middleware-secret-1234567890")

	origFetch := fetchAuthUser
	t.Cleanup(func() { fetchAuthUser = origFetch })

	userID := primitive.NewObjectID()
	token, err := utils.GenerateJWT(userID, 3)
	if err != nil {
		t.Fatalf("GenerateJWT failed: %v", err)
	}

	t.Run("returns 401 when user document is missing", func(t *testing.T) {
		fetchAuthUser = func(ctx context.Context, id primitive.ObjectID) (authUserState, error) {
			return authUserState{}, mongo.ErrNoDocuments
		}

		r := gin.New()
		r.Use(AuthMiddleware())
		r.GET("/me", func(c *gin.Context) { c.Status(http.StatusOK) })

		req := httptest.NewRequest(http.MethodGet, "/me", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("returns 503 on database timeout or unavailability", func(t *testing.T) {
		fetchAuthUser = func(ctx context.Context, id primitive.ObjectID) (authUserState, error) {
			return authUserState{}, context.DeadlineExceeded
		}

		r := gin.New()
		r.Use(AuthMiddleware())
		r.GET("/me", func(c *gin.Context) { c.Status(http.StatusOK) })

		req := httptest.NewRequest(http.MethodGet, "/me", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected status 503, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("returns 401 on token version mismatch", func(t *testing.T) {
		fetchAuthUser = func(ctx context.Context, id primitive.ObjectID) (authUserState, error) {
			return authUserState{TokenVersion: 4, Role: "passenger"}, nil
		}

		r := gin.New()
		r.Use(AuthMiddleware())
		r.GET("/me", func(c *gin.Context) { c.Status(http.StatusOK) })

		req := httptest.NewRequest(http.MethodGet, "/me", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("passes userRole to AdminOnlyMiddleware without second DB query", func(t *testing.T) {
		calls := 0
		fetchAuthUser = func(ctx context.Context, id primitive.ObjectID) (authUserState, error) {
			calls++
			return authUserState{TokenVersion: 3, Role: "admin"}, nil
		}

		r := gin.New()
		r.Use(AuthMiddleware(), AdminOnlyMiddleware())
		r.GET("/admin", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"role": c.GetString("userRole")})
		})

		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d (%s)", w.Code, w.Body.String())
		}
		if calls != 1 {
			t.Fatalf("expected exactly 1 DB fetch across Auth+Admin middlewares, got %d", calls)
		}
	})
}

func TestAdminOnlyMiddleware_FallbackErrorMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	origFetch := fetchAuthUser
	t.Cleanup(func() { fetchAuthUser = origFetch })

	userID := primitive.NewObjectID()

	t.Run("forbids non-admin role from context without DB lookup", func(t *testing.T) {
		fetchAuthUser = func(ctx context.Context, id primitive.ObjectID) (authUserState, error) {
			t.Fatal("fetchAuthUser should not be called when userRole is already on context")
			return authUserState{}, nil
		}

		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("userId", userID)
			c.Set("userRole", "driver")
			c.Next()
		}, AdminOnlyMiddleware())
		r.GET("/admin", func(c *gin.Context) { c.Status(http.StatusOK) })

		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Fatalf("expected status 403, got %d", w.Code)
		}
	})

	t.Run("maps fallback DB outage to 503", func(t *testing.T) {
		fetchAuthUser = func(ctx context.Context, id primitive.ObjectID) (authUserState, error) {
			return authUserState{}, errors.Join(context.DeadlineExceeded)
		}

		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("userId", userID)
			c.Next()
		}, AdminOnlyMiddleware())
		r.GET("/admin", func(c *gin.Context) { c.Status(http.StatusOK) })

		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected status 503, got %d", w.Code)
		}
	})

	t.Run("maps missing user on fallback to 401", func(t *testing.T) {
		fetchAuthUser = func(ctx context.Context, id primitive.ObjectID) (authUserState, error) {
			return authUserState{}, mongo.ErrNoDocuments
		}

		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("userId", userID)
			c.Next()
		}, AdminOnlyMiddleware())
		r.GET("/admin", func(c *gin.Context) { c.Status(http.StatusOK) })

		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", w.Code)
		}
	})
}
