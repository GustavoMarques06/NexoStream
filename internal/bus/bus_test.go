package bus

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		p, s string
		want bool
	}{
		{"a.b.c", "a.b.c", true},
		{"a.*.c", "a.x.c", true},
		{"a.*.c", "a.x.d", false},
		{"a.>", "a.b.c", true},
		{"a.>", "a", false},
		{"a.*", "a.b.c", false},
		{"nexostream.channel.*.event", "nexostream.channel.canal-001.event", true},
	}
	for _, c := range cases {
		if got := Match(c.p, c.s); got != c.want {
			t.Errorf("Match(%q,%q)=%v want %v", c.p, c.s, got, c.want)
		}
	}
}

// RunConformance exercises any Bus implementation.
func RunConformance(t *testing.T, b Bus) {
	t.Helper()
	ctx := context.Background()

	got := make(chan string, 4)
	sub, err := b.Subscribe("x.*.status", func(s string, d []byte) { got <- s + "=" + string(d) })
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe()
	time.Sleep(20 * time.Millisecond)
	if err := b.Publish("x.one.status", []byte("ok")); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-got:
		if v != "x.one.status=ok" {
			t.Fatalf("got %q", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no message")
	}

	h, err := b.Handle("svc.echo", func(_ string, d []byte) []byte { return append([]byte("re:"), d...) })
	if err != nil {
		t.Fatal(err)
	}
	defer h.Unsubscribe()
	time.Sleep(20 * time.Millisecond)
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	r, err := b.Request(rctx, "svc.echo", []byte("hi"))
	if err != nil || string(r) != "re:hi" {
		t.Fatalf("request: %q %v", r, err)
	}

	if _, err := b.Request(rctx, "svc.nobody", nil); err == nil {
		t.Fatal("expected error with no responders")
	}

	// Durable: publish before the consumer exists, then consume.
	if err := b.PublishDurable(ctx, "x.c1.event", "id-1", []byte("e1")); err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	var failedOnce atomic.Bool
	dsub, err := b.ConsumeDurable(ctx, "test-consumer", []string{"x.*.event"}, func(_ string, d []byte) error {
		if !failedOnce.Swap(true) {
			return context.DeadlineExceeded // first delivery fails, must be retried
		}
		n.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dsub.Unsubscribe()
	deadline := time.Now().Add(5 * time.Second)
	for n.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n.Load() != 1 {
		t.Fatalf("durable deliveries=%d want 1", n.Load())
	}
}

func TestMemoryConformance(t *testing.T) {
	b := NewMemory()
	defer b.Close()
	RunConformance(t, b)
}
