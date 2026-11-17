// Package alerts evaluates the operational alerts of spec section 14.3 from
// the current state of channels and workers, for the panel. The same rules
// exist for Prometheus/Alertmanager in deploy/configs/prometheus/rules.
package alerts

import (
	"fmt"
	"sort"
	"time"

	"nexostream/internal/model"
)

// Thresholds (spec 14.3).
const (
	NoSignalAfter     = 10 * time.Second
	OnBackupAfter     = 10 * time.Minute
	RestartsWindow    = 10 * time.Minute
	MaxRestarts       = 5
	CCErrorsPerMinute = 100
	DiskWarnPercent   = 85
	DiskCritPercent   = 95
	ResourcePercent   = 90
	OfflineForgetting = 24 * time.Hour
)

// Severities.
const (
	Critical = "critical"
	Warning  = "warning"
)

// Alert is one active problem.
type Alert struct {
	ID        string    `json:"id"`
	Rule      string    `json:"rule"`
	Severity  string    `json:"severity"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail"`
	ChannelID string    `json:"channel_id,omitempty"`
	WorkerID  string    `json:"worker_id,omitempty"`
	Since     time.Time `json:"since"`
}

// Input is the state the rules look at.
type Input struct {
	Now      time.Time
	Channels []model.Channel
	Statuses map[string]model.ChannelStatus
	Workers  []model.Worker
	// Restarts counts pipeline restarts per channel in the last RestartsWindow.
	Restarts map[string]int
	// DVRProblems counts DVR integrity problems per channel in the last 24 h.
	DVRProblems map[string]int
}

func pct(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) * 100 / float64(total)
}

// Evaluate returns the active alerts, critical first, then oldest first.
func Evaluate(in Input) []Alert {
	out := []Alert{}
	add := func(a Alert) {
		a.ID = a.Rule + ":" + a.ChannelID + a.WorkerID
		out = append(out, a)
	}
	for _, c := range in.Channels {
		if !c.Enabled {
			continue
		}
		st, ok := in.Statuses[c.ID]
		if !ok {
			continue
		}
		name := c.Name
		if name == "" {
			name = c.ID
		}
		var last time.Time
		if st.Input != nil && st.Input.LastPacketAt != nil {
			last = *st.Input.LastPacketAt
		}
		switch {
		case st.State == model.StateFailed:
			add(Alert{Rule: "no_signal", Severity: Critical, ChannelID: c.ID, Since: st.Since,
				Title: "Canal sem sinal: " + name, Detail: "Canal em falha; nenhuma fonte entrega sinal válido."})
		case st.State != model.StateStopped && st.State != model.StateStarting && !last.IsZero() && in.Now.Sub(last) > NoSignalAfter:
			add(Alert{Rule: "no_signal", Severity: Critical, ChannelID: c.ID, Since: last,
				Title: "Canal sem sinal: " + name, Detail: fmt.Sprintf("Sem pacotes há %s.", in.Now.Sub(last).Round(time.Second))})
		}
		if st.State == model.StateOnBackup && in.Now.Sub(st.Since) > OnBackupAfter {
			src := ""
			if st.ActiveSource != nil {
				src = fmt.Sprintf(" (fonte %d)", st.ActiveSource.Priority)
			}
			add(Alert{Rule: "on_backup", Severity: Warning, ChannelID: c.ID, Since: st.Since,
				Title:  "Canal na fonte alternativa: " + name,
				Detail: fmt.Sprintf("No backup%s há %s.", src, in.Now.Sub(st.Since).Round(time.Minute))})
		}
		if n := in.Restarts[c.ID]; n > MaxRestarts {
			add(Alert{Rule: "restarts", Severity: Warning, ChannelID: c.ID, Since: in.Now.Add(-RestartsWindow),
				Title: "Reinícios excessivos: " + name, Detail: fmt.Sprintf("%d reinícios do pipeline nos últimos 10 min.", n)})
		}
		if n := in.DVRProblems[c.ID]; n > 0 && c.DVR.Enabled {
			add(Alert{Rule: "dvr_integrity", Severity: Warning, ChannelID: c.ID, Since: in.Now.Add(-24 * time.Hour),
				Title:  "Gravação com problema: " + name,
				Detail: fmt.Sprintf("%d problema(s) em horas gravadas nas últimas 24 h (veja os eventos do canal).", n)})
		}
		if st.Input != nil && st.Input.CCErrorsLastMinute > CCErrorsPerMinute {
			add(Alert{Rule: "cc_errors", Severity: Warning, ChannelID: c.ID, Since: in.Now.Add(-time.Minute),
				Title:  "Erros de continuidade: " + name,
				Detail: fmt.Sprintf("%d erros CC no último minuto (limite %d): perda de pacotes na entrada.", st.Input.CCErrorsLastMinute, CCErrorsPerMinute)})
		}
	}
	for _, w := range in.Workers {
		if w.State == model.WorkerOffline {
			if in.Now.Sub(w.LastHeartbeatAt) < OfflineForgetting {
				add(Alert{Rule: "worker_offline", Severity: Critical, WorkerID: w.ID, Since: w.LastHeartbeatAt,
					Title: "Servidor indisponível: " + w.ID, Detail: "Sem heartbeat; os canais foram realocados."})
			}
			continue
		}
		r := w.Resources
		disks := r.DVRDisks
		if len(disks) == 0 && r.DVRTotalBytes > 0 {
			disks = []model.DiskUsage{{Path: "DVR", UsedBytes: r.DVRUsedBytes, TotalBytes: r.DVRTotalBytes}}
		}
		for _, d := range disks {
			p := pct(d.UsedBytes, d.TotalBytes)
			switch {
			case d.Down:
				add(Alert{Rule: "dvr_disk", Severity: Critical, WorkerID: w.ID, Since: in.Now,
					Title: "Disco do DVR com falha: " + w.ID, Detail: d.Path + ": falha de escrita; gravando nos outros discos."})
			case p >= DiskWarnPercent:
				sev := Warning
				if p >= DiskCritPercent {
					sev = Critical
				}
				add(Alert{Rule: "dvr_disk", Severity: sev, WorkerID: w.ID, Since: in.Now,
					Title: "Disco do DVR quase cheio: " + w.ID, Detail: fmt.Sprintf("%s em %.0f%%.", d.Path, p)})
			}
		}
		if r.CPUPercent > ResourcePercent {
			add(Alert{Rule: "node_resources", Severity: Warning, WorkerID: w.ID, Since: in.Now,
				Title: "CPU alta: " + w.ID, Detail: fmt.Sprintf("CPU em %.0f%%.", r.CPUPercent)})
		}
		if r.GPUPercent != nil && *r.GPUPercent > ResourcePercent {
			add(Alert{Rule: "node_resources", Severity: Warning, WorkerID: w.ID, Since: in.Now,
				Title: "GPU alta: " + w.ID, Detail: fmt.Sprintf("GPU em %.0f%%.", *r.GPUPercent)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == Critical
		}
		return out[i].Since.Before(out[j].Since)
	})
	return out
}
