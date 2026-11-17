package sharedstate

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"nexostream/internal/logging"
)

func TestMemoryAllow(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	m := NewMemory()
	m.Now = func() time.Time { return now }
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if ok, _ := m.Allow(ctx, "login", "1.1.1.1", 3); !ok {
			t.Fatalf("hit %d refused inside the burst", i+1)
		}
	}
	if ok, _ := m.Allow(ctx, "login", "1.1.1.1", 3); ok {
		t.Fatal("the 4th hit must be refused")
	}
	if ok, _ := m.Allow(ctx, "login", "2.2.2.2", 3); !ok {
		t.Fatal("another key has its own budget")
	}
	if ok, _ := m.Allow(ctx, "user", "1.1.1.1", 3); !ok {
		t.Fatal("another bucket has its own budget")
	}
	now = now.Add(21 * time.Second) // 3 per minute refills one hit every 20 s
	if ok, _ := m.Allow(ctx, "login", "1.1.1.1", 3); !ok {
		t.Fatal("the bucket must refill with time")
	}
	// Changing the limit in the settings takes effect at once.
	if ok, _ := m.Allow(ctx, "login", "1.1.1.1", 50); !ok {
		t.Fatal("a raised limit must give a fresh budget")
	}
	if ok, _ := m.Allow(ctx, "login", "x", 0); !ok {
		t.Fatal("limit 0 means no limit")
	}
}

func TestMemoryReplies(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	m := NewMemory()
	m.Now = func() time.Time { return now }
	ctx := context.Background()
	if _, ok, _ := m.GetReply(ctx, "k"); ok {
		t.Fatal("unknown key")
	}
	want := Reply{Status: 201, Header: http.Header{"Location": {"/x"}}, Body: []byte(`{"id":"a"}`)}
	_ = m.PutReply(ctx, "k", want, time.Hour)
	got, ok, _ := m.GetReply(ctx, "k")
	if !ok || got.Status != 201 || string(got.Body) != `{"id":"a"}` || got.Header.Get("Location") != "/x" {
		t.Fatalf("reply %+v ok=%v", got, ok)
	}
	now = now.Add(2 * time.Hour)
	if _, ok, _ := m.GetReply(ctx, "k"); ok {
		t.Fatal("reply must expire")
	}
}

// Two API instances sharing one Redis share the limit, which is the point of
// moving it out of each process.
func TestRedisIsSharedBetweenInstances(t *testing.T) {
	mr := miniredis.RunT(t)
	a, err := NewRedis("redis://"+mr.Addr()+"/0", "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, _ := NewRedis("redis://"+mr.Addr()+"/0", "")
	defer b.Close()
	ctx := context.Background()

	// 10 attempts per minute in total, however they are spread over instances.
	allowed := 0
	for i := 0; i < 20; i++ {
		inst := a
		if i%2 == 1 {
			inst = b
		}
		if ok, err := inst.Allow(ctx, "login", "9.9.9.9", 10); err != nil {
			t.Fatal(err)
		} else if ok {
			allowed++
		}
	}
	if allowed != 10 {
		t.Fatalf("allowed %d attempts across two instances, want 10", allowed)
	}
	if ok, _ := b.Allow(ctx, "login", "8.8.8.8", 10); !ok {
		t.Fatal("other client unaffected")
	}

	// A reply stored through one instance replays through the other.
	rep := Reply{Status: 201, Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"ok":true}`)}
	if err := a.PutReply(ctx, "ana|/api/v1/channels|k1", rep, time.Hour); err != nil {
		t.Fatal(err)
	}
	got, ok, err := b.GetReply(ctx, "ana|/api/v1/channels|k1")
	if err != nil || !ok || got.Status != 201 || string(got.Body) != `{"ok":true}` || got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("replay through the other instance: %+v ok=%v err=%v", got, ok, err)
	}
	if _, ok, _ := b.GetReply(ctx, "ana|/api/v1/channels|other"); ok {
		t.Fatal("unknown key")
	}
	mr.FastForward(2 * time.Hour)
	if _, ok, _ := b.GetReply(ctx, "ana|/api/v1/channels|k1"); ok {
		t.Fatal("stored reply must expire with its ttl")
	}
}

func TestRedisWindowRollsOver(t *testing.T) {
	mr := miniredis.RunT(t)
	r, _ := NewRedis("redis://"+mr.Addr()+"/0", "")
	defer r.Close()
	now := time.Unix(60*1000, 0)
	r.now = func() time.Time { return now }
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		_, _ = r.Allow(ctx, "login", "k", 2)
	}
	if ok, _ := r.Allow(ctx, "login", "k", 2); ok {
		t.Fatal("third hit in the window must be refused")
	}
	now = now.Add(61 * time.Second)
	if ok, _ := r.Allow(ctx, "login", "k", 2); !ok {
		t.Fatal("a new minute starts a new window")
	}
}

// When Redis is down the API keeps working with its own memory.
func TestFallbackWhenRedisIsDown(t *testing.T) {
	mr := miniredis.RunT(t)
	r, _ := NewRedis("redis://"+mr.Addr()+"/0", "")
	defer r.Close()
	s := WithFallback(r, logging.Discard())
	ctx := context.Background()
	if ok, err := s.Allow(ctx, "login", "k", 2); err != nil || !ok {
		t.Fatalf("healthy: %v %v", ok, err)
	}
	mr.Close() // Redis goes away
	allowed := 0
	for i := 0; i < 5; i++ {
		ok, err := s.Allow(ctx, "login", "k2", 2)
		if err != nil {
			t.Fatalf("the fallback must hide the outage: %v", err)
		}
		if ok {
			allowed++
		}
	}
	if allowed != 2 {
		t.Fatalf("the local limit must still hold during the outage: allowed %d", allowed)
	}
	if err := s.PutReply(ctx, "k", Reply{Status: 200}, time.Minute); err != nil {
		t.Fatalf("put reply during the outage: %v", err)
	}
	if _, ok, err := s.GetReply(ctx, "k"); err != nil || !ok {
		t.Fatalf("get reply during the outage: %v %v", ok, err)
	}
}
