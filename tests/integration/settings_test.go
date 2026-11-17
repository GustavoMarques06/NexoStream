package integration

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nexostream/control-plane/api"
	"nexostream/control-plane/controller"
	"nexostream/control-plane/statuscache"
	"nexostream/control-plane/store/memory"
	"nexostream/data-plane/agent"
	"nexostream/internal/logging"
	"nexostream/media-engine/ingest/tstest"
	"nexostream/media-engine/pipeline/pipelinetest"
)

// TestSettingsPush checks that the Settings page reaches the Media Node:
// the controller sends the runtime, the node applies it (status URLs use
// the new public base), keeps it on disk and reports its hash.
func TestSettingsPush(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	url := startNATS(t)
	st := memory.New()
	cache := statuscache.NewMemory()
	srv, err := api.New(api.Config{JWTKey: []byte("integration-test-key-0123456789ab")}, api.Deps{Store: st, Cache: cache, Bus: connect(t, url, "api"), Log: logging.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	ccfg := controller.DefaultConfig()
	ccfg.Interval = time.Second
	ccfg.StartupGrace = time.Second
	ccfg.ResendAfter = 2 * time.Second
	ctrl := controller.New(ccfg, st, cache, connect(t, url, "controller"), logging.Discard())
	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	go func() { _ = ctrl.Run(cctx) }()

	stateDir := t.TempDir()
	a, err := agent.New(agent.Config{WorkerID: "media-01", HLSDir: t.TempDir(), StateDir: stateDir, FFprobePath: "-",
		Encoders: []string{"cpu"}, PublicHLSBase: "https://env.example"}, connect(t, url, "media-01"), &pipelinetest.Engine{}, logging.Discard())
	if err != nil {
		t.Fatal(err)
	}
	wctx, wcancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = a.Run(wctx) }()
	defer func() { wcancel(); <-done }()

	if _, err := api.EnsureBootstrapAdmin(context.Background(), st, "admin", "integration-pass-1"); err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, url: hs.URL}
	_, out := c.do("POST", "/api/v1/auth/token", map[string]string{"username": "admin", "password": "integration-pass-1"})
	c.tok = out["access_token"].(string)

	port := freePort(t)
	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	go func() { _ = tstest.Send(sctx, tstest.New(), fmt.Sprintf("127.0.0.1:%d", port), 2_000_000) }()
	code, out := c.do("POST", "/api/v1/channels", map[string]any{
		"id": "canal-001", "name": "Canal", "mode": "passthrough",
		"sources": []map[string]any{{"priority": 1, "url": fmt.Sprintf("udp://127.0.0.1:%d", port)}},
		"outputs": map[string]any{"hls": true},
	})
	if code != 201 {
		t.Fatalf("create %d %v", code, out)
	}
	hlsURL := func(s map[string]any) string {
		o, _ := s["outputs"].(map[string]any)
		v, _ := o["hls"].(string)
		return v
	}
	c.waitStatus("canal-001", 20*time.Second, func(s map[string]any) bool { return strings.HasPrefix(hlsURL(s), "https://env.example/") })

	code, out = c.do("PUT", "/api/v1/settings/servers/media-01", map[string]any{"public_hls_base": "https://painel.example/hls"})
	if code != 200 {
		t.Fatalf("server settings %d %v", code, out)
	}
	c.waitStatus("canal-001", 30*time.Second, func(s map[string]any) bool {
		return strings.HasPrefix(hlsURL(s), "https://painel.example/hls/canal-001/")
	})

	b, err := os.ReadFile(filepath.Join(stateDir, "settings.json"))
	if err != nil || !strings.Contains(string(b), "https://painel.example/hls") {
		t.Fatalf("settings not persisted on the node: %s %v", b, err)
	}
}
