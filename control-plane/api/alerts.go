package api

import (
	"net/http"
	"time"

	"nexostream/control-plane/alerts"
	"nexostream/control-plane/store"
	"nexostream/internal/model"
)

// handleAlerts returns the active alerts (spec 14.3) evaluated from the
// current state of channels and workers.
func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := s.now().UTC()
	var chs []model.Channel
	after := ""
	for {
		page, err := s.st.ListChannels(ctx, store.ChannelFilter{Page: store.Page{After: after, Limit: 200}})
		if err != nil {
			s.storeError(w, r, err, "channel")
			return
		}
		if len(page) == 0 {
			break
		}
		chs = append(chs, page...)
		after = page[len(page)-1].ID
	}
	ids := make([]string, len(chs))
	for i, c := range chs {
		ids[i] = c.ID
	}
	statuses, err := s.cache.GetMany(ctx, ids)
	if err != nil {
		statuses = map[string]model.ChannelStatus{}
	}
	ws, err := s.st.ListWorkers(ctx)
	if err != nil {
		s.storeError(w, r, err, "worker")
		return
	}
	restarts := map[string]int{}
	evs, err := s.st.ListEvents(ctx, store.EventFilter{Type: model.EventChannelPipelineRestart, From: now.Add(-alerts.RestartsWindow), Limit: 200})
	if err == nil {
		for _, e := range evs {
			restarts[e.ChannelID]++
		}
	}
	dvrProblems := map[string]int{}
	if evs, err := s.st.ListEvents(ctx, store.EventFilter{Type: model.EventDVRIntegrityError, From: now.Add(-24 * time.Hour), Limit: 200}); err == nil {
		for _, e := range evs {
			dvrProblems[e.ChannelID]++
		}
	}
	items := alerts.Evaluate(alerts.Input{Now: now, Channels: chs, Statuses: statuses, Workers: ws, Restarts: restarts, DVRProblems: dvrProblems})
	counts := map[string]int{alerts.Critical: 0, alerts.Warning: 0}
	for _, a := range items {
		counts[a.Severity]++
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "counts": counts, "evaluated_at": now, "window": alerts.RestartsWindow.String()})
}
