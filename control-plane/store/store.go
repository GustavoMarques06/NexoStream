// Package store defines the persistence contract of the Control Plane.
// PostgreSQL is the production implementation; memory is used by the
// all-in-one development mode and unit tests. Both pass the same
// conformance suite (storetest).
package store

import (
	"context"
	"errors"
	"time"

	"nexostream/internal/model"
)

// Errors returned by every implementation.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
	ErrInUse    = errors.New("in use")
)

// Page asks for a page of results ordered by ID (cursor = last ID seen).
type Page struct {
	After string
	Limit int
}

// ChannelFilter filters channel listings.
type ChannelFilter struct {
	Page
	Group   string
	Enabled *bool
	Search  string // substring of id or name
}

// EventFilter filters event listings (newest first; cursor = event seq).
type EventFilter struct {
	ChannelID string
	WorkerID  string
	Type      string
	From      time.Time
	To        time.Time
	Before    int64 // seq cursor
	Limit     int
}

// SessionFilter filters the play session history (newest first).
type SessionFilter struct {
	ChannelID string
	UserID    string
	IP        string
	From, To  time.Time // closed between
	Before    string    // cursor: "<closed_at unix nano>|<id>"
	Limit     int
}

// AuditFilter filters audit listings (newest first).
type AuditFilter struct {
	Actor  string
	Action string
	Target string
	From   time.Time
	To     time.Time
	Before int64
	Limit  int
}

// StoredEvent is an event with its storage sequence (cursor).
type StoredEvent struct {
	Seq int64 `json:"-"`
	model.Event
}

// The persistence contract is split by subject, so a component can depend on
// only what it uses (a test double or a narrow function parameter needs a few
// methods, not forty). Store is their composition and what the two
// implementations (memory and PostgreSQL) provide.

// ChannelStore is the channels part of the store.
type ChannelStore interface {
	// Channels. Create/Update set Revision, CreatedAt and UpdatedAt.
	ListChannels(ctx context.Context, f ChannelFilter) ([]model.Channel, error)
	GetChannel(ctx context.Context, id string) (model.Channel, error)
	CreateChannel(ctx context.Context, ch *model.Channel) error
	UpdateChannel(ctx context.Context, ch *model.Channel) error
	DeleteChannel(ctx context.Context, id string) error
	CountChannels(ctx context.Context) (total, enabled int, err error)
}

// ProfileStore is the transcode profiles part of the store.
type ProfileStore interface {
	// Transcode profiles.
	ListProfiles(ctx context.Context) ([]model.TranscodeProfile, error)
	GetProfile(ctx context.Context, id string) (model.TranscodeProfile, error)
	CreateProfile(ctx context.Context, p model.TranscodeProfile) error
	UpdateProfile(ctx context.Context, p model.TranscodeProfile) error
	DeleteProfile(ctx context.Context, id string) error // ErrInUse when referenced
}

// WorkerStore is the workers and their channel assignments part of the store.
type WorkerStore interface {
	// Workers and assignments.
	UpsertWorker(ctx context.Context, w model.Worker) error
	ListWorkers(ctx context.Context) ([]model.Worker, error)
	GetWorker(ctx context.Context, id string) (model.Worker, error)
	SetWorkerDraining(ctx context.Context, id string, draining bool) error
	SetWorkerState(ctx context.Context, id string, state model.WorkerState) error
	ListAssignments(ctx context.Context) ([]model.Assignment, error)
	SetAssignment(ctx context.Context, a model.Assignment) error
	DeleteAssignment(ctx context.Context, channelID string) error
}

// EventStore is the events part of the store.
type EventStore interface {
	// Events (idempotent on Event.ID).
	InsertEvent(ctx context.Context, ev model.Event) error
	ListEvents(ctx context.Context, f EventFilter) ([]StoredEvent, error)
}

// UserStore is the users and integration tokens part of the store.
type UserStore interface {
	// Users and tokens.
	CreateUser(ctx context.Context, u model.User) error
	UpdateUser(ctx context.Context, u model.User) error
	GetUser(ctx context.Context, id string) (model.User, error)
	GetUserByUsername(ctx context.Context, username string) (model.User, error)
	ListUsers(ctx context.Context) ([]model.User, error)
	CountUsers(ctx context.Context) (int, error)
	CreateAPIToken(ctx context.Context, t model.APIToken) error
	GetAPITokenByHash(ctx context.Context, hash string) (model.APIToken, error)
	ListAPITokens(ctx context.Context) ([]model.APIToken, error)
	RevokeAPIToken(ctx context.Context, id string, at time.Time) error
	TouchAPIToken(ctx context.Context, id string, at time.Time) error
}

// RefreshTokenStore is the rotating refresh tokens part of the store.
type RefreshTokenStore interface {
	CreateRefreshToken(ctx context.Context, t model.RefreshToken) error
	// UseRefreshToken marks the token as used. The first use returns
	// reused=false; any later use returns reused=true (the token was stolen or
	// replayed). ErrNotFound when the token is unknown or was revoked.
	UseRefreshToken(ctx context.Context, id string, at time.Time) (t model.RefreshToken, reused bool, err error)
	// RevokeRefreshFamily deletes every token of a family (reuse detected).
	RevokeRefreshFamily(ctx context.Context, familyID string) error
	// RevokeUserRefreshTokens deletes every refresh token of a user.
	RevokeUserRefreshTokens(ctx context.Context, userID string) error
	// PruneRefreshTokens deletes tokens that expired before the instant.
	PruneRefreshTokens(ctx context.Context, before time.Time) error
}

// DVRStorageStore is the dvr storages (disks and raid mode) part of the store.
type DVRStorageStore interface {
	// DVR storages (disks and RAID mode).
	ListDVRStorages(ctx context.Context) ([]model.DVRStorage, error)
	GetDVRStorage(ctx context.Context, id string) (model.DVRStorage, error)
	CreateDVRStorage(ctx context.Context, s model.DVRStorage) error
	UpdateDVRStorage(ctx context.Context, s model.DVRStorage) error
	DeleteDVRStorage(ctx context.Context, id string) error // ErrInUse when a channel uses it
}

// SettingsStore is the settings, as json documents by key ("global", "server:<id>") part of the store.
type SettingsStore interface {
	// Settings (JSON documents by key: "global", "server:<id>").
	GetSetting(ctx context.Context, key string) ([]byte, error) // ErrNotFound when absent
	PutSetting(ctx context.Context, key string, value []byte) error
	ListSettings(ctx context.Context, prefix string) (map[string][]byte, error)
}

// EPGStore is the epg programs (adr 0009) part of the store.
type EPGStore interface {
	// EPG programs (ADR 0009). ReplacePrograms replaces the programs of a
	// channel from the earliest start of progs on (older ones are kept for
	// catch-up) and drops programs that ended more than 30 days ago.
	ReplacePrograms(ctx context.Context, channelID string, progs []model.Program) error
	ListPrograms(ctx context.Context, channelID string, from, to time.Time) ([]model.Program, error)
}

// StatsStore is the statistics samples part of the store.
type StatsStore interface {
	// Statistics: one sample per key per minute, written by the leader.
	// ListStats averages the points of each key matching keys (exact) or
	// prefix into buckets of step; TopStats aggregates keys by prefix.
	InsertStats(ctx context.Context, at time.Time, values map[string]float64) error
	ListStats(ctx context.Context, keys []string, prefix string, from, to time.Time, step time.Duration) (map[string][]model.StatPoint, error)
	TopStats(ctx context.Context, prefix string, from, to time.Time, limit int) ([]model.StatTotal, error)
	PruneStats(ctx context.Context, before time.Time) error
}

// SessionStore is the play session history part of the store.
type SessionStore interface {
	// Play session history (closed sessions, kept 30 days). Inserting the
	// same session again is a no-op.
	InsertPlaySessions(ctx context.Context, ss []model.PlaySession) error
	ListPlaySessions(ctx context.Context, f SessionFilter) ([]model.PlaySession, error)
	PrunePlaySessions(ctx context.Context, before time.Time) error
}

// LeaseStore is the controller lease (adr 0010) part of the store.
type LeaseStore interface {
	// Leases elect one active controller among several (ADR 0010).
	// AcquireLease takes or renews name for holder when it is free, expired
	// or already held by holder, and reports whether holder has it now.
	AcquireLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error)
	ReleaseLease(ctx context.Context, name, holder string) error
}

// AuditStore is the audit log part of the store.
type AuditStore interface {
	// Audit log (append-only).
	InsertAudit(ctx context.Context, e model.AuditEntry) error
	ListAudit(ctx context.Context, f AuditFilter) ([]model.AuditEntry, error)
}

// Store is the persistence contract.
type Store interface {
	ChannelStore
	ProfileStore
	WorkerStore
	EventStore
	UserStore
	RefreshTokenStore
	DVRStorageStore
	SettingsStore
	EPGStore
	StatsStore
	SessionStore
	LeaseStore
	AuditStore

	Ping(ctx context.Context) error
	Close()
}

// ClampLimit applies the API pagination rules (default 50, max 200).
func ClampLimit(n int) int {
	switch {
	case n <= 0:
		return 50
	case n > 200:
		return 200
	}
	return n
}
