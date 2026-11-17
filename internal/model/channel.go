// Package model holds the domain types shared by the Control Plane and the
// Data Plane. Field names follow the public API (snake_case JSON).
package model

import (
	"regexp"
	"sort"
	"time"
)

// ChannelState is the lifecycle state of a channel (spec section 8.4).
type ChannelState string

const (
	StateStopped    ChannelState = "STOPPED"
	StateStarting   ChannelState = "STARTING"
	StateRunning    ChannelState = "RUNNING"
	StateDegraded   ChannelState = "DEGRADED"
	StateSwitching  ChannelState = "SWITCHING"
	StateOnBackup   ChannelState = "ON_BACKUP"
	StateRecovering ChannelState = "RECOVERING"
	StateFailed     ChannelState = "FAILED"
)

// AllStates lists every channel state, in display order.
var AllStates = []ChannelState{
	StateStopped, StateStarting, StateRunning, StateDegraded,
	StateSwitching, StateOnBackup, StateRecovering, StateFailed,
}

// Valid reports whether s is a known state.
func (s ChannelState) Valid() bool {
	for _, v := range AllStates {
		if v == s {
			return true
		}
	}
	return false
}

// Up reports whether the channel is delivering output (metric channel_up).
func (s ChannelState) Up() bool {
	return s == StateRunning || s == StateOnBackup || s == StateRecovering
}

// ProcessingMode selects how the media is processed.
type ProcessingMode string

const (
	ModeAuto        ProcessingMode = "auto"
	ModePassthrough ProcessingMode = "passthrough"
	ModeTranscode   ProcessingMode = "transcode"
)

// Valid reports whether m is a known mode.
func (m ProcessingMode) Valid() bool {
	return m == ModeAuto || m == ModePassthrough || m == ModeTranscode
}

// Channel is the desired configuration of a live channel.
type Channel struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Group    string         `json:"group"`
	Enabled  bool           `json:"enabled"`
	Mode     ProcessingMode `json:"mode"`
	Profiles []string       `json:"profiles"`
	Sources  []Source       `json:"sources"`
	Outputs  Outputs        `json:"outputs"`
	DVR      DVRConfig      `json:"dvr"`
	DRM      DRMConfig      `json:"drm"`
	Auth     AuthConfig     `json:"auth"`
	EPG      EPGConfig      `json:"epg"`
	Overlay  OverlayConfig  `json:"overlay"`
	Failover FailoverPolicy `json:"failover"`
	// Revision increases on every configuration change. Workers report the
	// revision they are running so the controller can detect stale pipelines.
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SortedSources returns the sources ordered by priority (1 = primary).
func (c Channel) SortedSources() []Source {
	out := make([]Source, len(c.Sources))
	copy(out, c.Sources)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out
}

// Source is one input of a channel.
type Source struct {
	ID        string `json:"id"`
	Priority  int    `json:"priority"`
	URL       string `json:"url"`
	Interface string `json:"interface,omitempty"`
}

// Outputs lists the enabled outputs of a channel.
type Outputs struct {
	HLS        bool `json:"hls"`
	MPEGTSHTTP bool `json:"mpegts_http"`
	// MPEGTSUDP is an optional destination such as "udp://239.10.0.1:5000".
	MPEGTSUDP string `json:"mpegts_udp,omitempty"`
	// DASH publishes an MPEG-DASH manifest (fMP4) next to the HLS.
	DASH bool `json:"dash"`
	// Push sends the channel to other servers (rtmp://, rtmps://, srt://),
	// each with its own process that reconnects on failure, like Flussonic.
	Push []string `json:"push,omitempty"`
}

// DVRConfig is the recording policy of a channel. Recording happens on the
// Media Node running the channel, in Path or, when empty, in the node's DVR
// folder (NEXOSTREAM_DVR_DIR), like "dvr /storage 3d" in Flussonic.
type DVRConfig struct {
	Enabled        bool   `json:"enabled"`
	RetentionHours int    `json:"retention_hours"`
	Path           string `json:"path,omitempty"`
	// Storage names a DVR storage (several disks, RAID). It takes
	// precedence over Path.
	Storage string `json:"storage,omitempty"`
	// AllQualities records every quality of an ABR channel. Off (the
	// default), only the highest is recorded, which keeps the disk use of a
	// channel independent of its ladder.
	AllQualities bool `json:"all_qualities,omitempty"`
}

// DRMConfig protects the HLS output and the DVR of a channel (ADR 0008).
// With "aes-128", segments are encrypted with AES-128 and the key is only
// given to players presenting a valid playback token.
type DRMConfig struct {
	Enabled bool   `json:"enabled"`
	Method  string `json:"method,omitempty"` // aes-128
	// KeyRotationMinutes is how long one content key is used (default 60).
	KeyRotationMinutes int `json:"key_rotation_minutes,omitempty"`
}

// AuthConfig decides who may play a channel, like Flussonic's on_play
// auth backend (ADR 0009). It is enforced by the Media Node on every new
// play session: deny list, allow list, session limit, then the backend.
type AuthConfig struct {
	Enabled bool `json:"enabled"`
	// BackendURL is called (GET) for each new session; 2xx allows.
	BackendURL string `json:"backend_url,omitempty"`
	// AllowIPs and DenyIPs hold IPs or CIDRs.
	AllowIPs []string `json:"allow_ips,omitempty"`
	DenyIPs  []string `json:"deny_ips,omitempty"`
	// MaxSessions limits open sessions of the channel on a node (0 = no limit).
	MaxSessions int `json:"max_sessions,omitempty"`
	// CacheSeconds is how long a backend answer is reused (default 60).
	CacheSeconds int `json:"cache_seconds,omitempty"`
	// AllowOnError lets viewers in when the backend cannot be reached
	// (default: deny).
	AllowOnError bool `json:"allow_on_error,omitempty"`
}

// Overlay positions.
const (
	OverlayTopLeft     = "top-left"
	OverlayTopRight    = "top-right"
	OverlayBottomLeft  = "bottom-left"
	OverlayBottomRight = "bottom-right"
)

// OverlayConfig burns a logo into the video of a transcoded channel, like a
// broadcaster's watermark. Image is a file name inside the overlay folder of
// the Media Nodes (never a path), so the setting cannot read other files.
type OverlayConfig struct {
	Enabled  bool   `json:"enabled"`
	Image    string `json:"image,omitempty"`
	Position string `json:"position,omitempty"` // top-left, top-right (default), bottom-left, bottom-right
	// Margin is the distance to the edges, in pixels of the source picture.
	Margin int `json:"margin,omitempty"`
	// Width scales the logo to this width in source pixels (0 = as is).
	Width int `json:"width,omitempty"`
	// Opacity goes from 0.05 to 1 (0 = 1, fully opaque).
	Opacity float64 `json:"opacity,omitempty"`
}

// overlayImageRe accepts a plain file name (no folders) of a PNG or JPEG.
var overlayImageRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.(?i:png|jpe?g)$`)

// ValidOverlayImage reports whether name is an acceptable logo file name. It
// is never a path: the Media Node joins it to its own overlay folder.
func ValidOverlayImage(name string) bool { return overlayImageRe.MatchString(name) }

// EPGConfig points to the XMLTV guide of a channel (ADR 0009).
type EPGConfig struct {
	URL string `json:"url,omitempty"`
	// XMLTVID is the channel id inside the XMLTV file (default: channel id).
	XMLTVID string `json:"xmltv_id,omitempty"`
}

// Program is one EPG entry.
type Program struct {
	ChannelID   string    `json:"channel_id"`
	Start       time.Time `json:"start"`
	Stop        time.Time `json:"stop"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Category    string    `json:"category,omitempty"`
}

// StatPoint is one value of a statistics series (Statistics page).
type StatPoint struct {
	At    time.Time `json:"t"`
	Value float64   `json:"v"`
}

// StatTotal aggregates one series over a period.
type StatTotal struct {
	Key   string  `json:"key"`
	Max   float64 `json:"max"`
	Sum   float64 `json:"sum"`
	Count int     `json:"count"`
}

// EPGStatus is the result of the last import of a channel guide.
type EPGStatus struct {
	FetchedAt time.Time `json:"fetched_at"`
	Programs  int       `json:"programs"`
	Error     string    `json:"error,omitempty"`
}

// PlaySession is one viewer of a channel on a Media Node.
type PlaySession struct {
	ID        string    `json:"id"`
	ChannelID string    `json:"channel_id"`
	WorkerID  string    `json:"worker_id"`
	Proto     string    `json:"proto"` // hls | mpegts | dvr
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent,omitempty"`
	UserID    string    `json:"user_id,omitempty"` // from the auth backend
	Token     string    `json:"token,omitempty"`   // shortened
	StartedAt time.Time `json:"started_at"`
	LastSeen  time.Time `json:"last_seen"`
	ClosedAt  time.Time `json:"closed_at,omitzero"`
	Bytes     int64     `json:"bytes"`
	Requests  int64     `json:"requests"`
	Reason    string    `json:"reason,omitempty"` // why it closed
}

// DVRStorage is a set of disks on the Media Nodes used for recording, like
// the disks of a Flussonic DVR. The paths must exist on every node that
// records channels using it (usually mount points).
type DVRStorage struct {
	ID        string    `json:"id"`
	Mode      string    `json:"mode"` // single | raid0 | raid1
	Disks     []string  `json:"disks"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FailoverPolicy holds the detection thresholds of spec section 8.5.
// Zero values are replaced by DefaultFailover() when a channel is saved.
type FailoverPolicy struct {
	NoPacketsSeconds     float64 `json:"no_packets_seconds"`
	ConfirmWindowSeconds float64 `json:"confirm_window_seconds"`
	LowBitrateRatio      float64 `json:"low_bitrate_ratio"`
	LowBitrateSeconds    float64 `json:"low_bitrate_seconds"`
	MissingStreamSeconds float64 `json:"missing_stream_seconds"`
	ReturnToPrimary      *bool   `json:"return_to_primary"`
	PrimaryStableSeconds float64 `json:"primary_stable_seconds"`
}

// DefaultFailover returns the starting values from spec section 8.5.
func DefaultFailover() FailoverPolicy {
	t := true
	return FailoverPolicy{
		NoPacketsSeconds:     3,
		ConfirmWindowSeconds: 5,
		LowBitrateRatio:      0.30,
		LowBitrateSeconds:    10,
		MissingStreamSeconds: 5,
		ReturnToPrimary:      &t,
		PrimaryStableSeconds: 60,
	}
}

// WithDefaults fills zero fields with the defaults.
func (p FailoverPolicy) WithDefaults() FailoverPolicy {
	d := DefaultFailover()
	if p.NoPacketsSeconds <= 0 {
		p.NoPacketsSeconds = d.NoPacketsSeconds
	}
	if p.ConfirmWindowSeconds <= 0 {
		p.ConfirmWindowSeconds = d.ConfirmWindowSeconds
	}
	if p.LowBitrateRatio <= 0 {
		p.LowBitrateRatio = d.LowBitrateRatio
	}
	if p.LowBitrateSeconds <= 0 {
		p.LowBitrateSeconds = d.LowBitrateSeconds
	}
	if p.MissingStreamSeconds <= 0 {
		p.MissingStreamSeconds = d.MissingStreamSeconds
	}
	if p.ReturnToPrimary == nil {
		p.ReturnToPrimary = d.ReturnToPrimary
	}
	if p.PrimaryStableSeconds <= 0 {
		p.PrimaryStableSeconds = d.PrimaryStableSeconds
	}
	return p
}

// ReturnsToPrimary reports the effective return_to_primary flag.
func (p FailoverPolicy) ReturnsToPrimary() bool {
	return p.ReturnToPrimary == nil || *p.ReturnToPrimary
}

// Seconds converts a float number of seconds to a Duration.
func Seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
