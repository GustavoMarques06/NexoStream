// Package controller reconciles the desired state (PostgreSQL) with the
// state reported by the workers (heartbeats), the same way after a failure
// as in normal operation (docs/architecture.md section 3).
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"nexostream/control-plane/scheduler"
	"nexostream/control-plane/settings"
	"nexostream/control-plane/statuscache"
	"nexostream/control-plane/store"
	"nexostream/internal/bus"
	"nexostream/internal/model"
	"nexostream/internal/protocol"
)

// Config configures the controller.
type Config struct {
	Interval time.Duration // reconciliation period (5 s)
	// OfflineAfter marks a worker OFFLINE (3 missed heartbeats = 15 s).
	OfflineAfter time.Duration
	// StartupGrace waits for heartbeats after a controller restart before
	// taking decisions, so live workers are not considered offline.
	StartupGrace time.Duration
	// ResendAfter re-sends a start command not yet reflected in heartbeats.
	ResendAfter       time.Duration
	CommandTimeout    time.Duration
	MigrationsPerPass int // drain speed
	Concurrency       int
	DefaultProfile    string // sent with every channel for auto mode
	Scheduler         scheduler.Options
	// Holder identifies this controller in the leader lease; several
	// controllers may run, only the lease holder reconciles (ADR 0010).
	Holder   string
	LeaseTTL time.Duration
}

// DefaultConfig returns the values of spec section 9.1.
func DefaultConfig() Config {
	return Config{
		Interval:          5 * time.Second,
		OfflineAfter:      protocol.MissedHeartbeats * protocol.HeartbeatInterval,
		StartupGrace:      2*protocol.HeartbeatInterval + time.Second,
		ResendAfter:       15 * time.Second,
		CommandTimeout:    5 * time.Second,
		MigrationsPerPass: 5,
		Concurrency:       16,
		DefaultProfile:    "hd",
		Scheduler:         scheduler.DefaultOptions(),
	}
}

// Controller is the cluster controller.
type Controller struct {
	cfg   Config
	store store.Store
	cache statuscache.Cache
	bus   bus.Bus
	log   *slog.Logger

	mu        sync.Mutex
	hb        map[string]hbEntry
	protoSeen map[string]int // worker -> contract revision already reported in the log
	lastStart map[string]startMark
	lastConf  map[string]startMark // worker -> runtime sent (revision unused)
	confHash  map[string]string
	started   time.Time
	kick      chan struct{}
	now       func() time.Time
	leader    atomic.Bool
	lastPrune time.Time
}

type hbEntry struct {
	hb protocol.Heartbeat
	at time.Time
}

type startMark struct {
	worker   string
	revision int64
	at       time.Time
}

// New creates a controller.
func New(cfg Config, s store.Store, c statuscache.Cache, b bus.Bus, log *slog.Logger) *Controller {
	if log == nil {
		log = slog.Default()
	}
	return &Controller{
		cfg: cfg, store: s, cache: c, bus: b, log: log.With("component", "controller"),
		hb: map[string]hbEntry{}, protoSeen: map[string]int{}, lastStart: map[string]startMark{},
		lastConf: map[string]startMark{}, confHash: map[string]string{},
		kick: make(chan struct{}, 1), now: time.Now,
	}
}

// Kick asks for a reconciliation as soon as possible.
func (c *Controller) Kick() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// Run blocks until ctx is canceled.
func (c *Controller) Run(ctx context.Context) error {
	c.started = c.now()
	subs := []bus.Subscription{}
	defer func() {
		for _, s := range subs {
			_ = s.Unsubscribe()
		}
	}()
	s, err := c.bus.Subscribe(protocol.AllHeartbeats, func(_ string, d []byte) { c.onHeartbeat(ctx, d) })
	if err != nil {
		return fmt.Errorf("subscribe heartbeats: %w", err)
	}
	subs = append(subs, s)
	s, err = c.bus.Subscribe(protocol.AllChannelStatus, func(_ string, d []byte) { c.onStatus(ctx, d) })
	if err != nil {
		return fmt.Errorf("subscribe status: %w", err)
	}
	subs = append(subs, s)
	s, err = c.bus.Subscribe(protocol.SessionsClosed, func(_ string, d []byte) { c.onSessions(ctx, d) })
	if err != nil {
		return fmt.Errorf("subscribe sessions: %w", err)
	}
	subs = append(subs, s)
	s, err = c.bus.Subscribe(protocol.ReconcileSubject, func(string, []byte) { c.Kick() })
	if err != nil {
		return err
	}
	subs = append(subs, s)
	s, err = c.consumeEvents(ctx)
	if err != nil {
		return err
	}
	subs = append(subs, s)

	c.log.Info("controller started", "grace", c.cfg.StartupGrace.String())
	t := time.NewTicker(c.cfg.Interval)
	defer t.Stop()
	graceTimer := time.NewTimer(c.cfg.StartupGrace)
	defer graceTimer.Stop()
	statsT := time.NewTicker(StatsEvery)
	defer statsT.Stop()
	defer func() {
		if c.leader.Load() {
			// Hand over at once on a clean stop.
			rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = c.store.ReleaseLease(rctx, leaseName, c.cfg.Holder)
			cancel()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-graceTimer.C:
			c.reconcileLogged(ctx)
		case <-statsT.C:
			if c.Leader() {
				c.sampleStats(ctx)
			}
		case <-t.C:
			c.reconcileLogged(ctx)
		case <-c.kick:
			c.reconcileLogged(ctx)
		}
	}
}

func (c *Controller) consumeEvents(ctx context.Context) (bus.Subscription, error) {
	var sub bus.Subscription
	var err error
	for attempt := 0; attempt < 30; attempt++ {
		sub, err = c.bus.ConsumeDurable(ctx, protocol.EventsConsumer, protocol.EventSubjects, func(_ string, d []byte) error {
			var ev model.Event
			if err := json.Unmarshal(d, &ev); err != nil {
				c.log.Error("bad event", "error", err)
				return nil // poison message: drop
			}
			return c.store.InsertEvent(ctx, ev)
		})
		if err == nil {
			return sub, nil
		}
		c.log.Warn("event consumer not ready, retrying", "error", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil, fmt.Errorf("event consumer: %w", err)
}

func (c *Controller) onHeartbeat(ctx context.Context, d []byte) {
	var hb protocol.Heartbeat
	if err := json.Unmarshal(d, &hb); err != nil || hb.WorkerID == "" {
		return
	}
	now := c.now().UTC()
	c.mu.Lock()
	c.hb[hb.WorkerID] = hbEntry{hb: hb, at: now}
	warn := hb.Protocol != protocol.Version && c.protoSeen[hb.WorkerID] != hb.Protocol+1
	if warn {
		c.protoSeen[hb.WorkerID] = hb.Protocol + 1 // log each (worker, revision) once
	}
	c.mu.Unlock()
	if warn {
		c.log.Warn("worker speaks another contract revision; upgrade it or the controller to the same release",
			"worker_id", hb.WorkerID, "worker_protocol", hb.Protocol, "controller_protocol", protocol.Version, "worker_version", hb.Version)
	}
	w := model.Worker{
		ID: hb.WorkerID, State: model.WorkerOnline, LastHeartbeatAt: now, StartedAt: hb.StartedAt,
		Version: hb.Version, Capabilities: hb.Capabilities, Resources: hb.Resources, Channels: len(hb.Channels),
	}
	if err := c.store.UpsertWorker(ctx, w); err != nil {
		c.log.Error("upsert worker", "worker_id", hb.WorkerID, "error", err)
	}
}

func (c *Controller) onStatus(ctx context.Context, d []byte) {
	var st model.ChannelStatus
	if err := json.Unmarshal(d, &st); err != nil || st.ChannelID == "" {
		return
	}
	if err := c.cache.Set(ctx, st); err != nil {
		c.log.Warn("status cache", "error", err)
	}
}

func (c *Controller) emit(ctx context.Context, ev model.Event) {
	b, _ := json.Marshal(ev)
	subject := protocol.WorkerEvent(ev.WorkerID)
	if ev.ChannelID != "" {
		subject = protocol.ChannelEvent(ev.ChannelID)
	}
	if err := c.bus.PublishDurable(ctx, subject, ev.ID, b); err != nil {
		c.log.Warn("publish event failed; storing directly", "error", err)
		_ = c.store.InsertEvent(ctx, ev)
	}
}

// Leader reports whether this controller holds the lease and acts.
func (c *Controller) Leader() bool { return c.leader.Load() }

// lead takes or renews the leader lease.
func (c *Controller) lead(ctx context.Context) bool {
	ttl := c.cfg.LeaseTTL
	if ttl <= 0 {
		ttl = 3 * c.cfg.Interval
	}
	holder := c.cfg.Holder
	if holder == "" {
		holder = "controller"
	}
	ok, err := c.store.AcquireLease(ctx, leaseName, holder, ttl)
	if err != nil {
		c.log.Error("leader lease", "error", err)
		ok = false
	}
	if was := c.leader.Swap(ok); was != ok {
		if ok {
			c.log.Info("this controller is now the leader", "holder", holder)
		} else {
			c.log.Warn("this controller is on standby (another controller leads)", "holder", holder)
		}
	}
	return ok
}

const leaseName = "controller"

func (c *Controller) reconcileLogged(ctx context.Context) {
	if !c.lead(ctx) {
		return
	}
	if err := c.Reconcile(ctx); err != nil {
		c.log.Error("reconcile", "error", err)
	}
}

// Send sends a command to a worker and waits for its reply.
func Send(ctx context.Context, b bus.Bus, workerID string, cmd protocol.Command, timeout time.Duration) (protocol.Reply, error) {
	data, err := json.Marshal(cmd)
	if err != nil {
		return protocol.Reply{}, err
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := b.Request(rctx, protocol.WorkerCommand(workerID), data)
	if err != nil {
		if errors.Is(err, bus.ErrNoResponders) {
			return protocol.Reply{}, fmt.Errorf("worker %s is not reachable", workerID)
		}
		return protocol.Reply{}, fmt.Errorf("worker %s: %w", workerID, err)
	}
	var rep protocol.Reply
	if err := json.Unmarshal(raw, &rep); err != nil {
		return rep, fmt.Errorf("worker %s: bad reply: %w", workerID, err)
	}
	if !rep.OK {
		return rep, fmt.Errorf("worker %s: %s", workerID, rep.Error)
	}
	return rep, nil
}

// BuildSpec resolves the profiles and DVR storage a worker needs for a channel.
func BuildSpec(ch model.Channel, profiles map[string]model.TranscodeProfile, defaultProfile string, storages map[string]model.DVRStorage) protocol.ChannelSpec {
	spec := protocol.ChannelSpec{Channel: ch}
	if st, ok := storages[ch.DVR.Storage]; ok && ch.DVR.Storage != "" {
		spec.DVRStorage = &st
	}
	seen := map[string]bool{}
	for _, id := range append(append([]string{}, ch.Profiles...), defaultProfile) {
		if p, ok := profiles[id]; ok && !seen[id] {
			spec.Profiles = append(spec.Profiles, p)
			seen[id] = true
		}
	}
	return spec
}

type action struct {
	worker string
	cmd    protocol.Command
	after  func(error)
}

// effective returns the controller configuration with the Settings page
// on top (once saved; before that the .env values apply).
func (c *Controller) effective(ctx context.Context) (Config, model.Settings, bool) {
	cfg := c.cfg
	st, saved, err := settings.Load(ctx, c.store)
	if err != nil {
		c.log.Warn("load settings", "error", err)
	}
	if !saved {
		return cfg, st, false
	}
	cfg.OfflineAfter = time.Duration(st.WorkerOfflineSeconds) * time.Second
	cfg.MigrationsPerPass = st.DrainMigrationsPerPass
	cfg.DefaultProfile = st.DefaultProfile
	cfg.Scheduler.MaxCPUPercent = st.SchedulerMaxCPUPercent
	cfg.Scheduler.Costs.Passthrough = st.CostPassthrough
	cfg.Scheduler.Costs.TranscodePerMegapixel = st.CostTranscodePerMegapixel
	cfg.Scheduler.Costs.GPUOffload = st.CostGPUOffload
	return cfg, st, true
}

// configureActions sends the panel-managed settings to every live worker
// whose heartbeat reports another runtime.
func (c *Controller) configureActions(ctx context.Context, cfg Config, st model.Settings, saved bool, now time.Time, live func(string) bool) []action {
	overrides, err := settings.ListServers(ctx, c.store)
	if err != nil {
		c.log.Warn("load server settings", "error", err)
		return nil
	}
	var out []action
	c.mu.Lock()
	defer c.mu.Unlock()
	for wid, e := range c.hb {
		if !live(wid) || now.Sub(e.at) > cfg.OfflineAfter {
			continue
		}
		o, has := overrides[wid]
		if !saved && !has {
			continue // nothing managed in the panel: the node keeps its .env
		}
		var rt model.WorkerRuntime
		if saved {
			rt = st.Runtime(o)
		} else {
			rt = model.Settings{}.Runtime(o) // only the node's overrides
		}
		h := rt.Hash()
		if e.hb.RuntimeHash == h {
			continue
		}
		if m, ok := c.lastConf[wid]; ok && c.confHash[wid] == h && now.Sub(m.at) < c.cfg.ResendAfter {
			continue
		}
		wid, rt := wid, rt
		out = append(out, action{worker: wid, cmd: protocol.Command{Type: protocol.CmdConfigure, Runtime: &rt},
			after: func(err error) {
				if err == nil {
					c.mu.Lock()
					c.lastConf[wid] = startMark{worker: wid, at: now}
					c.confHash[wid] = h
					c.mu.Unlock()
				}
			}})
	}
	return out
}

// Reconcile runs one pass. It is exported for tests and the API.
func (c *Controller) Reconcile(ctx context.Context) error {
	now := c.now().UTC()
	cfg, st, saved := c.effective(ctx)
	inGrace := now.Sub(c.started) < cfg.StartupGrace

	workers, err := c.store.ListWorkers(ctx)
	if err != nil {
		return fmt.Errorf("list workers: %w", err)
	}
	byID := map[string]*model.Worker{}
	for i := range workers {
		w := &workers[i]
		byID[w.ID] = w
		if !inGrace && w.State != model.WorkerOffline && now.Sub(w.LastHeartbeatAt) > cfg.OfflineAfter {
			if err := c.store.SetWorkerState(ctx, w.ID, model.WorkerOffline); err == nil {
				w.State = model.WorkerOffline
				c.log.Warn("worker offline", "worker_id", w.ID, "last_heartbeat_at", w.LastHeartbeatAt)
				ev := model.NewEvent(model.EventWorkerOffline, now)
				ev.WorkerID = w.ID
				ev.Reason = "missed_heartbeats"
				c.emit(ctx, ev)
			}
		}
	}
	if inGrace {
		return nil
	}

	channels, err := c.allChannels(ctx)
	if err != nil {
		return err
	}
	chByID := map[string]model.Channel{}
	for _, ch := range channels {
		chByID[ch.ID] = ch
	}
	profList, err := c.store.ListProfiles(ctx)
	if err != nil {
		return fmt.Errorf("list profiles: %w", err)
	}
	profiles := map[string]model.TranscodeProfile{}
	for _, p := range profList {
		profiles[p.ID] = p
	}
	stList, err := c.store.ListDVRStorages(ctx)
	if err != nil {
		return fmt.Errorf("list dvr storages: %w", err)
	}
	storages := map[string]model.DVRStorage{}
	for _, st := range stList {
		storages[st.ID] = st
	}
	asList, err := c.store.ListAssignments(ctx)
	if err != nil {
		return fmt.Errorf("list assignments: %w", err)
	}
	assign := map[string]model.Assignment{}
	counts := map[string]int{}
	for _, a := range asList {
		assign[a.ChannelID] = a
		counts[a.WorkerID]++
	}

	// What each live worker reports running.
	running := map[string]map[string]protocol.ChannelBrief{}
	c.mu.Lock()
	for id, e := range c.hb {
		if now.Sub(e.at) > cfg.OfflineAfter {
			continue
		}
		m := map[string]protocol.ChannelBrief{}
		for _, b := range e.hb.Channels {
			m[b.ID] = b
		}
		running[id] = m
	}
	c.mu.Unlock()

	online := func(id string) bool {
		w, ok := byID[id]
		return ok && w.State != model.WorkerOffline
	}

	// Adopt channels running somewhere without an assignment (lost DB row).
	for wid, chans := range running {
		for id := range chans {
			ch, ok := chByID[id]
			if _, has := assign[id]; ok && ch.Enabled && !has {
				a := model.Assignment{ChannelID: id, WorkerID: wid, Since: now, Reason: "adopted"}
				if err := c.store.SetAssignment(ctx, a); err == nil {
					assign[id] = a
					counts[wid]++
				}
			}
		}
	}

	roles := map[string][]string{}
	if overrides, err := settings.ListServers(ctx, c.store); err == nil {
		for _, o := range overrides {
			roles[o.WorkerID] = o.Roles
		}
	}
	var eligible []model.Worker
	for _, w := range workers {
		if w.State != model.WorkerOffline {
			w.Capabilities.Roles = roles[w.ID] // node roles are set in the panel
			eligible = append(eligible, w)
		}
	}
	cluster := scheduler.NewCluster(eligible, counts, cfg.Scheduler)
	reassigned := map[string][]string{}
	migrations := 0
	var actions []action

	start := func(ch model.Channel, wid string) {
		c.mu.Lock()
		m, ok := c.lastStart[ch.ID]
		c.mu.Unlock()
		if ok && m.worker == wid && m.revision == ch.Revision && now.Sub(m.at) < c.cfg.ResendAfter {
			return
		}
		spec := BuildSpec(ch, profiles, cfg.DefaultProfile, storages)
		actions = append(actions, action{worker: wid, cmd: protocol.Command{Type: protocol.CmdStart, ChannelID: ch.ID, Spec: &spec},
			after: func(err error) {
				if err == nil {
					c.mu.Lock()
					c.lastStart[ch.ID] = startMark{worker: wid, revision: ch.Revision, at: now}
					c.mu.Unlock()
				}
			}})
	}
	schedule := func(ch model.Channel, exclude, reason string) (string, bool) {
		wid, err := cluster.Pick(ch, profiles, exclude)
		if err != nil {
			c.log.Warn("cannot schedule channel", "channel_id", ch.ID, "error", err)
			return "", false
		}
		a := model.Assignment{ChannelID: ch.ID, WorkerID: wid, Since: now, Reason: reason}
		if err := c.store.SetAssignment(ctx, a); err != nil {
			c.log.Error("set assignment", "channel_id", ch.ID, "error", err)
			return "", false
		}
		assign[ch.ID] = a
		c.log.Info("channel assigned", "channel_id", ch.ID, "worker_id", wid, "reason", reason)
		return wid, true
	}

	for _, ch := range channels {
		a, has := assign[ch.ID]
		if !ch.Enabled {
			if has {
				if err := c.store.DeleteAssignment(ctx, ch.ID); err == nil {
					delete(assign, ch.ID)
				}
			}
			continue
		}
		if !has {
			if wid, ok := schedule(ch, "", "scheduled"); ok {
				start(ch, wid)
			}
			continue
		}
		w := byID[a.WorkerID]
		switch {
		case w == nil || w.State == model.WorkerOffline:
			cluster.Release(a.WorkerID)
			if wid, ok := schedule(ch, a.WorkerID, "worker_offline"); ok {
				reassigned[a.WorkerID] = append(reassigned[a.WorkerID], ch.ID)
				start(ch, wid)
			}
		case w.Draining && migrations < cfg.MigrationsPerPass:
			cluster.Release(w.ID)
			if wid, ok := schedule(ch, w.ID, "drain"); ok {
				migrations++
				reassigned[w.ID] = append(reassigned[w.ID], ch.ID)
				start(ch, wid) // the old copy stops once the new one runs
			}
		default:
			if b, ok := running[w.ID][ch.ID]; !ok || b.Revision != ch.Revision {
				start(ch, w.ID)
			}
		}
	}

	// Stop copies that should not run where they run (make-before-break).
	for wid, chans := range running {
		if !online(wid) {
			continue
		}
		for id := range chans {
			ch, exists := chByID[id]
			a, has := assign[id]
			switch {
			case !exists || !ch.Enabled:
			case has && a.WorkerID == wid:
				continue
			case has:
				target := a.WorkerID
				if _, ok := running[target][id]; !ok && online(target) {
					continue // new copy not up yet
				}
			default:
				continue
			}
			id := id
			actions = append(actions, action{worker: wid, cmd: protocol.Command{Type: protocol.CmdStop, ChannelID: id},
				after: func(err error) {
					if err == nil && (!exists || !ch.Enabled) {
						_ = c.cache.Delete(ctx, id)
					}
				}})
		}
	}

	for wid, ids := range reassigned {
		ev := model.NewEvent(model.EventWorkerChannelsReassigned, now)
		ev.WorkerID = wid
		ev.Details = map[string]any{"channels": ids, "count": len(ids)}
		if w := byID[wid]; w != nil && w.Draining && w.State != model.WorkerOffline {
			ev.Reason = "drain"
		} else {
			ev.Reason = "worker_offline"
		}
		c.emit(ctx, ev)
	}

	// Settings first, so channels start with the new values.
	c.execute(ctx, c.configureActions(ctx, cfg, st, saved, now, online))
	c.execute(ctx, actions)
	return nil
}

func (c *Controller) allChannels(ctx context.Context) ([]model.Channel, error) {
	var out []model.Channel
	after := ""
	for {
		page, err := c.store.ListChannels(ctx, store.ChannelFilter{Page: store.Page{After: after, Limit: 200}})
		if err != nil {
			return nil, fmt.Errorf("list channels: %w", err)
		}
		out = append(out, page...)
		if len(page) < 200 {
			return out, nil
		}
		after = page[len(page)-1].ID
	}
}

func (c *Controller) execute(ctx context.Context, actions []action) {
	if len(actions) == 0 {
		return
	}
	sem := make(chan struct{}, c.cfg.Concurrency)
	var wg sync.WaitGroup
	for _, a := range actions {
		wg.Add(1)
		sem <- struct{}{}
		go func(a action) {
			defer wg.Done()
			defer func() { <-sem }()
			_, err := Send(ctx, c.bus, a.worker, a.cmd, c.cfg.CommandTimeout)
			if err != nil {
				c.log.Warn("command failed", "type", a.cmd.Type, "channel_id", a.cmd.ChannelID, "worker_id", a.worker, "error", err)
			} else {
				c.log.Info("command sent", "type", a.cmd.Type, "channel_id", a.cmd.ChannelID, "worker_id", a.worker)
			}
			if a.after != nil {
				a.after(err)
			}
		}(a)
	}
	wg.Wait()
}
