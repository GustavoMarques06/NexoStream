package controller

import (
	"testing"

	"nexostream/internal/model"
)

func TestStatsSample(t *testing.T) {
	chs := []model.Channel{{ID: "a", Enabled: true}, {ID: "b", Enabled: true}, {ID: "c"}}
	sts := map[string]model.ChannelStatus{
		"a": {State: model.StateRunning, Viewers: 3, Input: &model.InputStatus{BitrateBps: 4_000_000}},
		"b": {State: model.StateFailed},
	}
	ws := []model.Worker{
		{ID: "w1", State: model.WorkerOnline, Resources: model.Resources{CPUPercent: 40, MemoryUsedBytes: 25, MemoryTotalBytes: 100, NetworkInBps: 9}},
		{ID: "w2", State: model.WorkerOffline},
	}
	v := StatsSample(chs, sts, ws)
	want := map[string]float64{"channels.total": 3, "channels.enabled": 2, "channels.up": 1, "channels.failed": 1,
		"viewers.total": 3, "input.bps": 4_000_000, "workers.online": 1, "channel:a:viewers": 3,
		"worker:w1:cpu": 40, "worker:w1:mem": 25, "worker:w1:net_in": 9}
	for k, x := range want {
		if v[k] != x {
			t.Errorf("%s = %v, want %v", k, v[k], x)
		}
	}
	if _, ok := v["channel:b:viewers"]; ok {
		t.Error("channels without viewers are not stored")
	}
	if _, ok := v["worker:w2:cpu"]; ok {
		t.Error("offline workers are not sampled")
	}
}
