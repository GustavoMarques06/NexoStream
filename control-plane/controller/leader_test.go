package controller

import (
	"context"
	"testing"
	"time"

	"nexostream/control-plane/statuscache"
	"nexostream/control-plane/store/memory"
	"nexostream/internal/bus"
	"nexostream/internal/logging"
)

// TestLeaderElection: two controllers share the store; one leads, the other
// stands by and takes over when the leader stops or stops renewing.
func TestLeaderElection(t *testing.T) {
	st := memory.New()
	b := bus.NewMemory()
	defer b.Close()
	cfg := DefaultConfig()
	cfg.Interval, cfg.StartupGrace, cfg.LeaseTTL = 100*time.Millisecond, 50*time.Millisecond, 400*time.Millisecond
	a, c := cfg, cfg
	a.Holder, c.Holder = "ctrl-a", "ctrl-b"
	ca := New(a, st, statuscache.NewMemory(), b, logging.Discard())
	cb := New(c, st, statuscache.NewMemory(), b, logging.Discard())
	ctx := context.Background()

	if !ca.lead(ctx) || cb.lead(ctx) {
		t.Fatal("exactly one controller must lead")
	}
	if !ca.lead(ctx) || !ca.Leader() || cb.Leader() {
		t.Fatal("the leader keeps the lease while it renews")
	}
	// The leader stops renewing (hung or partitioned): the lease expires.
	time.Sleep(500 * time.Millisecond)
	if !cb.lead(ctx) || ca.lead(ctx) || ca.Leader() {
		t.Fatal("the standby must take an expired lease and the old leader must step down")
	}

	// A clean stop hands over at once.
	actx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = cb.Run(actx); close(done) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
	if !ca.lead(ctx) {
		t.Fatal("lease not released when the leader stopped")
	}
}
