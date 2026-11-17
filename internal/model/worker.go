package model

import "time"

// WorkerState is the cluster state of a Media Node.
type WorkerState string

const (
	WorkerOnline   WorkerState = "ONLINE"
	WorkerDraining WorkerState = "DRAINING"
	WorkerOffline  WorkerState = "OFFLINE"
)

// Worker is a Media Node as seen by the Control Plane.
type Worker struct {
	ID              string       `json:"id"`
	State           WorkerState  `json:"state"`
	Draining        bool         `json:"draining"`
	LastHeartbeatAt time.Time    `json:"last_heartbeat_at"`
	StartedAt       time.Time    `json:"started_at"`
	Version         string       `json:"version"`
	Capabilities    Capabilities `json:"capabilities"`
	Resources       Resources    `json:"resources"`
	Channels        int          `json:"channels"`
}

// Capabilities describes what a node can do.
type Capabilities struct {
	GPU        string   `json:"gpu,omitempty"` // nvidia | intel | ""
	Encoders   []string `json:"encoders"`      // cpu, nvenc, qsv (validated by a test encode)
	Interfaces []string `json:"interfaces"`    // network interfaces usable for ingest
	Engine     string   `json:"engine"`        // ffmpeg
	// Roles set in the panel for the node (empty = every channel).
	Roles []string `json:"roles,omitempty"`
}

// Node roles.
const (
	RolePassthrough = "passthrough"
	RoleTranscode   = "transcode"
	RoleDVR         = "dvr"
)

// Roles lists the valid node roles.
var Roles = []string{RolePassthrough, RoleTranscode, RoleDVR}

// RolesNeeded are the roles a node must have to run a channel.
func (c Channel) RolesNeeded() []string {
	need := []string{RolePassthrough}
	if c.Mode == ModeTranscode {
		need = []string{RoleTranscode}
	}
	if c.DVR.Enabled {
		need = append(need, RoleDVR)
	}
	return need
}

// HasEncoder reports whether the node supports encoder e.
func (c Capabilities) HasEncoder(e Encoder) bool {
	if e == EncoderAuto {
		return true
	}
	for _, v := range c.Encoders {
		if v == string(e) {
			return true
		}
	}
	return false
}

// HasInterface reports whether the node has the named interface.
func (c Capabilities) HasInterface(name string) bool {
	if name == "" {
		return true
	}
	for _, v := range c.Interfaces {
		if v == name {
			return true
		}
	}
	return false
}

// Resources is a resource usage sample reported in heartbeats.
type Resources struct {
	CPUPercent       float64     `json:"cpu_percent"`
	MemoryUsedBytes  uint64      `json:"memory_used_bytes"`
	MemoryTotalBytes uint64      `json:"memory_total_bytes"`
	GPUPercent       *float64    `json:"gpu_percent"`
	NetworkInBps     uint64      `json:"network_in_bps"`
	NetworkOutBps    uint64      `json:"network_out_bps"`
	DVRUsedBytes     uint64      `json:"dvr_used_bytes"`
	DVRTotalBytes    uint64      `json:"dvr_total_bytes"`
	DVRDisks         []DiskUsage `json:"dvr_disks,omitempty"`
}

// DiskUsage is the state of one DVR disk on a node.
type DiskUsage struct {
	Path       string `json:"path"`
	UsedBytes  uint64 `json:"used_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
	Down       bool   `json:"down"` // recent write failure: recording continues on the other disks
}

// Assignment records which worker runs a channel.
type Assignment struct {
	ChannelID string    `json:"channel_id"`
	WorkerID  string    `json:"worker_id"`
	Since     time.Time `json:"since"`
	Reason    string    `json:"reason"`
}
