// Package integration runs the Control Plane and two workers together over
// a real (embedded) NATS server with JetStream, using the in-memory store,
// synthetic UDP sources and the fake media engine.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"

	"nexostream/control-plane/api"
	"nexostream/control-plane/controller"
	"nexostream/control-plane/statuscache"
	"nexostream/control-plane/store"
	"nexostream/control-plane/store/memory"
	"nexostream/data-plane/agent"
	"nexostream/internal/bus"
	"nexostream/internal/logging"
	"nexostream/internal/model"
	"nexostream/internal/protocol"
	"nexostream/media-engine/ingest/tstest"
	"nexostream/media-engine/pipeline/pipelinetest"
)

func startNATS(t *testing.T) string {
	s, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats not ready")
	}
	t.Cleanup(s.Shutdown)
	return s.ClientURL()
}

func connect(t *testing.T, url, name string) *bus.NATS {
	b, err := bus.NewNATS(context.Background(), bus.NATSConfig{URL: url, Name: name,
		StreamName: protocol.EventsStream, StreamSubjects: protocol.EventSubjects})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func freePort(t *testing.T) int {
	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.LocalAddr().(*net.UDPAddr).Port
}

type workerHandle struct {
	agent  *agent.Agent
	cancel context.CancelFunc
	done   chan struct{}
}

func startWorker(t *testing.T, url, id string) *workerHandle {
	b := connect(t, url, id)
	a, err := agent.New(agent.Config{WorkerID: id, HLSDir: t.TempDir(), StateDir: t.TempDir(), FFprobePath: "-",
		Encoders: []string{"cpu"}, PublicHLSBase: "https://origin.example"}, b, &pipelinetest.Engine{}, logging.Discard())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &workerHandle{agent: a, cancel: cancel, done: make(chan struct{})}
	go func() { defer close(h.done); _ = a.Run(ctx) }()
	t.Cleanup(func() { h.stop() })
	return h
}

func (h *workerHandle) stop() {
	h.cancel()
	<-h.done
}

type client struct {
	t   *testing.T
	url string
	tok string
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, c.url+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if c.tok != "" {
		req.Header.Set("Authorization", "Bearer "+c.tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (c *client) waitStatus(id string, timeout time.Duration, ok func(map[string]any) bool) map[string]any {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	var st map[string]any
	for time.Now().Before(deadline) {
		_, st = c.do("GET", "/api/v1/channels/"+id+"/status", nil)
		if ok(st) {
			return st
		}
		time.Sleep(200 * time.Millisecond)
	}
	c.t.Fatalf("status condition not met for %s: %v", id, st)
	return nil
}

func TestStack(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	url := startNATS(t)
	st := memory.New()
	cache := statuscache.NewMemory()

	// Control Plane: API and controller with their own NATS connections.
	apiBus := connect(t, url, "api")
	srv, err := api.New(api.Config{JWTKey: []byte("integration-test-key-0123456789ab")}, api.Deps{Store: st, Cache: cache, Bus: apiBus, Log: logging.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	ccfg := controller.DefaultConfig()
	ccfg.Interval = time.Second
	ccfg.StartupGrace = time.Second
	ccfg.OfflineAfter = 7 * time.Second // > heartbeat interval (5 s)
	ctrl := controller.New(ccfg, st, cache, connect(t, url, "controller"), logging.Discard())
	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	go func() { _ = ctrl.Run(cctx) }()

	w1 := startWorker(t, url, "media-01")
	w2 := startWorker(t, url, "media-02")
	_ = w2

	if _, err := api.EnsureBootstrapAdmin(context.Background(), st, "admin", "integration-pass-1"); err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, url: hs.URL}
	code, out := c.do("POST", "/api/v1/auth/token", map[string]string{"username": "admin", "password": "integration-pass-1"})
	if code != 200 {
		t.Fatalf("login %d %v", code, out)
	}
	c.tok = out["access_token"].(string)

	// Wait for both workers.
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, out = c.do("GET", "/api/v1/workers", nil)
		if items, _ := out["items"].([]any); len(items) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("workers not registered: %v", out)
		}
		time.Sleep(200 * time.Millisecond)
	}

	port := freePort(t)
	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	go func() { _ = tstest.Send(sctx, tstest.New(), fmt.Sprintf("127.0.0.1:%d", port), 2_000_000) }()

	fast := true
	code, out = c.do("POST", "/api/v1/channels", map[string]any{
		"id": "canal-001", "name": "Canal Integração", "mode": "auto",
		"sources":  []map[string]any{{"priority": 1, "url": fmt.Sprintf("udp://127.0.0.1:%d", port)}},
		"outputs":  map[string]any{"hls": true},
		"failover": map[string]any{"no_packets_seconds": 1, "confirm_window_seconds": 1, "return_to_primary": fast},
	})
	if code != 201 {
		t.Fatalf("create %d %v", code, out)
	}
	stat := c.waitStatus("canal-001", 10*time.Second, func(s map[string]any) bool { return s["state"] == "RUNNING" })
	first := stat["worker_id"].(string)
	if stat["processing"] != "passthrough" || stat["outputs"].(map[string]any)["hls"] != "https://origin.example/canal-001/index.m3u8" {
		t.Fatalf("status %v", stat)
	}

	// Restart through the API reaches the worker.
	if code, out := c.do("POST", "/api/v1/channels/canal-001/restart", nil); code != 202 {
		t.Fatalf("restart %d %v", code, out)
	}

	// Rename only: no pipeline restart needed, revision follows.
	if code, _ := c.do("PATCH", "/api/v1/channels/canal-001", map[string]any{"name": "Renomeado"}); code != 200 {
		t.Fatal("patch")
	}
	c.waitStatus("canal-001", 8*time.Second, func(s map[string]any) bool { return s["revision"].(float64) == 2 && s["state"] == "RUNNING" })

	// Kill the worker running the channel: it must be reassigned.
	victim := w1
	if first == "media-02" {
		victim = w2
	}
	victim.stop()
	stat = c.waitStatus("canal-001", 25*time.Second, func(s map[string]any) bool {
		return s["state"] == "RUNNING" && s["worker_id"] != first
	})
	t.Logf("reassigned from %s to %s", first, stat["worker_id"])

	_, out = c.do("GET", "/api/v1/workers/"+first, nil)
	if out["worker"].(map[string]any)["state"] != "OFFLINE" {
		t.Fatalf("worker state %v", out)
	}
	// Events made it through JetStream into the store.
	deadline = time.Now().Add(5 * time.Second)
	for {
		evs, _ := st.ListEvents(context.Background(), store.EventFilter{Type: model.EventWorkerChannelsReassigned})
		if len(evs) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker.channels_reassigned not persisted")
		}
		time.Sleep(200 * time.Millisecond)
	}
	evs, _ := st.ListEvents(context.Background(), store.EventFilter{ChannelID: "canal-001", Type: model.EventChannelStateChanged})
	if len(evs) == 0 {
		t.Fatal("channel events not persisted")
	}

	// Stop through the API: the worker stops the pipeline.
	if code, _ := c.do("POST", "/api/v1/channels/canal-001/stop", nil); code != 202 {
		t.Fatal("stop")
	}
	c.waitStatus("canal-001", 8*time.Second, func(s map[string]any) bool { return s["state"] == "STOPPED" })
	if code, out := c.do("DELETE", "/api/v1/channels/canal-001", nil); code != 204 {
		t.Fatalf("delete %d %v", code, out)
	}
}

func TestDrain(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	url := startNATS(t)
	st := memory.New()
	cache := statuscache.NewMemory()
	ccfg := controller.DefaultConfig()
	ccfg.Interval, ccfg.StartupGrace = time.Second, time.Second
	b := connect(t, url, "controller")
	ctrl := controller.New(ccfg, st, cache, b, logging.Discard())
	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	go func() { _ = ctrl.Run(cctx) }()
	startWorker(t, url, "media-01")

	port := freePort(t)
	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	go func() { _ = tstest.Send(sctx, tstest.New(), fmt.Sprintf("127.0.0.1:%d", port), 1_000_000) }()
	time.Sleep(1500 * time.Millisecond)
	for i := 0; i < 3; i++ {
		ch := &model.Channel{ID: fmt.Sprintf("c%d", i), Name: "c", Enabled: true, Mode: model.ModePassthrough,
			Sources: []model.Source{{ID: "1", Priority: 1, URL: fmt.Sprintf("udp://127.0.0.1:%d", port)}},
			Outputs: model.Outputs{HLS: true}, Failover: model.DefaultFailover()}
		if err := st.CreateChannel(context.Background(), ch); err != nil {
			t.Fatal(err)
		}
	}
	ctrl.Kick()
	waitAssigned := func(worker string, n int) {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			as, _ := st.ListAssignments(context.Background())
			cnt := 0
			for _, a := range as {
				if a.WorkerID == worker {
					cnt++
				}
			}
			if cnt == n {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("worker %s does not have %d channels", worker, n)
	}
	waitAssigned("media-01", 3)

	w2 := startWorker(t, url, "media-02")
	time.Sleep(1500 * time.Millisecond)
	if err := st.SetWorkerDraining(context.Background(), "media-01", true); err != nil {
		t.Fatal(err)
	}
	ctrl.Kick()
	waitAssigned("media-02", 3)
	// Make-before-break: media-01 ends up with no running channel.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if len(w2.agent.Statuses()) == 3 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if n := len(w2.agent.Statuses()); n != 3 {
		t.Fatalf("media-02 runs %d channels", n)
	}
}
