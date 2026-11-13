package config

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
)

func TestBuildMongoClientOptions_WTimeoutConfigured(t *testing.T) {
	opts, err := BuildMongoClientOptions("mongodb://user:pass@mongo1.internal:27017,mongo2.internal:27017/raahi?replicaSet=rs0", "production")
	if err != nil {
		t.Fatalf("BuildMongoClientOptions failed: %v", err)
	}
	if opts.WriteConcern == nil {
		t.Fatal("expected WriteConcern to be configured")
	}
	if got := opts.WriteConcern.GetWTimeout(); got != 5*time.Second {
		t.Fatalf("WriteConcern.WTimeout = %v, want 5s", got)
	}
}

func TestCollectionIndexSpecs_PartialUniqueAndBookingID(t *testing.T) {
	specs := CollectionIndexSpecs()

	var foundActiveRide, foundBookingID, foundActiveSeatBooking bool
	for _, spec := range specs {
		for _, m := range spec.Models {
			if m.Options == nil {
				continue
			}
			name := ""
			if m.Options.Name != nil {
				name = *m.Options.Name
			}
			switch {
			case spec.Collection == "rides" && name == "uniq_active_ride_per_driver":
				if m.Options.Unique == nil || !*m.Options.Unique || m.Options.PartialFilterExpression == nil {
					t.Fatalf("rides.%s must be unique with partialFilterExpression", name)
				}
				foundActiveRide = true
			case spec.Collection == "bookings" && name == "uniq_active_seat_booking_per_passenger":
				if m.Options.Unique == nil || !*m.Options.Unique || m.Options.PartialFilterExpression == nil {
					t.Fatalf("bookings.%s must be unique with partialFilterExpression", name)
				}
				foundActiveSeatBooking = true
			case spec.Collection == "bookings" && m.Options.Unique != nil && *m.Options.Unique && m.Options.Sparse != nil && *m.Options.Sparse:
				foundBookingID = true
			}
		}
	}

	if !foundActiveRide {
		t.Fatal("missing rides partial unique index uniq_active_ride_per_driver")
	}
	if !foundBookingID {
		t.Fatal("missing bookings sparse unique index on bookingId")
	}
	if !foundActiveSeatBooking {
		t.Fatal("missing bookings partial unique index uniq_active_seat_booking_per_passenger")
	}
}

func TestRunInTransactionWith_Modes(t *testing.T) {
	standaloneMode := ClassifyTopology(TopologyInfo{}, false).Mode
	replicaSetMode := ClassifyTopology(TopologyInfo{
		SetName: "rs0",
		Hosts:   []string{"m1:27017", "m2:27017", "m3:27017"},
	}, true).Mode

	t.Run("non-prod standalone executes directly without sessionRunner", func(t *testing.T) {
		calledFn := false
		runner := func(ctx context.Context, fn func(context.Context) error) error {
			t.Fatal("sessionRunner should not be invoked when topology is standalone in non-prod")
			return nil
		}
		err := runInTransactionWith(context.Background(), false, standaloneMode, true, runner, func(txCtx context.Context) error {
			calledFn = true
			return nil
		})
		if err != nil || !calledFn {
			t.Fatalf("expected direct execution success, calledFn=%v err=%v", calledFn, err)
		}
	})

	t.Run("non-prod unknown topology falls back cleanly when server rejects transactions (code 20)", func(t *testing.T) {
		fnCalls := 0
		runner := func(ctx context.Context, fn func(context.Context) error) error {
			return mongo.CommandError{
				Code:    20,
				Message: "Transaction numbers are only allowed on a replica set member or mongos",
			}
		}
		err := runInTransactionWith(context.Background(), false, "", true, runner, func(txCtx context.Context) error {
			fnCalls++
			return nil
		})
		if err != nil || fnCalls != 1 {
			t.Fatalf("expected fallback execution success, fnCalls=%d err=%v", fnCalls, err)
		}
	})

	t.Run("production enforces transaction and refuses standalone fallback", func(t *testing.T) {
		fnCalls := 0
		runner := func(ctx context.Context, fn func(context.Context) error) error {
			return mongo.CommandError{
				Code:    20,
				Message: "Transaction numbers are only allowed on a replica set member or mongos",
			}
		}
		err := runInTransactionWith(context.Background(), true, standaloneMode, true, runner, func(txCtx context.Context) error {
			fnCalls++
			return nil
		})
		if err == nil || fnCalls != 0 {
			t.Fatalf("expected error without fallback in production standalone, fnCalls=%d err=%v", fnCalls, err)
		}

		err = runInTransactionWith(context.Background(), true, replicaSetMode, true, runner, func(txCtx context.Context) error {
			fnCalls++
			return nil
		})
		if err == nil || fnCalls != 0 {
			t.Fatalf("expected error without fallback on code 20 in production, fnCalls=%d err=%v", fnCalls, err)
		}
	})

	t.Run("production fails when DB client is not initialized", func(t *testing.T) {
		err := runInTransactionWith(context.Background(), true, replicaSetMode, false, nil, func(txCtx context.Context) error {
			return nil
		})
		if err == nil {
			t.Fatal("expected error when DB client is uninitialized in production")
		}
	})

	t.Run("production replica set succeeds via sessionRunner", func(t *testing.T) {
		fnCalls := 0
		runner := func(ctx context.Context, fn func(context.Context) error) error {
			return fn(ctx)
		}
		err := runInTransactionWith(context.Background(), true, replicaSetMode, true, runner, func(txCtx context.Context) error {
			fnCalls++
			return nil
		})
		if err != nil || fnCalls != 1 {
			t.Fatalf("expected production transaction success, fnCalls=%d err=%v", fnCalls, err)
		}
	})
}

func TestDBErrorClassification(t *testing.T) {
	if !IsDBUnavailableError(context.DeadlineExceeded) {
		t.Error("expected DeadlineExceeded to be classified as DB unavailable")
	}
	if !IsDBUnavailableError(mongo.ErrClientDisconnected) {
		t.Error("expected ErrClientDisconnected to be classified as DB unavailable")
	}
	if !IsDBUnavailableError(mongo.CommandError{Labels: []string{"TransientTransactionError"}, Message: "failover"}) {
		t.Error("expected TransientTransactionError to be classified as DB unavailable")
	}
	if IsDBUnavailableError(errors.New("bson decode mismatch")) {
		t.Error("did not expect plain application error to be classified as DB unavailable")
	}

	if DBErrorHTTPStatus(context.DeadlineExceeded) != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for DeadlineExceeded, got %d", DBErrorHTTPStatus(context.DeadlineExceeded))
	}
	if DBErrorHTTPStatus(errors.New("plain error")) != http.StatusInternalServerError {
		t.Errorf("expected 500 for plain error, got %d", DBErrorHTTPStatus(errors.New("plain error")))
	}

	if !IsTransactionUnsupportedError(mongo.CommandError{Code: 20, Message: "Transaction numbers are only allowed on a replica set member or mongos"}) {
		t.Error("expected command code 20 to be recognized as transaction unsupported")
	}
}
