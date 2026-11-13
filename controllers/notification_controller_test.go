package controllers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"raahi-backend/models"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestBuildAdminNotificationURL(t *testing.T) {
	if got := buildAdminNotificationURL("", "", "", ""); got != "/" {
		t.Fatalf("empty buildAdminNotificationURL() = %q, want %q", got, "/")
	}

	got := buildAdminNotificationURL(models.AdminViewDriverVerification, "65f000000000000000000123", models.NotifTypeDriverKYCSubmitted, "65f000000000000000000999")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("failed to parse url %q: %v", got, err)
	}
	if parsed.Path != "/" {
		t.Errorf("path = %q, want /", parsed.Path)
	}
	q := parsed.Query()
	if q.Get("adminView") != models.AdminViewDriverVerification {
		t.Errorf("adminView = %q, want %q", q.Get("adminView"), models.AdminViewDriverVerification)
	}
	if q.Get("relatedId") != "65f000000000000000000123" {
		t.Errorf("relatedId = %q", q.Get("relatedId"))
	}
	if q.Get("type") != models.NotifTypeDriverKYCSubmitted {
		t.Errorf("type = %q", q.Get("type"))
	}
	if q.Get("notificationId") != "65f000000000000000000999" {
		t.Errorf("notificationId = %q", q.Get("notificationId"))
	}
}

func TestAppendNotificationIDToURL(t *testing.T) {
	if got := appendNotificationIDToURL("", "abc"); got != "" {
		t.Errorf("empty URL = %q, want empty", got)
	}
	if got := appendNotificationIDToURL("/?adminView=bookings", ""); got != "/?adminView=bookings" {
		t.Errorf("empty notificationId changed URL to %q", got)
	}

	updated := appendNotificationIDToURL("/?adminView=bookings&relatedId=r1", "notif-1")
	parsed, err := url.Parse(updated)
	if err != nil {
		t.Fatalf("parse updated URL: %v", err)
	}
	q := parsed.Query()
	if q.Get("adminView") != "bookings" || q.Get("relatedId") != "r1" || q.Get("notificationId") != "notif-1" {
		t.Errorf("unexpected query after append: %v", q)
	}

	// Existing notificationId must not be overwritten
	preserved := appendNotificationIDToURL("/?adminView=bookings&notificationId=existing", "new-id")
	parsedPreserved, _ := url.Parse(preserved)
	if parsedPreserved.Query().Get("notificationId") != "existing" {
		t.Errorf("existing notificationId was overwritten: %q", preserved)
	}
}

func TestBuildFCMNotificationData(t *testing.T) {
	notifID := primitive.NewObjectID()
	meta := map[string]string{
		"url":   "/?adminView=parcels&relatedId=rel1",
		"bid":   "RH-PAR-101",
		"empty": "",
	}
	metaSnapshot := map[string]string{
		"url":   meta["url"],
		"bid":   meta["bid"],
		"empty": meta["empty"],
	}

	data := buildFCMNotificationData(
		notifID,
		"New Parcel",
		"Parcel created",
		models.NotifTypeParcelCreated,
		models.NotifCategoryParcels,
		models.AdminViewParcels,
		"rel1",
		meta,
	)

	// Verify caller metadata was not mutated
	if meta["url"] != metaSnapshot["url"] || meta["bid"] != metaSnapshot["bid"] {
		t.Fatalf("caller metadata mutated: %#v", meta)
	}

	if data["title"] != "New Parcel" || data["body"] != "Parcel created" {
		t.Errorf("unexpected title/body: %#v", data)
	}
	if data["type"] != models.NotifTypeParcelCreated || data["category"] != models.NotifCategoryParcels || data["targetView"] != models.AdminViewParcels {
		t.Errorf("missing core routing fields: %#v", data)
	}
	if data["relatedId"] != "rel1" || data["bid"] != "RH-PAR-101" {
		t.Errorf("missing entity metadata: %#v", data)
	}
	if _, exists := data["empty"]; exists {
		t.Errorf("empty metadata key should not be included")
	}
	if data["notificationId"] != notifID.Hex() {
		t.Errorf("notificationId = %q, want %q", data["notificationId"], notifID.Hex())
	}
	parsedURL, err := url.Parse(data["url"])
	if err != nil {
		t.Fatalf("parse url %q: %v", data["url"], err)
	}
	if parsedURL.Query().Get("notificationId") != notifID.Hex() {
		t.Errorf("url missing per-admin notificationId: %q", data["url"])
	}
}

func TestResolveNotificationRetention(t *testing.T) {
	tests := []struct {
		name string
		role string
		days string
		want time.Duration
	}{
		{"admin default", "admin", "", 30 * 24 * time.Hour},
		{"admin valid days 7", "admin", "7", 7 * 24 * time.Hour},
		{"admin min days 1", "admin", "1", 24 * time.Hour},
		{"admin max days 30", "admin", "30", 30 * 24 * time.Hour},
		{"admin out of bounds low", "admin", "0", 30 * 24 * time.Hour},
		{"admin out of bounds high", "admin", "31", 30 * 24 * time.Hour},
		{"admin invalid text", "admin", "abc", 30 * 24 * time.Hour},
		{"passenger ignores days param", "passenger", "30", 24 * time.Hour},
		{"driver default", "driver", "15", 24 * time.Hour},
		{"empty role default", "", "", 24 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveNotificationRetention(tc.role, tc.days); got != tc.want {
				t.Errorf("resolveNotificationRetention(%q, %q) = %v, want %v", tc.role, tc.days, got, tc.want)
			}
		})
	}
}

func TestBuildNotificationQueryFilter(t *testing.T) {
	uid := primitive.NewObjectID()
	cutoff := time.Now().Add(-24 * time.Hour)

	fAll := buildNotificationQueryFilter(uid, cutoff, false, "all", "")
	if fAll["userId"] != uid {
		t.Errorf("userId mismatch")
	}
	if _, hasRead := fAll["read"]; hasRead {
		t.Errorf("unexpected read filter when unreadOnly=false")
	}
	if _, hasCat := fAll["category"]; hasCat {
		t.Errorf("category 'all' should not add category filter")
	}
	if _, hasType := fAll["type"]; hasType {
		t.Errorf("empty type should not add type filter")
	}

	fFiltered := buildNotificationQueryFilter(uid, cutoff, true, " parcels ", " parcel_created ")
	if fFiltered["read"] != false {
		t.Errorf("expected read=false when unreadOnly=true")
	}
	if fFiltered["category"] != "parcels" {
		t.Errorf("category = %v, want parcels", fFiltered["category"])
	}
	if fFiltered["type"] != "parcel_created" {
		t.Errorf("type = %v, want parcel_created", fFiltered["type"])
	}
	if _, ok := fFiltered["createdAt"].(bson.M); !ok {
		t.Errorf("expected createdAt bson.M filter")
	}
}

func TestParseNotificationsPagination(t *testing.T) {
	limit, offset := parseNotificationsPagination("", "", "")
	if limit != defaultNotificationLimit || offset != 0 {
		t.Fatalf("defaults = (%d, %d), want (%d, 0)", limit, offset, defaultNotificationLimit)
	}

	limit, offset = parseNotificationsPagination("25", "50", "10")
	if limit != 25 || offset != 50 {
		t.Errorf("explicit limit/offset = (%d, %d), want (25, 50)", limit, offset)
	}

	limit, offset = parseNotificationsPagination("9999", "", "40")
	if limit != maxNotificationLimit || offset != 40 {
		t.Errorf("clamped limit + skip = (%d, %d), want (%d, 40)", limit, offset, maxNotificationLimit)
	}

	limit, offset = parseNotificationsPagination("-5", "999999", "")
	if limit != defaultNotificationLimit || offset != 10000 {
		t.Errorf("negative limit / clamped offset = (%d, %d), want (%d, 10000)", limit, offset, defaultNotificationLimit)
	}
}

func TestNotifyAdminsSync(t *testing.T) {
	admin1 := primitive.NewObjectID()
	admin2 := primitive.NewObjectID()
	adminExcluded := primitive.NewObjectID()
	fixedTime := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

	t.Run("fans out DB docs and deduplicates cross-admin FCM tokens with per-admin notificationId", func(t *testing.T) {
		var inserted []models.Notification
		type pushCall struct {
			tokens []string
			title  string
			body   string
			data   map[string]string
		}
		var pushes []pushCall

		deps := adminNotifyDeps{
			findAdmins: func(ctx context.Context) ([]adminRecipient, error) {
				return []adminRecipient{
					{ID: adminExcluded, Tokens: []string{"tok-excluded"}},
					{ID: admin1, Tokens: []string{"tok-shared", "tok-admin1"}},
					{ID: admin2, Tokens: []string{"tok-shared", "tok-admin2"}},
				}, nil
			},
			insertNotif: func(ctx context.Context, notif models.Notification) error {
				inserted = append(inserted, notif)
				return nil
			},
			sendPush: func(tokens []string, title, body string, data map[string]string) error {
				pushes = append(pushes, pushCall{tokens: tokens, title: title, body: body, data: data})
				return nil
			},
			now: func() time.Time { return fixedTime },
		}

		notifyAdminsSync(context.Background(), AdminNotificationInput{
			Title:          "New KYC Submitted",
			Message:        "Driver Ramesh uploaded documents",
			Type:           models.NotifTypeDriverKYCSubmitted,
			Category:       models.NotifCategoryDrivers,
			TargetView:     models.AdminViewDriverVerification,
			RelatedID:      "driver-123",
			ExcludeAdminID: adminExcluded,
			Metadata: map[string]string{
				"driverId":   "driver-123",
				"actorName":  "Ramesh",
				"actorPhone": "+919876543210",
			},
		}, deps)

		if len(inserted) != 2 {
			t.Fatalf("inserted %d docs, want 2 (excluded admin must be skipped)", len(inserted))
		}
		if inserted[0].UserID != admin1 || inserted[1].UserID != admin2 {
			t.Fatalf("unexpected recipient userIDs: %s, %s", inserted[0].UserID.Hex(), inserted[1].UserID.Hex())
		}
		if inserted[0].ID == inserted[1].ID {
			t.Fatalf("each admin must receive a distinct notification ObjectID")
		}
		for i, n := range inserted {
			if n.Category != models.NotifCategoryDrivers || n.TargetView != models.AdminViewDriverVerification {
				t.Errorf("notif[%d] category/targetView mismatch: %q / %q", i, n.Category, n.TargetView)
			}
			if n.Metadata["audience"] != "admin" {
				t.Errorf("notif[%d] missing audience=admin", i)
			}
			if !strings.Contains(n.Metadata["url"], "notificationId="+n.ID.Hex()) {
				t.Errorf("notif[%d] metadata url missing own notificationId %s: %q", i, n.ID.Hex(), n.Metadata["url"])
			}
		}

		if len(pushes) != 2 {
			t.Fatalf("push batches = %d, want 2", len(pushes))
		}
		// admin1 receives tok-shared and tok-admin1; admin2 only receives tok-admin2
		if len(pushes[0].tokens) != 2 || pushes[0].tokens[0] != "tok-shared" || pushes[0].tokens[1] != "tok-admin1" {
			t.Errorf("admin1 tokens = %v, want [tok-shared tok-admin1]", pushes[0].tokens)
		}
		if len(pushes[1].tokens) != 1 || pushes[1].tokens[0] != "tok-admin2" {
			t.Errorf("admin2 tokens = %v, want [tok-admin2] (shared token must be deduplicated)", pushes[1].tokens)
		}
		if pushes[0].data["notificationId"] != inserted[0].ID.Hex() {
			t.Errorf("push[0] notificationId = %q, want %q", pushes[0].data["notificationId"], inserted[0].ID.Hex())
		}
		if pushes[1].data["notificationId"] != inserted[1].ID.Hex() {
			t.Errorf("push[1] notificationId = %q, want %q", pushes[1].data["notificationId"], inserted[1].ID.Hex())
		}
		if !strings.Contains(pushes[1].data["url"], "notificationId="+inserted[1].ID.Hex()) {
			t.Errorf("push[1] url = %q, want own notificationId", pushes[1].data["url"])
		}
	})

	t.Run("dedup skips duplicate admin alert and insert failure skips push without blocking next admin", func(t *testing.T) {
		var inserted []models.Notification
		var pushCount int
		deps := adminNotifyDeps{
			findAdmins: func(ctx context.Context) ([]adminRecipient, error) {
				return []adminRecipient{
					{ID: admin1, Tokens: []string{"tok-1"}},
					{ID: admin2, Tokens: []string{"tok-2"}},
				}, nil
			},
			isDuplicate: func(ctx context.Context, adminID primitive.ObjectID, input AdminNotificationInput, since time.Time) (bool, error) {
				return adminID == admin1, nil
			},
			insertNotif: func(ctx context.Context, notif models.Notification) error {
				inserted = append(inserted, notif)
				return nil
			},
			sendPush: func(tokens []string, title, body string, data map[string]string) error {
				pushCount++
				return errors.New("fcm temporary failure")
			},
		}

		notifyAdminsSync(context.Background(), AdminNotificationInput{
			Title:   "Ride Created",
			Message: "Test",
			Type:    models.NotifTypeRideCreated,
		}, deps)

		if len(inserted) != 1 || inserted[0].UserID != admin2 {
			t.Fatalf("expected only admin2 notification after admin1 dedup, got %#v", inserted)
		}
		if pushCount != 1 {
			t.Fatalf("expected 1 push attempt, got %d", pushCount)
		}
	})

	t.Run("empty admins and query errors exit safely without panics", func(t *testing.T) {
		notifyAdminsSync(context.Background(), AdminNotificationInput{Title: "x"}, adminNotifyDeps{
			findAdmins: func(ctx context.Context) ([]adminRecipient, error) {
				return nil, nil
			},
			insertNotif: func(ctx context.Context, notif models.Notification) error {
				t.Fatal("should not insert when no admins")
				return nil
			},
		})
		notifyAdminsSync(context.Background(), AdminNotificationInput{Title: "x"}, adminNotifyDeps{
			findAdmins: func(ctx context.Context) ([]adminRecipient, error) {
				return nil, errors.New("db down")
			},
			insertNotif: func(ctx context.Context, notif models.Notification) error {
				t.Fatal("should not insert on query error")
				return nil
			},
		})
	})
}

func TestNotificationHandlers_UninitializedCollectionAndBadID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	origNotifCol := notificationCollection
	origUserCol := userCollection
	notificationCollection = nil
	userCollection = nil
	defer func() {
		notificationCollection = origNotifCol
		userCollection = origUserCol
	}()

	uid := primitive.NewObjectID()

	t.Run("GetMyNotifications returns 503 without DB", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/notifications/", nil)
		c.Set("userId", uid)
		c.Set("userRole", "admin")

		GetMyNotifications(c)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", w.Code)
		}
	})

	t.Run("GetUnreadNotificationCount returns 503 without DB", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/notifications/unread-count", nil)
		c.Set("userId", uid)
		c.Set("userRole", "admin")

		GetUnreadNotificationCount(c)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", w.Code)
		}
	})

	t.Run("MarkNotificationRead returns 400 on invalid hex ID", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/notifications/not-a-hex/read", nil)
		c.Params = gin.Params{{Key: "id", Value: "not-a-hex"}}
		c.Set("userId", uid)

		MarkNotificationRead(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})

	t.Run("MarkAllNotificationsRead returns 503 without DB", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/notifications/read-all", nil)
		c.Set("userId", uid)

		MarkAllNotificationsRead(c)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", w.Code)
		}
	})

	t.Run("ClearAllNotifications returns 503 without DB", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/api/notifications/clear", nil)
		c.Set("userId", uid)

		ClearAllNotifications(c)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", w.Code)
		}
	})

	t.Run("TestPushNotification returns 503 without DB", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/notifications/test-push", nil)
		c.Set("userId", uid)

		TestPushNotification(c)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", w.Code)
		}
	})
}

func TestBuildBookingLifecycleAdminNotification(t *testing.T) {
	bookingID := primitive.NewObjectID()
	rideID := primitive.NewObjectID()
	driverID := primitive.NewObjectID()
	passengerID := primitive.NewObjectID()

	ride := models.Ride{
		ID:            rideID,
		DriverID:      driverID,
		DriverName:    "Suresh Driver",
		VehicleModel:  "Bolero",
		VehicleNumber: "UK07AB1234",
		Pickup:        "Rishikesh",
		Dropoff:       "Srinagar",
	}

	t.Run("parcel confirmation separates passenger actor from driverName", func(t *testing.T) {
		parcel := models.Booking{
			ID:            bookingID,
			BookingID:     "RH-PAR-42",
			RideID:        rideID,
			PassengerID:   passengerID,
			Type:          "parcel",
			Pickup:        "Rishikesh",
			Dropoff:       "Devprayag",
			ParcelSize:    "medium",
			RecipientName: "Amit",
			ContactNumber: "+919999900000",
		}
		input, ok := buildBookingLifecycleAdminNotification(parcel, ride, string(models.BookingStatusAccepted), "", "Priya Sender", "+919888877777")
		if !ok {
			t.Fatalf("expected ok=true")
		}
		if input.Type != models.NotifTypeParcelConfirmed || input.Category != models.NotifCategoryParcels || input.TargetView != models.AdminViewParcels {
			t.Errorf("unexpected routing fields: type=%q cat=%q view=%q", input.Type, input.Category, input.TargetView)
		}
		if input.Metadata["actorName"] != "Priya Sender" || input.Metadata["passengerName"] != "Priya Sender" {
			t.Errorf("expected passenger actorName/passengerName, got %#v", input.Metadata)
		}
		if input.Metadata["actorPhone"] != "+919888877777" {
			t.Errorf("expected passenger actorPhone, got %q", input.Metadata["actorPhone"])
		}
		if input.Metadata["driverName"] != "Suresh Driver" {
			t.Errorf("expected driverName=Suresh Driver, got %q", input.Metadata["driverName"])
		}
	})

	t.Run("seat completion and missing passenger name never populate driver into actorName", func(t *testing.T) {
		seatBooking := models.Booking{
			ID:             bookingID,
			BookingID:      "RH-101",
			RideID:         rideID,
			PassengerID:    passengerID,
			Type:           "seat",
			Pickup:         "Rishikesh",
			Dropoff:        "Srinagar",
			SeatsRequested: 2,
		}
		input, ok := buildBookingLifecycleAdminNotification(seatBooking, ride, string(models.BookingStatusCompleted), "", "", "+919811122233")
		if !ok {
			t.Fatalf("expected ok=true")
		}
		if input.Type != models.NotifTypeBookingCompleted || input.Category != models.NotifCategoryBookings || input.TargetView != models.AdminViewBookings {
			t.Errorf("unexpected routing fields: type=%q cat=%q view=%q", input.Type, input.Category, input.TargetView)
		}
		if input.Metadata["actorName"] != "" {
			t.Errorf("actorName must not fall back to driver name when passenger name unavailable, got %q", input.Metadata["actorName"])
		}
		if input.Metadata["actorPhone"] != "+919811122233" || input.Metadata["driverName"] != "Suresh Driver" {
			t.Errorf("unexpected actorPhone/driverName: %#v", input.Metadata)
		}
	})
}
