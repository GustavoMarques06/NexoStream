package controller

import (
	"context"
	"encoding/json"
	"time"

	"nexostream/control-plane/store"
	"nexostream/internal/model"
)

// Statistics (Statistics page): the leader writes one sample per minute.
const (
	StatsEvery     = time.Minute
	StatsRetention = 30 * 24 * time.Hour
)

// StatsSample collects the values of one minute:
//
//	channels.total, channels.enabled, channels.up, channels.failed,
//	viewers.total, input.bps, workers.online,
//	worker:<id>:cpu, worker:<id>:mem, worker:<id>:net_in, worker:<id>:net_out (percent, bits/s),
//	channel:<id>:viewers (only channels with viewers).
func StatsSample(chs []model.Channel, statuses map[string]model.ChannelStatus, ws []model.Worker) map[string]float64 {
	v := map[string]float64{}
	var total, enabled, up, failed, viewers, input, online float64
	for _, c := range chs {
		total++
		if c.Enabled {
			enabled++
		}
		st, ok := statuses[c.ID]
		if !ok {
			continue
		}
		if st.State.Up() {
			up++
			if st.Input != nil {
				input += float64(st.Input.BitrateBps)
			}
		}
		if st.State == model.StateFailed {
			failed++
		}
		if st.Viewers > 0 {
			viewers += float64(st.Viewers)
			v["channel:"+c.ID+":viewers"] = float64(st.Viewers)
		}
	}
	for _, w := range ws {
		if w.State == model.WorkerOffline {
			continue
		}
		online++
		r := w.Resources
		p := "worker:" + w.ID + ":"
		v[p+"cpu"] = r.CPUPercent
		if r.MemoryTotalBytes > 0 {
			v[p+"mem"] = float64(r.MemoryUsedBytes) * 100 / float64(r.MemoryTotalBytes)
		}
		v[p+"net_in"] = float64(r.NetworkInBps)
		v[p+"net_out"] = float64(r.NetworkOutBps)
	}
	v["channels.total"], v["channels.enabled"], v["channels.up"], v["channels.failed"] = total, enabled, up, failed
	v["viewers.total"], v["input.bps"], v["workers.online"] = viewers, input, online
	return v
}

// sampleStats stores the minute sample and, once an hour, drops old ones.
func (c *Controller) sampleStats(ctx context.Context) {
	var chs []model.Channel
	after := ""
	for {
		page, err := c.store.ListChannels(ctx, store.ChannelFilter{Page: store.Page{After: after, Limit: 200}})
		if err != nil {
			c.log.Warn("stats: list channels", "error", err)
			return
		}
		if len(page) == 0 {
			break
		}
		chs = append(chs, page...)
		after = page[len(page)-1].ID
	}
	ids := make([]string, len(chs))
	for i, ch := range chs {
		ids[i] = ch.ID
	}
	statuses, err := c.cache.GetMany(ctx, ids)
	if err != nil {
		statuses = map[string]model.ChannelStatus{}
	}
	ws, err := c.store.ListWorkers(ctx)
	if err != nil {
		c.log.Warn("stats: list workers", "error", err)
		return
	}
	now := c.now()
	if err := c.store.InsertStats(ctx, now, StatsSample(chs, statuses, ws)); err != nil {
		c.log.Warn("stats: insert", "error", err)
	}
	if now.Sub(c.lastPrune) > time.Hour {
		c.lastPrune = now
		if err := c.store.PruneStats(ctx, now.Add(-StatsRetention)); err != nil {
			c.log.Warn("stats: prune", "error", err)
		}
		if err := c.store.PruneRefreshTokens(ctx, now); err != nil {
			c.log.Warn("prune refresh tokens", "error", err)
		}
		if err := c.store.PrunePlaySessions(ctx, now.Add(-StatsRetention)); err != nil {
			c.log.Warn("sessions: prune", "error", err)
		}
	}
}

// onSessions stores a batch of closed play sessions. Every controller does
// it (inserts are idempotent), so a standby keeps the history too.
func (c *Controller) onSessions(ctx context.Context, data []byte) {
	var ss []model.PlaySession
	if err := json.Unmarshal(data, &ss); err != nil || len(ss) == 0 {
		return
	}
	if err := c.store.InsertPlaySessions(ctx, ss); err != nil {
		c.log.Warn("store play sessions", "error", err, "sessions", len(ss))
	}
}
