package model

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Event types (docs/api.md section 11.2).
const (
	EventChannelStateChanged      = "channel.state_changed"
	EventChannelSourceSwitched    = "channel.source_switched"
	EventChannelPipelineRestart   = "channel.pipeline_restarted"
	EventChannelInputChanged      = "channel.input_changed"
	EventWorkerOnline             = "worker.online"
	EventWorkerOffline            = "worker.offline"
	EventWorkerChannelsReassigned = "worker.channels_reassigned"
	EventDVRStorageWarning        = "dvr.storage_warning"
	EventDVRIntegrityError        = "dvr.integrity_error"
	EventEncoderFailed            = "worker.encoder_failed"
)

// Event is something that happened to a channel or worker. Events are
// persisted (JetStream, then PostgreSQL) and streamed to the panel.
type Event struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	At        time.Time      `json:"at"`
	ChannelID string         `json:"channel_id,omitempty"`
	WorkerID  string         `json:"worker_id,omitempty"`
	From      ChannelState   `json:"from,omitempty"`
	To        ChannelState   `json:"to,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

// NewEventID returns a random identifier used to deduplicate redeliveries.
func NewEventID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// NewEvent creates an event with a fresh ID.
func NewEvent(typ string, at time.Time) Event {
	return Event{ID: NewEventID(), Type: typ, At: at.UTC()}
}
