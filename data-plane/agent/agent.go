// Package workers implements the Media Node agent (nexostream-worker). The
// agent receives commands from the controller, runs and supervises one
// pipeline per channel, performs source failover locally, publishes status,
// events, heartbeats and Prometheus metrics, and keeps the last received
// configuration on disk so channels keep running when the Control Plane is
// unavailable (ADR 0001).
package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"nexostream/internal/bus"
	"nexostream/internal/model"
	"nexostream/internal/protocol"
	"nexostream/internal/version"
	"nexostream/media-engine/drm"
	"nexostream/media-engine/dvr"
	"nexostream/media-engine/ingest"
	"nexostream/media-engine/pipeline"
	"nexostream/media-engine/sessions"
)

// Config configures the agent.
type Config struct {
	WorkerID string
	HLSDir   string
	DVRDir   string
	// OverlayDir holds the logo images channels can burn into the video; a
	// channel names a file in it, never a path.
	OverlayDir  string
	StateDir    string // where the last configuration is persisted
	MetricsAddr string // /metrics and /healthz (management network)
	MediaAddr   string // /live/<id>/stream.ts (behind nginx); may equal MetricsAddr
	// TLSCertFile and TLSKeyFile make the media address serve HTTPS (TLS 1.2
	// or newer) so viewers, tokens and DVR playback are encrypted without a
	// proxy in front. When MetricsAddr is a separate address it stays plain
	// HTTP: keep it on the management network.
	TLSCertFile, TLSKeyFile string
	PublicHLSBase           string // e.g. https://origin.example/ (status URLs)
	PublicTSBase            string
	PublicDVRBase           string // e.g. https://origin.example/dvr (catch-up URLs)
	IngestInterfaces        []string
	FFmpegPath              string
	FFprobePath             string // "-" disables ffprobe
	ReadBuffer              int
	HLSSegmentSeconds       int
	HLSListSize             int
	DefaultProfile          string
	AllowHEVCPassthrough    bool
	// DRMKey derives the content keys of protected channels; it must be the
	// same on every Media Node. PlaybackTokenKey checks viewer tokens (same
	// secret as the API). Without them, protected channels are not served.
	DRMKey           []byte
	PlaybackTokenKey []byte
	// Encoders overrides detection (tests); nil = detect with ffmpeg.
	Encoders []string
}

// Agent is the worker agent.
type Agent struct {
	cfg      Config
	bus      bus.Bus
	engine   pipeline.Engine
	log      *slog.Logger
	metrics  *metrics
	sampler  *sampler
	started  time.Time
	encoders []string

	rtMu sync.RWMutex
	rt   *model.WorkerRuntime // settings pushed by the controller (nil = .env only)

	mu      sync.Mutex
	runners map[string]*runner
	specs   map[string]protocol.ChannelSpec

	outbox    chan model.Event
	live      drm.Live // live window of protected channels
	sessions  *sessions.Tracker
	dvrState  *dvr.State // open hour files, failed disks and exports of this node's DVR
	thumbs    thumbs
	encHealth encoderHealth
	closedQ   chan model.PlaySession
}

// New creates an agent.
func New(cfg Config, b bus.Bus, engine pipeline.Engine, log *slog.Logger) (*Agent, error) {
	if cfg.WorkerID == "" {
		return nil, errors.New("worker id is required")
	}
	if cfg.HLSDir == "" {
		cfg.HLSDir = "/var/lib/nexostream/hls"
	}
	if cfg.StateDir == "" {
		cfg.StateDir = "/var/lib/nexostream/worker"
	}
	if cfg.DefaultProfile == "" {
		cfg.DefaultProfile = "hd"
	}
	if cfg.DVRDir == "" {
		cfg.DVRDir = "/srv/nexostream/dvr"
	}
	if log == nil {
		log = slog.Default()
	}
	log = log.With("worker_id", cfg.WorkerID)
	a := &Agent{
		cfg:      cfg,
		bus:      b,
		engine:   engine,
		log:      log,
		metrics:  newMetrics(cfg.WorkerID),
		sampler:  newSampler(cfg.IngestInterfaces),
		started:  time.Now(),
		runners:  map[string]*runner{},
		specs:    map[string]protocol.ChannelSpec{},
		outbox:   make(chan model.Event, 10000),
		sessions: &sessions.Tracker{WorkerID: cfg.WorkerID, Auth: &sessions.Authorizer{}},
		dvrState: dvr.NewState(),
		closedQ:  make(chan model.PlaySession, 20000),
	}
	a.sessions.OnClose = func(s model.PlaySession) {
		select {
		case a.closedQ <- s:
		default: // history is best effort: never block viewers
		}
	}
	return a, nil
}

// Run starts the agent and blocks until ctx is canceled.
func (a *Agent) Run(ctx context.Context) error {
	if err := os.MkdirAll(a.cfg.HLSDir, 0o755); err != nil { //nolint:gosec // nginx (another user) must read HLS files
		return fmt.Errorf("hls dir: %w", err)
	}
	if err := os.MkdirAll(a.cfg.StateDir, 0o750); err != nil {
		return fmt.Errorf("state dir: %w", err)
	}
	a.encoders = a.cfg.Encoders
	if a.encoders == nil {
		a.encoders = pipeline.DetectEncoders(ctx, a.cfg.FFmpegPath)
		if len(a.encoders) == 0 {
			a.log.Warn("no working encoder detected; only passthrough will work (is ffmpeg installed?)")
		}
	}
	a.log.Info("agent starting", "version", version.String(), "engine", a.engine.Name(), "encoders", a.encoders)

	go a.runOutbox(ctx)
	go a.runRetention(ctx)
	go a.runSessions(ctx)
	go a.runSessionLog(ctx)
	go func() {
		share := &sessions.Share{Tracker: a.sessions, Bus: a.bus, Subject: protocol.SessionUsers}
		if err := share.Run(ctx); err != nil {
			a.log.Error("session counts exchange stopped; user limits apply per node", "error", err)
		}
	}()

	sub, err := a.bus.Handle(protocol.WorkerCommand(a.cfg.WorkerID), a.handleCommand)
	if err != nil {
		return fmt.Errorf("subscribe commands: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	// Data Plane independence: restart the channels we were running.
	a.restoreSettings()
	a.restoreState()

	servers := a.startHTTP()
	ev := model.NewEvent(model.EventWorkerOnline, time.Now())
	ev.WorkerID = a.cfg.WorkerID
	ev.Details = map[string]any{"version": version.Version}
	a.emit(ev)

	t := time.NewTicker(protocol.HeartbeatInterval)
	defer t.Stop()
	a.heartbeat(ctx)
	for {
		select {
		case <-ctx.Done():
			a.log.Info("agent stopping")
			for _, s := range servers {
				sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = s.Shutdown(sctx)
				cancel()
			}
			a.stopAll()
			return nil
		case <-t.C:
			a.heartbeat(ctx)
		}
	}
}

func (a *Agent) stopAll() {
	a.mu.Lock()
	rs := make([]*runner, 0, len(a.runners))
	for _, r := range a.runners {
		rs = append(rs, r)
	}
	a.runners = map[string]*runner{}
	a.mu.Unlock()
	var wg sync.WaitGroup
	for _, r := range rs {
		wg.Add(1)
		go func(r *runner) { defer wg.Done(); r.terminate(false) }(r)
	}
	wg.Wait()
}

// MetricsHandler serves /metrics, /healthz and /status.
func (a *Agent) MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(a.metrics.reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(a.Statuses())
	})
	return mux
}

// MediaHandler serves MPEG-TS over HTTP and, in development, the HLS files.
func (a *Agent) MediaHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /live/{id}/stream.ts", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		a.mu.Lock()
		rn := a.runners[id]
		out := a.specs[id].Channel
		a.mu.Unlock()
		// The relay may exist only for push: serve viewers only when the
		// output is on (and never for a protected channel).
		if rn == nil || rn.relay == nil || !out.Outputs.MPEGTSHTTP || out.DRM.Enabled {
			http.NotFound(w, r)
			return
		}
		a.play(w, r, id, "mpegts", true, rn.relay.ServeHTTP)
	})
	// DVR playback (timeshift, catch-up, Flussonic-style URLs) is served by
	// the node that records, like Flussonic.
	exportFFmpeg := a.cfg.FFmpegPath
	if exportFFmpeg == "" {
		exportFFmpeg = "ffmpeg"
	}
	dvrHandler := dvr.Handler{
		Storage: func(id string) (dvr.Storage, bool) {
			a.mu.Lock()
			spec, ok := a.specs[id]
			a.mu.Unlock()
			return a.dvrStorage(spec), ok
		},
		Protect: a.protector,
		FFmpeg:  exportFFmpeg,
	}
	mux.HandleFunc("GET /dvr/", func(w http.ResponseWriter, r *http.Request) {
		ch, file, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/dvr/"), "/")
		if file == "recordings.json" {
			dvrHandler.ServeHTTP(w, r) // metadata, not a viewer
			return
		}
		a.play(w, r, ch, "dvr", false, dvrHandler.ServeHTTP)
	})
	mux.Handle("GET /hls/", http.HandlerFunc(a.serveHLS))
	return mux
}

func (a *Agent) startHTTP() []*http.Server {
	var servers []*http.Server
	tlsOn := a.cfg.TLSCertFile != "" && a.cfg.TLSKeyFile != ""
	serve := func(addr string, h http.Handler, secure bool) {
		if addr == "" {
			return
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			a.log.Error("http listen", "addr", addr, "error", err)
			return
		}
		s := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 64 << 10,
			Addr: ln.Addr().String()} // the real address (the port may have been chosen by the system)
		servers = append(servers, s)
		go func() {
			var err error
			if secure {
				s.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
				err = s.ServeTLS(ln, a.cfg.TLSCertFile, a.cfg.TLSKeyFile)
			} else {
				err = s.Serve(ln)
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				a.log.Error("http server", "addr", addr, "tls", secure, "error", err)
			}
		}()
		a.log.Info("http listening", "addr", addr, "tls", secure)
	}
	if a.cfg.MediaAddr == a.cfg.MetricsAddr {
		mux := http.NewServeMux()
		mux.Handle("/live/", a.MediaHandler())
		mux.Handle("/hls/", a.MediaHandler())
		mux.Handle("/dvr/", a.MediaHandler())
		mux.Handle("/", a.MetricsHandler())
		serve(a.cfg.MetricsAddr, mux, tlsOn)
	} else {
		serve(a.cfg.MetricsAddr, a.MetricsHandler(), false)
		serve(a.cfg.MediaAddr, a.MediaHandler(), tlsOn)
	}
	return servers
}

// dvrStorage returns where a channel records: a named storage (RAID),
// its own folder, or the node default folder.
func (a *Agent) dvrStorage(spec protocol.ChannelSpec) dvr.Storage {
	if st := spec.DVRStorage; st != nil && len(st.Disks) > 0 {
		return dvr.Storage{Mode: st.Mode, Disks: st.Disks, State: a.dvrState}
	}
	root := a.conf().DVRDir
	if p := spec.Channel.DVR.Path; p != "" {
		root = p
	}
	st := dvr.Single(root)
	st.State = a.dvrState
	return st
}

// dvrDisks reports usage and health of every DVR disk known to the node.
func (a *Agent) dvrDisks() []model.DiskUsage {
	defDir := a.conf().DVRDir
	seen := map[string]bool{defDir: true}
	paths := []string{defDir}
	a.mu.Lock()
	for _, s := range a.specs {
		if !s.Channel.DVR.Enabled {
			continue
		}
		for _, d := range a.dvrStorage(s).Disks {
			if !seen[d] {
				seen[d] = true
				paths = append(paths, d)
			}
		}
	}
	a.mu.Unlock()
	out := make([]model.DiskUsage, 0, len(paths))
	for _, p := range paths {
		du := model.DiskUsage{Path: p, Down: a.dvrState.DiskDown(p)}
		if used, total, ok := diskUsage(p); ok {
			du.UsedBytes, du.TotalBytes = used, total
		} else {
			du.Down = du.Down || p != defDir
		}
		out = append(out, du)
	}
	return out
}

// runRetention removes recordings older than each channel's retention.
func (a *Agent) runRetention(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		a.mu.Lock()
		specs := make([]protocol.ChannelSpec, 0, len(a.specs))
		for _, s := range a.specs {
			specs = append(specs, s)
		}
		a.mu.Unlock()
		for _, s := range specs {
			ch := s.Channel
			if !ch.DVR.Enabled || ch.DVR.RetentionHours <= 0 {
				continue
			}
			removed, err := a.dvrStorage(s).Cleanup(ch.ID, time.Now(), time.Duration(ch.DVR.RetentionHours)*time.Hour)
			if err != nil {
				a.log.Error("dvr retention", "channel_id", ch.ID, "error", err)
			} else if len(removed) > 0 {
				a.log.Info("dvr retention removed old hours", "channel_id", ch.ID, "hours", len(removed))
			}
			a.verifyDVR(s)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Statuses returns the status of every channel on the node.
func (a *Agent) Statuses() []model.ChannelStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]model.ChannelStatus, 0, len(a.runners))
	for _, r := range a.runners {
		out = append(out, r.currentStatus())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChannelID < out[j].ChannelID })
	return out
}

// Status returns the status of one channel.
func (a *Agent) Status(id string) (model.ChannelStatus, bool) {
	a.mu.Lock()
	r := a.runners[id]
	a.mu.Unlock()
	if r == nil {
		return model.ChannelStatus{}, false
	}
	return r.currentStatus(), true
}

func (a *Agent) heartbeat(ctx context.Context) {
	a.mu.Lock()
	briefs := make([]protocol.ChannelBrief, 0, len(a.runners))
	for _, r := range a.runners {
		briefs = append(briefs, r.brief())
	}
	a.mu.Unlock()
	sort.Slice(briefs, func(i, j int) bool { return briefs[i].ID < briefs[j].ID })
	a.metrics.workerChannels.WithLabelValues(a.cfg.WorkerID).Set(float64(len(briefs)))
	c := a.conf()
	a.sampler.interfaces = c.IngestInterfaces // heartbeats run on one goroutine
	res := a.sampler.sample(ctx)
	if used, total, ok := diskUsage(c.DVRDir); ok {
		a.metrics.dvrUsed.WithLabelValues(c.DVRDir).Set(float64(used))
		a.metrics.dvrTotal.WithLabelValues(c.DVRDir).Set(float64(total))
		res.DVRUsedBytes, res.DVRTotalBytes = used, total
	}
	res.DVRDisks = a.dvrDisks()
	hb := protocol.Heartbeat{
		WorkerID:  a.cfg.WorkerID,
		Version:   version.Version,
		Protocol:  protocol.Version,
		StartedAt: a.started.UTC(),
		At:        time.Now().UTC(),
		Capabilities: model.Capabilities{
			GPU:        gpuKind(a.encoders),
			Encoders:   a.encHealth.usable(a.encoders, time.Now()),
			Interfaces: a.interfaces(),
			Engine:     a.engine.Name(),
		},
		Resources:   res,
		Channels:    briefs,
		RuntimeHash: a.runtimeHash(),
	}
	a.publishJSON(protocol.WorkerHeartbeat(a.cfg.WorkerID), hb)
}

func gpuKind(encs []string) string {
	for _, e := range encs {
		switch e {
		case string(model.EncoderNVENC):
			return "nvidia"
		case string(model.EncoderQSV):
			return "intel"
		}
	}
	return ""
}

func (a *Agent) interfaces() []string {
	if ifs := a.conf().IngestInterfaces; len(ifs) > 0 {
		return ifs
	}
	ifs, _ := net.Interfaces()
	var out []string
	for _, i := range ifs {
		if i.Flags&net.FlagUp != 0 {
			out = append(out, i.Name)
		}
	}
	return out
}

func (a *Agent) publishJSON(subject string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		a.log.Error("marshal", "subject", subject, "error", err)
		return
	}
	if err := a.bus.Publish(subject, b); err != nil {
		a.log.Debug("publish failed", "subject", subject, "error", err)
	}
}

// emit queues an event for durable publication. Events are kept in memory
// while NATS is unreachable (bounded; the oldest are dropped when full).
func (a *Agent) emit(ev model.Event) {
	for {
		select {
		case a.outbox <- ev:
			return
		default:
			select {
			case old := <-a.outbox:
				a.log.Warn("event outbox full, dropping oldest event", "type", old.Type, "channel_id", old.ChannelID)
			default:
			}
		}
	}
}

func (a *Agent) runOutbox(ctx context.Context) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-a.outbox:
			subject := protocol.ChannelEvent(ev.ChannelID)
			if ev.ChannelID == "" {
				subject = protocol.WorkerEvent(ev.WorkerID)
			}
			data, _ := json.Marshal(ev)
			for {
				pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := a.bus.PublishDurable(pctx, subject, ev.ID, data)
				cancel()
				if err == nil {
					backoff = time.Second
					break
				}
				a.log.Warn("event publish failed, retrying", "type", ev.Type, "error", err, "retry_in", backoff.String())
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < 30*time.Second {
					backoff *= 2
				}
			}
		}
	}
}

// handleCommand answers controller requests.
func (a *Agent) handleCommand(_ string, data []byte) []byte {
	var cmd protocol.Command
	reply := protocol.Reply{OK: true}
	if err := json.Unmarshal(data, &cmd); err != nil {
		reply = protocol.Reply{Error: "invalid command: " + err.Error()}
	} else if err := a.execute(cmd, &reply); err != nil {
		reply = protocol.Reply{Error: err.Error()}
	}
	b, _ := json.Marshal(reply)
	return b
}

func (a *Agent) execute(cmd protocol.Command, reply *protocol.Reply) error {
	switch cmd.Type {
	case protocol.CmdStart:
		if cmd.Spec == nil {
			return errors.New("start requires a spec")
		}
		return a.StartChannel(*cmd.Spec)
	case protocol.CmdStop:
		return a.StopChannel(cmd.ChannelID)
	case protocol.CmdRestart:
		r := a.runner(cmd.ChannelID)
		if r == nil {
			return fmt.Errorf("channel %s is not running on %s", cmd.ChannelID, a.cfg.WorkerID)
		}
		return r.restart()
	case protocol.CmdSwitchSource:
		r := a.runner(cmd.ChannelID)
		if r == nil {
			return fmt.Errorf("channel %s is not running on %s", cmd.ChannelID, a.cfg.WorkerID)
		}
		return r.switchSource(cmd.Priority)
	case protocol.CmdSessions:
		open, closed := a.sessions.List(cmd.ChannelID)
		reply.Sessions = &protocol.SessionsInfo{Open: open, Closed: closed}
		return nil
	case protocol.CmdKillSession:
		if !a.sessions.Kill(cmd.SessionID) {
			return fmt.Errorf("session %s is not open on %s", cmd.SessionID, a.cfg.WorkerID)
		}
		return nil
	case protocol.CmdDVRRanges:
		a.mu.Lock()
		spec, ok := a.specs[cmd.ChannelID]
		a.mu.Unlock()
		if !ok {
			return fmt.Errorf("channel %s is not on %s", cmd.ChannelID, a.cfg.WorkerID)
		}
		to := cmd.To
		if to.IsZero() {
			to = time.Now().Add(time.Minute)
		}
		segs, err := a.dvrStorage(spec).List(cmd.ChannelID, cmd.From, to)
		if err != nil {
			return err
		}
		info := &protocol.DVRInfo{Ranges: []protocol.TimeRange{}, Segments: len(segs)}
		for _, s := range segs {
			info.Bytes += s.Size
		}
		for _, rg := range dvr.Ranges(segs) {
			info.Ranges = append(info.Ranges, protocol.TimeRange{From: rg.From, To: rg.To})
		}
		if base := a.conf().PublicDVRBase; base != "" {
			info.URL = strings.TrimRight(base, "/") + "/" + cmd.ChannelID + "/"
		}
		reply.DVR = info
		return nil
	case protocol.CmdConfigure:
		if cmd.Runtime == nil {
			return errors.New("configure requires a runtime")
		}
		return a.Configure(*cmd.Runtime)
	case protocol.CmdProbe:
		if cmd.Probe == nil {
			return errors.New("probe requires a url")
		}
		res := a.Probe(context.Background(), *cmd.Probe)
		reply.Probe = &res
		return nil
	}
	return fmt.Errorf("unknown command %q", cmd.Type)
}

func (a *Agent) runner(id string) *runner {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runners[id]
}

// StartChannel runs a channel, or replaces it when the revision changed.
// It is idempotent for the same revision.
func (a *Agent) StartChannel(spec protocol.ChannelSpec) error {
	ch := spec.Channel
	if ch.ID == "" || len(ch.Sources) == 0 {
		return errors.New("channel id and at least one source are required")
	}
	if ch.DRM.Enabled && (len(a.cfg.DRMKey) == 0 || len(a.cfg.PlaybackTokenKey) == 0) {
		a.log.Warn("protected channel on a node without DRM secrets: its outputs will not be served", "channel_id", ch.ID)
	}
	for _, s := range ch.Sources {
		if _, err := ingest.ParseSourceURL(s.URL); err != nil {
			return fmt.Errorf("source %d: %w", s.Priority, err)
		}
	}
	a.mu.Lock()
	old := a.runners[ch.ID]
	oldSpec, hadSpec := a.specs[ch.ID]
	if old != nil && hadSpec && oldSpec.Channel.Revision == ch.Revision {
		a.mu.Unlock()
		return nil
	}
	if old != nil && hadSpec && mediaKey(oldSpec) == mediaKey(spec) {
		a.specs[ch.ID] = spec
		a.mu.Unlock()
		if err := old.updateInPlace(spec); err == nil {
			a.saveState()
			return nil
		}
		a.mu.Lock()
	}
	delete(a.runners, ch.ID)
	a.mu.Unlock()
	if old != nil {
		a.log.Info("configuration changed, restarting channel", "channel_id", ch.ID, "revision", ch.Revision)
		old.terminate(false)
	}
	r := newRunner(a, spec)
	a.mu.Lock()
	a.runners[ch.ID] = r
	a.specs[ch.ID] = spec
	a.mu.Unlock()
	a.saveState()
	go r.run()
	return nil
}

// StopChannel stops a channel and forgets it.
func (a *Agent) StopChannel(id string) error {
	a.mu.Lock()
	r := a.runners[id]
	delete(a.runners, id)
	delete(a.specs, id)
	a.mu.Unlock()
	a.saveState()
	if r != nil {
		r.terminate(true)
	}
	a.sessions.CloseChannel(id, "channel_stopped")
	a.thumbs.forget(id)
	return nil
}

func (a *Agent) statePath() string { return filepath.Join(a.cfg.StateDir, "channels.json") }

func (a *Agent) saveState() {
	a.mu.Lock()
	specs := make([]protocol.ChannelSpec, 0, len(a.specs))
	for _, s := range a.specs {
		specs = append(specs, s)
	}
	a.mu.Unlock()
	sort.Slice(specs, func(i, j int) bool { return specs[i].Channel.ID < specs[j].Channel.ID })
	b, err := json.MarshalIndent(specs, "", "  ")
	if err != nil {
		return
	}
	tmp := a.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		a.log.Error("save state", "error", err)
		return
	}
	if err := os.Rename(tmp, a.statePath()); err != nil {
		a.log.Error("save state", "error", err)
	}
}

func (a *Agent) restoreState() {
	b, err := os.ReadFile(a.statePath())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			a.log.Error("read state", "error", err)
		}
		return
	}
	var specs []protocol.ChannelSpec
	if err := json.Unmarshal(b, &specs); err != nil {
		a.log.Error("parse state", "error", err)
		return
	}
	for _, s := range specs {
		if err := a.StartChannel(s); err != nil {
			a.log.Error("restore channel", "channel_id", s.Channel.ID, "error", err)
		}
	}
	if len(specs) > 0 {
		a.log.Info("restored channels from local state", "count", len(specs))
	}
}

// Probe analyzes a source for a few seconds without registering it.
func (a *Agent) Probe(ctx context.Context, req protocol.ProbeRequest) model.ProbeResult {
	res := model.ProbeResult{URL: req.URL, Audio: []model.AudioInfo{}, Streams: []model.Stream{}}
	secs := req.Seconds
	if secs <= 0 || secs > 15 {
		secs = 3
	}
	rcv, err := ingest.Open(ingest.ReceiverConfig{URL: req.URL, Interface: req.Interface, ReadBuffer: a.conf().ReadBuffer, FFmpegPath: a.cfg.FFmpegPath})
	if err != nil {
		res.Error = err.Error()
		return res
	}
	an := ingest.NewAnalyzer(0)
	var mu sync.Mutex
	var capture []byte
	pctx, cancel := context.WithTimeout(ctx, time.Duration(secs*float64(time.Second)))
	defer cancel()
	start := time.Now()
	err = rcv.Run(pctx, func(b []byte) {
		an.Feed(b, time.Now())
		mu.Lock()
		if len(capture) < captureBytes {
			capture = append(capture, b...)
		}
		mu.Unlock()
	})
	res.Seconds = time.Since(start).Seconds()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	s := an.Snapshot()
	res.Reachable = s.Packets > 0
	if !res.Reachable {
		res.Error = "no packets received"
		return res
	}
	if el := s.LastPacketAt.Sub(s.FirstPacketAt).Seconds(); el > 0 {
		res.BitrateBps = int64(float64(s.Bytes) * 8 / el)
	}
	res.Program, res.CCErrors, res.Streams = s.Program, s.CCErrors, s.Streams
	if v := s.Video(); v != nil {
		res.Video = &model.VideoInfo{Codec: v.Codec}
	}
	for _, st := range s.Streams {
		if st.Kind == "audio" {
			res.Audio = append(res.Audio, model.AudioInfo{Codec: st.Codec, Language: st.Language})
		}
	}
	if a.cfg.FFprobePath != "-" {
		fctx, fcancel := context.WithTimeout(ctx, 10*time.Second)
		defer fcancel()
		if v, au, err := pipeline.FFprobe(fctx, a.cfg.FFprobePath, capture, s.Program); err == nil {
			if v != nil {
				res.Video = v
			}
			if len(au) > 0 {
				res.Audio = au
			}
		}
	}
	if !s.PMTSeen {
		res.Error = "no PAT/PMT found: is this MPEG-TS?"
	}
	return res
}
