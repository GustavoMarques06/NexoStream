package sessions

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"nexostream/internal/bus"
)

// Per-user session limits (X-Max-Sessions) must hold across the whole cluster:
// a user cannot watch on two Media Nodes at once to get around the limit.
// Nodes tell each other how many open sessions each limited user has, over the
// bus. The exchange is best effort and keeps every node independent:
//
//   - counts are published every Interval, so a user can briefly exceed the
//     limit by opening sessions on two nodes within one interval;
//   - counts from a node that stopped talking expire after TTL, so a network
//     partition or a dead node never blocks viewers;
//   - only users with a limit are shared, at most MaxUsers per message.

// Default timing of the exchange.
const (
	DefaultShareInterval = 5 * time.Second
	DefaultShareTTL      = 15 * time.Second
	MaxSharedUsers       = 5000
)

// userCounts is the message each node publishes.
type userCounts struct {
	WorkerID string         `json:"worker_id"`
	Users    map[string]int `json:"users"`
}

type peerEntry struct {
	at    time.Time
	users map[string]int
}

// Share exchanges per-user session counts with the other nodes.
type Share struct {
	Tracker  *Tracker
	Bus      bus.Bus
	Subject  string
	Interval time.Duration
	TTL      time.Duration
	Now      func() time.Time

	mu    sync.Mutex
	peers map[string]peerEntry
}

func (s *Share) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Share) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return DefaultShareTTL
}

// Remote returns the open sessions of a user on the other nodes, ignoring
// nodes that have not reported within the TTL.
func (s *Share) Remote(user string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	n := 0
	for _, p := range s.peers {
		if now.Sub(p.at) <= s.ttl() {
			n += p.users[user]
		}
	}
	return n
}

func (s *Share) receive(_ string, data []byte) {
	var m userCounts
	if json.Unmarshal(data, &m) != nil || m.WorkerID == "" || m.WorkerID == s.Tracker.WorkerID {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.peers == nil {
		s.peers = map[string]peerEntry{}
	}
	now := s.now()
	s.peers[m.WorkerID] = peerEntry{at: now, users: m.Users}
	for id, p := range s.peers { // forget nodes gone for a long time
		if now.Sub(p.at) > 4*s.ttl() {
			delete(s.peers, id)
		}
	}
}

// publish sends the local counts; an empty message is sent once after the
// last limited user left, so the other nodes stop counting them.
func (s *Share) publish(wasEmpty *bool) {
	users := s.Tracker.LimitedUserCounts(MaxSharedUsers)
	if len(users) == 0 && *wasEmpty {
		return
	}
	*wasEmpty = len(users) == 0
	b, _ := json.Marshal(userCounts{WorkerID: s.Tracker.WorkerID, Users: users})
	_ = s.Bus.Publish(s.Subject, b)
}

// Run connects the tracker to the other nodes until ctx ends.
func (s *Share) Run(ctx context.Context) error {
	sub, err := s.Bus.Subscribe(s.Subject, s.receive)
	if err != nil {
		return err
	}
	defer func() { _ = sub.Unsubscribe() }()
	s.Tracker.SetRemote(s.Remote)
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultShareInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	wasEmpty := true
	for {
		select {
		case <-ctx.Done():
			s.Tracker.SetRemote(nil)
			return nil
		case <-t.C:
			s.publish(&wasEmpty)
		}
	}
}
