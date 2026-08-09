package identity

import (
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultAttemptLimitCapacity = 4096

type AttemptLimit struct {
	Maximum  int
	Window   time.Duration
	BlockFor time.Duration
	Capacity int
}

type attemptState struct {
	failures     []time.Time
	blockedUntil time.Time
	lastFailure  time.Time
	lastActivity time.Time
	inFlight     int
}

type completionKind uint8

const (
	completionCancel completionKind = iota
	completionFailure
	completionSuccess
)

// AttemptReservation represents one atomically admitted attempt across all
// supplied dimensions. It must be completed exactly once. A completion is
// idempotent so handlers can use a deferred failure completion and override it
// with success on the normal path.
type AttemptReservation struct {
	limiter *AttemptLimiter
	keys    []string
	once    sync.Once
}

// CompleteFailure records a failed attempt in every reserved dimension.
func (r *AttemptReservation) CompleteFailure() {
	r.complete(completionFailure, nil)
}

// CompleteSuccess completes the attempt and clears only the dimensions named
// in resetKeys. Login uses this to clear the account dimension while retaining
// source-IP failures; sensitive endpoints pass their single source key.
func (r *AttemptReservation) CompleteSuccess(resetKeys ...string) {
	r.complete(completionSuccess, normalizedLimitKeys(resetKeys))
}

// Cancel releases the in-flight reservation without recording a failure. It
// is used only by the legacy Allow compatibility helper; production request
// paths should complete a reservation with success or failure.
func (r *AttemptReservation) Cancel() {
	r.complete(completionCancel, nil)
}

func (r *AttemptReservation) complete(kind completionKind, resetKeys []string) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		if r.limiter != nil {
			r.limiter.complete(r.keys, kind, resetKeys)
		}
	})
}

// AttemptLimiter tracks independent dimensions, such as normalized account
// and source IP. A request is blocked when any supplied dimension is blocked
// or has already admitted the configured maximum number of failures and
// in-flight attempts. Reserve makes admission and in-flight accounting one
// atomic operation.
type AttemptLimiter struct {
	mu      sync.Mutex
	config  AttemptLimit
	now     func() time.Time
	entries map[string]attemptState
}

func NewAttemptLimiter(config AttemptLimit, now func() time.Time) *AttemptLimiter {
	if config.Capacity == 0 {
		config.Capacity = defaultAttemptLimitCapacity
	}
	if config.Maximum < 1 || config.Window <= 0 || config.BlockFor <= 0 || config.Capacity < 1 || now == nil {
		return nil
	}
	return &AttemptLimiter{config: config, now: now, entries: map[string]attemptState{}}
}

// Reserve atomically admits one attempt across all dimensions. The returned
// reservation must be completed exactly once. In-flight reservations count
// toward Maximum and cannot be evicted to satisfy Capacity.
func (l *AttemptLimiter) Reserve(keys ...string) (*AttemptReservation, bool, time.Duration) {
	reservation := &AttemptReservation{limiter: l, keys: normalizedLimitKeys(keys)}
	if l == nil || len(reservation.keys) == 0 {
		return reservation, true, 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	maxRetryAfter := time.Duration(0)
	for _, key := range reservation.keys {
		state, ok := l.entries[key]
		if !ok {
			continue
		}
		state = l.prune(state, now)
		if len(state.failures) == 0 && state.blockedUntil.IsZero() && state.inFlight == 0 {
			delete(l.entries, key)
			continue
		}
		l.entries[key] = state
		if retryAfter := l.retryAfter(state, now); retryAfter > maxRetryAfter {
			maxRetryAfter = retryAfter
		}
	}
	if maxRetryAfter > 0 {
		return nil, false, maxRetryAfter
	}
	if !l.ensureCapacity(reservation.keys, now) {
		return nil, false, time.Second
	}
	for _, key := range reservation.keys {
		state := l.entries[key]
		state.inFlight++
		state.lastActivity = now
		l.entries[key] = state
	}
	return reservation, true, 0
}

// Allow is retained for callers that need a point-in-time check. New request
// paths must use Reserve so the work between admission and completion is
// counted. The compatibility helper admits and immediately cancels a
// reservation, so it never leaves an untracked in-flight attempt.
func (l *AttemptLimiter) Allow(keys ...string) (bool, time.Duration) {
	reservation, allowed, retryAfter := l.Reserve(keys...)
	if allowed {
		reservation.Cancel()
	}
	return allowed, retryAfter
}

// RecordFailure is retained for compatibility with older callers. Production
// request paths should call CompleteFailure on the reservation returned by
// Reserve.
func (l *AttemptLimiter) RecordFailure(keys ...string) {
	reservation, allowed, _ := l.Reserve(keys...)
	if allowed {
		reservation.CompleteFailure()
	}
}

func (l *AttemptLimiter) complete(keys []string, kind completionKind, resetKeys []string) {
	if l == nil {
		return
	}
	reset := make(map[string]struct{}, len(resetKeys))
	for _, key := range resetKeys {
		reset[key] = struct{}{}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, key := range normalizedLimitKeys(keys) {
		state, ok := l.entries[key]
		if !ok {
			continue
		}
		state = l.prune(state, now)
		if state.inFlight > 0 {
			state.inFlight--
		}
		switch kind {
		case completionFailure:
			if !now.Before(state.blockedUntil) {
				state.failures = append(state.failures, now)
				state.lastFailure = now
				if len(state.failures) >= l.config.Maximum {
					state.failures = nil
					state.blockedUntil = now.Add(l.config.BlockFor)
				}
			}
		case completionSuccess:
			if _, shouldReset := reset[key]; shouldReset {
				state.failures = nil
				state.blockedUntil = time.Time{}
			}
		}
		state.lastActivity = now
		l.storeOrDelete(key, state)
	}
}

func (l *AttemptLimiter) ensureCapacity(requested []string, now time.Time) bool {
	requestedSet := make(map[string]struct{}, len(requested))
	for _, key := range requested {
		requestedSet[key] = struct{}{}
	}
	for key, state := range l.entries {
		state = l.prune(state, now)
		l.storeOrDelete(key, state)
	}
	missing := 0
	for _, key := range requested {
		if _, ok := l.entries[key]; !ok {
			missing++
		}
	}
	if len(l.entries)+missing <= l.config.Capacity {
		return true
	}

	type candidate struct {
		key string
		at  time.Time
	}
	candidates := make([]candidate, 0, len(l.entries))
	for key, state := range l.entries {
		if _, requested := requestedSet[key]; requested || state.inFlight > 0 || !state.blockedUntil.IsZero() {
			continue
		}
		at := state.lastActivity
		if at.IsZero() {
			at = state.lastFailure
		}
		candidates = append(candidates, candidate{key: key, at: at})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].at.Before(candidates[j].at)
	})
	if len(candidates) < len(l.entries)+missing-l.config.Capacity {
		return false
	}
	for _, candidate := range candidates[:len(l.entries)+missing-l.config.Capacity] {
		delete(l.entries, candidate.key)
	}
	return true
}

func (l *AttemptLimiter) retryAfter(state attemptState, now time.Time) time.Duration {
	if now.Before(state.blockedUntil) {
		return state.blockedUntil.Sub(now)
	}
	if len(state.failures)+state.inFlight < l.config.Maximum {
		return 0
	}
	if state.inFlight > 0 && len(state.failures) == 0 {
		return time.Second
	}
	if len(state.failures) > 0 {
		retryAfter := state.failures[0].Add(l.config.Window).Sub(now)
		if retryAfter > 0 {
			return retryAfter
		}
	}
	return time.Second
}

func (l *AttemptLimiter) Reset(keys ...string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, key := range normalizedLimitKeys(keys) {
		state, ok := l.entries[key]
		if !ok {
			continue
		}
		state = l.prune(state, now)
		state.failures = nil
		state.blockedUntil = time.Time{}
		l.storeOrDelete(key, state)
	}
}

func (l *AttemptLimiter) prune(state attemptState, now time.Time) attemptState {
	if !state.blockedUntil.IsZero() {
		if now.Before(state.blockedUntil) {
			return state
		}
		state.blockedUntil = time.Time{}
		state.failures = nil
	}
	cutoff := now.Add(-l.config.Window)
	firstCurrent := 0
	for firstCurrent < len(state.failures) && state.failures[firstCurrent].Before(cutoff) {
		firstCurrent++
	}
	state.failures = append([]time.Time(nil), state.failures[firstCurrent:]...)
	return state
}

func (l *AttemptLimiter) storeOrDelete(key string, state attemptState) {
	if len(state.failures) == 0 && state.blockedUntil.IsZero() && state.inFlight == 0 {
		delete(l.entries, key)
		return
	}
	l.entries[key] = state
}

func normalizedLimitKeys(keys []string) []string {
	result := make([]string, 0, len(keys))
	seen := map[string]struct{}{}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result
}
