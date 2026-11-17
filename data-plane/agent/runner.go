package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nexostream/internal/model"
	"nexostream/internal/protocol"
	"nexostream/internal/redact"
	"nexostream/media-engine/health"
	"nexostream/media-engine/ingest"
	"nexostream/media-engine/packaging"
	"nexostream/media-engine/pipeline"
)

// tickInterval is how often a runner evaluates health.
const tickInterval = 200 * time.Millisecond

// pulledConnectGrace is the extra time given to sources pulled through
// ffmpeg (SRT, RTMP, RTSP, HLS) to connect and deliver the first packets.
const pulledConnectGrace = 15 * time.Second

// stallTimeout restarts a pipeline that receives input but produces nothing.
const stallTimeout = 20 * time.Second

// runner supervises one channel on this node: it owns the source receivers,
// the state machine, the pipeline process and the TS relay.
type runner struct {
	a       *Agent
	spec    protocol.ChannelSpec
	id      string
	sources []model.Source
	log     *slog.Logger
	m       *health.Machine

	cmds chan func()
	stop chan struct{}
	done chan struct{}

	recv   map[int]*sourceRecv
	active atomic.Int64
	feed   atomic.Pointer[feeder]

	proc        pipeline.Process
	procGen     int
	procPlan    *pipeline.Plan
	procSig     string
	procStarted time.Time
	stopping    bool
	stallCause  string
	lastOut     time.Duration
	lastOutAt   time.Time
	lastErr     string

	relay   *packaging.TSRelay
	pushers []*pusher

	// counters for metric deltas
	activeIdx     int
	lastBytes     int64
	lastCC        int64
	lastDropped   int64
	lastRelayOut  int64
	lastSeq       int64
	playlistCheck time.Time
	rec           *dvrRecorder
	ccWin         *health.Window

	capture atomic.Pointer[capture]
	probed  struct {
		sync.Mutex
		video *model.VideoInfo
		audio []model.AudioInfo
		sig   string
		idx   int // source the result belongs to
	}

	statusMu      sync.Mutex
	status        model.ChannelStatus
	lastPublished time.Time
	lastPubState  model.ChannelState
	lastPubActive int
	dropped       atomic.Int64
}

type sourceRecv struct {
	idx      int
	cancel   context.CancelFunc
	an       *ingest.Analyzer
	rate     *health.Bitrate
	done     chan struct{}
	err      atomic.Pointer[string]
	failedAt time.Time
}

// feeder copies input packets to the pipeline without blocking receivers.
type feeder struct {
	mu     sync.RWMutex
	ch     chan []byte
	closed bool
}

// send queues b; it returns false when the queue is full or closed.
func (f *feeder) send(b []byte) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return true
	}
	select {
	case f.ch <- append([]byte(nil), b...):
		return true
	default:
		return false
	}
}

func (f *feeder) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.ch)
	}
}

// capture keeps the first seconds of a source for ffprobe.
type capture struct {
	mu      sync.Mutex
	buf     []byte
	start   time.Time
	full    bool
	idx     int
	attempt int
}

const (
	captureBytes = 8 << 20
	// captureTime should contain at least one keyframe (SPS/PPS); when the
	// probe still finds no resolution, another capture is taken.
	captureTime   = 5 * time.Second
	probeAttempts = 5
)

func newRunner(a *Agent, spec protocol.ChannelSpec) *runner {
	ch := spec.Channel
	pol := health.NewPolicy(ch.Failover)
	for _, src := range ch.SortedSources() {
		var g time.Duration
		if a, err := ingest.ParseSourceURL(src.URL); err == nil && a.Pulled {
			g = pulledConnectGrace
		}
		pol.ConnectGrace = append(pol.ConnectGrace, g)
	}
	r := &runner{
		a:       a,
		spec:    spec,
		id:      ch.ID,
		sources: ch.SortedSources(),
		log:     a.log.With("channel_id", ch.ID),
		cmds:    make(chan func(), 16),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		recv:    map[int]*sourceRecv{},
		ccWin:   health.NewWindow(time.Minute),
	}
	r.m = health.NewMachine(pol, len(r.sources))
	r.active.Store(-1)
	r.activeIdx = -1
	r.rec = newDVRRecorder(r)
	r.status = model.ChannelStatus{ChannelID: r.id, State: model.StateStopped, WorkerID: a.cfg.WorkerID, Revision: ch.Revision}
	// The relay feeds MPEG-TS over HTTP (never in clear for a protected
	// channel) and the push destinations.
	if (ch.Outputs.MPEGTSHTTP && !ch.DRM.Enabled) || len(ch.Outputs.Push) > 0 {
		relay, err := packaging.NewTSRelay()
		if err != nil {
			r.lastErr = err.Error()
			r.log.Error("ts relay", "error", err)
		} else {
			r.relay = relay
			for _, dst := range ch.Outputs.Push {
				r.pushers = append(r.pushers, startPusher(dst, relay, a.cfg.FFmpegPath, r.log))
			}
		}
	}
	return r
}

// overlay returns the logo of the channel, or nil. A missing image does not
// stop the channel: it goes on air without the logo and the reason is logged.
func (r *runner) overlay() *pipeline.Overlay {
	o := r.spec.Channel.Overlay
	if !o.Enabled {
		return nil
	}
	dir := r.a.cfg.OverlayDir
	if dir == "" || !model.ValidOverlayImage(o.Image) {
		r.log.Error("overlay not applied: no overlay folder on this node or invalid image name", "image", o.Image)
		return nil
	}
	path := filepath.Join(dir, o.Image)
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		r.log.Error("overlay not applied: image not found on this node", "path", path)
		return nil
	}
	opacity := o.Opacity
	if opacity <= 0 {
		opacity = 1
	}
	return &pipeline.Overlay{Image: path, Position: o.Position, Margin: o.Margin, Width: o.Width, Opacity: opacity}
}

// mediaKey identifies the configuration that affects the media pipeline.
// Changes outside it (name, group) do not restart the channel.
func mediaKey(spec protocol.ChannelSpec) string {
	c := spec.Channel
	c.Name, c.Group, c.Revision, c.Enabled = "", "", 0, false
	c.CreatedAt, c.UpdatedAt = time.Time{}, time.Time{}
	// DVR settings are read on every segment and the key period on every
	// request: changing them never restarts the pipeline.
	c.DVR = model.DVRConfig{Enabled: c.DVR.Enabled}
	c.DRM = model.DRMConfig{Enabled: c.DRM.Enabled}
	// Access control and the guide never touch the pipeline.
	c.Auth, c.EPG = model.AuthConfig{}, model.EPGConfig{}
	b, _ := json.Marshal(protocol.ChannelSpec{Channel: c, Profiles: spec.Profiles})
	return string(b)
}

// updateInPlace adopts a new revision whose media configuration is unchanged.
func (r *runner) updateInPlace(spec protocol.ChannelSpec) error {
	return r.do(func() {
		r.spec = spec
		r.statusMu.Lock()
		r.status.Revision = spec.Channel.Revision
		r.statusMu.Unlock()
	})
}

// run is the supervision loop.
func (r *runner) run() {
	defer close(r.done)
	now := time.Now()
	r.handle(r.m.Start(now))
	r.reconcile(now)

	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		var procDone <-chan struct{}
		if r.proc != nil {
			procDone = r.proc.Done()
		}
		select {
		case <-r.stop:
			r.shutdown()
			return
		case f := <-r.cmds:
			f()
		case <-procDone:
			r.onProcExit(time.Now())
		case now := <-t.C:
			r.tick(now)
		}
	}
}

// do runs f in the runner goroutine and waits for it.
func (r *runner) do(f func()) error {
	ok := make(chan struct{})
	select {
	case r.cmds <- func() { f(); close(ok) }:
	case <-r.done:
		return fmt.Errorf("channel %s is not running", r.id)
	}
	select {
	case <-ok:
		return nil
	case <-r.done:
		return fmt.Errorf("channel %s is not running", r.id)
	}
}

// terminate stops the runner. final=true means the channel was stopped by
// the operator (as opposed to the agent shutting down).
func (r *runner) terminate(final bool) {
	if final {
		_ = r.do(func() {
			now := time.Now()
			r.handle(r.m.Stop(now))
			r.updateStatus(now)
			r.publishStatus(true)
		})
	}
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
	<-r.done
}

func (r *runner) shutdown() {
	r.stopProc()
	for i := range r.recv {
		r.stopRecv(i)
	}
	for _, p := range r.pushers {
		p.stop()
	}
	if r.relay != nil {
		_ = r.relay.Close()
	}
	r.a.metrics.forget(r.id)
}

func (r *runner) restart() error {
	return r.do(func() {
		now := time.Now()
		r.handle(r.m.Restart(now))
		r.reconcile(now)
	})
}

func (r *runner) switchSource(priority int) error {
	idx := -1
	for i, s := range r.sources {
		if s.Priority == priority {
			idx = i
		}
	}
	if idx < 0 {
		return fmt.Errorf("channel %s has no source with priority %d", r.id, priority)
	}
	var err error
	if e := r.do(func() {
		now := time.Now()
		var out health.Output
		out, err = r.m.ManualSwitch(now, idx)
		if err == nil {
			r.handle(out)
			r.reconcile(now)
		}
	}); e != nil {
		return e
	}
	return err
}

func (r *runner) tick(now time.Time) {
	hs := map[int]health.SourceHealth{}
	for i, rc := range r.recv {
		if rc.cancel == nil {
			continue
		}
		s := rc.an.Snapshot()
		rc.rate.Sample(now, s.Bytes)
		hs[i] = health.SourceHealth{
			FirstPacketAt: s.FirstPacketAt, LastPacketAt: s.LastPacketAt,
			BitrateBps: rc.rate.Current(), AvgBitrateBps: rc.rate.Average(),
			PMTSeen: s.PMTSeen, HasVideo: s.HasVideo(), HasAudio: s.HasAudio(),
			LastVideoAt: s.LastVideoAt, LastAudioAt: s.LastAudioAt,
		}
	}
	r.handle(r.m.Tick(now, hs, r.proc != nil))
	r.reconcile(now)
	r.checkStall(now)
	r.updateStatus(now)
	r.updateMetrics(now)
	r.rec.tick(now)
	r.publishStatus(false)
}

// handle turns machine outputs into events, logs and metrics.
func (r *runner) handle(o health.Output) {
	for _, t := range o.Transitions {
		r.log.Info("state changed", "from", t.From, "to", t.To, "reason", t.Reason)
		ev := model.NewEvent(model.EventChannelStateChanged, t.At)
		ev.ChannelID, ev.WorkerID, ev.From, ev.To, ev.Reason = r.id, r.a.cfg.WorkerID, t.From, t.To, t.Reason
		r.a.emit(ev)
		r.a.metrics.setState(r.id, t.To)
	}
	for _, s := range o.Switches {
		ev := model.NewEvent(model.EventChannelSourceSwitched, s.At)
		ev.ChannelID, ev.WorkerID, ev.Reason = r.id, r.a.cfg.WorkerID, s.Reason
		ev.Details = map[string]any{"from_priority": r.priorityOf(s.From), "to_priority": r.priorityOf(s.To), "to_url": redact.URL(r.urlOf(s.To))}
		r.log.Warn("source switched", "from", r.priorityOf(s.From), "to", r.priorityOf(s.To), "reason", s.Reason)
		r.a.emit(ev)
		r.a.metrics.switches.WithLabelValues(r.id).Inc()
	}
	for _, rs := range o.Restarts {
		ev := model.NewEvent(model.EventChannelPipelineRestart, rs.At)
		ev.ChannelID, ev.WorkerID, ev.Reason = r.id, r.a.cfg.WorkerID, rs.Reason
		ev.Details = map[string]any{"attempt": rs.Attempt, "delay_seconds": rs.Delay.Seconds()}
		if r.lastErr != "" {
			ev.Details["last_error"] = r.lastErr
		}
		r.a.emit(ev)
		r.a.metrics.restarts.WithLabelValues(r.id).Inc()
	}
}

func (r *runner) priorityOf(i int) int {
	if i < 0 || i >= len(r.sources) {
		return 0
	}
	return r.sources[i].Priority
}

func (r *runner) urlOf(i int) string {
	if i < 0 || i >= len(r.sources) {
		return ""
	}
	return r.sources[i].URL
}

// reconcile makes receivers and pipeline match the machine's Desired.
func (r *runner) reconcile(now time.Time) {
	d := r.m.Desired()
	want := map[int]bool{}
	for _, i := range d.Listen {
		want[i] = true
	}
	for i := range r.recv {
		if !want[i] {
			r.stopRecv(i)
		}
	}
	for i := range want {
		rc, ok := r.recv[i]
		switch {
		case !ok:
			r.startRecv(i)
		case rc.cancel == nil && now.Sub(rc.failedAt) > 2*time.Second:
			delete(r.recv, i)
			r.startRecv(i)
		}
	}
	if d.Active != r.activeIdx {
		r.activeIdx = d.Active
		r.active.Store(int64(d.Active))
		r.lastBytes, r.lastCC = 0, 0
		if rc, ok := r.recv[d.Active]; ok {
			s := rc.an.Snapshot()
			r.lastBytes, r.lastCC = s.Bytes, s.CCErrors
		}
		r.ccWin = health.NewWindow(time.Minute)
		r.capture.Store(&capture{start: now, idx: d.Active})
	}

	if !d.Pipeline {
		r.stopProc()
		return
	}
	if r.proc != nil && r.procGen != d.Generation {
		r.stopProc()
	}
	rc, ok := r.recv[d.Active]
	if !ok {
		return
	}
	snap := rc.an.Snapshot()
	if r.proc != nil && snap.PMTSeen && snap.StreamsSignature != r.procSig {
		ev := model.NewEvent(model.EventChannelInputChanged, now)
		ev.ChannelID, ev.WorkerID = r.id, r.a.cfg.WorkerID
		ev.Details = map[string]any{"from": r.procSig, "to": snap.StreamsSignature}
		r.a.emit(ev)
		r.log.Warn("input changed, restarting pipeline", "from", r.procSig, "to", snap.StreamsSignature)
		r.stopProc()
	}
	if r.proc == nil && snap.PMTSeen {
		r.startProc(now, d.Generation, snap)
	}
}

func (r *runner) startRecv(i int) {
	src := r.sources[i]
	iface := src.Interface
	rcv, err := ingest.Open(ingest.ReceiverConfig{URL: src.URL, Interface: iface, ReadBuffer: r.a.conf().ReadBuffer, FFmpegPath: r.a.cfg.FFmpegPath})
	sr := &sourceRecv{idx: i, an: ingest.NewAnalyzer(0), rate: health.NewBitrate(), done: make(chan struct{})}
	r.recv[i] = sr
	if err != nil {
		msg := err.Error()
		sr.err.Store(&msg)
		sr.failedAt = time.Now()
		close(sr.done)
		r.lastErr = msg
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	sr.cancel = cancel
	go func() {
		defer close(sr.done)
		err := rcv.Run(ctx, func(b []byte) {
			now := time.Now()
			sr.an.Feed(b, now)
			if int(r.active.Load()) != i {
				return
			}
			if c := r.capture.Load(); c != nil && c.idx == i {
				c.add(b, now)
			}
			if f := r.feed.Load(); f != nil && !f.send(b) {
				r.dropped.Add(int64(len(b)))
			}
		})
		if err != nil && ctx.Err() == nil {
			msg := err.Error()
			sr.err.Store(&msg)
			r.log.Error("receiver failed", "url", redact.URL(src.URL), "error", redact.Text(err.Error()))
			_ = r.do(func() {
				if cur, ok := r.recv[i]; ok && cur == sr {
					cur.cancel = nil
					cur.failedAt = time.Now()
					r.lastErr = msg
				}
			})
		}
	}()
}

func (r *runner) stopRecv(i int) {
	rc, ok := r.recv[i]
	if !ok {
		return
	}
	delete(r.recv, i)
	if rc.cancel != nil {
		rc.cancel()
	}
}

func (r *runner) buildPlan(snap ingest.Snapshot) (pipeline.Plan, error) {
	ch := r.spec.Channel
	var prof *model.TranscodeProfile
	if len(ch.Profiles) > 0 {
		for _, p := range r.spec.Profiles {
			if p.ID == ch.Profiles[0] {
				p := p
				prof = &p
			}
		}
		if prof == nil {
			return pipeline.Plan{}, fmt.Errorf("profile %q not provided", ch.Profiles[0])
		}
	}
	var def *model.TranscodeProfile
	for _, p := range r.spec.Profiles {
		if p.ID == r.a.conf().DefaultProfile {
			p := p
			def = &p
		}
	}
	if def == nil {
		for _, p := range model.DefaultProfiles() {
			if p.ID == r.a.conf().DefaultProfile {
				p := p
				def = &p
			}
		}
	}
	in := pipeline.Input{Program: snap.Program}
	if v := snap.Video(); v != nil {
		in.VideoCodec = v.Codec
	}
	if a := snap.Audio(); a != nil {
		in.AudioCodec = a.Codec
	}
	for _, es := range snap.Streams {
		switch {
		case es.Kind == "audio":
			in.AudioCodecs = append(in.AudioCodecs, es.Codec)
		case es.Codec == "dvb_subtitle" || es.Codec == "teletext":
			in.SubtitleTracks++
		}
	}
	var ladder []model.TranscodeProfile
	if ch.Mode == model.ModeTranscode && len(ch.Profiles) > 1 {
		for _, id := range ch.Profiles {
			found := false
			for _, p := range r.spec.Profiles {
				if p.ID == id {
					ladder, found = append(ladder, p), true
				}
			}
			if !found {
				return pipeline.Plan{}, fmt.Errorf("profile %q not provided", id)
			}
		}
	}
	return pipeline.Decide(ch.Mode, in, prof, pipeline.Options{
		AllowHEVCPassthrough: r.a.conf().AllowHEVCPassthrough,
		DefaultProfile:       def,
		Encoders:             r.a.encHealth.usable(r.a.encoders, time.Now()),
		Ladder:               ladder,
	})
}

func (r *runner) startProc(now time.Time, gen int, snap ingest.Snapshot) {
	plan, err := r.buildPlan(snap)
	if err != nil {
		if r.lastErr != err.Error() {
			r.log.Error("cannot plan pipeline", "error", err)
		}
		r.lastErr = err.Error()
		return
	}
	ch := r.spec.Channel
	spec := pipeline.Spec{ChannelID: r.id, Plan: plan, UDPOutput: ch.Outputs.MPEGTSUDP, Overlay: r.overlay()}
	// The DVR records the live HLS segments, so HLS is produced internally
	// whenever recording is on, even if the HLS output is not published.
	if ch.Outputs.HLS || ch.DVR.Enabled || ch.Outputs.DASH {
		dir := r.a.hlsDir(ch)
		// Switching protection on or off leaves the other folder behind; a
		// clear copy of a protected channel must never stay published.
		_ = os.RemoveAll(r.a.otherHLSDir(ch))
		if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // nginx (another user) must read HLS files
			r.lastErr = err.Error()
			r.log.Error("hls dir", "error", err)
			return
		}
		if err := prepareLayout(dir, len(plan.Renditions)); err != nil {
			r.lastErr = err.Error()
			r.log.Error("hls dir", "error", err)
			return
		}
		if len(plan.Renditions) > 1 {
			if err := packaging.WriteMaster(dir, masterVariants(plan)); err != nil {
				r.lastErr = err.Error()
				r.log.Error("hls master playlist", "error", err)
				return
			}
		}
		h := packaging.DefaultHLS(dir)
		if r.a.conf().HLSSegmentSeconds > 0 {
			h.SegmentSeconds = r.a.conf().HLSSegmentSeconds
		}
		if r.a.conf().HLSListSize > 0 {
			h.ListSize = r.a.conf().HLSListSize
		}
		if ch.Outputs.HLS || ch.DVR.Enabled {
			spec.HLS = &h
		}
		if ch.Outputs.DASH && !ch.DRM.Enabled { // DASH is not encrypted
			if err := os.MkdirAll(filepath.Join(dir, packaging.DASHDir), 0o755); err != nil { //nolint:gosec // read by the HTTP origin
				r.lastErr = err.Error()
				return
			}
			d := h
			spec.DASH = &d
		}
	}
	if r.relay != nil {
		spec.TSRelayURL = r.relay.Addr()
	}
	proc, err := r.a.engine.Start(context.Background(), spec)
	if err != nil {
		r.lastErr = err.Error()
		r.log.Error("pipeline start failed", "error", err)
		r.handle(r.m.PipelineExited(now, "start_failed"))
		return
	}
	f := &feeder{ch: make(chan []byte, 2048)}
	go func(p pipeline.Process) {
		w := p.Input()
		for b := range f.ch {
			if _, err := w.Write(b); err != nil {
				for range f.ch { // drain until closed
				}
				return
			}
		}
	}(proc)
	r.feed.Store(f)
	r.proc, r.procGen, r.procPlan, r.procSig = proc, gen, &plan, snap.StreamsSignature
	r.procStarted = now
	r.stopping, r.stallCause = false, ""
	r.lastOut, r.lastOutAt = 0, now
	r.m.PipelineStarted(now)
	r.log.Info("pipeline running", "processing", plan.Processing, "engine", r.a.engine.Name(), "notes", strings.Join(plan.Notes, "; "))
}

func (r *runner) closeFeed() {
	if f := r.feed.Swap(nil); f != nil {
		f.close()
	}
}

func (r *runner) stopProc() {
	if r.proc == nil {
		return
	}
	r.stopping = true
	r.closeFeed()
	r.proc.Stop(3 * time.Second)
	r.proc = nil
	r.procPlan = nil
}

func (r *runner) onProcExit(now time.Time) {
	if r.proc == nil {
		return
	}
	p := r.proc
	// cause is a short code for events; the text goes to last_error.
	cause := r.stallCause
	if cause == "" {
		cause = "exited"
	}
	msg := "pipeline exited"
	if err := p.Err(); err != nil {
		msg = err.Error()
	}
	if lines := p.LastLog(); len(lines) > 0 {
		msg += ": " + lines[len(lines)-1]
	}
	r.lastErr = msg
	if enc := hwEncoderIn(msg); enc != "" && r.a.encHealth.fail(enc, now) {
		r.log.Error("hardware encoder failed; channels with encoder auto fall back to the next encoder",
			"encoder", enc, "quarantine", hwEncoderQuarantine.String(), "error", msg)
		ev := model.NewEvent(model.EventEncoderFailed, now)
		ev.ChannelID, ev.WorkerID, ev.Reason = r.id, r.a.cfg.WorkerID, enc
		ev.Details = map[string]any{"error": msg, "quarantine_minutes": int(hwEncoderQuarantine / time.Minute)}
		r.a.emit(ev)
	}
	r.closeFeed()
	r.proc, r.procPlan = nil, nil
	r.log.Warn("pipeline exited", "cause", cause, "last_error", r.lastErr)
	r.handle(r.m.PipelineExited(now, cause))
	r.reconcile(now)
}

// checkStall kills a pipeline that gets input but produces no output.
func (r *runner) checkStall(now time.Time) {
	if r.proc == nil || r.stallCause != "" {
		return
	}
	pr := r.proc.Progress()
	if pr.OutTime != r.lastOut {
		r.lastOut, r.lastOutAt = pr.OutTime, now
		return
	}
	rc, ok := r.recv[r.activeIdx]
	if !ok {
		return
	}
	if s := rc.an.Snapshot(); now.Sub(s.LastPacketAt) > time.Second {
		r.lastOutAt = now // no input: not a pipeline problem
		return
	}
	if now.Sub(r.lastOutAt) > stallTimeout {
		r.stallCause = "pipeline_stalled"
		r.log.Warn("pipeline stalled, killing it")
		go r.proc.Stop(0)
	}
}

func (c *capture) add(b []byte, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.full {
		return
	}
	c.buf = append(c.buf, b...)
	if len(c.buf) >= captureBytes || now.Sub(c.start) > captureTime {
		c.full = true
	}
}

func (c *capture) take() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.full || c.buf == nil {
		return nil
	}
	b := c.buf
	c.buf = nil
	return b
}

// maybeProbe runs ffprobe once per active source to fill resolution/FPS.
func (r *runner) maybeProbe(sig string) {
	c := r.capture.Load()
	if c == nil || r.a.cfg.FFprobePath == "-" {
		return
	}
	data := c.take()
	if data == nil {
		return
	}
	program := 0
	if rc, ok := r.recv[r.activeIdx]; ok {
		program = rc.an.Snapshot().Program
	}
	retry := func() {
		if c.attempt+1 < probeAttempts {
			r.capture.CompareAndSwap(c, &capture{start: time.Now(), idx: c.idx, attempt: c.attempt + 1})
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		v, a, err := pipeline.FFprobe(ctx, r.a.cfg.FFprobePath, data, program)
		if err != nil {
			r.log.Debug("ffprobe failed", "error", err, "attempt", c.attempt+1)
			retry()
			return
		}
		incomplete := v != nil && (v.Width == 0 || v.Height == 0)
		r.probed.Lock()
		// Never replace a complete result of this source with a partial one.
		if !incomplete || r.probed.idx != c.idx || r.probed.video == nil {
			r.probed.video, r.probed.audio, r.probed.sig, r.probed.idx = v, a, sig, c.idx
		}
		r.probed.Unlock()
		if incomplete {
			r.log.Debug("ffprobe found no keyframe, capturing again", "attempt", c.attempt+1)
			retry()
		}
	}()
}
