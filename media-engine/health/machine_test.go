package health

import (
	"testing"
	"time"

	"nexostream/internal/model"
)

// sim drives a Machine with simulated sources.
type sim struct {
	t     *testing.T
	m     *Machine
	now   time.Time
	alive []bool // per source: is it sending?
	h     map[int]*SourceHealth
	pipe  bool
	trans []Transition
	sw    []Switch
	rs    []Restart
}

func newSim(t *testing.T, n int) *sim {
	p := NewPolicy(model.FailoverPolicy{})
	s := &sim{t: t, m: NewMachine(p, n), now: time.Unix(1_800_000_000, 0), alive: make([]bool, n), h: map[int]*SourceHealth{}}
	for i := range s.alive {
		s.alive[i] = true
	}
	return s
}

func (s *sim) collect(o Output) {
	s.trans = append(s.trans, o.Transitions...)
	s.sw = append(s.sw, o.Switches...)
	s.rs = append(s.rs, o.Restarts...)
}

// step advances the clock by d in 200 ms ticks, simulating packets on the
// listened sources and a pipeline that follows Desired.
func (s *sim) step(d time.Duration) {
	for end := s.now.Add(d); s.now.Before(end); s.now = s.now.Add(200 * time.Millisecond) {
		des := s.m.Desired()
		health := map[int]SourceHealth{}
		for _, i := range des.Listen {
			h, ok := s.h[i]
			if !ok {
				h = &SourceHealth{HasVideo: true, HasAudio: true}
				s.h[i] = h
			}
			if s.alive[i] {
				if h.FirstPacketAt.IsZero() {
					h.FirstPacketAt = s.now
				}
				h.LastPacketAt, h.LastVideoAt, h.LastAudioAt = s.now, s.now, s.now
				h.PMTSeen = true
				h.BitrateBps, h.AvgBitrateBps = 4e6, 4e6
			}
			health[i] = *h
		}
		// Forget sources that are no longer listened (a new socket starts fresh).
		for i := range s.h {
			listened := false
			for _, l := range des.Listen {
				listened = listened || l == i
			}
			if !listened {
				delete(s.h, i)
			}
		}
		s.pipe = des.Pipeline
		s.collect(s.m.Tick(s.now, health, s.pipe))
	}
}

func (s *sim) expect(state model.ChannelState) {
	s.t.Helper()
	if st, _, r := s.m.State(); st != state {
		s.t.Fatalf("state=%s (%s) want %s; transitions=%v", st, r, state, s.trans)
	}
}

func TestStartRunning(t *testing.T) {
	s := newSim(t, 2)
	s.collect(s.m.Start(s.now))
	s.expect(model.StateStarting)
	s.step(time.Second)
	s.expect(model.StateRunning)
	if d := s.m.Desired(); !d.Pipeline || d.Active != 0 || len(d.Listen) != 1 {
		t.Fatalf("desired=%+v", d)
	}
}

func TestFailoverAndReturnToPrimary(t *testing.T) {
	s := newSim(t, 2)
	s.collect(s.m.Start(s.now))
	s.step(2 * time.Second)
	s.expect(model.StateRunning)

	s.alive[0] = false
	s.step(3500 * time.Millisecond) // > 3 s without packets
	s.expect(model.StateDegraded)
	s.step(5 * time.Second) // confirmation window
	// SWITCHING picks the backup, which is alive: ON_BACKUP within a tick or two.
	s.step(time.Second)
	s.expect(model.StateOnBackup)
	if s.m.Active() != 1 || len(s.sw) != 1 || s.sw[0].Reason != "failover" {
		t.Fatalf("active=%d switches=%v", s.m.Active(), s.sw)
	}
	gen := s.m.Desired().Generation

	// Primary returns: RECOVERING, then RUNNING after 60 s of stability.
	s.alive[0] = true
	s.step(time.Second)
	s.expect(model.StateRecovering)
	s.step(30 * time.Second)
	s.expect(model.StateRecovering)
	s.step(31 * time.Second)
	s.expect(model.StateRunning)
	if s.m.Active() != 0 || s.m.Desired().Generation == gen {
		t.Fatal("expected pipeline restart on primary")
	}
}

func TestBriefDropDoesNotSwitch(t *testing.T) {
	s := newSim(t, 2)
	s.collect(s.m.Start(s.now))
	s.step(2 * time.Second)
	s.alive[0] = false
	s.step(4 * time.Second) // DEGRADED
	s.expect(model.StateDegraded)
	s.alive[0] = true
	s.step(time.Second)
	s.expect(model.StateRunning)
	if len(s.sw) != 0 {
		t.Fatalf("unexpected switch %v", s.sw)
	}
}

func TestPrimaryFlappingDuringRecovery(t *testing.T) {
	s := newSim(t, 2)
	s.collect(s.m.Start(s.now))
	s.step(2 * time.Second)
	s.alive[0] = false
	s.step(10 * time.Second)
	s.expect(model.StateOnBackup)
	s.alive[0] = true
	s.step(2 * time.Second)
	s.expect(model.StateRecovering)
	s.alive[0] = false
	s.step(4 * time.Second)
	s.expect(model.StateOnBackup)
}

func TestAllSourcesDownThenBack(t *testing.T) {
	s := newSim(t, 2)
	s.collect(s.m.Start(s.now))
	s.step(2 * time.Second)
	s.alive[0], s.alive[1] = false, false
	s.step(15 * time.Second)
	s.expect(model.StateFailed)
	if s.m.Desired().Pipeline {
		t.Fatal("pipeline must stop in FAILED")
	}
	if l := s.m.Desired().Listen; len(l) != 2 {
		t.Fatalf("FAILED must listen to all sources, got %v", l)
	}
	s.alive[1] = true
	s.step(2 * time.Second)
	s.expect(model.StateOnBackup)
}

func TestSingleSourceFailsAndRecovers(t *testing.T) {
	s := newSim(t, 1)
	s.collect(s.m.Start(s.now))
	s.step(2 * time.Second)
	s.alive[0] = false
	s.step(10 * time.Second)
	s.expect(model.StateFailed)
	s.alive[0] = true
	s.step(2 * time.Second)
	s.expect(model.StateRunning)
}

func TestPipelineRestartBackoffAndLimit(t *testing.T) {
	s := newSim(t, 1)
	s.collect(s.m.Start(s.now))
	s.step(2 * time.Second)
	s.expect(model.StateRunning)

	var delays []time.Duration
	for i := 0; i < 5; i++ {
		s.m.PipelineStarted(s.now)
		o := s.m.PipelineExited(s.now, "exit status 1")
		s.collect(o)
		if len(o.Restarts) != 1 {
			t.Fatalf("restart %d: %+v", i, o)
		}
		delays = append(delays, o.Restarts[0].Delay)
		s.expect(model.StateStarting)
		if s.m.Desired().Pipeline {
			t.Fatal("pipeline must wait for the backoff")
		}
		s.step(o.Restarts[0].Delay + 400*time.Millisecond)
		s.expect(model.StateRunning)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	for i := range want {
		if delays[i] != want[i] {
			t.Fatalf("delays=%v want %v", delays, want)
		}
	}
	s.collect(s.m.PipelineExited(s.now, "exit status 1"))
	s.expect(model.StateFailed)
	if _, _, r := s.m.State(); r != "restart_limit" {
		t.Fatalf("reason=%s", r)
	}
	// Automatic retry after the cooldown.
	s.step(10*time.Minute + time.Second)
	s.expect(model.StateRunning)
}

func TestManualSwitchHoldsBackup(t *testing.T) {
	s := newSim(t, 2)
	s.collect(s.m.Start(s.now))
	s.step(2 * time.Second)
	if _, err := s.m.ManualSwitch(s.now, 1); err != nil {
		t.Fatal(err)
	}
	s.step(2 * time.Minute)
	s.expect(model.StateOnBackup) // no automatic return while held
	if _, err := s.m.ManualSwitch(s.now, 0); err != nil {
		t.Fatal(err)
	}
	s.step(time.Second)
	s.expect(model.StateRunning)
	if _, err := s.m.ManualSwitch(s.now, 5); err == nil {
		t.Fatal("expected out of range error")
	}
}

func TestConnectGraceForPulledSources(t *testing.T) {
	s := newSim(t, 2)
	s.m.p.ConnectGrace = []time.Duration{0, 10 * time.Second}
	s.collect(s.m.Start(s.now))
	s.step(2 * time.Second)
	s.alive[0] = false
	// The backup needs 6 s to connect: more than SwitchTimeout (3 s) but
	// within the grace, so the failover must succeed instead of FAILED.
	s.alive[1] = false
	s.step(9 * time.Second) // DEGRADED + confirmation -> SWITCHING
	s.expect(model.StateSwitching)
	s.step(5 * time.Second)
	s.expect(model.StateSwitching)
	s.alive[1] = true
	s.step(time.Second)
	s.expect(model.StateOnBackup)
}

func TestLowBitrateAndMissingAudio(t *testing.T) {
	p := NewPolicy(model.FailoverPolicy{})
	m := NewMachine(p, 1)
	now := time.Unix(1_800_000_000, 0)
	m.Start(now)
	h := SourceHealth{FirstPacketAt: now, PMTSeen: true, HasVideo: true, HasAudio: true, AvgBitrateBps: 4e6, BitrateBps: 4e6}
	tick := func(d time.Duration, mut func(*SourceHealth)) {
		for end := now.Add(d); now.Before(end); now = now.Add(200 * time.Millisecond) {
			h.LastPacketAt, h.LastVideoAt = now, now
			mut(&h)
			m.Tick(now, map[int]SourceHealth{0: h}, true)
		}
	}
	tick(time.Second, func(h *SourceHealth) { h.LastAudioAt = h.LastPacketAt })
	if st, _, _ := m.State(); st != model.StateRunning {
		t.Fatalf("state=%s", st)
	}
	tick(9*time.Second, func(h *SourceHealth) { h.BitrateBps = 0.5e6; h.LastAudioAt = h.LastPacketAt })
	if st, _, _ := m.State(); st != model.StateRunning {
		t.Fatalf("low bitrate must take 10 s, state=%s", st)
	}
	tick(2*time.Second, func(h *SourceHealth) { h.LastAudioAt = h.LastPacketAt })
	tick(1*time.Second, func(h *SourceHealth) { h.BitrateBps = 4e6 })
	// Audio stopped at the last tick; after 5 s it must be DEGRADED/no_audio.
	tick(6*time.Second, func(h *SourceHealth) {})
	st, _, r := m.State()
	if st != model.StateDegraded || r != "no_audio" {
		t.Fatalf("state=%s reason=%s", st, r)
	}
}

func TestBitrateTracker(t *testing.T) {
	b := NewBitrate()
	now := time.Unix(0, 0)
	var total int64
	for i := 0; i < 50; i++ { // 10 s at 4 Mbps
		total += 4e6 / 8 / 5
		b.Sample(now, total)
		now = now.Add(200 * time.Millisecond)
	}
	if c := b.Current(); c < 3.9e6 || c > 4.1e6 {
		t.Fatalf("current=%v", c)
	}
	if a := b.Average(); a < 3.9e6 || a > 4.1e6 {
		t.Fatalf("average=%v", a)
	}
}
