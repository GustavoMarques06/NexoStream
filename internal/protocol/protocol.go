// Package protocol defines the messages exchanged over NATS between the
// Control Plane and the worker agents (docs/architecture.md section 4.1).
package protocol

import (
	"time"

	"nexostream/internal/model"
)

// Subject helpers.
func WorkerHeartbeat(workerID string) string { return "nexostream.worker." + workerID + ".heartbeat" }
func WorkerCommand(workerID string) string   { return "nexostream.worker." + workerID + ".cmd" }
func WorkerEvent(workerID string) string     { return "nexostream.worker." + workerID + ".event" }
func ChannelStatus(channelID string) string  { return "nexostream.channel." + channelID + ".status" }
func ChannelEvent(channelID string) string   { return "nexostream.channel." + channelID + ".event" }

// Wildcard subjects.
const (
	AllHeartbeats    = "nexostream.worker.*.heartbeat"
	AllChannelStatus = "nexostream.channel.*.status"
	AllChannelEvents = "nexostream.channel.*.event"
	AllWorkerEvents  = "nexostream.worker.*.event"
	ReconcileSubject = "nexostream.control.reconcile"
	// SessionsClosed carries batches of closed play sessions ([]model.PlaySession),
	// best effort, stored by the controllers for the session history.
	SessionsClosed = "nexostream.sessions.closed"
	// SessionUsers carries the open sessions per limited user that each Media
	// Node reports to the others, so per-user limits hold across the cluster.
	SessionUsers      = "nexostream.sessions.users"
	EventsStream      = "NEXOSTREAM_EVENTS"
	EventsConsumer    = "nexostream-controller-events"
	HeartbeatInterval = 5 * time.Second
	// MissedHeartbeats is how many heartbeats may be lost before a worker is
	// considered offline (3 x 5 s = 15 s, spec section 9.1).
	MissedHeartbeats = 3
)

// EventSubjects are channel and worker events (persisted, also streamed to the panel).
var EventSubjects = []string{AllChannelEvents, AllWorkerEvents}

// Command types.
const (
	CmdStart        = "start"
	CmdStop         = "stop"
	CmdRestart      = "restart"
	CmdSwitchSource = "switch-source"
	CmdProbe        = "probe"
	CmdDVRRanges    = "dvr-ranges"
	CmdConfigure    = "configure"
	CmdSessions     = "sessions"
	CmdKillSession  = "kill-session"
)

// TimeRange is a continuous recorded interval.
type TimeRange struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Command is sent by the Control Plane to a worker (request/reply).
type Command struct {
	Type      string        `json:"type"`
	ChannelID string        `json:"channel_id,omitempty"`
	Spec      *ChannelSpec  `json:"spec,omitempty"`
	Priority  int           `json:"priority,omitempty"`
	Reason    string        `json:"reason,omitempty"`
	Probe     *ProbeRequest `json:"probe,omitempty"`
	Actor     string        `json:"actor,omitempty"`
	From      time.Time     `json:"from,omitempty"`
	To        time.Time     `json:"to,omitempty"`
	// Runtime answers configure: settings managed in the panel.
	Runtime *model.WorkerRuntime `json:"runtime,omitempty"`
	// SessionID is the play session closed by kill-session.
	SessionID string `json:"session_id,omitempty"`
}

// ChannelSpec is everything a worker needs to run a channel without access
// to the database: the channel plus the resolved transcode profiles.
type ChannelSpec struct {
	Channel  model.Channel            `json:"channel"`
	Profiles []model.TranscodeProfile `json:"profiles"`
	// DVRStorage is the resolved storage named by Channel.DVR.Storage.
	DVRStorage *model.DVRStorage `json:"dvr_storage,omitempty"`
}

// ProbeRequest asks a worker to analyze a source URL.
type ProbeRequest struct {
	URL       string  `json:"url"`
	Interface string  `json:"interface,omitempty"`
	Seconds   float64 `json:"seconds,omitempty"`
}

// Reply answers a Command.
type Reply struct {
	OK    bool               `json:"ok"`
	Error string             `json:"error,omitempty"`
	Probe *model.ProbeResult `json:"probe,omitempty"`
	// DVR answers dvr-ranges.
	DVR *DVRInfo `json:"dvr,omitempty"`
	// Sessions answers sessions.
	Sessions *SessionsInfo `json:"sessions,omitempty"`
}

// SessionsInfo lists the play sessions of a channel on a worker.
type SessionsInfo struct {
	Open   []model.PlaySession `json:"open"`
	Closed []model.PlaySession `json:"closed"`
}

// DVRInfo describes the recordings of a channel on a worker.
type DVRInfo struct {
	Ranges   []TimeRange `json:"ranges"`
	Segments int         `json:"segments"`
	Bytes    int64       `json:"bytes"`
	URL      string      `json:"url,omitempty"` // playback base: <url>index.m3u8?from=&to=
}

// Version is the revision of the controller-worker contract (subjects,
// commands, heartbeat and status). Raise it when a change is not compatible
// with a peer running the previous one; the controller warns about workers
// that report a different value, so a mixed-version cluster is visible
// instead of failing silently.
const Version = 1

// Heartbeat is published by every worker each HeartbeatInterval.
type Heartbeat struct {
	WorkerID     string             `json:"worker_id"`
	Version      string             `json:"version"`
	Protocol     int                `json:"protocol"` // contract revision (protocol.Version); 0 = older worker
	StartedAt    time.Time          `json:"started_at"`
	At           time.Time          `json:"at"`
	Capabilities model.Capabilities `json:"capabilities"`
	Resources    model.Resources    `json:"resources"`
	Channels     []ChannelBrief     `json:"channels"`
	// RuntimeHash identifies the panel-managed settings in use ("" = none yet).
	RuntimeHash string `json:"runtime_hash"`
}

// ChannelBrief is a compact view of a channel running on a worker.
type ChannelBrief struct {
	ID       string             `json:"id"`
	Revision int64              `json:"revision"`
	State    model.ChannelState `json:"state"`
}
