package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"nexostream/internal/model"
)

// Panel-managed settings (Settings page). The controller pushes a
// model.WorkerRuntime with CmdConfigure; the node keeps it in
// StateDir/settings.json so it starts with the same settings when the
// Control Plane is down. Empty fields keep the value from the node's .env.

func (a *Agent) settingsPath() string { return filepath.Join(a.cfg.StateDir, "settings.json") }

// conf returns the effective configuration: .env values with the runtime
// received from the controller on top.
func (a *Agent) conf() Config {
	a.rtMu.RLock()
	defer a.rtMu.RUnlock()
	c := a.cfg
	rt := a.rt
	if rt == nil {
		return c
	}
	if rt.HLSSegmentSeconds > 0 {
		c.HLSSegmentSeconds = rt.HLSSegmentSeconds
	}
	if rt.HLSListSize > 0 {
		c.HLSListSize = rt.HLSListSize
	}
	if rt.DefaultProfile != "" {
		c.DefaultProfile = rt.DefaultProfile
	}
	c.AllowHEVCPassthrough = c.AllowHEVCPassthrough || rt.AllowHEVCPassthrough
	if rt.UDPReadBufferBytes > 0 {
		c.ReadBuffer = rt.UDPReadBufferBytes
	}
	if len(rt.IngestInterfaces) > 0 {
		c.IngestInterfaces = rt.IngestInterfaces
	}
	if rt.DVRDir != "" {
		c.DVRDir = rt.DVRDir
	}
	if rt.PublicHLSBase != "" {
		c.PublicHLSBase = rt.PublicHLSBase
	}
	if rt.PublicTSBase != "" {
		c.PublicTSBase = rt.PublicTSBase
	}
	if rt.PublicDVRBase != "" {
		c.PublicDVRBase = rt.PublicDVRBase
	}
	return c
}

// runtimeHash is reported in heartbeats ("" = nothing received yet).
func (a *Agent) runtimeHash() string {
	a.rtMu.RLock()
	defer a.rtMu.RUnlock()
	if a.rt == nil {
		return ""
	}
	return a.rt.Hash()
}

// mediaChanged reports whether two configurations differ in a way that
// requires restarting the channel pipelines.
func mediaChanged(old, cur Config) bool {
	return old.HLSSegmentSeconds != cur.HLSSegmentSeconds || old.HLSListSize != cur.HLSListSize ||
		old.DefaultProfile != cur.DefaultProfile || old.AllowHEVCPassthrough != cur.AllowHEVCPassthrough ||
		old.ReadBuffer != cur.ReadBuffer || !slices.Equal(old.IngestInterfaces, cur.IngestInterfaces)
}

// Configure applies a runtime pushed by the controller. Channels are
// restarted only when a media setting changed.
func (a *Agent) Configure(rt model.WorkerRuntime) error {
	old := a.conf()
	a.rtMu.Lock()
	same := a.rt != nil && a.rt.Hash() == rt.Hash()
	a.rt = &rt
	a.rtMu.Unlock()
	if same {
		return nil
	}
	a.saveSettings(rt)
	cur := a.conf()
	if cur.DVRDir != old.DVRDir {
		if err := os.MkdirAll(cur.DVRDir, 0o750); err != nil {
			a.log.Warn("dvr dir not available", "dir", cur.DVRDir, "error", err)
		}
	}
	a.log.Info("settings applied", "hash", rt.Hash(), "hls_segment_seconds", cur.HLSSegmentSeconds,
		"hls_list_size", cur.HLSListSize, "default_profile", cur.DefaultProfile, "dvr_dir", cur.DVRDir)
	if !mediaChanged(old, cur) {
		return nil
	}
	a.mu.Lock()
	runners := make([]*runner, 0, len(a.runners))
	for _, r := range a.runners {
		runners = append(runners, r)
	}
	a.mu.Unlock()
	for _, r := range runners {
		if err := r.restart(); err != nil {
			a.log.Warn("restart after settings change", "channel_id", r.id, "error", err)
		}
	}
	if len(runners) > 0 {
		a.log.Info("channels restarted to apply settings", "count", len(runners))
	}
	return nil
}

func (a *Agent) saveSettings(rt model.WorkerRuntime) {
	b, err := json.MarshalIndent(rt, "", "  ")
	if err != nil {
		return
	}
	tmp := a.settingsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		a.log.Error("save settings", "error", err)
		return
	}
	if err := os.Rename(tmp, a.settingsPath()); err != nil {
		a.log.Error("save settings", "error", err)
	}
}

func (a *Agent) restoreSettings() {
	b, err := os.ReadFile(a.settingsPath())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			a.log.Error("read settings", "error", err)
		}
		return
	}
	var rt model.WorkerRuntime
	if err := json.Unmarshal(b, &rt); err != nil {
		a.log.Error("parse settings", "error", err)
		return
	}
	a.rtMu.Lock()
	a.rt = &rt
	a.rtMu.Unlock()
	a.log.Info("restored settings from local state", "hash", rt.Hash())
}
