// Package sharedstate holds the small pieces of state that every API
// instance must agree on: how many login attempts a client made, and the
// stored answer of a request retried with the same Idempotency-Key.
//
// Kept in each instance's memory, a limit of 10 attempts per minute becomes
// 10 x N with N API instances behind the proxy, and a retry that reaches
// another instance runs twice. Redis (already used for channel status)
// solves both; the in-memory version serves development and single-instance
// setups.
package sharedstate

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Reply is the stored answer of an idempotent request.
type Reply struct {
	Status int         `json:"status"`
	Header http.Header `json:"header"`
	Body   []byte      `json:"body"`
}

// State is the shared state of the API instances.
type State interface {
	// Allow counts one hit of key in bucket and reports whether it is still
	// within limit hits per minute.
	Allow(ctx context.Context, bucket, key string, limit int) (bool, error)
	// GetReply returns the stored answer for key, if any.
	GetReply(ctx context.Context, key string) (Reply, bool, error)
	// PutReply stores an answer for ttl.
	PutReply(ctx context.Context, key string, r Reply, ttl time.Duration) error
}

// ---- in memory ----

type limiterEntry struct {
	l     *rate.Limiter
	limit int
	seen  time.Time
}

type replyEntry struct {
	r       Reply
	expires time.Time
}

// Memory is a State private to one process.
type Memory struct {
	Now func() time.Time

	mu      sync.Mutex
	buckets map[string]*limiterEntry
	replies map[string]replyEntry
}

// NewMemory returns an in-process State.
func NewMemory() *Memory {
	return &Memory{buckets: map[string]*limiterEntry{}, replies: map[string]replyEntry{}}
}

func (m *Memory) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Allow is a token bucket: a burst of limit hits, refilled at limit per minute.
func (m *Memory) Allow(_ context.Context, bucket, key string, limit int) (bool, error) {
	if limit <= 0 {
		return true, nil
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	id := bucket + "|" + key
	e, ok := m.buckets[id]
	if !ok || e.limit != limit { // first hit, or the limit was changed in the settings
		if len(m.buckets) > 10000 { // forget idle keys
			for k, v := range m.buckets {
				if now.Sub(v.seen) > 10*time.Minute {
					delete(m.buckets, k)
				}
			}
		}
		e = &limiterEntry{l: rate.NewLimiter(rate.Limit(float64(limit)/60), limit), limit: limit}
		m.buckets[id] = e
	}
	e.seen = now
	return e.l.AllowN(now, 1), nil
}

func (m *Memory) GetReply(_ context.Context, key string) (Reply, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.replies[key]
	if !ok || m.now().After(e.expires) {
		delete(m.replies, key)
		return Reply{}, false, nil
	}
	return e.r, true, nil
}

func (m *Memory) PutReply(_ context.Context, key string, r Reply, ttl time.Duration) error {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.replies) > 10000 {
		for k, v := range m.replies {
			if now.After(v.expires) {
				delete(m.replies, k)
			}
		}
	}
	m.replies[key] = replyEntry{r: r, expires: now.Add(ttl)}
	return nil
}

// ---- resilience ----

// fallback uses primary and, when it fails (Redis down), the local state, so
// the login rate limit never disappears and requests never fail because the
// shared store is unavailable.
type fallback struct {
	primary, local State
	log            *slog.Logger

	mu        sync.Mutex
	logged    time.Time
	downUntil time.Time // circuit breaker: skip primary until then
}

// breakerOpen is how long the primary is skipped after a failure, so an
// outage costs one short timeout per period instead of one per request.
const breakerOpen = 10 * time.Second

// callTimeout bounds each call to the primary.
const callTimeout = 500 * time.Millisecond

func (f *fallback) usable() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return time.Now().After(f.downUntil)
}

// WithFallback wraps primary with an in-memory State used while primary fails.
func WithFallback(primary State, log *slog.Logger) State {
	if log == nil {
		log = slog.Default()
	}
	return &fallback{primary: primary, local: NewMemory(), log: log}
}

func (f *fallback) failed(op string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downUntil = time.Now().Add(breakerOpen)
	if time.Since(f.logged) > 30*time.Second {
		f.logged = time.Now()
		f.log.Warn("shared state unavailable; using this instance's memory", "operation", op, "error", err)
	}
}

func (f *fallback) Allow(ctx context.Context, bucket, key string, limit int) (bool, error) {
	if !f.usable() {
		return f.local.Allow(ctx, bucket, key, limit)
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	ok, err := f.primary.Allow(ctx, bucket, key, limit)
	if err != nil {
		f.failed("allow", err)
		return f.local.Allow(ctx, bucket, key, limit)
	}
	return ok, nil
}

func (f *fallback) GetReply(ctx context.Context, key string) (Reply, bool, error) {
	if !f.usable() {
		return f.local.GetReply(ctx, key)
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	r, ok, err := f.primary.GetReply(ctx, key)
	if err != nil {
		f.failed("get reply", err)
		return f.local.GetReply(ctx, key)
	}
	return r, ok, nil
}

func (f *fallback) PutReply(ctx context.Context, key string, r Reply, ttl time.Duration) error {
	if !f.usable() {
		return f.local.PutReply(ctx, key, r, ttl)
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if err := f.primary.PutReply(ctx, key, r, ttl); err != nil {
		f.failed("put reply", err)
		return f.local.PutReply(ctx, key, r, ttl)
	}
	return nil
}
