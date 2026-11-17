package scheduler

import (
	"errors"
	"testing"

	"nexostream/internal/model"
)

func worker(id string, cpu float64, encs []string, ifs ...string) model.Worker {
	return model.Worker{ID: id, State: model.WorkerOnline, Resources: model.Resources{CPUPercent: cpu},
		Capabilities: model.Capabilities{Encoders: encs, Interfaces: ifs}}
}

func profiles() map[string]model.TranscodeProfile {
	m := map[string]model.TranscodeProfile{}
	for _, p := range model.DefaultProfiles() {
		m[p.ID] = p
	}
	return m
}

func pass(id string, iface string) model.Channel {
	return model.Channel{ID: id, Mode: model.ModePassthrough, Sources: []model.Source{{Priority: 1, URL: "udp://239.1.1.1:1", Interface: iface}}}
}

func TestPickBalances(t *testing.T) {
	ws := []model.Worker{worker("a", 10, []string{"cpu"}), worker("b", 10, []string{"cpu"})}
	c := NewCluster(ws, nil, DefaultOptions())
	counts := map[string]int{}
	for i := 0; i < 10; i++ {
		id, err := c.Pick(pass("c", ""), profiles(), "")
		if err != nil {
			t.Fatal(err)
		}
		counts[id]++
	}
	if counts["a"] != 5 || counts["b"] != 5 {
		t.Fatalf("unbalanced %v", counts)
	}
}

func TestPickConstraints(t *testing.T) {
	off := worker("off", 0, []string{"cpu"})
	off.State = model.WorkerOffline
	drain := worker("drain", 0, []string{"cpu"})
	drain.Draining = true
	busy := worker("busy", 85, []string{"cpu"})
	ws := []model.Worker{off, drain, busy, worker("eno3only", 0, []string{"cpu"}, "eno3"), worker("ok", 50, []string{"cpu"}, "eno2")}
	c := NewCluster(ws, nil, DefaultOptions())
	id, err := c.Pick(pass("c1", "eno2"), profiles(), "")
	if err != nil || id != "ok" {
		t.Fatalf("got %q %v", id, err)
	}
	if _, err := c.Pick(pass("c2", "eno9"), profiles(), ""); !errors.Is(err, ErrNoWorker) {
		t.Fatalf("expected ErrNoWorker, got %v", err)
	}
	if id, err := c.Pick(pass("c3", "eno2"), profiles(), "ok"); err == nil {
		t.Fatalf("exclude ignored: %s", id)
	}
}

func TestPickEncoderAndGPU(t *testing.T) {
	ps := profiles()
	nv := ps["hd"]
	nv.Encoder = model.EncoderNVENC
	ps["hd-nv"] = nv
	ws := []model.Worker{worker("cpu", 0, []string{"cpu"}), worker("gpu", 20, []string{"cpu", "nvenc"})}
	c := NewCluster(ws, nil, DefaultOptions())
	ch := model.Channel{ID: "t", Mode: model.ModeTranscode, Profiles: []string{"hd-nv"}}
	if id, err := c.Pick(ch, ps, ""); err != nil || id != "gpu" {
		t.Fatalf("got %q %v", id, err)
	}
	// Auto encoder: GPU offload makes transcode cheaper on the GPU node.
	cl := NewCluster(ws, nil, DefaultOptions())
	if cpu, gpu := cl.Cost(model.Channel{Mode: model.ModeTranscode, Profiles: []string{"fhd"}}, ps, ws[0]),
		cl.Cost(model.Channel{Mode: model.ModeTranscode, Profiles: []string{"fhd"}}, ps, ws[1]); gpu >= cpu {
		t.Fatalf("gpu cost %v >= cpu cost %v", gpu, cpu)
	}
}

func TestCPULimitRespected(t *testing.T) {
	opt := DefaultOptions()
	opt.Costs.TranscodePerMegapixel = 13 // ~27% per fhd channel (2.07 MP)
	c := NewCluster([]model.Worker{worker("a", 20, []string{"cpu"})}, nil, opt)
	ch := model.Channel{ID: "t", Mode: model.ModeTranscode, Profiles: []string{"fhd"}}
	n := 0
	for ; n < 10; n++ {
		if _, err := c.Pick(ch, profiles(), ""); err != nil {
			break
		}
	}
	if n != 2 {
		t.Fatalf("placed %d channels, want 2 under 80%%", n)
	}
}
