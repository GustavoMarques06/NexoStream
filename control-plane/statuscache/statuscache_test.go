package statuscache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"nexostream/internal/model"
)

func run(t *testing.T, c Cache) {
	ctx := context.Background()
	st := model.ChannelStatus{ChannelID: "canal-001", State: model.StateOnBackup, WorkerID: "media-02",
		ActiveSource: &model.SourceRef{Priority: 2, URL: "udp://239.1.2.1:1234"}}
	if err := c.Set(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.Get(ctx, "canal-001")
	if err != nil || !ok || got.State != model.StateOnBackup || got.ActiveSource.Priority != 2 {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	if _, ok, _ := c.Get(ctx, "nope"); ok {
		t.Fatal("unexpected hit")
	}
	m, err := c.GetMany(ctx, []string{"canal-001", "nope"})
	if err != nil || len(m) != 1 {
		t.Fatalf("%v %v", m, err)
	}
	_ = c.Delete(ctx, "canal-001")
	if _, ok, _ := c.Get(ctx, "canal-001"); ok {
		t.Fatal("not deleted")
	}
}

func TestMemory(t *testing.T) {
	c := NewMemory()
	run(t, c)
	now := time.Now()
	c.now = func() time.Time { return now }
	_ = c.Set(context.Background(), model.ChannelStatus{ChannelID: "x"})
	c.now = func() time.Time { return now.Add(TTL + time.Second) }
	if _, ok, _ := c.Get(context.Background(), "x"); ok {
		t.Fatal("stale status returned")
	}
}

func TestRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	c, err := NewRedis("redis://"+mr.Addr()+"/0", "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	run(t, c)
	_ = c.Set(context.Background(), model.ChannelStatus{ChannelID: "x"})
	mr.FastForward(TTL + time.Second)
	if _, ok, _ := c.Get(context.Background(), "x"); ok {
		t.Fatal("ttl not applied")
	}
}
