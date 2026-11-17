package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nexostream/internal/bus"
	"nexostream/internal/model"
)

// limitedBackend answers every token with the user "ana" limited to 1
// session, and the token "free" with a user without limit.
func limitedBackend(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") == "free" {
			w.Header().Set("X-UserId", "bob")
			return
		}
		w.Header().Set("X-UserId", "ana")
		w.Header().Set("X-Max-Sessions", "1")
		w.Header().Set("X-AuthDuration", "300")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A user limited to one session cannot get a second one on another node, and
// can again once the first closes.
func TestUserLimitAcrossNodes(t *testing.T) {
	backend := limitedBackend(t)
	b := bus.NewMemory()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := model.AuthConfig{Enabled: true, BackendURL: backend.URL + "/auth"}
	mk := func(id string) (*Tracker, *Share) {
		tr := &Tracker{WorkerID: id, Auth: &Authorizer{}}
		sh := &Share{Tracker: tr, Bus: b, Subject: "test.sessions.users", Interval: 20 * time.Millisecond}
		go func() { _ = sh.Run(ctx) }()
		return tr, sh
	}
	a, _ := mk("node-a")
	bb, shB := mk("node-b")
	req := func(token string) Request {
		return Request{ChannelID: "c1", Proto: "hls", IP: "1.1.1.1", UserAgent: "p", Token: token}
	}

	first, err := a.Begin(ctx, cfg, req("t1"), false)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "node B to learn about the session on node A", func() bool { return shB.Remote("ana") == 1 })
	_, err = bb.Begin(ctx, cfg, req("t2"), false)
	denied(t, err, ReasonUserMaxSessions)

	// The session on A ends; B stops counting it after A's next report.
	if !a.Kill(first.ID()) {
		t.Fatal("kill")
	}
	eventually(t, "node B to see the session gone", func() bool { return shB.Remote("ana") == 0 })
	if _, err := bb.Begin(ctx, cfg, req("t3"), false); err != nil {
		t.Fatalf("limit must free up: %v", err)
	}
}

func TestShareOnlyLimitedUsersAndOwnMessages(t *testing.T) {
	backend := limitedBackend(t)
	ctx := context.Background()
	cfg := model.AuthConfig{Enabled: true, BackendURL: backend.URL + "/auth"}
	tr := &Tracker{WorkerID: "node-a", Auth: &Authorizer{}}
	req := func(token string) Request {
		return Request{ChannelID: "c1", Proto: "hls", IP: "1.1.1.1", UserAgent: "p", Token: token}
	}
	if _, err := tr.Begin(ctx, cfg, req("t1"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Begin(ctx, cfg, req("free"), false); err != nil { // user without limit
		t.Fatal(err)
	}
	got := tr.LimitedUserCounts(100)
	if len(got) != 1 || got["ana"] != 1 {
		t.Fatalf("only the limited user is shared, got %v", got)
	}

	// A node ignores its own broadcasts (it already counts those locally).
	sh := &Share{Tracker: tr}
	own, _ := json.Marshal(userCounts{WorkerID: "node-a", Users: map[string]int{"ana": 5}})
	sh.receive("", own)
	sh.receive("", []byte("not json"))
	if n := sh.Remote("ana"); n != 0 {
		t.Fatalf("own or malformed message counted: %d", n)
	}
}

// A node that stops reporting (crash, partition) stops counting: its users
// must not stay blocked.
func TestSharedCountsExpire(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	sh := &Share{Tracker: &Tracker{WorkerID: "node-a"}, TTL: 15 * time.Second, Now: func() time.Time { return now }}
	msg, _ := json.Marshal(userCounts{WorkerID: "node-b", Users: map[string]int{"ana": 2}})
	sh.receive("", msg)
	if n := sh.Remote("ana"); n != 2 {
		t.Fatalf("remote %d", n)
	}
	now = now.Add(14 * time.Second)
	if n := sh.Remote("ana"); n != 2 {
		t.Fatalf("still fresh: %d", n)
	}
	now = now.Add(2 * time.Second)
	if n := sh.Remote("ana"); n != 0 {
		t.Fatalf("stale report still counted: %d", n)
	}
}

func TestSharedCountsAreCapped(t *testing.T) {
	tr := &Tracker{WorkerID: "n"}
	tr.init()
	for i := 0; i < 10; i++ {
		id := string(rune('a' + i))
		tr.open[id] = &entry{s: model.PlaySession{UserID: "user-" + id}, userMax: 1}
	}
	if got := tr.LimitedUserCounts(4); len(got) != 4 {
		t.Fatalf("cap not applied: %d users", len(got))
	}
}
