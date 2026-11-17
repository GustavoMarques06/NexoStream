package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nexostream/control-plane/sharedstate"
	"nexostream/control-plane/statuscache"
	"nexostream/control-plane/store/memory"
	"nexostream/internal/bus"
	"nexostream/internal/logging"
)

func TestClientIPIgnoresForgedHeaders(t *testing.T) {
	mk := func(trust bool, proxies []string) *Server {
		s := &Server{cfg: Config{TrustProxy: trust, TrustedProxies: proxies}}
		for _, c := range proxies {
			_, n, err := net.ParseCIDR(c)
			if err != nil {
				t.Fatal(err)
			}
			s.proxies = append(s.proxies, n)
		}
		return s
	}
	req := func(peer, real, xff string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = peer
		if real != "" {
			r.Header.Set("X-Real-IP", real)
		}
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	cases := []struct {
		name string
		s    *Server
		r    *http.Request
		want string
	}{
		{"direct client cannot forge XFF", mk(true, nil), req("203.0.113.9:5555", "", "1.2.3.4"), "203.0.113.9"},
		{"direct client cannot forge X-Real-IP", mk(true, nil), req("203.0.113.9:5555", "1.2.3.4", ""), "203.0.113.9"},
		{"loopback proxy: X-Real-IP", mk(true, nil), req("127.0.0.1:1", "198.51.100.7", "9.9.9.9"), "198.51.100.7"},
		{"loopback proxy: last XFF entry", mk(true, nil), req("127.0.0.1:1", "", "6.6.6.6, 198.51.100.7"), "198.51.100.7"},
		{"configured proxy", mk(true, []string{"10.0.0.0/8"}), req("10.1.2.3:1", "198.51.100.7", ""), "198.51.100.7"},
		{"other private peer is not trusted", mk(true, []string{"10.0.0.0/8"}), req("192.168.1.5:1", "1.2.3.4", ""), "192.168.1.5"},
		{"trust disabled", mk(false, nil), req("127.0.0.1:1", "198.51.100.7", ""), "127.0.0.1"},
		{"garbage header ignored", mk(true, nil), req("127.0.0.1:1", "not-an-ip", ""), "127.0.0.1"},
	}
	for _, c := range cases {
		if got := c.s.clientIP(c.r); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestLogoutAndPasswordChangeRevokeSessions(t *testing.T) {
	e := newTestEnv(t)
	code, out, _ := e.do("POST", "/api/v1/auth/token", "", map[string]string{"username": "admin", "password": "admin-password-123"})
	if code != 200 {
		t.Fatalf("login %d", code)
	}
	access, refresh := out["access_token"].(string), out["refresh_token"].(string)
	// Tokens are issued with one-second precision: wait so the revocation
	// instant is strictly after them.
	waitNextSecond()
	if code, _, _ := e.do("POST", "/api/v1/auth/logout", access, nil); code != 204 {
		t.Fatalf("logout %d", code)
	}
	if code, _, _ := e.do("GET", "/api/v1/auth/me", access, nil); code != 401 {
		t.Fatalf("revoked access token still works: %d", code)
	}
	if code, _, _ := e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": refresh}); code != 401 {
		t.Fatalf("revoked refresh token still works: %d", code)
	}
	// A new login after the revocation works.
	waitNextSecond()
	fresh := e.login("admin", "admin-password-123")
	if code, _, _ := e.do("GET", "/api/v1/auth/me", fresh, nil); code != 200 {
		t.Fatalf("fresh token refused: %d", code)
	}

	// Another user: changing the password ends that user's sessions.
	if code, _, _ := e.do("POST", "/api/v1/users", fresh, map[string]any{"username": "ana", "password": "ana-zebra-lantern-7", "role": "viewer"}); code != 201 {
		t.Fatalf("create user %d", code)
	}
	anaTok := e.login("ana", "ana-zebra-lantern-7")
	_, users, _ := e.do("GET", "/api/v1/users", fresh, nil)
	var anaID string
	for _, it := range users["items"].([]any) {
		if m := it.(map[string]any); m["username"] == "ana" {
			anaID = m["id"].(string)
		}
	}
	waitNextSecond()
	if code, _, _ := e.do("PATCH", "/api/v1/users/"+anaID, fresh, map[string]any{"password": "velvet-comet-orbit-9"}); code != 200 {
		t.Fatalf("patch user %d", code)
	}
	if code, _, _ := e.do("GET", "/api/v1/auth/me", anaTok, nil); code != 401 {
		t.Fatalf("old session survived a password change: %d", code)
	}
}

func waitNextSecond() {
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(1100 * time.Millisecond)))
}

func TestIntegrationTokenCannotLogout(t *testing.T) {
	e := newTestEnv(t)
	code, out, _ := e.do("POST", "/api/v1/api-tokens", e.tok, map[string]any{"name": "mw", "scopes": []string{"channels:read"}})
	if code != 201 {
		t.Fatalf("create token %d %v", code, out)
	}
	if code, _, _ := e.do("POST", "/api/v1/auth/logout", out["token"].(string), nil); code != 422 {
		t.Fatalf("integration token logout: %d", code)
	}
}

func TestPasswordPolicy(t *testing.T) {
	for pw, ok := range map[string]bool{
		"short":                  false,
		"aaaaaaaaaaaa":           false, // one repeated character
		"1234512345":             true,  // five distinct digits and no common word: allowed
		"password":               false,
		"passwordpassword":       false,
		"admin-password-123":     true,
		"correct horse battery":  true,
		strings.Repeat("x1", 40): false, // > 72 bytes
	} {
		err := validPassword(pw)
		if (err == nil) != ok {
			t.Errorf("validPassword(%q) = %v, want ok=%v", pw, err, ok)
		}
	}
}

func TestUserLoginThrottleAndFailureAudit(t *testing.T) {
	e := newTestEnv(t)
	limited := false
	for i := 0; i < 30 && !limited; i++ {
		code, _, _ := e.do("POST", "/api/v1/auth/token", "", map[string]string{"username": "alvo", "password": "errada-errada"})
		limited = code == 429
	}
	if !limited {
		t.Fatal("repeated attempts against one user were never throttled")
	}
	_, out, _ := e.do("GET", "/api/v1/audit-log", e.tok, nil)
	found := false
	items, _ := out["items"].([]any)
	for _, it := range items {
		if m, _ := it.(map[string]any); m["action"] == "auth.login_failed" && m["target"] == "alvo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("failed logins were not audited: %v", out)
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := newTestEnv(t)
	_, _, h := e.do("GET", "/api/v1/auth/me", e.tok, nil)
	if h.Get("Cache-Control") != "no-store" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("headers %v", h)
	}
	if h.Get("Strict-Transport-Security") != "" {
		t.Fatal("HSTS must not be sent over plain HTTP")
	}
	// Behind a trusted proxy (loopback peer) that terminated TLS.
	trusted := &Server{cfg: Config{TrustProxy: true}}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("X-Forwarded-Proto", "https")
	if !trusted.secureRequest(r) {
		t.Fatal("HSTS expected behind a trusted TLS proxy")
	}
	r.RemoteAddr = "203.0.113.9:1" // a direct client cannot claim HTTPS
	if trusted.secureRequest(r) {
		t.Fatal("forged X-Forwarded-Proto accepted from an untrusted peer")
	}
}

// twoInstances starts two API servers over the same store and shared state,
// like two replicas behind the proxy.
func twoInstances(t *testing.T, loginPerMinute int) (a, b *tenv) {
	t.Helper()
	st := memory.New()
	cache := statuscache.NewMemory()
	bs := bus.NewMemory()
	shared := sharedstate.NewMemory()
	mk := func() *tenv {
		s, err := New(Config{JWTKey: []byte("test-key-0123456789"), Version: "test", PlaybackTokenKey: testPlaybackKey, LoginRatePerMinute: loginPerMinute},
			Deps{Store: st, Cache: cache, Bus: bs, Log: logging.Discard(), Shared: shared})
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(s.Handler())
		t.Cleanup(func() { srv.Close(); s.Close() })
		return &tenv{t: t, srv: srv, st: st, cache: cache, bus: bs}
	}
	t.Cleanup(func() { bs.Close() })
	if _, err := EnsureBootstrapAdmin(context.Background(), st, "admin", "admin-password-123"); err != nil {
		t.Fatal(err)
	}
	return mk(), mk()
}

// The login limit counts attempts across API instances: with 4 allowed per
// minute, alternating between two replicas must stop at 4, not at 8.
func TestLoginLimitIsSharedBetweenInstances(t *testing.T) {
	a, b := twoInstances(t, 4)
	instances := []*tenv{a, b}
	refused := -1
	for i := 0; i < 8; i++ {
		// a different user name each time, so only the per-address limit counts
		code, _, _ := instances[i%2].do("POST", "/api/v1/auth/token", "", map[string]string{"username": "u" + string(rune('a'+i)), "password": "wrong-password-1"})
		if code == 429 {
			refused = i
			break
		}
	}
	if refused != 4 {
		t.Fatalf("the 5th attempt (index 4) must be the first refused across both instances, got index %d", refused)
	}
}

// A retry with the same Idempotency-Key that lands on another replica is
// replayed, not executed twice.
func TestIdempotencyIsSharedBetweenInstances(t *testing.T) {
	a, b := twoInstances(t, 100)
	tok := a.login("admin", "admin-password-123")
	code, _, _ := a.do("POST", "/api/v1/channels", tok, channelBody("idem-1"), "Idempotency-Key", "retry-42")
	if code != 201 {
		t.Fatalf("first create: %d", code)
	}
	code, _, hdr := b.do("POST", "/api/v1/channels", tok, channelBody("idem-1"), "Idempotency-Key", "retry-42")
	if code != 201 || hdr.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("retry on the other instance: %d replayed=%q (without sharing this would be 409)", code, hdr.Get("Idempotent-Replayed"))
	}
	if code, _, _ := b.do("POST", "/api/v1/channels", tok, channelBody("idem-1")); code != 409 {
		t.Fatalf("without the key a duplicate must conflict: %d", code)
	}
}
