package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"nexostream/control-plane/store"
	"nexostream/internal/model"
	"nexostream/media-engine/transcoding"
)

// ---- transcode profiles ----

func (s *Server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	ps, err := s.st.ListProfiles(r.Context())
	if err != nil {
		s.storeError(w, r, err, "profile")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ps, "next_cursor": nil})
}

func (s *Server) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.st.GetProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, r, err, "profile")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func normalizeProfile(p *model.TranscodeProfile) {
	if p.Encoder == "" {
		p.Encoder = model.EncoderAuto
	}
	if p.Video.Deinterlace == "" {
		p.Video.Deinterlace = "auto"
	}
	if p.Video.MaxBitrateBps == 0 {
		p.Video.MaxBitrateBps = p.Video.BitrateBps
	}
	if p.Audio.SampleRate == 0 {
		p.Audio.SampleRate = 48000
	}
	if p.Audio.Channels == 0 {
		p.Audio.Channels = 2
	}
}

func (s *Server) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	var p model.TranscodeProfile
	if err := decode(r, &p); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	normalizeProfile(&p)
	if !validName.MatchString(p.ID) {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_profile", "id deve ter de 1 a 64 caracteres [a-z0-9._-].")
		return
	}
	if err := transcoding.ValidateProfile(p); err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_profile", err.Error())
		return
	}
	if err := s.st.CreateProfile(r.Context(), p); err != nil {
		s.storeError(w, r, err, "profile")
		return
	}
	s.audit(r, "profile.create", p.ID, nil)
	w.Header().Set("Location", "/api/v1/transcode-profiles/"+p.ID)
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handlePatchProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.st.GetProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, r, err, "profile")
		return
	}
	// Merge: decode the patch over the current value.
	b, _ := json.Marshal(p)
	var merged map[string]any
	_ = json.Unmarshal(b, &merged)
	var patch map[string]any
	if err := decode(r, &patch); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	for k, v := range patch {
		if sub, ok := v.(map[string]any); ok {
			if cur, ok := merged[k].(map[string]any); ok {
				for sk, sv := range sub {
					cur[sk] = sv
				}
				continue
			}
		}
		merged[k] = v
	}
	merged["id"] = p.ID
	b, _ = json.Marshal(merged)
	var np model.TranscodeProfile
	if err := json.Unmarshal(b, &np); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", "Campos inválidos: "+err.Error())
		return
	}
	normalizeProfile(&np)
	if err := transcoding.ValidateProfile(np); err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_profile", err.Error())
		return
	}
	if err := s.st.UpdateProfile(r.Context(), np); err != nil {
		s.storeError(w, r, err, "profile")
		return
	}
	s.audit(r, "profile.update", np.ID, patch)
	writeJSON(w, http.StatusOK, np)
}

func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if st, _ := s.effectiveSettings(r.Context()); id == st.DefaultProfile {
		writeError(w, r, http.StatusConflict, "profile_in_use", "Este é o perfil padrão do modo auto e não pode ser removido.")
		return
	}
	if err := s.st.DeleteProfile(r.Context(), id); err != nil {
		s.storeError(w, r, err, "profile")
		return
	}
	s.audit(r, "profile.delete", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---- workers ----

func (s *Server) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	ws, err := s.st.ListWorkers(r.Context())
	if err != nil {
		s.storeError(w, r, err, "worker")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ws, "next_cursor": nil})
}

func (s *Server) handleGetWorker(w http.ResponseWriter, r *http.Request) {
	wk, err := s.st.GetWorker(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, r, err, "worker")
		return
	}
	as, err := s.st.ListAssignments(r.Context())
	if err != nil {
		s.storeError(w, r, err, "worker")
		return
	}
	assigned := []model.Assignment{}
	for _, a := range as {
		if a.WorkerID == wk.ID {
			assigned = append(assigned, a)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"worker": wk, "assignments": assigned})
}

func (s *Server) handleDrain(drain bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := s.st.SetWorkerDraining(r.Context(), id, drain); err != nil {
			s.storeError(w, r, err, "worker")
			return
		}
		action := "worker.undrain"
		if drain {
			action = "worker.drain"
		}
		s.audit(r, action, id, nil)
		s.kick()
		wk, _ := s.st.GetWorker(r.Context(), id)
		writeJSON(w, http.StatusAccepted, wk)
	}
}

// ---- events and audit ----

func parseTime(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(time.RFC3339, v)
	return t, err == nil
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request, channelID string) {
	q := r.URL.Query()
	from, ok1 := parseTime(q.Get("from"))
	to, ok2 := parseTime(q.Get("to"))
	if !ok1 || !ok2 {
		writeError(w, r, http.StatusBadRequest, "invalid_time", "Use datas ISO 8601 (2026-11-16T14:00:00Z).")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	before, _ := strconv.ParseInt(q.Get("cursor"), 10, 64)
	f := store.EventFilter{ChannelID: channelID, WorkerID: q.Get("worker_id"), Type: q.Get("type"), From: from, To: to, Before: before, Limit: store.ClampLimit(limit)}
	evs, err := s.st.ListEvents(r.Context(), f)
	if err != nil {
		s.storeError(w, r, err, "event")
		return
	}
	items := make([]model.Event, len(evs))
	for i, e := range evs {
		items[i] = e.Event
	}
	var next any
	if len(evs) == f.Limit {
		next = strconv.FormatInt(evs[len(evs)-1].Seq, 10)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	s.listEvents(w, r, r.URL.Query().Get("channel_id"))
}

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, ok1 := parseTime(q.Get("from"))
	to, ok2 := parseTime(q.Get("to"))
	if !ok1 || !ok2 {
		writeError(w, r, http.StatusBadRequest, "invalid_time", "Use datas ISO 8601 (2026-11-16T14:00:00Z).")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	before, _ := strconv.ParseInt(q.Get("cursor"), 10, 64)
	f := store.AuditFilter{Actor: q.Get("actor"), Action: q.Get("action"), Target: q.Get("target"), From: from, To: to, Before: before, Limit: store.ClampLimit(limit)}
	items, err := s.st.ListAudit(r.Context(), f)
	if err != nil {
		s.storeError(w, r, err, "audit")
		return
	}
	var next any
	if len(items) == f.Limit {
		next = strconv.FormatInt(items[len(items)-1].ID, 10)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

// handleSummary feeds the dashboard.
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var all []model.Channel
	after := ""
	for {
		page, err := s.st.ListChannels(ctx, store.ChannelFilter{Page: store.Page{After: after, Limit: 200}})
		if err != nil {
			s.storeError(w, r, err, "channel")
			return
		}
		all = append(all, page...)
		if len(page) < 200 {
			break
		}
		after = page[len(page)-1].ID
	}
	views := s.views(ctx, all)
	byState := map[model.ChannelState]int{}
	for _, st := range model.AllStates {
		byState[st] = 0
	}
	byProcessing := map[string]int{}
	var inBps int64
	viewers, enabled := 0, 0
	var failing []map[string]any
	for _, v := range views {
		byState[v.Status.State]++
		if v.Enabled {
			enabled++
		}
		if v.Status.Processing != "" {
			byProcessing[v.Status.Processing]++
		}
		if v.Status.Input != nil {
			inBps += v.Status.Input.BitrateBps
		}
		viewers += v.Status.Viewers
		if v.Enabled && (v.Status.State == model.StateFailed || v.Status.State == model.StateDegraded ||
			v.Status.State == model.StateOnBackup || v.Status.State == model.StateSwitching) {
			failing = append(failing, map[string]any{"id": v.ID, "name": v.Name, "state": v.Status.State,
				"reason": v.Status.Reason, "since": v.Status.Since, "worker_id": v.Status.WorkerID})
		}
	}
	sort.Slice(failing, func(i, j int) bool { return failing[i]["id"].(string) < failing[j]["id"].(string) })
	ws, err := s.st.ListWorkers(ctx)
	if err != nil {
		s.storeError(w, r, err, "worker")
		return
	}
	wState := map[model.WorkerState]int{model.WorkerOnline: 0, model.WorkerDraining: 0, model.WorkerOffline: 0}
	var netIn, netOut uint64
	for _, wk := range ws {
		wState[wk.State]++
		if wk.State != model.WorkerOffline {
			netIn += wk.Resources.NetworkInBps
			netOut += wk.Resources.NetworkOutBps
		}
	}
	evs, _ := s.st.ListEvents(ctx, store.EventFilter{Limit: 15})
	recent := make([]model.Event, len(evs))
	for i, e := range evs {
		recent[i] = e.Event
	}
	if failing == nil {
		failing = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channels": map[string]any{"total": len(all), "enabled": enabled, "by_state": byState, "by_processing": byProcessing,
			"input_bitrate_bps": inBps, "viewers_ts_http": viewers},
		"workers":       map[string]any{"total": len(ws), "by_state": wState, "network_in_bps": netIn, "network_out_bps": netOut, "items": ws},
		"failing":       failing,
		"recent_events": recent,
		"version":       s.cfg.Version,
		"generated_at":  s.now().UTC(),
	})
}
