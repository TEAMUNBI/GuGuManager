package identity

import (
	"testing"
	"time"
)

func TestAttemptLimiterBlocksByAnyAccountOrSourceDimension(t *testing.T) {
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	limiter := NewAttemptLimiter(AttemptLimit{Maximum: 3, Window: time.Minute, BlockFor: 2 * time.Minute}, func() time.Time { return now })

	for range 3 {
		if allowed, _ := limiter.Allow("account:admin@gugu.local", "ip:192.0.2.10"); !allowed {
			t.Fatal("limiter blocked before the configured failure count")
		}
		limiter.RecordFailure("account:admin@gugu.local", "ip:192.0.2.10")
	}

	if allowed, retryAfter := limiter.Allow("account:another@gugu.local", "ip:192.0.2.10"); allowed || retryAfter != 2*time.Minute {
		t.Fatalf("shared source limit = %v, %s; want blocked for 2m", allowed, retryAfter)
	}
	if allowed, _ := limiter.Allow("account:admin@gugu.local", "ip:192.0.2.11"); allowed {
		t.Fatal("account limit did not apply from another source")
	}

	now = now.Add(2*time.Minute + time.Nanosecond)
	if allowed, retryAfter := limiter.Allow("account:admin@gugu.local", "ip:192.0.2.10"); !allowed || retryAfter != 0 {
		t.Fatalf("expired block = %v, %s; want allowed", allowed, retryAfter)
	}
}

func TestAttemptLimiterResetClearsOnlyTheSpecifiedDimension(t *testing.T) {
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	limiter := NewAttemptLimiter(AttemptLimit{Maximum: 2, Window: time.Minute, BlockFor: time.Minute}, func() time.Time { return now })
	for range 2 {
		limiter.RecordFailure("account:admin@gugu.local", "ip:192.0.2.10")
	}

	limiter.Reset("account:admin@gugu.local")
	if allowed, _ := limiter.Allow("account:admin@gugu.local", "ip:192.0.2.11"); !allowed {
		t.Fatal("reset account dimension remained blocked")
	}
	if allowed, _ := limiter.Allow("account:other@gugu.local", "ip:192.0.2.10"); allowed {
		t.Fatal("reset account dimension also cleared source failures")
	}
}

func TestAttemptLimiterBoundsEntriesWithoutEvictingActiveBlocks(t *testing.T) {
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	limiter := NewAttemptLimiter(AttemptLimit{Maximum: 2, Window: time.Minute, BlockFor: 2 * time.Minute, Capacity: 2}, func() time.Time { return now })

	limiter.RecordFailure("account:protected")
	limiter.RecordFailure("account:protected")
	now = now.Add(time.Second)
	limiter.RecordFailure("account:transient-1")
	now = now.Add(time.Second)
	limiter.RecordFailure("account:transient-2")

	if got := len(limiter.entries); got != 2 {
		t.Fatalf("entry count = %d, want capacity 2", got)
	}
	if allowed, _ := limiter.Allow("account:protected"); allowed {
		t.Fatal("capacity eviction removed an active block")
	}
	if _, ok := limiter.entries["account:transient-1"]; ok {
		t.Fatal("oldest non-blocked entry was not evicted")
	}
	if _, ok := limiter.entries["account:transient-2"]; !ok {
		t.Fatal("new failure was not tracked after eviction")
	}
}

func TestAttemptLimiterReservationCountsInFlightAcrossDimensions(t *testing.T) {
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	limiter := NewAttemptLimiter(AttemptLimit{Maximum: 2, Window: time.Minute, BlockFor: time.Minute, Capacity: 4}, func() time.Time { return now })

	first, allowed, retryAfter := limiter.Reserve("account:first", "ip:192.0.2.10")
	if !allowed || retryAfter != 0 || first == nil {
		t.Fatalf("first reservation = %v, %s, %v; want allowed", allowed, retryAfter, first)
	}
	second, allowed, retryAfter := limiter.Reserve("account:second", "ip:192.0.2.10")
	if !allowed || retryAfter != 0 || second == nil {
		t.Fatalf("second reservation = %v, %s, %v; want allowed", allowed, retryAfter, second)
	}
	if _, allowed, _ := limiter.Reserve("account:third", "ip:192.0.2.10"); allowed {
		t.Fatal("third attempt bypassed the source dimension's two in-flight reservations")
	}

	second.CompleteSuccess("account:second")
	third, allowed, retryAfter := limiter.Reserve("account:third", "ip:192.0.2.10")
	if !allowed || retryAfter != 0 || third == nil {
		t.Fatalf("reservation after one completion = %v, %s, %v; want allowed", allowed, retryAfter, third)
	}
	first.CompleteFailure()
	third.CompleteFailure()
	state, ok := limiter.entries["account:first"]
	if !ok || len(state.failures) != 1 {
		t.Fatalf("failure completion state = %+v, present=%v; want one account failure", state, ok)
	}
}

func TestAttemptLimiterCapacityNeverEvictsInFlightReservation(t *testing.T) {
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	limiter := NewAttemptLimiter(AttemptLimit{Maximum: 2, Window: time.Minute, BlockFor: time.Minute, Capacity: 1}, func() time.Time { return now })
	reservation, allowed, _ := limiter.Reserve("account:inflight")
	if !allowed || reservation == nil {
		t.Fatal("initial in-flight reservation was not accepted")
	}
	if _, allowed, _ := limiter.Reserve("account:other"); allowed {
		t.Fatal("capacity eviction removed an in-flight reservation")
	}
	if _, ok := limiter.entries["account:inflight"]; !ok {
		t.Fatal("in-flight reservation entry was evicted")
	}
	reservation.Cancel()
}

func TestNewAttemptLimiterRejectsUnsafeConfiguration(t *testing.T) {
	for _, config := range []AttemptLimit{
		{},
		{Maximum: 1, Window: time.Minute, BlockFor: 0},
		{Maximum: 1, Window: 0, BlockFor: time.Minute},
	} {
		if limiter := NewAttemptLimiter(config, time.Now); limiter != nil {
			t.Fatalf("NewAttemptLimiter(%+v) returned a limiter", config)
		}
	}
}
