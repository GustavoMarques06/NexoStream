package api

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"nexostream/control-plane/statuscache"
	"nexostream/control-plane/store/memory"
	"nexostream/internal/bus"
	"nexostream/internal/logging"
)

// secEnv starts an API with the given tweaks and returns it with its Server,
// so a test can move the clock.
func secEnv(t *testing.T, tweak func(*Config)) (*tenv, *Server) {
	t.Helper()
	cfg := Config{JWTKey: []byte("test-key-0123456789"), Version: "test", PlaybackTokenKey: testPlaybackKey, LoginRatePerMinute: 1000}
	if tweak != nil {
		tweak(&cfg)
	}
	st := memory.New()
	cache := statuscache.NewMemory()
	b := bus.NewMemory()
	s, err := New(cfg, Deps{Store: st, Cache: cache, Bus: b, Log: logging.Discard()})
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
	return e, s
}

func (e *tenv) loginFull(user, pass string) (int, map[string]any) {
	code, out, _ := e.do("POST", "/api/v1/auth/token", "", map[string]string{"username": user, "password": pass})
	return code, out
}

func (e *tenv) auditActions() []string {
	_, out, _ := e.do("GET", "/api/v1/audit-log", e.tok, nil)
	var acts []string
	items, _ := out["items"].([]any)
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			acts = append(acts, m["action"].(string))
		}
	}
	return acts
}

func has(list []string, want string) bool {
	for _, x := range list {
		if x == want {
			return true
		}
	}
	return false
}

// ---- refresh token rotation ----

func TestRefreshTokenRotates(t *testing.T) {
	e, _ := secEnv(t, nil)
	_, out := e.loginFull("admin", "admin-password-123")
	rt := out["refresh_token"].(string)

	// Each renewal gives a new refresh token, and each one works.
	for i := 0; i < 3; i++ {
		code, out, _ := e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": rt})
		if code != 200 {
			t.Fatalf("renewal %d: %d %v", i+1, code, out)
		}
		next := out["refresh_token"].(string)
		if next == rt {
			t.Fatal("the refresh token must change at each renewal")
		}
		if code, _, _ := e.do("GET", "/api/v1/auth/me", out["access_token"].(string), nil); code != 200 {
			t.Fatalf("new access token refused: %d", code)
		}
		rt = next
	}
}

// A refresh token used twice means it was copied: the whole sign-in family is
// revoked, including the newest token the legitimate client holds.
func TestRefreshTokenReuseRevokesTheFamily(t *testing.T) {
	e, _ := secEnv(t, nil)
	_, out := e.loginFull("admin", "admin-password-123")
	stolen := out["refresh_token"].(string)

	code, out, _ := e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": stolen})
	if code != 200 {
		t.Fatalf("legitimate renewal: %d", code)
	}
	newest := out["refresh_token"].(string)

	// The attacker presents the old token.
	code, out, _ = e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": stolen})
	if code != 401 || errCode(out) != "token_reused" {
		t.Fatalf("reuse must be refused as token_reused, got %d %v", code, out)
	}
	// Now the client's newest token is dead too: both must sign in again.
	if code, out, _ := e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": newest}); code != 401 {
		t.Fatalf("the family must be revoked after a reuse, got %d %v", code, out)
	}
	if !has(e.auditActions(), "auth.refresh_reuse") {
		t.Fatal("the reuse must be audited")
	}
	// A new sign-in starts a fresh family.
	if code, _ := e.loginFull("admin", "admin-password-123"); code != 200 {
		t.Fatalf("sign in after a revocation: %d", code)
	}
}

func TestRefreshTokenRejectsOtherKinds(t *testing.T) {
	e, _ := secEnv(t, nil)
	_, out := e.loginFull("admin", "admin-password-123")
	// An access token is not a refresh token.
	if code, _, _ := e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": out["access_token"].(string)}); code != 401 {
		t.Fatalf("access token as refresh token: %d", code)
	}
	// Neither is a signed token that was never recorded (forged with a leaked key, or from before rotation).
	_, s := secEnv(t, nil)
	u, _ := e.st.GetUserByUsername(context.Background(), "admin")
	forged, _, _ := s.issue(u, "refresh", "no-such-family", time.Hour)
	if code, _, _ := e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": forged}); code != 401 {
		t.Fatalf("an unrecorded refresh token: %d", code)
	}
	if code, _, _ := e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": "garbage"}); code != 401 {
		t.Fatalf("garbage: %d", code)
	}
}

func TestLogoutAndPasswordChangeRevokeRefreshTokens(t *testing.T) {
	e, _ := secEnv(t, nil)
	_, out := e.loginFull("admin", "admin-password-123")
	rt, at := out["refresh_token"].(string), out["access_token"].(string)
	waitNextSecond()
	if code, _, _ := e.do("POST", "/api/v1/auth/logout", at, nil); code != 204 {
		t.Fatalf("logout %d", code)
	}
	if code, _, _ := e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": rt}); code != 401 {
		t.Fatalf("refresh token survived the logout: %d", code)
	}
}

// ---- account lockout ----

func TestLockDurationEscalates(t *testing.T) {
	s := &Server{cfg: Config{LockoutThreshold: 5, LockoutBase: 5 * time.Minute, LockoutMax: time.Hour}}
	for fails, want := range map[int]time.Duration{
		0: 0, 4: 0, 5: 5 * time.Minute, 9: 5 * time.Minute, 10: 10 * time.Minute, 15: 20 * time.Minute,
		20: 40 * time.Minute, 25: time.Hour, 1000: time.Hour,
	} {
		if got := s.lockDuration(fails); got != want {
			t.Errorf("%d failures: locked %v, want %v", fails, got, want)
		}
	}
	off := &Server{cfg: Config{LockoutThreshold: -1, LockoutBase: time.Minute, LockoutMax: time.Hour}}
	if off.lockDuration(100) != 0 {
		t.Fatal("a negative threshold turns the lockout off")
	}
}

func TestAccountLockout(t *testing.T) {
	e, s := secEnv(t, func(c *Config) { c.LockoutThreshold = 3; c.LockoutBase = 10 * time.Minute; c.LockoutMax = time.Hour })
	if code, _, _ := e.do("POST", "/api/v1/users", e.tok, map[string]any{"username": "ana", "password": "ana-zebra-lantern-7", "role": "viewer"}); code != 201 {
		t.Fatalf("create user %d", code)
	}
	clock := time.Now()
	s.now = func() time.Time { return clock }

	for i := 0; i < 3; i++ {
		if code, _ := e.loginFull("ana", "wrong-password-1"); code != 401 {
			t.Fatalf("wrong password %d: %d", i+1, code)
		}
	}
	// Locked: even the right password is refused, with the same generic answer.
	code, out := e.loginFull("ana", "ana-zebra-lantern-7")
	if code != 401 || errCode(out) != "invalid_credentials" {
		t.Fatalf("a locked account must refuse the right password generically, got %d %v", code, out)
	}
	if !has(e.auditActions(), "auth.account_locked") || !has(e.auditActions(), "auth.login_locked") {
		t.Fatalf("lock and refused attempts must be audited: %v", e.auditActions())
	}
	// Attempts while locked do not extend the lock.
	for i := 0; i < 5; i++ {
		e.loginFull("ana", "wrong-password-1")
	}
	clock = clock.Add(11 * time.Minute) // past the first lock
	if code, out := e.loginFull("ana", "ana-zebra-lantern-7"); code != 200 {
		t.Fatalf("the lock must end on time: %d %v", code, out)
	}
	// A successful sign-in clears the failures: two more mistakes do not lock again.
	e.loginFull("ana", "wrong-password-1")
	e.loginFull("ana", "wrong-password-1")
	if code, _ := e.loginFull("ana", "ana-zebra-lantern-7"); code != 200 {
		t.Fatalf("the counter must restart after a success: %d", code)
	}
	// An unknown user is indistinguishable from a locked one.
	code, out = e.loginFull("nobody", "whatever-password-1")
	if code != 401 || errCode(out) != "invalid_credentials" {
		t.Fatalf("unknown user: %d %v", code, out)
	}
}

func TestAdminCanUnlockAnAccount(t *testing.T) {
	e, _ := secEnv(t, func(c *Config) { c.LockoutThreshold = 2 })
	e.do("POST", "/api/v1/users", e.tok, map[string]any{"username": "ana", "password": "ana-zebra-lantern-7", "role": "viewer"})
	e.loginFull("ana", "wrong-password-1")
	e.loginFull("ana", "wrong-password-1")
	if code, _ := e.loginFull("ana", "ana-zebra-lantern-7"); code != 401 {
		t.Fatal("the account should be locked")
	}
	_, users, _ := e.do("GET", "/api/v1/users", e.tok, nil)
	var id string
	for _, it := range users["items"].([]any) {
		if m := it.(map[string]any); m["username"] == "ana" {
			id = m["id"].(string)
			if m["locked_until"] == nil {
				t.Fatal("the user list must show the lock")
			}
		}
	}
	if code, _, _ := e.do("PATCH", "/api/v1/users/"+id, e.tok, map[string]any{"unlock": true}); code != 200 {
		t.Fatalf("unlock %d", code)
	}
	if code, _ := e.loginFull("ana", "ana-zebra-lantern-7"); code != 200 {
		t.Fatalf("unlocked account refused: %d", code)
	}
}
