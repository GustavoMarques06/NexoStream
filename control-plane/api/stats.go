package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// statsRange reads ?from=&to= (default: the last 24 h) and picks a bucket
// size giving about 240 points per series.
func (s *Server) statsRange(w http.ResponseWriter, r *http.Request) (from, to time.Time, step time.Duration, ok bool) {
	now := s.now().UTC()
	from, ok1 := parseTime(r.URL.Query().Get("from"))
	to, ok2 := parseTime(r.URL.Query().Get("to"))
	if !ok1 || !ok2 {
		writeError(w, r, http.StatusBadRequest, "invalid_time", "Use datas ISO 8601 (2026-11-16T14:00:00Z).")
		return from, to, 0, false
	}
	if to.IsZero() {
		to = now
	}
	if from.IsZero() {
		from = to.Add(-24 * time.Hour)
	}
	if !from.Before(to) || to.Sub(from) > 31*24*time.Hour {
		writeError(w, r, http.StatusBadRequest, "invalid_range", "Intervalo de até 31 dias, com from antes de to.")
		return from, to, 0, false
	}
	step = (to.Sub(from) / 240).Round(time.Minute)
	if step < time.Minute {
		step = time.Minute
	}
	return from, to, step, true
}

// handleStats returns series for the Statistics page:
// ?keys=viewers.total,channels.up&prefix=worker:&from=&to=
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	from, to, step, ok := s.statsRange(w, r)
	if !ok {
		return
	}
	var keys []string
	for _, k := range strings.Split(r.URL.Query().Get("keys"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	prefix := r.URL.Query().Get("prefix")
	if len(keys) == 0 && prefix == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_keys", "Informe keys ou prefix.")
		return
	}
	series, err := s.st.ListStats(r.Context(), keys, prefix, from, to, step)
	if err != nil {
		s.storeError(w, r, err, "stats")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "step_seconds": int(step / time.Second), "series": series})
}

// handleTopChannels ranks channels by audience in viewer-minutes (one
// sample per minute), with the peak of simultaneous viewers.
func (s *Server) handleTopChannels(w http.ResponseWriter, r *http.Request) {
	from, to, _, ok := s.statsRange(w, r)
	if !ok {
		return
	}
	limit := 20
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	top, err := s.st.TopStats(r.Context(), "channel:", from, to, limit)
	if err != nil {
		s.storeError(w, r, err, "stats")
		return
	}
	items := make([]map[string]any, 0, len(top))
	for _, t := range top {
		id := strings.TrimSuffix(strings.TrimPrefix(t.Key, "channel:"), ":viewers")
		name := id
		if ch, err := s.st.GetChannel(r.Context(), id); err == nil && ch.Name != "" {
			name = ch.Name
		}
		items = append(items, map[string]any{"channel_id": id, "name": name, "viewer_minutes": t.Sum, "peak_viewers": t.Max})
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "items": items})
}
