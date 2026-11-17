package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Settings are the cluster-wide settings edited in the panel (Settings
// page) or in the text config ("settings { ... }"), like the global options
// of a Flussonic config. Bootstrap values (database, NATS, keys, ports,
// working folders) stay in the .env files.
type Settings struct {
	// General and branding (whitelabel): what the panel shows as the
	// product name, logo, accent color and login message.
	SystemName           string `json:"system_name"`
	BrandTagline         string `json:"brand_tagline"`
	BrandColor           string `json:"brand_color"`          // #RRGGBB; "" = default
	BrandLogo            string `json:"brand_logo,omitempty"` // data:image/...;base64 (up to 256 KB)
	BrandLoginMessage    string `json:"brand_login_message"`
	DefaultProfile       string `json:"default_profile"`
	AllowHEVCPassthrough bool   `json:"allow_hevc_passthrough"`

	// Ingest
	UDPReadBufferBytes int            `json:"udp_read_buffer_bytes"`
	DefaultFailover    FailoverPolicy `json:"default_failover"`

	// HLS output
	HLSSegmentSeconds int `json:"hls_segment_seconds"`
	HLSListSize       int `json:"hls_list_size"`

	// DVR
	DVRDefaultRetentionHours int    `json:"dvr_default_retention_hours"`
	DVRDefaultStorage        string `json:"dvr_default_storage"`

	// Cluster
	SchedulerMaxCPUPercent    float64 `json:"scheduler_max_cpu_percent"`
	CostPassthrough           float64 `json:"cost_passthrough"`
	CostTranscodePerMegapixel float64 `json:"cost_transcode_per_megapixel"`
	CostGPUOffload            float64 `json:"cost_gpu_offload"`
	WorkerOfflineSeconds      int     `json:"worker_offline_seconds"`
	DrainMigrationsPerPass    int     `json:"drain_migrations_per_pass"`

	// Security
	AccessTokenMinutes     int `json:"access_token_minutes"`
	RefreshTokenHours      int `json:"refresh_token_hours"`
	LoginAttemptsPerMinute int `json:"login_attempts_per_minute"`

	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// DefaultSettings returns the values used until someone edits them.
func DefaultSettings() Settings {
	return Settings{
		SystemName:                "NexoStream",
		BrandTagline:              "Media Server",
		DefaultProfile:            "hd",
		UDPReadBufferBytes:        4 << 20,
		DefaultFailover:           DefaultFailover(),
		HLSSegmentSeconds:         6,
		HLSListSize:               10,
		DVRDefaultRetentionHours:  72,
		SchedulerMaxCPUPercent:    80,
		CostPassthrough:           0.2,
		CostTranscodePerMegapixel: 4,
		CostGPUOffload:            4,
		WorkerOfflineSeconds:      15,
		DrainMigrationsPerPass:    5,
		AccessTokenMinutes:        15,
		RefreshTokenHours:         12,
		LoginAttemptsPerMinute:    10,
	}
}

// WithDefaults fills zero values (settings saved by older versions).
func (s Settings) WithDefaults() Settings {
	d := DefaultSettings()
	if s.SystemName == "" {
		s.SystemName = d.SystemName
	}
	if s.DefaultProfile == "" {
		s.DefaultProfile = d.DefaultProfile
	}
	if s.UDPReadBufferBytes <= 0 {
		s.UDPReadBufferBytes = d.UDPReadBufferBytes
	}
	s.DefaultFailover = s.DefaultFailover.WithDefaults()
	if s.HLSSegmentSeconds <= 0 {
		s.HLSSegmentSeconds = d.HLSSegmentSeconds
	}
	if s.HLSListSize <= 0 {
		s.HLSListSize = d.HLSListSize
	}
	if s.DVRDefaultRetentionHours <= 0 {
		s.DVRDefaultRetentionHours = d.DVRDefaultRetentionHours
	}
	if s.SchedulerMaxCPUPercent <= 0 {
		s.SchedulerMaxCPUPercent = d.SchedulerMaxCPUPercent
	}
	if s.CostPassthrough <= 0 {
		s.CostPassthrough = d.CostPassthrough
	}
	if s.CostTranscodePerMegapixel <= 0 {
		s.CostTranscodePerMegapixel = d.CostTranscodePerMegapixel
	}
	if s.CostGPUOffload <= 0 {
		s.CostGPUOffload = d.CostGPUOffload
	}
	if s.WorkerOfflineSeconds <= 0 {
		s.WorkerOfflineSeconds = d.WorkerOfflineSeconds
	}
	if s.DrainMigrationsPerPass <= 0 {
		s.DrainMigrationsPerPass = d.DrainMigrationsPerPass
	}
	if s.AccessTokenMinutes <= 0 {
		s.AccessTokenMinutes = d.AccessTokenMinutes
	}
	if s.RefreshTokenHours <= 0 {
		s.RefreshTokenHours = d.RefreshTokenHours
	}
	if s.LoginAttemptsPerMinute <= 0 {
		s.LoginAttemptsPerMinute = d.LoginAttemptsPerMinute
	}
	return s
}

// ServerSettings are per Media Node overrides (empty = keep the node's .env).
type ServerSettings struct {
	WorkerID         string   `json:"worker_id"`
	IngestInterfaces []string `json:"ingest_interfaces"`
	DVRDir           string   `json:"dvr_dir"`
	PublicHLSBase    string   `json:"public_hls_base"`
	PublicTSBase     string   `json:"public_ts_base"`
	PublicDVRBase    string   `json:"public_dvr_base"`
	// Roles limit which channels the node runs (empty = all): passthrough,
	// transcode, dvr (spec section 9: ingest, transcoding and DVR nodes).
	Roles     []string  `json:"roles,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// WorkerRuntime is what the controller sends to a node: the global settings
// that matter to media plus the node's overrides.
type WorkerRuntime struct {
	HLSSegmentSeconds    int      `json:"hls_segment_seconds"`
	HLSListSize          int      `json:"hls_list_size"`
	DefaultProfile       string   `json:"default_profile"`
	AllowHEVCPassthrough bool     `json:"allow_hevc_passthrough"`
	UDPReadBufferBytes   int      `json:"udp_read_buffer_bytes"`
	IngestInterfaces     []string `json:"ingest_interfaces,omitempty"`
	DVRDir               string   `json:"dvr_dir,omitempty"`
	PublicHLSBase        string   `json:"public_hls_base,omitempty"`
	PublicTSBase         string   `json:"public_ts_base,omitempty"`
	PublicDVRBase        string   `json:"public_dvr_base,omitempty"`
}

// Runtime builds the runtime of a node.
func (s Settings) Runtime(o ServerSettings) WorkerRuntime {
	return WorkerRuntime{
		HLSSegmentSeconds: s.HLSSegmentSeconds, HLSListSize: s.HLSListSize,
		DefaultProfile: s.DefaultProfile, AllowHEVCPassthrough: s.AllowHEVCPassthrough,
		UDPReadBufferBytes: s.UDPReadBufferBytes,
		IngestInterfaces:   o.IngestInterfaces, DVRDir: o.DVRDir,
		PublicHLSBase: o.PublicHLSBase, PublicTSBase: o.PublicTSBase, PublicDVRBase: o.PublicDVRBase,
	}
}

// Hash identifies a runtime: workers report it in heartbeats and the
// controller re-sends the runtime when it differs.
func (r WorkerRuntime) Hash() string {
	b, _ := json.Marshal(r)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}
