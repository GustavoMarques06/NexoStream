package sessions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"nexostream/internal/model"
)

// Defaults of the tracker.
const (
	DefaultIdle = 30 * time.Second // HLS players ask every few seconds
	BanTime     = 2 * time.Minute  // a closed session cannot come back at once
	keepClosed  = 50               // closed sessions remembered per channel
)

type entry struct {
	s         model.PlaySession
	key       string
	authUntil time.Time
	userMax   int // the user's session limit from the auth backend (0 = none)
	bytes     atomic.Int64
	requests  atomic.Int64
	conns     atomic.Int32 // open streaming connections (MPEG-TS)
	ctx       context.Context
	cancel    context.CancelFunc // ends the session's requests (closed by an operator)
}

// Tracker holds the play sessions of one Media Node.
type Tracker struct {
	WorkerID string
	Auth     *Authorizer
	Idle     time.Duration
	Now      func() time.Time
	// OnClose receives every closed session (called with the tracker
	// locked: it must not block).
	OnClose func(model.PlaySession)

	mu     sync.Mutex
	remote func(user string) int // sessions of a user on the other nodes (see Share)
	open   map[string]*entry     // key -> session
	byID   map[string]*entry
	banned map[string]time.Time
	closed map[string][]model.PlaySession
}

func (t *Tracker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Tracker) init() {
	if t.open == nil {
		t.open, t.byID = map[string]*entry{}, map[string]*entry{}
		t.banned, t.closed = map[string]time.Time{}, map[string][]model.PlaySession{}
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Key identifies the session of a request: the token, or IP + User-Agent.
// unique gives every call its own session (MPEG-TS connections).
func Key(req Request, unique bool) string {
	who := "t:" + req.Token
	if req.Token == "" {
		who = "a:" + req.IP + "|" + req.UserAgent
	}
	if unique {
		who += "|" + newID()
	}
	return req.ChannelID + "|" + req.Proto + "|" + who
}

func shortToken(t string) string {
	if len(t) <= 12 {
		return t
	}
	return t[:6] + "…" + t[len(t)-4:]
}

// Handle is an authorized request inside a session.
type Handle struct {
	t      *Tracker
	e      *entry
	stream bool
	stop   func() bool
	cancel context.CancelFunc
	// Context is the request context, also canceled when the session is
	// closed (a stream being watched stops at once).
	Context context.Context
}

// ID returns the session id.
func (h *Handle) ID() string { return h.e.s.ID }

// Add counts bytes sent to the viewer.
func (h *Handle) Add(n int) { h.e.bytes.Add(int64(n)) }

// Done must be called when the request ends. A streaming connection
// closes its session.
func (h *Handle) Done() {
	h.stop()
	h.cancel()
	if h.stream && h.e.conns.Add(-1) <= 0 {
		h.t.close(h.e, "disconnected")
	}
}

// Begin authorizes a request and attaches it to its session, opening one
// when needed. stream=true is a long connection (MPEG-TS) that is its own
// session and must call Handle.Done.
func (t *Tracker) Begin(ctx context.Context, cfg model.AuthConfig, req Request, stream bool) (*Handle, error) {
	now := t.now()
	if cfg.Enabled {
		if reason := CheckIP(cfg, req.IP); reason != "" {
			return nil, &DeniedError{Reason: reason}
		}
	}
	key := Key(req, stream)
	t.mu.Lock()
	t.init()
	if until, ok := t.banned[key]; ok {
		if now.Before(until) {
			t.mu.Unlock()
			return nil, &DeniedError{Reason: ReasonBanned}
		}
		delete(t.banned, key)
	}
	e := t.open[key]
	needAuth := cfg.Enabled && (e == nil || now.After(e.authUntil))
	id := newID()
	if e != nil {
		id = e.s.ID
	}
	t.mu.Unlock()

	var d Decision
	if needAuth {
		d = t.Auth.Check(ctx, cfg, req, id)
		if !d.Allow {
			if e != nil {
				t.close(e, d.Reason)
			}
			return nil, &DeniedError{Reason: d.Reason}
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if cur := t.open[key]; cur != nil {
		e = cur
	} else {
		e = nil
	}
	if e == nil {
		if cfg.Enabled && cfg.MaxSessions > 0 && t.countLocked(req.ChannelID) >= cfg.MaxSessions {
			return nil, &DeniedError{Reason: ReasonMaxSessions}
		}
		if d.UserID != "" && d.MaxSessions > 0 && t.countUserLocked(d.UserID)+t.remoteUser(d.UserID) >= d.MaxSessions && !d.Unique {
			return nil, &DeniedError{Reason: ReasonUserMaxSessions}
		}
		e = &entry{key: key, s: model.PlaySession{
			ID: id, ChannelID: req.ChannelID, WorkerID: t.WorkerID, Proto: req.Proto, IP: req.IP,
			UserAgent: req.UserAgent, Token: shortToken(req.Token), StartedAt: now.UTC(),
		}}
		e.ctx, e.cancel = context.WithCancel(context.Background())
		t.open[key], t.byID[id] = e, e
	}
	if needAuth {
		e.authUntil = now.Add(d.Duration)
		if d.UserID != "" {
			e.s.UserID = d.UserID
			e.userMax = d.MaxSessions
			if d.Unique {
				for _, o := range t.open {
					if o != e && o.s.UserID == d.UserID {
						t.closeLocked(o, "replaced", now)
					}
				}
			}
		}
	}
	e.s.LastSeen = now.UTC()
	e.requests.Add(1)
	if stream {
		e.conns.Add(1)
	}
	hctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(e.ctx, cancel)
	return &Handle{t: t, e: e, stream: stream, stop: stop, cancel: cancel, Context: hctx}, nil
}

func (t *Tracker) countLocked(ch string) int {
	n := 0
	for _, e := range t.open {
		if e.s.ChannelID == ch {
			n++
		}
	}
	return n
}

// remoteUser returns the sessions of a user on the other nodes. It runs with
// the tracker locked, so the function given to SetRemote must not block.
func (t *Tracker) remoteUser(user string) int {
	if t.remote == nil {
		return 0
	}
	return t.remote(user)
}

// SetRemote sets where the sessions of a user on other nodes are counted
// (nil = this node only).
func (t *Tracker) SetRemote(f func(user string) int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.remote = f
}

// LimitedUserCounts returns the open sessions per user, only for users that
// have a session limit, at most limit users.
func (t *Tracker) LimitedUserCounts(limit int) map[string]int {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string]int{}
	for _, e := range t.open {
		if e.s.UserID == "" || e.userMax <= 0 {
			continue
		}
		if _, ok := out[e.s.UserID]; !ok && len(out) >= limit {
			continue
		}
		out[e.s.UserID]++
	}
	return out
}

func (t *Tracker) countUserLocked(user string) int {
	n := 0
	for _, e := range t.open {
		if e.s.UserID == user {
			n++
		}
	}
	return n
}

func (t *Tracker) close(e *entry, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeLocked(e, reason, t.now())
}

func (t *Tracker) closeLocked(e *entry, reason string, now time.Time) {
	if t.open[e.key] != e {
		return
	}
	delete(t.open, e.key)
	delete(t.byID, e.s.ID)
	if e.cancel != nil {
		e.cancel()
	}
	s := t.snapshot(e)
	s.ClosedAt, s.Reason = now.UTC(), reason
	if t.OnClose != nil {
		t.OnClose(s)
	}
	list := append(t.closed[s.ChannelID], s)
	if len(list) > keepClosed {
		list = list[len(list)-keepClosed:]
	}
	t.closed[s.ChannelID] = list
}

func (t *Tracker) snapshot(e *entry) model.PlaySession {
	s := e.s
	s.Bytes, s.Requests = e.bytes.Load(), e.requests.Load()
	return s
}

// Sweep closes sessions without requests for Idle (streams stay open while
// connected) and forgets expired bans. Call it every few seconds.
func (t *Tracker) Sweep() {
	idle := t.Idle
	if idle <= 0 {
		idle = DefaultIdle
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.init()
	for _, e := range t.open {
		if e.conns.Load() > 0 {
			e.s.LastSeen = now.UTC()
			continue
		}
		if now.Sub(e.s.LastSeen) > idle {
			t.closeLocked(e, "idle", now)
		}
	}
	for k, until := range t.banned {
		if now.After(until) {
			delete(t.banned, k)
		}
	}
}

// Kill closes a session and refuses it for BanTime.
func (t *Tracker) Kill(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.init()
	e, ok := t.byID[id]
	if !ok {
		return false
	}
	now := t.now()
	t.banned[e.key] = now.Add(BanTime)
	t.closeLocked(e, ReasonKilled, now)
	return true
}

// CloseChannel closes every session of a channel (channel stopped or moved).
func (t *Tracker) CloseChannel(ch, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.init()
	now := t.now()
	for _, e := range t.open {
		if e.s.ChannelID == ch {
			t.closeLocked(e, reason, now)
		}
	}
}

// Count returns the open sessions of a channel.
func (t *Tracker) Count(ch string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.init()
	return t.countLocked(ch)
}

// List returns the open sessions of a channel ("" = all), oldest first,
// and the recently closed ones, newest first.
func (t *Tracker) List(ch string) (open, closed []model.PlaySession) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.init()
	open, closed = []model.PlaySession{}, []model.PlaySession{}
	for _, e := range t.open {
		if ch == "" || e.s.ChannelID == ch {
			open = append(open, t.snapshot(e))
		}
	}
	sort.Slice(open, func(i, j int) bool { return open[i].StartedAt.Before(open[j].StartedAt) })
	for c, list := range t.closed {
		if ch == "" || c == ch {
			closed = append(closed, list...)
		}
	}
	sort.Slice(closed, func(i, j int) bool { return closed[i].ClosedAt.After(closed[j].ClosedAt) })
	return open, closed
}
