// Package memory is an in-memory store.Store for development and tests.
// Data is lost when the process stops.
package memory

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nexostream/control-plane/store"
	"nexostream/internal/model"
)

// Store is an in-memory implementation of store.Store.
type Store struct {
	mu          sync.RWMutex
	channels    map[string]model.Channel
	profiles    map[string]model.TranscodeProfile
	workers     map[string]model.Worker
	assignments map[string]model.Assignment
	events      []store.StoredEvent
	eventIDs    map[string]bool
	users       map[string]model.User
	tokens      map[string]model.APIToken
	refresh     map[string]*refreshEntry
	audit       []model.AuditEntry
	storages    map[string]model.DVRStorage
	settings    map[string][]byte
	programs    map[string][]model.Program
	leases      map[string]lease
	played      map[string]model.PlaySession
	stats       []statRow
	statIdx     map[string]int // key|minute -> index in stats
	seq         int64
}

type statRow struct {
	at    time.Time
	key   string
	value float64
}

type lease struct {
	holder  string
	expires time.Time
}

// New returns a store seeded with the default transcode profiles.
func New() *Store {
	s := &Store{
		channels:    map[string]model.Channel{},
		profiles:    map[string]model.TranscodeProfile{},
		workers:     map[string]model.Worker{},
		assignments: map[string]model.Assignment{},
		eventIDs:    map[string]bool{},
		users:       map[string]model.User{},
		tokens:      map[string]model.APIToken{},
		storages:    map[string]model.DVRStorage{},
		settings:    map[string][]byte{},
	}
	for _, p := range model.DefaultProfiles() {
		s.profiles[p.ID] = p
	}
	return s
}

// deep copies through JSON so callers never share slices with the store.
func deep[T any](v T) T {
	b, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *Store) ListChannels(_ context.Context, f store.ChannelFilter) ([]model.Channel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.channels))
	for id := range s.channels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	limit := store.ClampLimit(f.Limit)
	out := []model.Channel{}
	for _, id := range ids {
		if id <= f.After {
			continue
		}
		c := s.channels[id]
		if f.Group != "" && c.Group != f.Group {
			continue
		}
		if f.Enabled != nil && c.Enabled != *f.Enabled {
			continue
		}
		if f.Search != "" && !strings.Contains(strings.ToLower(c.ID+" "+c.Name), strings.ToLower(f.Search)) {
			continue
		}
		out = append(out, deep(c))
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (s *Store) GetChannel(_ context.Context, id string) (model.Channel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.channels[id]
	if !ok {
		return model.Channel{}, store.ErrNotFound
	}
	return deep(c), nil
}

func (s *Store) CreateChannel(_ context.Context, ch *model.Channel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.channels[ch.ID]; ok {
		return store.ErrConflict
	}
	if ch.DVR.Storage != "" {
		if _, ok := s.storages[ch.DVR.Storage]; !ok {
			return store.ErrNotFound
		}
	}
	now := time.Now().UTC()
	ch.CreatedAt, ch.UpdatedAt, ch.Revision = now, now, 1
	s.channels[ch.ID] = deep(*ch)
	return nil
}

func (s *Store) UpdateChannel(_ context.Context, ch *model.Channel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.channels[ch.ID]
	if !ok {
		return store.ErrNotFound
	}
	ch.CreatedAt = old.CreatedAt
	ch.UpdatedAt = time.Now().UTC()
	ch.Revision = old.Revision + 1
	s.channels[ch.ID] = deep(*ch)
	return nil
}

func (s *Store) DeleteChannel(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.channels[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.channels, id)
	delete(s.assignments, id)
	delete(s.programs, id)
	return nil
}

func (s *Store) CountChannels(_ context.Context) (int, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	en := 0
	for _, c := range s.channels {
		if c.Enabled {
			en++
		}
	}
	return len(s.channels), en, nil
}

func (s *Store) ListProfiles(_ context.Context) ([]model.TranscodeProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.TranscodeProfile, 0, len(s.profiles))
	for _, p := range s.profiles {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) GetProfile(_ context.Context, id string) (model.TranscodeProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.profiles[id]
	if !ok {
		return p, store.ErrNotFound
	}
	return p, nil
}

func (s *Store) CreateProfile(_ context.Context, p model.TranscodeProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.profiles[p.ID]; ok {
		return store.ErrConflict
	}
	s.profiles[p.ID] = p
	return nil
}

func (s *Store) UpdateProfile(_ context.Context, p model.TranscodeProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.profiles[p.ID]; !ok {
		return store.ErrNotFound
	}
	s.profiles[p.ID] = p
	return nil
}

func (s *Store) DeleteProfile(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.profiles[id]; !ok {
		return store.ErrNotFound
	}
	for _, c := range s.channels {
		for _, p := range c.Profiles {
			if p == id {
				return store.ErrInUse
			}
		}
	}
	delete(s.profiles, id)
	return nil
}

func (s *Store) UpsertWorker(_ context.Context, w model.Worker) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.workers[w.ID]; ok {
		w.Draining = old.Draining
	}
	if w.Draining && w.State == model.WorkerOnline {
		w.State = model.WorkerDraining
	}
	s.workers[w.ID] = deep(w)
	return nil
}

func (s *Store) ListWorkers(_ context.Context) ([]model.Worker, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Worker, 0, len(s.workers))
	for _, w := range s.workers {
		out = append(out, deep(w))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) GetWorker(_ context.Context, id string) (model.Worker, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w, ok := s.workers[id]
	if !ok {
		return w, store.ErrNotFound
	}
	return deep(w), nil
}

func (s *Store) SetWorkerDraining(_ context.Context, id string, draining bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.workers[id]
	if !ok {
		return store.ErrNotFound
	}
	w.Draining = draining
	switch {
	case draining && w.State == model.WorkerOnline:
		w.State = model.WorkerDraining
	case !draining && w.State == model.WorkerDraining:
		w.State = model.WorkerOnline
	}
	s.workers[id] = w
	return nil
}

func (s *Store) SetWorkerState(_ context.Context, id string, state model.WorkerState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.workers[id]
	if !ok {
		return store.ErrNotFound
	}
	w.State = state
	s.workers[id] = w
	return nil
}

func (s *Store) ListAssignments(_ context.Context) ([]model.Assignment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Assignment, 0, len(s.assignments))
	for _, a := range s.assignments {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChannelID < out[j].ChannelID })
	return out, nil
}

func (s *Store) SetAssignment(_ context.Context, a model.Assignment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.channels[a.ChannelID]; !ok {
		return store.ErrNotFound
	}
	s.assignments[a.ChannelID] = a
	return nil
}

func (s *Store) DeleteAssignment(_ context.Context, channelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.assignments, channelID)
	return nil
}

const maxEvents = 100_000

func (s *Store) InsertEvent(_ context.Context, ev model.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ev.ID != "" && s.eventIDs[ev.ID] {
		return nil
	}
	s.seq++
	s.eventIDs[ev.ID] = true
	s.events = append(s.events, store.StoredEvent{Seq: s.seq, Event: deep(ev)})
	if len(s.events) > maxEvents {
		for _, e := range s.events[:len(s.events)-maxEvents] {
			delete(s.eventIDs, e.ID)
		}
		s.events = append([]store.StoredEvent(nil), s.events[len(s.events)-maxEvents:]...)
	}
	return nil
}

func (s *Store) ListEvents(_ context.Context, f store.EventFilter) ([]store.StoredEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit := store.ClampLimit(f.Limit)
	out := []store.StoredEvent{}
	for i := len(s.events) - 1; i >= 0 && len(out) < limit; i-- {
		e := s.events[i]
		switch {
		case f.Before > 0 && e.Seq >= f.Before,
			f.ChannelID != "" && e.ChannelID != f.ChannelID,
			f.WorkerID != "" && e.WorkerID != f.WorkerID,
			f.Type != "" && e.Type != f.Type,
			!f.From.IsZero() && e.At.Before(f.From),
			!f.To.IsZero() && e.At.After(f.To):
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

type refreshEntry struct {
	t    model.RefreshToken
	used bool
}

func (s *Store) CreateRefreshToken(_ context.Context, t model.RefreshToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refresh == nil {
		s.refresh = map[string]*refreshEntry{}
	}
	if _, ok := s.refresh[t.ID]; ok {
		return store.ErrConflict
	}
	s.refresh[t.ID] = &refreshEntry{t: t}
	return nil
}

func (s *Store) UseRefreshToken(_ context.Context, id string, _ time.Time) (model.RefreshToken, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.refresh[id]
	if !ok {
		return model.RefreshToken{}, false, store.ErrNotFound
	}
	reused := e.used
	e.used = true
	return e.t, reused, nil
}

func (s *Store) RevokeRefreshFamily(_ context.Context, familyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.refresh {
		if e.t.FamilyID == familyID {
			delete(s.refresh, id)
		}
	}
	return nil
}

func (s *Store) RevokeUserRefreshTokens(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.refresh {
		if e.t.UserID == userID {
			delete(s.refresh, id)
		}
	}
	return nil
}

func (s *Store) PruneRefreshTokens(_ context.Context, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.refresh {
		if e.t.ExpiresAt.Before(before) {
			delete(s.refresh, id)
		}
	}
	return nil
}

func (s *Store) CreateUser(_ context.Context, u model.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.users {
		if x.Username == u.Username {
			return store.ErrConflict
		}
	}
	if _, ok := s.users[u.ID]; ok {
		return store.ErrConflict
	}
	s.users[u.ID] = u
	return nil
}

func (s *Store) UpdateUser(_ context.Context, u model.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[u.ID]; !ok {
		return store.ErrNotFound
	}
	s.users[u.ID] = u
	return nil
}

func (s *Store) GetUser(_ context.Context, id string) (model.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	if !ok {
		return u, store.ErrNotFound
	}
	return u, nil
}

func (s *Store) GetUserByUsername(_ context.Context, username string) (model.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u.Username == username {
			return u, nil
		}
	}
	return model.User{}, store.ErrNotFound
}

func (s *Store) ListUsers(_ context.Context) ([]model.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.User, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out, nil
}

func (s *Store) CountUsers(_ context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users), nil
}

func (s *Store) CreateAPIToken(_ context.Context, t model.APIToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tokens[t.ID]; ok {
		return store.ErrConflict
	}
	s.tokens[t.ID] = deep(t)
	s.tokens[t.ID] = withHash(s.tokens[t.ID], t.Hash)
	return nil
}

func withHash(t model.APIToken, h string) model.APIToken { t.Hash = h; return t }

func (s *Store) GetAPITokenByHash(_ context.Context, hash string) (model.APIToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.tokens {
		if t.Hash == hash {
			return t, nil
		}
	}
	return model.APIToken{}, store.ErrNotFound
}

func (s *Store) ListAPITokens(_ context.Context) ([]model.APIToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.APIToken, 0, len(s.tokens))
	for _, t := range s.tokens {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) RevokeAPIToken(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[id]
	if !ok {
		return store.ErrNotFound
	}
	if t.RevokedAt == nil {
		t.RevokedAt = &at
	}
	s.tokens[id] = t
	return nil
}

func (s *Store) TouchAPIToken(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[id]
	if !ok {
		return store.ErrNotFound
	}
	t.LastUsedAt = &at
	s.tokens[id] = t
	return nil
}

func (s *Store) ListDVRStorages(_ context.Context) ([]model.DVRStorage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.DVRStorage, 0, len(s.storages))
	for _, v := range s.storages {
		out = append(out, deep(v))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) GetDVRStorage(_ context.Context, id string) (model.DVRStorage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.storages[id]
	if !ok {
		return v, store.ErrNotFound
	}
	return deep(v), nil
}

func (s *Store) CreateDVRStorage(_ context.Context, v model.DVRStorage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.storages[v.ID]; ok {
		return store.ErrConflict
	}
	now := time.Now().UTC()
	v.CreatedAt, v.UpdatedAt = now, now
	s.storages[v.ID] = deep(v)
	return nil
}

func (s *Store) UpdateDVRStorage(_ context.Context, v model.DVRStorage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.storages[v.ID]
	if !ok {
		return store.ErrNotFound
	}
	v.CreatedAt, v.UpdatedAt = old.CreatedAt, time.Now().UTC()
	s.storages[v.ID] = deep(v)
	return nil
}

func (s *Store) DeleteDVRStorage(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.storages[id]; !ok {
		return store.ErrNotFound
	}
	for _, c := range s.channels {
		if c.DVR.Storage == id {
			return store.ErrInUse
		}
	}
	delete(s.storages, id)
	return nil
}

func (s *Store) GetSetting(_ context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.settings[key]
	if !ok {
		return nil, store.ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

func (s *Store) PutSetting(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings[key] = append([]byte(nil), value...)
	return nil
}

func (s *Store) ListSettings(_ context.Context, prefix string) (map[string][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string][]byte{}
	for k, v := range s.settings {
		if strings.HasPrefix(k, prefix) {
			out[k] = append([]byte(nil), v...)
		}
	}
	return out, nil
}

func (s *Store) InsertAudit(_ context.Context, e model.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	e.ID = s.seq
	s.audit = append(s.audit, deep(e))
	return nil
}

func (s *Store) ListAudit(_ context.Context, f store.AuditFilter) ([]model.AuditEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit := store.ClampLimit(f.Limit)
	out := []model.AuditEntry{}
	for i := len(s.audit) - 1; i >= 0 && len(out) < limit; i-- {
		e := s.audit[i]
		switch {
		case f.Before > 0 && e.ID >= f.Before,
			f.Actor != "" && e.Actor != f.Actor,
			f.Action != "" && e.Action != f.Action,
			f.Target != "" && e.Target != f.Target,
			!f.From.IsZero() && e.At.Before(f.From),
			!f.To.IsZero() && e.At.After(f.To):
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *Store) Ping(context.Context) error { return nil }
func (s *Store) Close()                     {}

var _ store.Store = (*Store)(nil)

// ---- EPG ----

func (s *Store) ReplacePrograms(_ context.Context, channelID string, progs []model.Program) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.programs == nil {
		s.programs = map[string][]model.Program{}
	}
	var from time.Time
	for _, p := range progs {
		if from.IsZero() || p.Start.Before(from) {
			from = p.Start
		}
	}
	limit := time.Now().Add(-30 * 24 * time.Hour)
	keep := []model.Program{}
	for _, p := range s.programs[channelID] {
		if (from.IsZero() || p.Start.Before(from)) && p.Stop.After(limit) {
			keep = append(keep, p)
		}
	}
	for _, p := range progs {
		p.ChannelID = channelID
		p.Start, p.Stop = p.Start.UTC(), p.Stop.UTC()
		keep = append(keep, p)
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].Start.Before(keep[j].Start) })
	s.programs[channelID] = keep
	return nil
}

func (s *Store) ListPrograms(_ context.Context, channelID string, from, to time.Time) ([]model.Program, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []model.Program{}
	for _, p := range s.programs[channelID] {
		if p.Stop.After(from) && p.Start.Before(to) {
			out = append(out, p)
		}
	}
	return out, nil
}

// ---- leases ----

func (s *Store) AcquireLease(_ context.Context, name, holder string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leases == nil {
		s.leases = map[string]lease{}
	}
	now := time.Now()
	l, ok := s.leases[name]
	if ok && l.holder != holder && now.Before(l.expires) {
		return false, nil
	}
	s.leases[name] = lease{holder: holder, expires: now.Add(ttl)}
	return true, nil
}

func (s *Store) ReleaseLease(_ context.Context, name, holder string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.leases[name]; ok && l.holder == holder {
		delete(s.leases, name)
	}
	return nil
}

// ---- statistics ----

func statMatch(key string, keys []string, prefix string) bool {
	if prefix != "" && strings.HasPrefix(key, prefix) {
		return true
	}
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}

func (s *Store) InsertStats(_ context.Context, at time.Time, values map[string]float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	at = at.UTC().Truncate(time.Minute)
	if s.statIdx == nil {
		s.statIdx = map[string]int{}
	}
	for k, v := range values {
		id := k + "|" + strconv.FormatInt(at.Unix(), 10)
		if i, ok := s.statIdx[id]; ok && i < len(s.stats) && s.stats[i].key == k && s.stats[i].at.Equal(at) {
			s.stats[i].value = v // same minute: the last value wins
			continue
		}
		s.statIdx[id] = len(s.stats)
		s.stats = append(s.stats, statRow{at: at, key: k, value: v})
	}
	return nil
}

func (s *Store) ListStats(_ context.Context, keys []string, prefix string, from, to time.Time, step time.Duration) (map[string][]model.StatPoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if step < time.Minute {
		step = time.Minute
	}
	type acc struct{ sum, n float64 }
	buckets := map[string]map[int64]*acc{}
	for _, r := range s.stats {
		if r.at.Before(from) || !r.at.Before(to) || !statMatch(r.key, keys, prefix) {
			continue
		}
		b := r.at.Unix() / int64(step/time.Second)
		if buckets[r.key] == nil {
			buckets[r.key] = map[int64]*acc{}
		}
		a := buckets[r.key][b]
		if a == nil {
			a = &acc{}
			buckets[r.key][b] = a
		}
		a.sum += r.value
		a.n++
	}
	out := map[string][]model.StatPoint{}
	for k, bs := range buckets {
		pts := make([]model.StatPoint, 0, len(bs))
		for b, a := range bs {
			pts = append(pts, model.StatPoint{At: time.Unix(b*int64(step/time.Second), 0).UTC(), Value: a.sum / a.n})
		}
		sort.Slice(pts, func(i, j int) bool { return pts[i].At.Before(pts[j].At) })
		out[k] = pts
	}
	return out, nil
}

func (s *Store) TopStats(_ context.Context, prefix string, from, to time.Time, limit int) ([]model.StatTotal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := map[string]*model.StatTotal{}
	for _, r := range s.stats {
		if r.at.Before(from) || !r.at.Before(to) || !strings.HasPrefix(r.key, prefix) {
			continue
		}
		t := m[r.key]
		if t == nil {
			t = &model.StatTotal{Key: r.key}
			m[r.key] = t
		}
		t.Sum += r.value
		t.Count++
		if r.value > t.Max {
			t.Max = r.value
		}
	}
	out := make([]model.StatTotal, 0, len(m))
	for _, t := range m {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sum != out[j].Sum {
			return out[i].Sum > out[j].Sum
		}
		return out[i].Key < out[j].Key
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) PruneStats(_ context.Context, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := s.stats[:0]
	s.statIdx = map[string]int{}
	for _, r := range s.stats {
		if !r.at.Before(before) {
			s.statIdx[r.key+"|"+strconv.FormatInt(r.at.Unix(), 10)] = len(keep)
			keep = append(keep, r)
		}
	}
	s.stats = keep
	return nil
}

// ---- play session history ----

func sessionCursor(s model.PlaySession) string {
	return strconv.FormatInt(s.ClosedAt.UnixNano(), 10) + "|" + s.ID
}

func (s *Store) InsertPlaySessions(_ context.Context, ss []model.PlaySession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.played == nil {
		s.played = map[string]model.PlaySession{}
	}
	for _, p := range ss {
		if _, ok := s.played[p.ID]; !ok && p.ID != "" {
			s.played[p.ID] = p
		}
	}
	return nil
}

func (s *Store) ListPlaySessions(_ context.Context, f store.SessionFilter) ([]model.PlaySession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []model.PlaySession{}
	for _, p := range s.played {
		switch {
		case f.ChannelID != "" && p.ChannelID != f.ChannelID,
			f.UserID != "" && p.UserID != f.UserID,
			f.IP != "" && p.IP != f.IP,
			!f.From.IsZero() && p.ClosedAt.Before(f.From),
			!f.To.IsZero() && !p.ClosedAt.Before(f.To):
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ClosedAt.Equal(out[j].ClosedAt) {
			return out[i].ClosedAt.After(out[j].ClosedAt)
		}
		return out[i].ID > out[j].ID
	})
	if f.Before != "" {
		for i, p := range out {
			if sessionCursor(p) == f.Before {
				out = out[i+1:]
				break
			}
		}
	}
	if l := store.ClampLimit(f.Limit); len(out) > l {
		out = out[:l]
	}
	return out, nil
}

func (s *Store) PrunePlaySessions(_ context.Context, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, p := range s.played {
		if p.ClosedAt.Before(before) {
			delete(s.played, id)
		}
	}
	return nil
}
