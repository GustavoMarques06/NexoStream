package alerts

import (
	"testing"
	"time"

	"nexostream/internal/model"
)

func TestEvaluate(t *testing.T) {
	now := time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	gpu := 95.0
	in := Input{
		Now: now,
		Channels: []model.Channel{
			{ID: "ok", Enabled: true}, {ID: "mudo", Name: "Mudo", Enabled: true}, {ID: "falha", Enabled: true},
			{ID: "backup", Enabled: true}, {ID: "backup-curto", Enabled: true}, {ID: "cc", Enabled: true},
			{ID: "reinicia", Enabled: true}, {ID: "parado", Enabled: false},
		},
		Statuses: map[string]model.ChannelStatus{
			"ok":           {State: model.StateRunning, Input: &model.InputStatus{LastPacketAt: ago(time.Second)}},
			"mudo":         {State: model.StateDegraded, Input: &model.InputStatus{LastPacketAt: ago(30 * time.Second)}},
			"falha":        {State: model.StateFailed, Since: now.Add(-time.Minute)},
			"backup":       {State: model.StateOnBackup, Since: now.Add(-11 * time.Minute), ActiveSource: &model.SourceRef{Priority: 2}},
			"backup-curto": {State: model.StateOnBackup, Since: now.Add(-time.Minute)},
			"cc":           {State: model.StateRunning, Input: &model.InputStatus{CCErrorsLastMinute: 500, LastPacketAt: ago(0)}},
			"reinicia":     {State: model.StateRunning},
			"parado":       {State: model.StateFailed},
		},
		Restarts: map[string]int{"reinicia": 6, "ok": 5},
		Workers: []model.Worker{
			{ID: "w-off", State: model.WorkerOffline, LastHeartbeatAt: now.Add(-time.Minute)},
			{ID: "w-velho", State: model.WorkerOffline, LastHeartbeatAt: now.Add(-48 * time.Hour)},
			{ID: "w-cheio", State: model.WorkerOnline, Resources: model.Resources{CPUPercent: 95, GPUPercent: &gpu,
				DVRDisks: []model.DiskUsage{{Path: "/d1", UsedBytes: 96, TotalBytes: 100}, {Path: "/d2", UsedBytes: 86, TotalBytes: 100},
					{Path: "/d3", UsedBytes: 10, TotalBytes: 100}, {Path: "/d4", Down: true}}}},
		},
	}
	got := map[string]Alert{}
	for _, a := range Evaluate(in) {
		got[a.Rule+"/"+a.ChannelID+a.WorkerID+"/"+a.Detail] = a
	}
	count := func(rule, target string) int {
		n := 0
		for _, a := range Evaluate(in) {
			if a.Rule == rule && a.ChannelID+a.WorkerID == target {
				n++
			}
		}
		return n
	}
	for _, c := range [][2]string{
		{"no_signal", "mudo"}, {"no_signal", "falha"}, {"on_backup", "backup"}, {"cc_errors", "cc"},
		{"restarts", "reinicia"}, {"worker_offline", "w-off"},
	} {
		if count(c[0], c[1]) != 1 {
			t.Errorf("missing %s for %s", c[0], c[1])
		}
	}
	for _, c := range [][2]string{
		{"no_signal", "ok"}, {"on_backup", "backup-curto"}, {"restarts", "ok"}, {"no_signal", "parado"}, {"worker_offline", "w-velho"},
	} {
		if count(c[0], c[1]) != 0 {
			t.Errorf("unexpected %s for %s", c[0], c[1])
		}
	}
	if count("dvr_disk", "w-cheio") != 3 || count("node_resources", "w-cheio") != 2 {
		t.Errorf("worker alerts: %+v", got)
	}
	all := Evaluate(in)
	if all[0].Severity != Critical || all[len(all)-1].Severity != Warning {
		t.Fatal("critical alerts come first")
	}
}
