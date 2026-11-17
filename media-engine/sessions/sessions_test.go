package sessions

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"nexostream/internal/model"
)

func denied(t *testing.T, err error, reason string) {
	t.Helper()
	var d *DeniedError
	if !errors.As(err, &d) || d.Reason != reason {
		t.Fatalf("want denial %q, got %v", reason, err)
	}
}

func TestRulesAndBackend(t *testing.T) {
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if q.Get("name") != "c1" || q.Get("session_id") == "" || q.Get("proto") != "hls" {
			t.Errorf("backend query %v", q)
		}
		switch q.Get("token") {
		case "good":
			w.Header().Set("X-UserId", "assinante-7")
			w.Header().Set("X-AuthDuration", "300")
		case "unique":
			w.Header().Set("X-UserId", "u2")
			w.Header().Set("X-Unique", "true")
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer backend.Close()
	now := time.Unix(1_000_000, 0)
	tr := &Tracker{WorkerID: "w1", Auth: &Authorizer{}, Now: func() time.Time { return now }}
	cfg := model.AuthConfig{Enabled: true, BackendURL: backend.URL + "/auth?x=1", DenyIPs: []string{"10.9.0.0/16"}, AllowIPs: []string{"10.0.0.0/8", "192.168.1.5"}}
	ctx := context.Background()
	req := func(ip, token string) Request {
		return Request{ChannelID: "c1", Proto: "hls", IP: ip, UserAgent: "VLC", Token: token}
	}

	_, err := tr.Begin(ctx, cfg, req("10.9.1.1", "good"), false)
	denied(t, err, ReasonIPDenied)
	_, err = tr.Begin(ctx, cfg, req("8.8.8.8", "good"), false)
	denied(t, err, ReasonIPNotAllowed)
	_, err = tr.Begin(ctx, cfg, req("10.1.1.1", "bad"), false)
	denied(t, err, ReasonBackendDenied)

	h, err := tr.Begin(ctx, cfg, req("10.1.1.1", "good"), false)
	if err != nil {
		t.Fatal(err)
	}
	h.Add(1000)
	h.Done()
	before := calls.Load()
	h2, err := tr.Begin(ctx, cfg, req("10.1.1.1", "good"), false)
	if err != nil || h2.ID() != h.ID() || calls.Load() != before {
		t.Fatalf("same token must reuse the session and the cached answer: %v", err)
	}
	h2.Done()
	open, _ := tr.List("c1")
	if len(open) != 1 || open[0].UserID != "assinante-7" || open[0].Bytes != 1000 || open[0].Requests != 2 {
		t.Fatalf("sessions %+v", open)
	}

	// A valid cluster token skips the backend.
	before = calls.Load()
	r := req("10.1.1.2", "signed")
	r.TokenValid = true
	if _, err := tr.Begin(ctx, cfg, r, false); err != nil || calls.Load() != before {
		t.Fatalf("cluster token: %v", err)
	}

	// X-Unique replaces the other sessions of the same user.
	if _, err := tr.Begin(ctx, cfg, Request{ChannelID: "c1", Proto: "hls", IP: "10.1.1.3", UserAgent: "A", Token: "unique"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Begin(ctx, cfg, Request{ChannelID: "c1", Proto: "hls", IP: "10.1.1.4", UserAgent: "B", Token: "unique", TokenValid: false}, false); err != nil {
		t.Fatal(err)
	}
	if n := tr.Count("c1"); n != 3 { // good, signed, latest unique
		t.Fatalf("count %d", n)
	}

	// Backend down: deny, unless allow_on_error.
	backend.Close()
	cfg.BackendURL = backend.URL
	_, err = tr.Begin(ctx, cfg, req("10.1.1.9", "x"), false)
	denied(t, err, ReasonBackendError)
	cfg.AllowOnError = true
	if _, err := tr.Begin(ctx, cfg, req("10.1.1.9", "x"), false); err != nil {
		t.Fatal(err)
	}
}

func TestLimitsKillAndIdle(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tr := &Tracker{WorkerID: "w1", Now: func() time.Time { return now }}
	ctx := context.Background()
	cfg := model.AuthConfig{Enabled: true, MaxSessions: 2}
	r := func(ip string) Request { return Request{ChannelID: "c1", Proto: "hls", IP: ip, UserAgent: "x"} }
	a, _ := tr.Begin(ctx, cfg, r("1.1.1.1"), false)
	if _, err := tr.Begin(ctx, cfg, r("1.1.1.2"), false); err != nil {
		t.Fatal(err)
	}
	_, err := tr.Begin(ctx, cfg, r("1.1.1.3"), false)
	denied(t, err, ReasonMaxSessions)

	if !tr.Kill(a.ID()) || tr.Count("c1") != 1 {
		t.Fatal("kill")
	}
	select {
	case <-a.Context.Done():
	case <-time.After(time.Second):
		t.Fatal("a killed session must cancel its requests")
	}
	_, err = tr.Begin(ctx, cfg, r("1.1.1.1"), false)
	denied(t, err, ReasonBanned)
	now = now.Add(BanTime + time.Second)
	tr.Sweep() // also closes 1.1.1.2: idle for > 30 s
	if tr.Count("c1") != 0 {
		t.Fatal("idle session not closed")
	}
	if _, err := tr.Begin(ctx, cfg, r("1.1.1.1"), false); err != nil {
		t.Fatalf("ban must expire: %v", err)
	}

	// MPEG-TS: one session per connection, kept while connected.
	s1, _ := tr.Begin(ctx, model.AuthConfig{}, Request{ChannelID: "c2", Proto: "mpegts", IP: "2.2.2.2"}, true)
	s2, _ := tr.Begin(ctx, model.AuthConfig{}, Request{ChannelID: "c2", Proto: "mpegts", IP: "2.2.2.2"}, true)
	now = now.Add(time.Hour)
	tr.Sweep()
	if tr.Count("c2") != 2 || s1.ID() == s2.ID() {
		t.Fatal("connected streams must stay open, one session each")
	}
	s1.Done()
	if tr.Count("c2") != 1 {
		t.Fatal("disconnect closes the session")
	}
	_, closed := tr.List("c2")
	if len(closed) != 1 || closed[0].Reason != "disconnected" {
		t.Fatalf("closed %+v", closed)
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	if ClientIP(r) != "203.0.113.9" {
		t.Fatal(ClientIP(r))
	}
	r.RemoteAddr = "198.51.100.2:5000" // not the local nginx: headers ignored
	if ClientIP(r) != "198.51.100.2" {
		t.Fatal(ClientIP(r))
	}
	if bad, ok := ValidIPList([]string{"10.0.0.0/8", "1.2.3.4", "x"}); ok || bad != "x" {
		t.Fatal("ValidIPList")
	}
}
