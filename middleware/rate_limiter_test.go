package middleware

import (
	"testing"
	"time"
)

func TestCleanupGlobalLimiterStore_RespectsConfiguredWindow(t *testing.T) {
	now := time.Now()
	longKey := "test:long-window"
	shortKey := "test:short-window"

	globalLimiterStore.mu.Lock()
	globalLimiterStore.buckets[longKey] = rateBucket{
		timestamps: []time.Time{now.Add(-20 * time.Minute)},
		window:     30 * time.Minute,
	}
	globalLimiterStore.buckets[shortKey] = rateBucket{
		timestamps: []time.Time{now.Add(-2 * time.Minute)},
		window:     1 * time.Minute,
	}
	globalLimiterStore.mu.Unlock()

	defer func() {
		globalLimiterStore.mu.Lock()
		delete(globalLimiterStore.buckets, longKey)
		delete(globalLimiterStore.buckets, shortKey)
		globalLimiterStore.mu.Unlock()
	}()

	cleanupGlobalLimiterStore(now)

	globalLimiterStore.mu.Lock()
	_, hasLong := globalLimiterStore.buckets[longKey]
	_, hasShort := globalLimiterStore.buckets[shortKey]
	globalLimiterStore.mu.Unlock()

	if !hasLong {
		t.Errorf("expected 30-minute window bucket at 20m age to be retained")
	}
	if hasShort {
		t.Errorf("expected 1-minute window bucket at 2m age to be evicted")
	}
}
