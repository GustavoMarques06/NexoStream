package api

import (
	"net/http"
	"strconv"
	"time"

	"nexostream/control-plane/epg"
	"nexostream/control-plane/store"
	"nexostream/internal/model"
)

// handleChannelEPG returns the guide of a channel (tab EPG):
// ?from=&to= (ISO 8601; default: 12 h before to 24 h after now).
func (s *Server) handleChannelEPG(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.getChannel(w, r)
	if !ok {
		return
	}
	now := s.now().UTC()
	from, ok1 := parseTime(r.URL.Query().Get("from"))
	to, ok2 := parseTime(r.URL.Query().Get("to"))
	if !ok1 || !ok2 {
		writeError(w, r, http.StatusBadRequest, "invalid_time", "Use datas ISO 8601 (2026-11-16T14:00:00Z).")
		return
	}
	if from.IsZero() {
		from = now.Add(-12 * time.Hour)
	}
	if to.IsZero() {
		to = now.Add(24 * time.Hour)
	}
	progs, err := s.st.ListPrograms(r.Context(), ch.ID, from, to)
	if err != nil {
		s.storeError(w, r, err, "channel")
		return
	}
	out := map[string]any{"channel_id": ch.ID, "epg": ch.EPG, "from": from, "to": to, "programs": progs}
	if st, ok := epg.LoadStatus(r.Context(), s.st, ch.ID); ok {
		out["status"] = st
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRefreshEPG imports the guide of a channel now (audited).
func (s *Server) handleRefreshEPG(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.getChannel(w, r)
	if !ok {
		return
	}
	if ch.EPG.URL == "" {
		writeError(w, r, http.StatusConflict, "epg_not_configured", "Configure a URL do guia XMLTV do canal.")
		return
	}
	im := &epg.Importer{Store: s.st, Log: s.log}
	st := im.Refresh(r.Context(), []model.Channel{ch})[ch.ID]
	s.audit(r, "epg.refresh", ch.ID, map[string]any{"programs": st.Programs, "error": st.Error})
	writeJSON(w, http.StatusOK, st)
}

// handleExportEPG serves the guide of every channel as XMLTV (?hours=,
// default 24 h back and 48 h ahead), for players and middleware.
func (s *Server) handleExportEPG(w http.ResponseWriter, r *http.Request) {
	hours := 48
	if v := r.URL.Query().Get("hours"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 24*14 {
			writeError(w, r, http.StatusBadRequest, "invalid_hours", "hours entre 1 e 336.")
			return
		}
		hours = n
	}
	now := s.now().UTC()
	var chs []model.Channel
	progs := map[string][]model.Program{}
	after := ""
	for {
		page, err := s.st.ListChannels(r.Context(), store.ChannelFilter{Page: store.Page{After: after, Limit: 200}})
		if err != nil {
			s.storeError(w, r, err, "channel")
			return
		}
		if len(page) == 0 {
			break
		}
		for _, c := range page {
			ps, err := s.st.ListPrograms(r.Context(), c.ID, now.Add(-24*time.Hour), now.Add(time.Duration(hours)*time.Hour))
			if err != nil || len(ps) == 0 {
				continue
			}
			chs = append(chs, c)
			progs[c.ID] = ps
		}
		after = page[len(page)-1].ID
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_ = epg.XMLTV(w, chs, progs)
}
