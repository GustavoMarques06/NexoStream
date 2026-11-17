// Package recovery contains the recovery scenarios of docs/operations.md
// section 7.3, run against the real agent with synthetic UDP sources and a
// fake media engine (no ffmpeg needed).
package recovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"nexostream/data-plane/agent"
	"nexostream/internal/bus"
	"nexostream/internal/logging"
	"nexostream/internal/model"
	"nexostream/internal/protocol"
	"nexostream/media-engine/ingest/tstest"
	"nexostream/media-engine/pipeline/pipelinetest"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.LocalAddr().(*net.UDPAddr).Port
}

// source is a controllable synthetic UDP source.
type source struct {
	addr   string
	cancel context.CancelFunc
}

func (s *source) start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go func() { _ = tstest.Send(ctx, tstest.New(), s.addr, 2_000_000) }()
}

func (s *source) stop() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

type env struct {
	t      *testing.T
	agent  *agent.Agent
	engine *pipelinetest.Engine
	bus    *bus.Memory
	mu     sync.Mutex
	events []model.Event
	cancel context.CancelFunc
	done   chan struct{}
}

func newEnv(t *testing.T) *env {
	b := bus.NewMemory()
	eng := &pipelinetest.Engine{}
	a, err := agent.New(agent.Config{
		WorkerID: "media-test", HLSDir: t.TempDir(), StateDir: t.TempDir(),
		FFprobePath: "-", Encoders: []string{"cpu"},
	}, b, eng, logging.Discard())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, agent: a, engine: eng, bus: b, done: make(chan struct{})}
	if _, err := b.ConsumeDurable(context.Background(), "test", protocol.EventSubjects, func(_ string, d []byte) error {
		var ev model.Event
		_ = json.Unmarshal(d, &ev)
		e.mu.Lock()
		e.events = append(e.events, ev)
		e.mu.Unlock()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	go func() { defer close(e.done); _ = a.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-e.done; b.Close() })
	return e
}

func (e *env) waitState(id string, want model.ChannelState, timeout time.Duration) model.ChannelStatus {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	var st model.ChannelStatus
	for time.Now().Before(deadline) {
		st, _ = e.agent.Status(id)
		if st.State == want {
			return st
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("channel %s: state %s (%s, err=%q), want %s", id, st.State, st.Reason, st.LastError, want)
	return st
}

func (e *env) hasEvent(typ string, match func(model.Event) bool) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range e.events {
		if ev.Type == typ && (match == nil || match(ev)) {
			return true
		}
	}
	return false
}

func fastFailover() model.FailoverPolicy {
	t := true
	return model.FailoverPolicy{
		NoPacketsSeconds: 1, ConfirmWindowSeconds: 1, MissingStreamSeconds: 2,
		ReturnToPrimary: &t, PrimaryStableSeconds: 3,
	}
}

func channel(id string, ports ...int) model.Channel {
	ch := model.Channel{ID: id, Name: id, Enabled: true, Mode: model.ModeAuto, Revision: 1,
		Outputs: model.Outputs{HLS: true, MPEGTSHTTP: true}, Failover: fastFailover()}
	for i, p := range ports {
		ch.Sources = append(ch.Sources, model.Source{ID: fmt.Sprint(i + 1), Priority: i + 1, URL: fmt.Sprintf("udp://127.0.0.1:%d", p)})
	}
	return ch
}

func TestSourceFailoverAndReturn(t *testing.T) {
	e := newEnv(t)
	p1, p2 := freePort(t), freePort(t)
	primary := &source{addr: fmt.Sprintf("127.0.0.1:%d", p1)}
	backup := &source{addr: fmt.Sprintf("127.0.0.1:%d", p2)}
	primary.start()
	backup.start()
	defer primary.stop()
	defer backup.stop()

	if err := e.agent.StartChannel(protocol.ChannelSpec{Channel: channel("canal-001", p1, p2)}); err != nil {
		t.Fatal(err)
	}
	st := e.waitState("canal-001", model.StateRunning, 5*time.Second)
	if st.Processing != "passthrough" || st.ActiveSource.Priority != 1 {
		t.Fatalf("status=%+v", st)
	}
	if st.Input == nil || st.Input.Video == nil || st.Input.Video.Codec != "h264" || st.Input.BitrateBps == 0 {
		t.Fatalf("input=%+v", st.Input)
	}
	time.Sleep(500 * time.Millisecond)
	if e.engine.Last().Bytes() == 0 {
		t.Fatal("pipeline received no input")
	}

	// Queda da fonte principal: DEGRADED -> SWITCHING -> ON_BACKUP.
	primary.stop()
	e.waitState("canal-001", model.StateDegraded, 3*time.Second)
	st = e.waitState("canal-001", model.StateOnBackup, 5*time.Second)
	if st.ActiveSource.Priority != 2 {
		t.Fatalf("active=%+v", st.ActiveSource)
	}
	if e.engine.Starts() < 2 {
		t.Fatalf("pipeline must restart on the backup, starts=%d", e.engine.Starts())
	}

	// Retorno da fonte principal: RECOVERING -> RUNNING after 3 s stable.
	primary.start()
	e.waitState("canal-001", model.StateRecovering, 4*time.Second)
	st = e.waitState("canal-001", model.StateRunning, 6*time.Second)
	if st.ActiveSource.Priority != 1 {
		t.Fatalf("active=%+v", st.ActiveSource)
	}

	time.Sleep(300 * time.Millisecond)
	if !e.hasEvent(model.EventChannelSourceSwitched, func(ev model.Event) bool { return ev.Reason == "failover" }) {
		t.Fatal("missing failover event")
	}
	if !e.hasEvent(model.EventChannelSourceSwitched, func(ev model.Event) bool { return ev.Reason == "primary_recovered" }) {
		t.Fatal("missing primary_recovered event")
	}
	if !e.hasEvent(model.EventChannelStateChanged, func(ev model.Event) bool { return ev.To == model.StateDegraded && ev.Reason == "no_packets" }) {
		t.Fatal("missing DEGRADED event")
	}
}

func TestPipelineCrashRestarts(t *testing.T) {
	e := newEnv(t)
	p1 := freePort(t)
	src := &source{addr: fmt.Sprintf("127.0.0.1:%d", p1)}
	src.start()
	defer src.stop()

	if err := e.agent.StartChannel(protocol.ChannelSpec{Channel: channel("canal-002", p1)}); err != nil {
		t.Fatal(err)
	}
	e.waitState("canal-002", model.StateRunning, 5*time.Second)
	e.engine.Last().Crash() // kill -9 equivalent
	e.waitState("canal-002", model.StateStarting, 2*time.Second)
	e.waitState("canal-002", model.StateRunning, 4*time.Second) // after the 1 s backoff
	if e.engine.Starts() != 2 {
		t.Fatalf("starts=%d", e.engine.Starts())
	}
	time.Sleep(300 * time.Millisecond)
	if !e.hasEvent(model.EventChannelPipelineRestart, nil) {
		t.Fatal("missing pipeline_restarted event")
	}
}

func TestManualSwitchAndStop(t *testing.T) {
	e := newEnv(t)
	p1, p2 := freePort(t), freePort(t)
	s1 := &source{addr: fmt.Sprintf("127.0.0.1:%d", p1)}
	s2 := &source{addr: fmt.Sprintf("127.0.0.1:%d", p2)}
	s1.start()
	s2.start()
	defer s1.stop()
	defer s2.stop()

	if err := e.agent.StartChannel(protocol.ChannelSpec{Channel: channel("canal-003", p1, p2)}); err != nil {
		t.Fatal(err)
	}
	e.waitState("canal-003", model.StateRunning, 5*time.Second)

	// The command path the controller uses.
	cmd, _ := json.Marshal(protocol.Command{Type: protocol.CmdSwitchSource, ChannelID: "canal-003", Priority: 2})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := e.bus.Request(ctx, protocol.WorkerCommand("media-test"), cmd)
	if err != nil {
		t.Fatal(err)
	}
	var rep protocol.Reply
	_ = json.Unmarshal(raw, &rep)
	if !rep.OK {
		t.Fatalf("reply=%+v", rep)
	}
	st := e.waitState("canal-003", model.StateOnBackup, 2*time.Second)
	if st.ActiveSource.Priority != 2 {
		t.Fatalf("active=%+v", st.ActiveSource)
	}
	time.Sleep(5 * time.Second) // longer than primary_stable_seconds: must stay held
	e.waitState("canal-003", model.StateOnBackup, time.Second)

	bad, _ := json.Marshal(protocol.Command{Type: protocol.CmdSwitchSource, ChannelID: "canal-003", Priority: 9})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	raw, err = e.bus.Request(ctx2, protocol.WorkerCommand("media-test"), bad)
	if err != nil {
		t.Fatal(err)
	}
	rep = protocol.Reply{}
	_ = json.Unmarshal(raw, &rep)
	if rep.OK || rep.Error == "" {
		t.Fatalf("switch to an unknown priority must fail: %+v", rep)
	}

	if err := e.agent.StopChannel("canal-003"); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.agent.Status("canal-003"); ok {
		t.Fatal("channel still present after stop")
	}
}

func TestStateSurvivesAgentRestart(t *testing.T) {
	stateDir := t.TempDir()
	p1 := freePort(t)
	src := &source{addr: fmt.Sprintf("127.0.0.1:%d", p1)}
	src.start()
	defer src.stop()

	run := func() (*agent.Agent, context.CancelFunc, chan struct{}) {
		a, err := agent.New(agent.Config{WorkerID: "media-r", HLSDir: t.TempDir(), StateDir: stateDir,
			FFprobePath: "-", Encoders: []string{"cpu"}}, bus.NewMemory(), &pipelinetest.Engine{}, logging.Discard())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = a.Run(ctx) }()
		return a, cancel, done
	}
	a, cancel, done := run()
	time.Sleep(100 * time.Millisecond)
	if err := a.StartChannel(protocol.ChannelSpec{Channel: channel("canal-004", p1)}); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done

	// Control Plane unavailable: the new agent restores the channel alone.
	a2, cancel2, done2 := run()
	defer func() { cancel2(); <-done2 }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := a2.Status("canal-004"); ok && st.State == model.StateRunning {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("channel not restored from local state")
}
