package api

import (
	"net/http"
	"sort"
	"strconv"

	"nexostream/control-plane/controller"
	"nexostream/control-plane/store"
	"nexostream/internal/model"
	"nexostream/internal/protocol"
)

// handleChannelSessions lists the play sessions of a channel (tab Play
// sessions, ADR 0009), asked to the worker running it.
func (s *Server) handleChannelSessions(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.getChannel(w, r)
	if !ok {
		return
	}
	out := map[string]any{"channel_id": ch.ID, "worker_id": "", "open": []model.PlaySession{}, "closed": []model.PlaySession{}}
	wid, err := s.assignedWorker(r.Context(), ch.ID)
	if err != nil {
		s.storeError(w, r, err, "channel")
		return
	}
	if wid == "" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	rep, err := controller.Send(r.Context(), s.bus, wid, protocol.Command{Type: protocol.CmdSessions, ChannelID: ch.ID}, s.cfg.CommandTimeout)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "worker_command_failed", err.Error())
		return
	}
	out["worker_id"] = wid
	if rep.Sessions != nil {
		out["open"], out["closed"] = rep.Sessions.Open, rep.Sessions.Closed
	}
	writeJSON(w, http.StatusOK, out)
}

// handleKillSession closes a play session; the viewer is refused for two
// minutes (audited).
func (s *Server) handleKillSession(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	ch, ok := s.getChannel(w, r)
	if !ok {
		return
	}
	wid, err := s.assignedWorker(r.Context(), ch.ID)
	if err != nil || wid == "" {
		writeError(w, r, http.StatusNotFound, "session_not_found", "O canal não está em nenhum servidor agora.")
		return
	}
	cmd := protocol.Command{Type: protocol.CmdKillSession, ChannelID: ch.ID, SessionID: sid, Actor: principalFrom(r.Context()).Actor}
	if _, err := controller.Send(r.Context(), s.bus, wid, cmd, s.cfg.CommandTimeout); err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
		return
	}
	s.audit(r, "session.kill", ch.ID, map[string]any{"session_id": sid, "worker_id": wid})
	w.WriteHeader(http.StatusNoContent)
}

// handleAllSessions lists the open play sessions of every online worker.
func (s *Server) handleAllSessions(w http.ResponseWriter, r *http.Request) {
	ws, err := s.st.ListWorkers(r.Context())
	if err != nil {
		s.storeError(w, r, err, "worker")
		return
	}
	open := []model.PlaySession{}
	var failed []string
	for _, wk := range ws {
		if wk.State == model.WorkerOffline {
			continue
		}
		rep, err := controller.Send(r.Context(), s.bus, wk.ID, protocol.Command{Type: protocol.CmdSessions}, s.cfg.CommandTimeout)
		if err != nil || rep.Sessions == nil {
			failed = append(failed, wk.ID)
			continue
		}
		open = append(open, rep.Sessions.Open...)
	}
	sort.Slice(open, func(i, j int) bool { return open[i].StartedAt.Before(open[j].StartedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"open": open, "unreachable_workers": failed})
}

// handleSessionHistory lists closed play sessions (30 days), newest first:
// ?channel_id=&user_id=&ip=&from=&to=&limit=&cursor=
func (s *Server) handleSessionHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, ok1 := parseTime(q.Get("from"))
	to, ok2 := parseTime(q.Get("to"))
	if !ok1 || !ok2 {
		writeError(w, r, http.StatusBadRequest, "invalid_time", "Use datas ISO 8601 (2026-11-16T14:00:00Z).")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	f := store.SessionFilter{ChannelID: q.Get("channel_id"), UserID: q.Get("user_id"), IP: q.Get("ip"),
		From: from, To: to, Before: q.Get("cursor"), Limit: store.ClampLimit(limit)}
	items, err := s.st.ListPlaySessions(r.Context(), f)
	if err != nil {
		s.storeError(w, r, err, "session")
		return
	}
	out := map[string]any{"items": items}
	if len(items) == f.Limit {
		last := items[len(items)-1]
		out["next_cursor"] = strconv.FormatInt(last.ClosedAt.UnixNano(), 10) + "|" + last.ID
	}
	writeJSON(w, http.StatusOK, out)
}
