package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"nexostream/control-plane/controller"
	"nexostream/control-plane/statuscache"
	"nexostream/control-plane/store"
	"nexostream/control-plane/store/memory"
	"nexostream/internal/bus"
	"nexostream/internal/logging"
	"nexostream/internal/model"
	"nexostream/internal/protocol"
	"nexostream/media-engine/pipeline/pipelinetest"
)

// TestSessionHistory: a session closed on the node reaches the controller
// over the bus and is stored in the history.
func TestSessionHistory(t *testing.T) {
	sessionLogEvery = 100 * time.Millisecond
	defer func() { sessionLogEvery = 10 * time.Second }()
	st := memory.New()
	b := bus.NewMemory()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctrl := controller.New(controller.DefaultConfig(), st, statuscache.NewMemory(), b, logging.Discard())
	cerr := make(chan error, 1)
	go func() { cerr <- ctrl.Run(ctx) }()
	var batches atomic.Int32
	sub, _ := b.Subscribe(protocol.SessionsClosed, func(string, []byte) { batches.Add(1) })
	defer func() { _ = sub.Unsubscribe() }()

	hls := t.TempDir()
	a, err := New(Config{WorkerID: "w1", HLSDir: hls, StateDir: t.TempDir(), DVRDir: t.TempDir(), FFprobePath: "-", Encoders: []string{"cpu"}},
		b, &pipelinetest.Engine{}, logging.Discard())
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = a.Run(ctx) }()
	ch := model.Channel{ID: "c1", Enabled: true, Mode: model.ModePassthrough, Revision: 1,
		Sources: []model.Source{{ID: "1", Priority: 1, URL: "udp://" + freeUDP(t)}}, Outputs: model.Outputs{HLS: true}, Failover: model.DefaultFailover()}
	if err := a.StartChannel(protocol.ChannelSpec{Channel: ch}); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(hls, "c1"), 0o755)
	_ = os.WriteFile(filepath.Join(hls, "c1", "index.m3u8"), []byte("#EXTM3U\n"), 0o644)
	srv := httptest.NewServer(a.MediaHandler())
	defer srv.Close()
	time.Sleep(100 * time.Millisecond) // the session log goroutine is running
	resp, err := http.Get(srv.URL + "/hls/c1/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	open, _ := a.sessions.List("c1")
	if len(open) != 1 || !a.sessions.Kill(open[0].ID) {
		t.Fatalf("session %+v", open)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := st.ListPlaySessions(ctx, store.SessionFilter{ChannelID: "c1"})
		if len(got) == 1 && got[0].Reason == "killed" && got[0].WorkerID == "w1" {
			return
		}
		if time.Now().After(deadline) {
			var ce error
			select {
			case ce = <-cerr:
			default:
			}
			t.Fatalf("history %+v; batches published %d; controller error %v; queued %d", got, batches.Load(), ce, len(a.closedQ))
		}
		time.Sleep(50 * time.Millisecond)
	}
}
