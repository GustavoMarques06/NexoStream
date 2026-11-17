package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nexostream/control-plane/statuscache"
	"nexostream/control-plane/store/memory"
	"nexostream/internal/bus"
	"nexostream/internal/logging"
	"nexostream/internal/model"
	"nexostream/internal/protocol"
)

var testPlaybackKey = []byte("playback-token-key-for-tests-0123456789")

type tenv struct {
	t     *testing.T
	srv   *httptest.Server
	st    *memory.Store
	cache *statuscache.Memory
	bus   *bus.Memory
	tok   string
}

func newTestEnv(t *testing.T) *tenv {
	st := memory.New()
	cache := statuscache.NewMemory()
	b := bus.NewMemory()
	s, err := New(Config{JWTKey: []byte("test-key-0123456789"), Version: "test", PlaybackTokenKey: testPlaybackKey}, Deps{Store: st, Cache: cache, Bus: b, Log: logging.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); s.Close(); b.Close() })
	if _, err := EnsureBootstrapAdmin(context.Background(), st, "admin", "admin-password-123"); err != nil {
		t.Fatal(err)
	}
	e := &tenv{t: t, srv: srv, st: st, cache: cache, bus: b}
	e.tok = e.login("admin", "admin-password-123")
	return e
}

func (e *tenv) do(method, path, tok string, body any, hdr ...string) (int, map[string]any, http.Header) {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

func (e *tenv) login(u, p string) string {
	e.t.Helper()
	code, out, _ := e.do("POST", "/api/v1/auth/token", "", map[string]string{"username": u, "password": p})
	if code != 200 {
		e.t.Fatalf("login %s: %d %v", u, code, out)
	}
	return out["access_token"].(string)
}

func errCode(out map[string]any) string {
	if e, ok := out["error"].(map[string]any); ok {
		return e["code"].(string)
	}
	return ""
}

func channelBody(id string) map[string]any {
	return map[string]any{
		"id": id, "name": "Canal Exemplo", "group": "Noticias", "mode": "passthrough",
		"sources": []map[string]any{
			{"priority": 1, "url": "udp://239.1.1.1:1234", "interface": "eno2"},
			{"priority": 2, "url": "udp://239.1.2.1:1234", "interface": "eno3"},
		},
		"outputs":  map[string]any{"hls": true, "mpegts_http": true},
		"dvr":      map[string]any{"enabled": true, "retention_hours": 72},
		"failover": map[string]any{"no_packets_seconds": 3, "confirm_window_seconds": 5, "return_to_primary": true, "primary_stable_seconds": 60},
	}
}

func TestAuth(t *testing.T) {
	e := newTestEnv(t)
	code, out, hdr := e.do("GET", "/api/v1/channels", "", nil)
	if code != 401 || errCode(out) != "unauthorized" || hdr.Get("X-Request-Id") == "" {
		t.Fatalf("%d %v", code, out)
	}
	if code, _, _ := e.do("POST", "/api/v1/auth/token", "", map[string]string{"username": "admin", "password": "wrong"}); code != 401 {
		t.Fatalf("bad password: %d", code)
	}
	code, out, _ = e.do("GET", "/api/v1/auth/me", e.tok, nil)
	if code != 200 || out["role"] != "admin" {
		t.Fatalf("%d %v", code, out)
	}
	// Refresh.
	_, tokens, _ := e.do("POST", "/api/v1/auth/token", "", map[string]string{"username": "admin", "password": "admin-password-123"})
	code, out, _ = e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": tokens["refresh_token"].(string)})
	if code != 200 || out["access_token"] == "" {
		t.Fatalf("refresh %d %v", code, out)
	}
	// An access token is not a refresh token.
	if code, _, _ := e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": e.tok}); code != 401 {
		t.Fatalf("access as refresh: %d", code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newTestEnv(t)
	limited := false
	for i := 0; i < 15; i++ {
		code, _, _ := e.do("POST", "/api/v1/auth/token", "", map[string]string{"username": "x", "password": "y"})
		if code == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("login was never rate limited")
	}
}

func TestRBAC(t *testing.T) {
	e := newTestEnv(t)
	for _, u := range []struct{ name, role string }{{"viewer1", "viewer"}, {"operador1", "operator"}} {
		code, out, _ := e.do("POST", "/api/v1/users", e.tok, map[string]string{"username": u.name, "password": "senha-forte-123", "role": u.role})
		if code != 201 {
			t.Fatalf("create user: %d %v", code, out)
		}
	}
	viewer := e.login("viewer1", "senha-forte-123")
	op := e.login("operador1", "senha-forte-123")
	if code, _, _ := e.do("GET", "/api/v1/channels", viewer, nil); code != 200 {
		t.Fatalf("viewer read: %d", code)
	}
	if code, out, _ := e.do("POST", "/api/v1/channels", viewer, channelBody("c1")); code != 403 || errCode(out) != "forbidden" {
		t.Fatalf("viewer write: %d", code)
	}
	if code, _, _ := e.do("POST", "/api/v1/channels", op, channelBody("c1")); code != 201 {
		t.Fatalf("operator write: %d", code)
	}
	if code, _, _ := e.do("GET", "/api/v1/users", op, nil); code != 403 {
		t.Fatalf("operator users: %d", code)
	}
	if code, _, _ := e.do("GET", "/api/v1/audit-log", op, nil); code != 403 {
		t.Fatalf("operator audit: %d", code)
	}
	// Weak password and invalid role.
	if code, _, _ := e.do("POST", "/api/v1/users", e.tok, map[string]string{"username": "x", "password": "short", "role": "viewer"}); code != 422 {
		t.Fatalf("weak password: %d", code)
	}
	// Disabled users lose access immediately.
	users, _ := e.st.ListUsers(context.Background())
	var vid string
	for _, u := range users {
		if u.Username == "viewer1" {
			vid = u.ID
		}
	}
	if code, _, _ := e.do("PATCH", "/api/v1/users/"+vid, e.tok, map[string]any{"disabled": true}); code != 200 {
		t.Fatalf("disable: %d", code)
	}
	if code, _, _ := e.do("GET", "/api/v1/channels", viewer, nil); code != 401 {
		t.Fatalf("disabled user still authorized: %d", code)
	}
}

func TestChannelLifecycle(t *testing.T) {
	e := newTestEnv(t)
	kicks := make(chan struct{}, 10)
	_, _ = e.bus.Subscribe(protocol.ReconcileSubject, func(string, []byte) { kicks <- struct{}{} })
	time.Sleep(20 * time.Millisecond)

	code, out, hdr := e.do("POST", "/api/v1/channels", e.tok, channelBody("canal-001"))
	if code != 201 || hdr.Get("Location") != "/api/v1/channels/canal-001" {
		t.Fatalf("%d %v", code, out)
	}
	if out["status"].(map[string]any)["state"] != "STARTING" || out["revision"].(float64) != 1 {
		t.Fatalf("status %v", out["status"])
	}
	select {
	case <-kicks:
	case <-time.After(time.Second):
		t.Fatal("controller not kicked")
	}
	if code, out, _ := e.do("POST", "/api/v1/channels", e.tok, channelBody("canal-001")); code != 409 || errCode(out) != "channel_exists" {
		t.Fatalf("dup: %d %v", code, out)
	}

	// Validation errors.
	bad := channelBody("Canal Inválido")
	bad["sources"] = []map[string]any{{"priority": 1, "url": "ftp://1.2.3.4:21"}}
	bad["mode"] = "transcode"
	code, out, _ = e.do("POST", "/api/v1/channels", e.tok, bad)
	det, _ := out["error"].(map[string]any)["details"].(map[string]any)
	if code != 422 || det["id"] == nil || det["sources[0].url"] == nil || det["profiles"] == nil {
		t.Fatalf("validation: %d %v", code, out)
	}
	if code, _, _ := e.do("POST", "/api/v1/channels", e.tok, map[string]any{"name": "x", "unknown": 1}); code != 400 {
		t.Fatalf("unknown field: %d", code)
	}
	two := channelBody("canal-abr")
	two["mode"], two["profiles"] = "auto", []string{"hd", "sd"}
	if code, _, _ := e.do("POST", "/api/v1/channels", e.tok, two); code != 422 {
		t.Fatalf("an ABR ladder needs mode transcode: %d", code)
	}
	two["mode"], two["profiles"] = "transcode", []string{"hd", "hd"}
	if code, _, _ := e.do("POST", "/api/v1/channels", e.tok, two); code != 422 {
		t.Fatalf("repeated profile: %d", code)
	}
	two["profiles"] = []string{"hd", "sd"}
	if code, out, _ := e.do("POST", "/api/v1/channels", e.tok, two); code != 201 || len(out["profiles"].([]any)) != 2 {
		t.Fatalf("ABR ladder: %d %v", code, out)
	}

	// Status from the cache.
	_ = e.cache.Set(context.Background(), model.ChannelStatus{ChannelID: "canal-001", State: model.StateOnBackup, WorkerID: "media-02",
		Processing: "passthrough", UpdatedAt: time.Now(), Input: &model.InputStatus{BitrateBps: 4_120_000}})
	code, out, _ = e.do("GET", "/api/v1/channels/canal-001/status", e.tok, nil)
	if code != 200 || out["state"] != "ON_BACKUP" || out["worker_id"] != "media-02" {
		t.Fatalf("status %d %v", code, out)
	}
	code, out, _ = e.do("GET", "/api/v1/channels?state=ON_BACKUP", e.tok, nil)
	if code != 200 || len(out["items"].([]any)) != 1 {
		t.Fatalf("filter %d %v", code, out)
	}
	_, out, _ = e.do("GET", "/api/v1/channels?state=RUNNING", e.tok, nil)
	if len(out["items"].([]any)) != 0 {
		t.Fatalf("filter running %v", out)
	}

	// PATCH bumps the revision and keeps other fields.
	code, out, _ = e.do("PATCH", "/api/v1/channels/canal-001", e.tok, map[string]any{"name": "Novo nome"})
	if code != 200 || out["name"] != "Novo nome" || out["revision"].(float64) != 2 || len(out["sources"].([]any)) != 2 {
		t.Fatalf("patch %d %v", code, out)
	}

	// Sources.
	code, out, _ = e.do("POST", "/api/v1/channels/canal-001/sources", e.tok, map[string]any{"url": "udp://239.1.3.1:1234"})
	if code != 201 || out["priority"].(float64) != 3 {
		t.Fatalf("add source %d %v", code, out)
	}
	if code, _, _ := e.do("POST", "/api/v1/channels/canal-001/sources", e.tok, map[string]any{"priority": 1, "url": "udp://239.9.9.9:1"}); code != 422 {
		t.Fatalf("duplicate priority: %d", code)
	}
	if code, _, _ := e.do("DELETE", "/api/v1/channels/canal-001/sources/3", e.tok, nil); code != 204 {
		t.Fatalf("delete source: %d", code)
	}

	// Restart needs an assigned worker.
	if code, out, _ := e.do("POST", "/api/v1/channels/canal-001/restart", e.tok, nil); code != 409 || errCode(out) != "channel_not_assigned" {
		t.Fatalf("restart: %d %v", code, out)
	}
	if code, _, _ := e.do("POST", "/api/v1/channels/canal-001/switch-source", e.tok, map[string]any{"priority": 9}); code != 422 {
		t.Fatalf("switch unknown: %d", code)
	}

	// Delete requires the channel to be stopped.
	if code, out, _ := e.do("DELETE", "/api/v1/channels/canal-001", e.tok, nil); code != 409 || errCode(out) != "channel_not_stopped" {
		t.Fatalf("delete running: %d %v", code, out)
	}
	if code, out, _ := e.do("POST", "/api/v1/channels/canal-001/stop", e.tok, nil); code != 202 || out["enabled"] != false {
		t.Fatalf("stop: %d %v", code, out)
	}
	_ = e.cache.Delete(context.Background(), "canal-001")
	if code, _, _ := e.do("DELETE", "/api/v1/channels/canal-001", e.tok, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, out, _ := e.do("GET", "/api/v1/channels/canal-001", e.tok, nil); code != 404 || errCode(out) != "channel_not_found" {
		t.Fatalf("get deleted: %d %v", code, out)
	}

	// Every write was audited.
	_, out, _ = e.do("GET", "/api/v1/audit-log?target=canal-001", e.tok, nil)
	actions := map[string]bool{}
	for _, it := range out["items"].([]any) {
		actions[it.(map[string]any)["action"].(string)] = true
	}
	for _, a := range []string{"channel.create", "channel.update", "source.create", "source.delete", "channel.stop", "channel.delete"} {
		if !actions[a] {
			t.Errorf("missing audit %s in %v", a, actions)
		}
	}
}

func TestIdempotencyKey(t *testing.T) {
	e := newTestEnv(t)
	body := channelBody("")
	delete(body, "id")
	c1, o1, _ := e.do("POST", "/api/v1/channels", e.tok, body, "Idempotency-Key", "abc")
	c2, o2, h2 := e.do("POST", "/api/v1/channels", e.tok, body, "Idempotency-Key", "abc")
	if c1 != 201 || c2 != 201 || o1["id"] != o2["id"] || h2.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("%d %d %v %v", c1, c2, o1["id"], o2["id"])
	}
	if !strings.HasPrefix(o1["id"].(string), "canal-") {
		t.Fatalf("generated id %v", o1["id"])
	}
	n, _, _ := e.st.CountChannels(context.Background())
	if n != 1 {
		t.Fatalf("channels=%d", n)
	}
}

func TestIntegrationTokens(t *testing.T) {
	e := newTestEnv(t)
	code, out, _ := e.do("POST", "/api/v1/api-tokens", e.tok, map[string]any{"name": "middleware", "scopes": []string{"channels:read"}})
	if code != 201 {
		t.Fatalf("%d %v", code, out)
	}
	secret := out["token"].(string)
	if !strings.HasPrefix(secret, TokenPrefix) {
		t.Fatal(secret)
	}
	if code, _, _ := e.do("GET", "/api/v1/channels", secret, nil); code != 200 {
		t.Fatalf("token read: %d", code)
	}
	if code, _, _ := e.do("POST", "/api/v1/channels", secret, channelBody("c1")); code != 403 {
		t.Fatalf("token write: %d", code)
	}
	id := out["api_token"].(map[string]any)["id"].(string)
	if code, _, _ := e.do("DELETE", "/api/v1/api-tokens/"+id, e.tok, nil); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if code, _, _ := e.do("GET", "/api/v1/channels", secret, nil); code != 401 {
		t.Fatalf("revoked token still works: %d", code)
	}
	if code, _, _ := e.do("POST", "/api/v1/api-tokens", e.tok, map[string]any{"name": "x", "scopes": []string{"users:admin"}}); code != 422 {
		t.Fatalf("admin scope on token: %d", code)
	}
}

func TestProfilesAndWorkers(t *testing.T) {
	e := newTestEnv(t)
	code, out, _ := e.do("GET", "/api/v1/transcode-profiles", e.tok, nil)
	if code != 200 || len(out["items"].([]any)) != 3 {
		t.Fatalf("%d %v", code, out)
	}
	p := map[string]any{"id": "mobile", "video": map[string]any{"codec": "h264", "profile": "main", "width": 640, "height": 360, "bitrate_bps": 800000, "gop_seconds": 2},
		"audio": map[string]any{"codec": "aac", "bitrate_bps": 64000}}
	if code, out, _ := e.do("POST", "/api/v1/transcode-profiles", e.tok, p); code != 201 || out["encoder"] != "auto" {
		t.Fatalf("create profile %d %v", code, out)
	}
	if code, out, _ := e.do("PATCH", "/api/v1/transcode-profiles/mobile", e.tok, map[string]any{"video": map[string]any{"width": 641}}); code != 422 {
		t.Fatalf("odd width: %d %v", code, out)
	}
	if code, out, _ := e.do("PATCH", "/api/v1/transcode-profiles/mobile", e.tok, map[string]any{"video": map[string]any{"bitrate_bps": 900000, "max_bitrate_bps": 900000}}); code != 200 ||
		out["video"].(map[string]any)["width"].(float64) != 640 {
		t.Fatalf("patch profile: %d %v", code, out)
	}
	if code, _, _ := e.do("DELETE", "/api/v1/transcode-profiles/hd", e.tok, nil); code != 409 {
		t.Fatalf("default profile delete: %d", code)
	}
	if code, _, _ := e.do("DELETE", "/api/v1/transcode-profiles/mobile", e.tok, nil); code != 204 {
		t.Fatalf("delete profile: %d", code)
	}

	_ = e.st.UpsertWorker(context.Background(), model.Worker{ID: "media-01", State: model.WorkerOnline, LastHeartbeatAt: time.Now(), StartedAt: time.Now()})
	if code, out, _ := e.do("POST", "/api/v1/workers/media-01/drain", e.tok, nil); code != 202 || out["state"] != "DRAINING" {
		t.Fatalf("drain %d %v", code, out)
	}
	if code, out, _ := e.do("POST", "/api/v1/workers/media-01/undrain", e.tok, nil); code != 202 || out["state"] != "ONLINE" {
		t.Fatalf("undrain %d %v", code, out)
	}
	if code, _, _ := e.do("POST", "/api/v1/workers/ghost/drain", e.tok, nil); code != 404 {
		t.Fatalf("ghost drain %d", code)
	}
	if code, out, _ := e.do("GET", "/api/v1/dvr/canal-001/recordings", e.tok, nil); code != 404 || errCode(out) != "channel_not_found" {
		t.Fatalf("dvr %d %v", code, out)
	}
	if code, out, _ := e.do("GET", "/api/v1/system/summary", e.tok, nil); code != 200 || out["workers"].(map[string]any)["total"].(float64) != 1 {
		t.Fatalf("summary %d %v", code, out)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/openapi.json", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("openapi %v", err)
	}
	resp.Body.Close()
}

func TestServerSentEvents(t *testing.T) {
	e := newTestEnv(t)
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/stream/events?status=true", nil)
	req.Header.Set("Authorization", "Bearer "+e.tok)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %s", ct)
	}
	time.Sleep(50 * time.Millisecond)
	ev := model.NewEvent(model.EventChannelStateChanged, time.Now())
	ev.ChannelID, ev.From, ev.To, ev.Reason = "canal-001", model.StateDegraded, model.StateSwitching, "no_packets"
	b, _ := json.Marshal(ev)
	_ = e.bus.PublishDurable(context.Background(), protocol.ChannelEvent("canal-001"), ev.ID, b)

	sc := bufio.NewScanner(resp.Body)
	var gotEvent, gotData bool
	for sc.Scan() {
		line := sc.Text()
		if line == "event: channel.state_changed" {
			gotEvent = true
		}
		if gotEvent && strings.HasPrefix(line, "data: ") && strings.Contains(line, `"to":"SWITCHING"`) {
			gotData = true
			break
		}
	}
	if !gotEvent || !gotData {
		t.Fatal("event not streamed")
	}
}
