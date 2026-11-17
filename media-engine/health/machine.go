package health

import (
	"fmt"
	"time"

	"nexostream/internal/model"
)

// SourceHealth is what the agent measured on one source.
type SourceHealth struct {
	FirstPacketAt time.Time
	LastPacketAt  time.Time
	BitrateBps    float64
	AvgBitrateBps float64
	PMTSeen       bool
	HasVideo      bool
	HasAudio      bool
	LastVideoAt   time.Time
	LastAudioAt   time.Time
}

// Policy holds every threshold used by the machine.
type Policy struct {
	model.FailoverPolicy
	RestartLimit      int           // pipeline restarts allowed in RestartWindow (5)
	RestartWindow     time.Duration // 10 min
	BackoffMin        time.Duration // 1 s
	BackoffMax        time.Duration // 30 s
	BackoffResetAfter time.Duration // pipeline alive this long resets the backoff
	FailedCooldown    time.Duration // automatic retry after restart_limit
	// StartTimeout is how long STARTING waits for the source before trying
	// the next one (default no_packets + confirm_window).
	StartTimeout time.Duration
	// SwitchTimeout is how long each candidate gets during SWITCHING.
	SwitchTimeout time.Duration
	// FreshPacket is how recent the last packet must be for a candidate
	// source to be accepted.
	FreshPacket time.Duration
	// ConnectGrace adds time for sources that must connect before the first
	// packet (SRT, RTMP, RTSP, HLS pulled through ffmpeg), indexed by
	// source. UDP sources need none.
	ConnectGrace []time.Duration
}

func (p Policy) grace(idx int) time.Duration {
	if idx >= 0 && idx < len(p.ConnectGrace) {
		return p.ConnectGrace[idx]
	}
	return 0
}

// NewPolicy resolves a failover policy with the defaults of spec 8.5.
func NewPolicy(fp model.FailoverPolicy) Policy {
	fp = fp.WithDefaults()
	return Policy{
		FailoverPolicy:    fp,
		RestartLimit:      5,
		RestartWindow:     10 * time.Minute,
		BackoffMin:        time.Second,
		BackoffMax:        30 * time.Second,
		BackoffResetAfter: 60 * time.Second,
		FailedCooldown:    10 * time.Minute,
		StartTimeout:      model.Seconds(fp.NoPacketsSeconds + fp.ConfirmWindowSeconds),
		SwitchTimeout:     model.Seconds(fp.NoPacketsSeconds),
		FreshPacket:       time.Second,
	}
}

// Desired is what the agent must make true after each call.
type Desired struct {
	Active     int   // index of the active source (sorted by priority), -1 = none
	Listen     []int // sources that must be received
	Pipeline   bool  // the media pipeline must be running
	Generation int   // a change means "restart the pipeline"
}

// Transition is a state change.
type Transition struct {
	From, To model.ChannelState
	Reason   string
	At       time.Time
}

// Switch is a change of the active source.
type Switch struct {
	From, To int
	Reason   string
	At       time.Time
}

// Restart is a scheduled pipeline restart.
type Restart struct {
	Reason  string
	Attempt int
	Delay   time.Duration
	At      time.Time
}

// Output collects what happened during a call.
type Output struct {
	Transitions []Transition
	Switches    []Switch
	Restarts    []Restart
}

// Machine is the per-channel state machine. It is pure: time and
// measurements are inputs, so it can be tested without real streams. It is
// not safe for concurrent use.
type Machine struct {
	p        Policy
	n        int
	state    model.ChannelState
	since    time.Time
	reason   string
	active   int
	prev     model.ChannelState // state to return to after DEGRADED
	degraded time.Time
	lowSince map[int]time.Time

	candidate      int
	candidateSince time.Time
	tried          map[int]bool

	recoverSince time.Time
	manualHold   bool

	generation      int
	pipelineWanted  bool
	pipelineSince   time.Time
	restartAt       time.Time
	restarts        []time.Time
	backoff         time.Duration
	failedByRestart bool
	startingSince   time.Time

	out Output
}

// NewMachine creates a stopped machine for n sources.
func NewMachine(p Policy, n int) *Machine {
	return &Machine{p: p, n: n, state: model.StateStopped, active: -1, lowSince: map[int]time.Time{}}
}

// State returns the current state, since when and why.
func (m *Machine) State() (model.ChannelState, time.Time, string) { return m.state, m.since, m.reason }

// Active returns the active source index (-1 when none).
func (m *Machine) Active() int { return m.active }

// SetPolicy replaces the thresholds (configuration change).
func (m *Machine) SetPolicy(p Policy) { m.p = p }

func (m *Machine) to(now time.Time, s model.ChannelState, reason string) {
	if s == m.state && reason == m.reason {
		return
	}
	if s != m.state {
		m.out.Transitions = append(m.out.Transitions, Transition{From: m.state, To: s, Reason: reason, At: now})
		m.since = now
	}
	m.state = s
	m.reason = reason
	if s == model.StateStarting {
		m.startingSince = now
	}
}

func (m *Machine) flush() Output {
	o := m.out
	m.out = Output{}
	return o
}

func (m *Machine) restartPipeline() {
	m.generation++
	m.pipelineWanted = true
	m.restartAt = time.Time{}
}

func (m *Machine) runningState() model.ChannelState {
	if m.active == 0 {
		return model.StateRunning
	}
	return model.StateOnBackup
}

// Start begins operation on the primary source.
func (m *Machine) Start(now time.Time) Output {
	m.active = 0
	m.manualHold = false
	m.restarts = nil
	m.backoff = 0
	m.failedByRestart = false
	m.tried = nil
	m.restartPipeline()
	m.to(now, model.StateStarting, "start")
	return m.flush()
}

// Stop stops everything.
func (m *Machine) Stop(now time.Time) Output {
	m.pipelineWanted = false
	m.restartAt = time.Time{}
	m.active = -1
	m.to(now, model.StateStopped, "stop")
	return m.flush()
}

// Restart restarts the pipeline on request of the operator. Manual restarts
// do not count toward the restart limit.
func (m *Machine) Restart(now time.Time) Output {
	switch m.state {
	case model.StateStopped:
		return m.flush()
	case model.StateFailed:
		if m.active < 0 {
			m.active = 0
		}
		m.restarts = nil
		m.failedByRestart = false
		m.restartPipeline()
		m.to(now, model.StateStarting, "manual_restart")
	default:
		m.restartPipeline()
		m.out.Restarts = append(m.out.Restarts, Restart{Reason: "manual", At: now})
	}
	return m.flush()
}

// ManualSwitch activates source idx. Choosing a non-primary source holds it:
// the automatic return to the primary is suspended until the next manual
// switch, restart or start.
func (m *Machine) ManualSwitch(now time.Time, idx int) (Output, error) {
	if idx < 0 || idx >= m.n {
		return Output{}, fmt.Errorf("source index %d out of range", idx)
	}
	if m.state == model.StateStopped {
		return Output{}, fmt.Errorf("channel is stopped")
	}
	m.manualHold = idx != 0
	m.switchTo(now, idx, "manual")
	return m.flush(), nil
}

func (m *Machine) switchTo(now time.Time, idx int, reason string) {
	if idx != m.active {
		m.out.Switches = append(m.out.Switches, Switch{From: m.active, To: idx, Reason: reason, At: now})
	}
	m.active = idx
	m.tried = nil
	m.restartPipeline()
	m.to(now, m.runningState(), reason)
}

// PipelineStarted records that the pipeline process is up.
func (m *Machine) PipelineStarted(now time.Time) { m.pipelineSince = now }

// PipelineExited handles an unexpected end of the pipeline: exponential
// backoff, and FAILED after RestartLimit restarts within RestartWindow.
func (m *Machine) PipelineExited(now time.Time, cause string) Output {
	if m.state == model.StateStopped || !m.pipelineWanted {
		return m.flush()
	}
	if !m.pipelineSince.IsZero() && now.Sub(m.pipelineSince) >= m.p.BackoffResetAfter {
		m.backoff = 0
	}
	m.pipelineSince = time.Time{}
	m.restarts = append(m.restarts, now)
	keep := m.restarts[:0]
	for _, t := range m.restarts {
		if now.Sub(t) < m.p.RestartWindow {
			keep = append(keep, t)
		}
	}
	m.restarts = keep
	m.pipelineWanted = false
	if len(m.restarts) > m.p.RestartLimit {
		m.failedByRestart = true
		m.to(now, model.StateFailed, "restart_limit")
		return m.flush()
	}
	if m.backoff == 0 {
		m.backoff = m.p.BackoffMin
	} else {
		m.backoff *= 2
		if m.backoff > m.p.BackoffMax {
			m.backoff = m.p.BackoffMax
		}
	}
	m.restartAt = now.Add(m.backoff)
	m.out.Restarts = append(m.out.Restarts, Restart{Reason: cause, Attempt: len(m.restarts), Delay: m.backoff, At: now})
	m.to(now, model.StateStarting, "pipeline_exited")
	return m.flush()
}

// problem evaluates one source; it also maintains the low-bitrate timers.
func (m *Machine) problem(idx int, h SourceHealth, now time.Time) string {
	if h.LastPacketAt.IsZero() || now.Sub(h.LastPacketAt) > model.Seconds(m.p.NoPacketsSeconds) {
		delete(m.lowSince, idx)
		return "no_packets"
	}
	if h.AvgBitrateBps > 0 && h.BitrateBps < m.p.LowBitrateRatio*h.AvgBitrateBps {
		t, ok := m.lowSince[idx]
		if !ok {
			m.lowSince[idx] = now
			t = now
		}
		if now.Sub(t) >= model.Seconds(m.p.LowBitrateSeconds) {
			return "low_bitrate"
		}
	} else {
		delete(m.lowSince, idx)
	}
	missing := model.Seconds(m.p.MissingStreamSeconds)
	ref := func(t time.Time) time.Time {
		if t.IsZero() {
			return h.FirstPacketAt
		}
		return t
	}
	if !h.PMTSeen {
		if now.Sub(h.FirstPacketAt) > missing {
			return "no_pmt"
		}
		return ""
	}
	if h.HasVideo && now.Sub(ref(h.LastVideoAt)) > missing {
		return "no_video"
	}
	if h.HasAudio && now.Sub(ref(h.LastAudioAt)) > missing {
		return "no_audio"
	}
	return ""
}

func (m *Machine) fresh(h SourceHealth, now time.Time) bool {
	return !h.LastPacketAt.IsZero() && now.Sub(h.LastPacketAt) <= m.p.FreshPacket && h.PMTSeen
}

func (m *Machine) beginSwitch(now time.Time, reason string) {
	m.tried = map[int]bool{m.active: true}
	next := m.nextCandidate()
	if next < 0 {
		m.to(now, model.StateFailed, "no_source_available")
		m.pipelineWanted = false
		return
	}
	m.candidate = next
	m.candidateSince = now
	m.to(now, model.StateSwitching, reason)
}

// nextCandidate returns the next untried source after the active one, in
// priority order and wrapping around.
func (m *Machine) nextCandidate() int {
	for k := 1; k <= m.n; k++ {
		i := (m.active + k) % m.n
		if !m.tried[i] {
			return i
		}
	}
	return -1
}

// Tick evaluates the measurements. health is indexed by source index and
// only contains the sources currently received.
func (m *Machine) Tick(now time.Time, health map[int]SourceHealth, pipelineAlive bool) Output {
	probs := map[int]string{}
	for i, h := range health {
		probs[i] = m.problem(i, h, now)
	}
	activeProblem := "no_packets"
	if p, ok := probs[m.active]; ok {
		activeProblem = p
	}

	switch m.state {
	case model.StateStopped:

	case model.StateStarting:
		if !m.restartAt.IsZero() {
			if now.Before(m.restartAt) {
				break
			}
			m.restartPipeline()
		}
		if activeProblem == "" && pipelineAlive {
			m.to(now, m.runningState(), "started")
			break
		}
		if activeProblem != "" && now.Sub(m.startingSince) >= m.p.StartTimeout+m.p.grace(m.active) {
			m.beginSwitch(now, activeProblem)
		}

	case model.StateRunning, model.StateOnBackup, model.StateRecovering:
		if activeProblem != "" {
			m.prev = m.runningState()
			m.degraded = now
			m.to(now, model.StateDegraded, activeProblem)
			break
		}
		if m.active == 0 {
			break
		}
		canReturn := m.p.ReturnsToPrimary() && !m.manualHold
		primary, listening := health[0]
		primaryOK := listening && probs[0] == "" && m.fresh(primary, now)
		switch {
		case m.state == model.StateOnBackup && canReturn && primaryOK:
			m.recoverSince = now
			m.to(now, model.StateRecovering, "primary_seen")
		case m.state == model.StateRecovering && (!canReturn || !primaryOK):
			m.to(now, model.StateOnBackup, "primary_unstable")
		case m.state == model.StateRecovering && now.Sub(m.recoverSince) >= model.Seconds(m.p.PrimaryStableSeconds):
			m.switchTo(now, 0, "primary_recovered")
		}

	case model.StateDegraded:
		if activeProblem == "" {
			m.to(now, m.prev, "recovered")
			break
		}
		if now.Sub(m.degraded) >= model.Seconds(m.p.ConfirmWindowSeconds) {
			m.beginSwitch(now, activeProblem)
		}

	case model.StateSwitching:
		if h, ok := health[m.candidate]; ok && probs[m.candidate] == "" && m.fresh(h, now) {
			m.switchTo(now, m.candidate, "failover")
			break
		}
		if h, ok := health[m.active]; ok && activeProblem == "" && m.fresh(h, now) {
			m.tried = nil
			m.to(now, m.runningState(), "recovered")
			break
		}
		if now.Sub(m.candidateSince) >= m.p.SwitchTimeout+m.p.grace(m.candidate) {
			m.tried[m.candidate] = true
			next := m.nextCandidate()
			if next < 0 {
				m.pipelineWanted = false
				m.to(now, model.StateFailed, "no_source_available")
				break
			}
			m.candidate = next
			m.candidateSince = now
		}

	case model.StateFailed:
		if m.failedByRestart {
			if now.Sub(m.since) >= m.p.FailedCooldown {
				m.restarts = nil
				m.failedByRestart = false
				m.backoff = 0
				m.restartPipeline()
				m.to(now, model.StateStarting, "cooldown_elapsed")
			}
			break
		}
		for i := 0; i < m.n; i++ {
			if h, ok := health[i]; ok && probs[i] == "" && m.fresh(h, now) {
				m.active = i
				m.restartPipeline()
				m.to(now, model.StateStarting, "source_available")
				break
			}
		}
	}
	return m.flush()
}

// Desired returns what the agent must enforce now.
func (m *Machine) Desired() Desired {
	d := Desired{Active: m.active, Generation: m.generation}
	add := func(i int) {
		if i < 0 || i >= m.n {
			return
		}
		for _, v := range d.Listen {
			if v == i {
				return
			}
		}
		d.Listen = append(d.Listen, i)
	}
	switch m.state {
	case model.StateStopped:
		d.Active = -1
		return d
	case model.StateSwitching:
		add(m.active)
		add(m.candidate)
	case model.StateOnBackup, model.StateRecovering:
		add(m.active)
		if m.p.ReturnsToPrimary() && !m.manualHold {
			add(0)
		}
	case model.StateFailed:
		if m.failedByRestart {
			add(m.active)
		} else {
			for i := 0; i < m.n; i++ {
				add(i)
			}
		}
	default:
		add(m.active)
	}
	d.Pipeline = m.pipelineWanted && m.restartAt.IsZero() && m.state != model.StateFailed
	return d
}
