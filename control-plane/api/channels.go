package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"nexostream/control-plane/controller"
	"nexostream/control-plane/store"
	"nexostream/internal/model"
	"nexostream/internal/protocol"
	"nexostream/internal/redact"
	"nexostream/media-engine/drm"
	"nexostream/media-engine/ingest"
	"nexostream/media-engine/sessions"
)

var (
	validID   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	validName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

// channelView is a channel with its live status.
type channelView struct {
	model.Channel
	Status model.ChannelStatus `json:"status"`
}

// channelInput is the body of POST and PATCH /channels. Pointers distinguish
// "absent" from "zero" for PATCH.
type channelInput struct {
	ID       *string               `json:"id"`
	Name     *string               `json:"name"`
	Group    *string               `json:"group"`
	Enabled  *bool                 `json:"enabled"`
	Mode     *model.ProcessingMode `json:"mode"`
	Profiles *[]string             `json:"profiles"`
	Sources  *[]model.Source       `json:"sources"`
	Outputs  *model.Outputs        `json:"outputs"`
	DVR      *model.DVRConfig      `json:"dvr"`
	DRM      *model.DRMConfig      `json:"drm"`
	Auth     *model.AuthConfig     `json:"auth"`
	EPG      *model.EPGConfig      `json:"epg"`
	Overlay  *model.OverlayConfig  `json:"overlay"`
	Failover *model.FailoverPolicy `json:"failover"`
}

type fieldErrors map[string]string

func (f fieldErrors) add(field, msg string) { f[field] = msg }

// apply merges the input into ch.
func (in channelInput) apply(ch *model.Channel) {
	if in.Name != nil {
		ch.Name = strings.TrimSpace(*in.Name)
	}
	if in.Group != nil {
		ch.Group = strings.TrimSpace(*in.Group)
	}
	if in.Enabled != nil {
		ch.Enabled = *in.Enabled
	}
	if in.Mode != nil {
		ch.Mode = *in.Mode
	}
	if in.Profiles != nil {
		ch.Profiles = *in.Profiles
	}
	if in.Sources != nil {
		ch.Sources = *in.Sources
	}
	if in.Outputs != nil {
		ch.Outputs = *in.Outputs
	}
	if in.DVR != nil {
		ch.DVR = *in.DVR
	}
	if in.DRM != nil {
		ch.DRM = *in.DRM
	}
	if in.Auth != nil {
		ch.Auth = *in.Auth
	}
	if in.EPG != nil {
		ch.EPG = *in.EPG
	}
	if in.Overlay != nil {
		ch.Overlay = *in.Overlay
	}
	if in.Failover != nil {
		ch.Failover = *in.Failover
	}
}

// normalizeSources assigns missing source IDs and sorts by priority.
func normalizeSources(srcs []model.Source) []model.Source {
	out := make([]model.Source, len(srcs))
	copy(out, srcs)
	used := map[string]bool{}
	for _, s := range out {
		if s.ID != "" {
			used[s.ID] = true
		}
	}
	for i := range out {
		out[i].URL = strings.TrimSpace(out[i].URL)
		if out[i].ID == "" {
			id := strconv.Itoa(out[i].Priority)
			for n := 1; used[id]; n++ {
				id = strconv.Itoa(out[i].Priority) + "-" + strconv.Itoa(n)
			}
			out[i].ID = id
			used[id] = true
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out
}

// validateChannel checks business rules. profiles are the known profiles.
func validateChannel(ch *model.Channel, profiles map[string]model.TranscodeProfile, storages map[string]bool) fieldErrors {
	fe := fieldErrors{}
	if !validID.MatchString(ch.ID) {
		fe.add("id", "use de 1 a 64 caracteres [a-z0-9-], começando por letra ou número")
	}
	if ch.Name == "" || len(ch.Name) > 200 {
		fe.add("name", "obrigatório, até 200 caracteres")
	}
	if len(ch.Group) > 100 {
		fe.add("group", "até 100 caracteres")
	}
	if ch.Mode == "" {
		ch.Mode = model.ModeAuto
	}
	if !ch.Mode.Valid() {
		fe.add("mode", "use auto, passthrough ou transcode")
	}
	if ch.Profiles == nil {
		ch.Profiles = []string{}
	}
	for _, p := range ch.Profiles {
		if _, ok := profiles[p]; !ok {
			fe.add("profiles", "perfil inexistente: "+p)
		}
	}
	seenProfile := map[string]bool{}
	for _, p := range ch.Profiles {
		if seenProfile[p] {
			fe.add("profiles", "perfil repetido: "+p)
		}
		seenProfile[p] = true
	}
	switch {
	case len(ch.Profiles) > 6:
		fe.add("profiles", "no máximo 6 qualidades (ABR)")
	case len(ch.Profiles) > 1 && ch.Mode != model.ModeTranscode:
		fe.add("profiles", "várias qualidades (ABR) exigem mode = transcode")
	}
	if ch.Mode == model.ModeTranscode && len(ch.Profiles) == 0 {
		fe.add("profiles", "obrigatório quando mode = transcode")
	}
	if len(ch.Sources) == 0 {
		fe.add("sources", "informe pelo menos uma fonte")
	}
	if len(ch.Sources) > 8 {
		fe.add("sources", "no máximo 8 fontes por canal")
	}
	ch.Sources = normalizeSources(ch.Sources)
	prios := map[int]bool{}
	ids := map[string]bool{}
	urls := map[string]bool{}
	for i, s := range ch.Sources {
		f := fmt.Sprintf("sources[%d]", i)
		if s.Priority <= 0 {
			fe.add(f+".priority", "deve ser maior que zero")
		}
		if prios[s.Priority] {
			fe.add(f+".priority", "prioridade repetida")
		}
		prios[s.Priority] = true
		if ids[s.ID] {
			fe.add(f+".id", "id repetido")
		}
		ids[s.ID] = true
		if a, err := ingest.ParseSourceURL(s.URL); err != nil {
			fe.add(f+".url", err.Error())
		} else if a.Listener && a.Scheme == "srt" && (a.PassphraseLen < ingest.MinSRTPassphrase || a.PassphraseLen > ingest.MaxSRTPassphrase) {
			// A listener opens a port to anyone who can reach it: without a
			// passphrase, any sender could inject video into the channel.
			fe.add(f+".url", fmt.Sprintf("SRT em modo listener exige passphrase de %d a %d caracteres (?mode=listener&passphrase=...): ela autentica quem envia e cifra o fluxo", ingest.MinSRTPassphrase, ingest.MaxSRTPassphrase))
		} else if a.Listener && a.Scheme == "rtmp" && a.StreamKeyLen < ingest.MinRTMPKey {
			// The stream key is the only thing between the open port and the channel.
			fe.add(f+".url", fmt.Sprintf("RTMP em modo listener exige uma chave de stream de pelo menos %d caracteres aleatórios (rtmp://0.0.0.0:1935/<app>/<chave>?mode=listener)", ingest.MinRTMPKey))
		}
		if urls[s.URL+"|"+s.Interface] {
			fe.add(f+".url", "fonte repetida")
		}
		urls[s.URL+"|"+s.Interface] = true
		if len(s.Interface) > 32 || strings.ContainsAny(s.Interface, " /") {
			fe.add(f+".interface", "nome de interface inválido")
		}
	}
	if !ch.Outputs.HLS && !ch.Outputs.MPEGTSHTTP && ch.Outputs.MPEGTSUDP == "" && !ch.Outputs.DASH && len(ch.Outputs.Push) == 0 {
		fe.add("outputs", "habilite pelo menos uma saída")
	}
	validatePush(ch, fe)
	if u := ch.Outputs.MPEGTSUDP; u != "" {
		if a, err := ingest.ParseSourceURL(u); err != nil || a.Scheme != "udp" || a.Host == "" {
			fe.add("outputs.mpegts_udp", "use udp://host:porta")
		}
	}
	if ch.DVR.Enabled && (ch.DVR.RetentionHours < 1 || ch.DVR.RetentionHours > 24*365) {
		fe.add("dvr.retention_hours", "entre 1 e 8760 quando a gravação estiver ativa")
	}
	ch.DVR.Storage = strings.TrimSpace(ch.DVR.Storage)
	if ch.DVR.Storage != "" && !storages[ch.DVR.Storage] {
		fe.add("dvr.storage", "armazenamento de DVR inexistente: "+ch.DVR.Storage)
	}
	ch.DVR.Path = strings.TrimSpace(ch.DVR.Path)
	if p := ch.DVR.Path; p != "" {
		if !absPath(p) || strings.Contains(p, "..") || len(p) > 255 {
			fe.add("dvr.path", "use um caminho absoluto no servidor de mídia, ex.: /storage/dvr")
		}
	}
	validateDRM(ch, fe)
	validateAuth(ch, fe)
	validateEPG(ch, fe)
	validateOverlay(ch, fe)
	ch.Failover = ch.Failover.WithDefaults()
	fo := ch.Failover
	check := func(name string, v, min, max float64) {
		if v < min || v > max {
			fe.add("failover."+name, fmt.Sprintf("entre %g e %g", min, max))
		}
	}
	check("no_packets_seconds", fo.NoPacketsSeconds, 0.5, 60)
	check("confirm_window_seconds", fo.ConfirmWindowSeconds, 0, 120)
	check("low_bitrate_ratio", fo.LowBitrateRatio, 0.01, 0.99)
	check("low_bitrate_seconds", fo.LowBitrateSeconds, 1, 600)
	check("missing_stream_seconds", fo.MissingStreamSeconds, 1, 120)
	check("primary_stable_seconds", fo.PrimaryStableSeconds, 1, 3600)
	return fe
}

func (s *Server) storageSet(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	list, _ := s.st.ListDVRStorages(ctx)
	for _, v := range list {
		out[v.ID] = true
	}
	return out
}

func (s *Server) profileMap(ctx context.Context) (map[string]model.TranscodeProfile, error) {
	ps, err := s.st.ListProfiles(ctx)
	if err != nil {
		return nil, err
	}
	m := map[string]model.TranscodeProfile{}
	for _, p := range ps {
		m[p.ID] = p
	}
	return m, nil
}

// status returns the cached status or a synthetic one.
func (s *Server) status(ctx context.Context, ch model.Channel, cached map[string]model.ChannelStatus) model.ChannelStatus {
	if st, ok := cached[ch.ID]; ok {
		if !ch.Enabled && st.State != model.StateStopped {
			st.Reason = "stopping"
		}
		return st
	}
	if ch.Enabled {
		return model.ChannelStatus{ChannelID: ch.ID, State: model.StateStarting, Reason: "awaiting_worker", Revision: ch.Revision}
	}
	return model.StoppedStatus(ch.ID)
}

func (s *Server) views(ctx context.Context, chs []model.Channel) []channelView {
	ids := make([]string, len(chs))
	for i, c := range chs {
		ids[i] = c.ID
	}
	cached, err := s.cache.GetMany(ctx, ids)
	if err != nil {
		s.log.Warn("status cache unavailable", "error", err)
		cached = map[string]model.ChannelStatus{}
	}
	out := make([]channelView, len(chs))
	for i, c := range chs {
		out[i] = channelView{Channel: forCaller(ctx, c), Status: s.status(ctx, c, cached)}
	}
	return out
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	limit = store.ClampLimit(limit)
	f := store.ChannelFilter{Page: store.Page{After: q.Get("cursor"), Limit: limit}, Group: q.Get("group"), Search: q.Get("q")}
	if v := q.Get("enabled"); v != "" {
		b := v == "true"
		f.Enabled = &b
	}
	state := model.ChannelState(strings.ToUpper(q.Get("state")))
	if state != "" && !state.Valid() {
		writeError(w, r, http.StatusBadRequest, "invalid_state", "Estado desconhecido: "+string(state))
		return
	}
	processing := q.Get("processing")
	worker := q.Get("worker_id")

	// State, worker and processing live in the status cache: fill the page
	// by scanning store pages when those filters are used.
	var items []channelView
	next := ""
	for {
		page, err := s.st.ListChannels(r.Context(), f)
		if err != nil {
			s.storeError(w, r, err, "channel")
			return
		}
		for _, v := range s.views(r.Context(), page) {
			if (state == "" || v.Status.State == state) && (worker == "" || v.Status.WorkerID == worker) &&
				(processing == "" || v.Status.Processing == processing) {
				items = append(items, v)
				if len(items) == limit {
					next = v.ID
					break
				}
			}
		}
		if next != "" || len(page) < f.Limit {
			break
		}
		f.After = page[len(page)-1].ID
	}
	if items == nil {
		items = []channelView{}
	}
	var nc any
	if next != "" {
		nc = next
	} else if len(items) == limit {
		nc = items[len(items)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nc})
}

// applyDVRDefaults fills the retention and storage of a new recording
// channel from the Settings page.
func applyDVRDefaults(ch *model.Channel, st model.Settings) {
	if !ch.DVR.Enabled {
		return
	}
	if ch.DVR.RetentionHours == 0 {
		ch.DVR.RetentionHours = st.DVRDefaultRetentionHours
	}
	if ch.DVR.Storage == "" && ch.DVR.Path == "" {
		ch.DVR.Storage = st.DVRDefaultStorage
	}
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	var in channelInput
	if err := decode(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	defaults, _ := s.effectiveSettings(r.Context())
	ch := model.Channel{Enabled: true, Mode: model.ModeAuto, Outputs: model.Outputs{HLS: true}, Failover: defaults.DefaultFailover}
	in.apply(&ch)
	applyDVRDefaults(&ch, defaults)
	if in.ID != nil {
		ch.ID = strings.TrimSpace(*in.ID)
	}
	if ch.ID == "" {
		ch.ID = "canal-" + newID(3)
	}
	profiles, err := s.profileMap(r.Context())
	if err != nil {
		s.storeError(w, r, err, "profile")
		return
	}
	if fe := validateChannel(&ch, profiles, s.storageSet(r.Context())); len(fe) > 0 {
		writeErrorDetails(w, r, http.StatusUnprocessableEntity, "invalid_channel", "Dados do canal inválidos.", fe)
		return
	}
	if err := s.st.CreateChannel(r.Context(), &ch); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, r, http.StatusConflict, "channel_exists", fmt.Sprintf("Já existe um canal com id '%s'.", ch.ID))
			return
		}
		s.storeError(w, r, err, "channel")
		return
	}
	s.audit(r, "channel.create", ch.ID, map[string]any{"name": ch.Name, "mode": ch.Mode, "enabled": ch.Enabled})
	s.kick()
	w.Header().Set("Location", "/api/v1/channels/"+ch.ID)
	writeJSON(w, http.StatusCreated, s.views(r.Context(), []model.Channel{ch})[0])
}

func (s *Server) getChannel(w http.ResponseWriter, r *http.Request) (model.Channel, bool) {
	ch, err := s.st.GetChannel(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "channel_not_found", fmt.Sprintf("Canal '%s' não encontrado.", r.PathValue("id")))
		} else {
			s.storeError(w, r, err, "channel")
		}
		return ch, false
	}
	return ch, true
}

func (s *Server) handleGetChannel(w http.ResponseWriter, r *http.Request) {
	if ch, ok := s.getChannel(w, r); ok {
		writeJSON(w, http.StatusOK, s.views(r.Context(), []model.Channel{ch})[0])
	}
}

// saveChannel validates and stores an updated channel.
func (s *Server) saveChannel(w http.ResponseWriter, r *http.Request, ch *model.Channel, action string, details map[string]any) bool {
	profiles, err := s.profileMap(r.Context())
	if err != nil {
		s.storeError(w, r, err, "profile")
		return false
	}
	if fe := validateChannel(ch, profiles, s.storageSet(r.Context())); len(fe) > 0 {
		writeErrorDetails(w, r, http.StatusUnprocessableEntity, "invalid_channel", "Dados do canal inválidos.", fe)
		return false
	}
	if err := s.st.UpdateChannel(r.Context(), ch); err != nil {
		s.storeError(w, r, err, "channel")
		return false
	}
	s.audit(r, action, ch.ID, details)
	s.kick()
	return true
}

func (s *Server) handlePatchChannel(w http.ResponseWriter, r *http.Request) {
	var in channelInput
	if err := decode(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if in.ID != nil && *in.ID != r.PathValue("id") {
		writeError(w, r, http.StatusUnprocessableEntity, "immutable_id", "O id do canal não pode ser alterado.")
		return
	}
	ch, ok := s.getChannel(w, r)
	if !ok {
		return
	}
	in.apply(&ch)
	if s.saveChannel(w, r, &ch, "channel.update", nil) {
		writeJSON(w, http.StatusOK, s.views(r.Context(), []model.Channel{ch})[0])
	}
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.getChannel(w, r)
	if !ok {
		return
	}
	st, found, _ := s.cache.Get(r.Context(), ch.ID)
	if ch.Enabled || (found && st.State != model.StateStopped && s.now().Sub(st.UpdatedAt) < 30*time.Second) {
		writeError(w, r, http.StatusConflict, "channel_not_stopped", "O canal precisa estar parado (STOPPED) para ser removido.")
		return
	}
	if err := s.st.DeleteChannel(r.Context(), ch.ID); err != nil {
		s.storeError(w, r, err, "channel")
		return
	}
	_ = s.cache.Delete(r.Context(), ch.ID)
	s.audit(r, "channel.delete", ch.ID, map[string]any{"name": ch.Name})
	s.kick()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setEnabled(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ch, ok := s.getChannel(w, r)
		if !ok {
			return
		}
		action := "channel.stop"
		if enabled {
			action = "channel.start"
		}
		if ch.Enabled != enabled {
			ch.Enabled = enabled
			if !s.saveChannel(w, r, &ch, action, nil) {
				return
			}
		} else {
			s.kick()
		}
		writeJSON(w, http.StatusAccepted, s.views(r.Context(), []model.Channel{ch})[0])
	}
}

func (s *Server) handleStartChannel(w http.ResponseWriter, r *http.Request) { s.setEnabled(true)(w, r) }
func (s *Server) handleStopChannel(w http.ResponseWriter, r *http.Request)  { s.setEnabled(false)(w, r) }

// assignedWorker returns the worker running a channel.
func (s *Server) assignedWorker(ctx context.Context, id string) (string, error) {
	as, err := s.st.ListAssignments(ctx)
	if err != nil {
		return "", err
	}
	for _, a := range as {
		if a.ChannelID == id {
			return a.WorkerID, nil
		}
	}
	return "", nil
}

func (s *Server) commandChannel(w http.ResponseWriter, r *http.Request, cmd protocol.Command, action string, details map[string]any) {
	ch, ok := s.getChannel(w, r)
	if !ok {
		return
	}
	if !ch.Enabled {
		writeError(w, r, http.StatusConflict, "channel_stopped", "O canal está parado.")
		return
	}
	wid, err := s.assignedWorker(r.Context(), ch.ID)
	if err != nil {
		s.storeError(w, r, err, "channel")
		return
	}
	if wid == "" {
		writeError(w, r, http.StatusConflict, "channel_not_assigned", "O canal ainda não foi atribuído a um worker.")
		return
	}
	cmd.ChannelID = ch.ID
	cmd.Actor = principalFrom(r.Context()).Actor
	if _, err := controller.Send(r.Context(), s.bus, wid, cmd, s.cfg.CommandTimeout); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "worker_command_failed", err.Error())
		return
	}
	if details == nil {
		details = map[string]any{}
	}
	details["worker_id"] = wid
	s.audit(r, action, ch.ID, details)
	writeJSON(w, http.StatusAccepted, map[string]any{"channel_id": ch.ID, "worker_id": wid, "accepted": true})
}

func (s *Server) handleRestartChannel(w http.ResponseWriter, r *http.Request) {
	s.commandChannel(w, r, protocol.Command{Type: protocol.CmdRestart}, "channel.restart", nil)
}

func (s *Server) handleSwitchSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Priority int    `json:"priority"`
		Reason   string `json:"reason"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ch, ok := s.getChannel(w, r)
	if !ok {
		return
	}
	found := false
	for _, src := range ch.Sources {
		found = found || src.Priority == req.Priority
	}
	if !found {
		writeError(w, r, http.StatusUnprocessableEntity, "source_not_found", fmt.Sprintf("O canal não tem fonte com prioridade %d.", req.Priority))
		return
	}
	s.commandChannel(w, r, protocol.Command{Type: protocol.CmdSwitchSource, Priority: req.Priority, Reason: req.Reason},
		"channel.switch_source", map[string]any{"priority": req.Priority, "reason": req.Reason})
}

func (s *Server) handleChannelStatus(w http.ResponseWriter, r *http.Request) {
	if ch, ok := s.getChannel(w, r); ok {
		writeJSON(w, http.StatusOK, s.views(r.Context(), []model.Channel{ch})[0].Status)
	}
}

func (s *Server) handleChannelEvents(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.getChannel(w, r); !ok {
		return
	}
	s.listEvents(w, r, r.PathValue("id"))
}

// ---- sources ----

func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	if ch, ok := s.getChannel(w, r); ok {
		writeJSON(w, http.StatusOK, map[string]any{"items": forCaller(r.Context(), ch).SortedSources(), "next_cursor": nil})
	}
}

func (s *Server) handleAddSource(w http.ResponseWriter, r *http.Request) {
	var src model.Source
	if err := decode(r, &src); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ch, ok := s.getChannel(w, r)
	if !ok {
		return
	}
	if src.Priority == 0 {
		for _, x := range ch.Sources {
			if x.Priority >= src.Priority {
				src.Priority = x.Priority + 1
			}
		}
	}
	ch.Sources = append(ch.Sources, src)
	if s.saveChannel(w, r, &ch, "source.create", map[string]any{"priority": src.Priority, "url": redact.URL(src.URL)}) {
		for _, x := range ch.Sources {
			if x.Priority == src.Priority {
				writeJSON(w, http.StatusCreated, x)
				return
			}
		}
	}
}

func (s *Server) handlePatchSource(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Priority  *int    `json:"priority"`
		URL       *string `json:"url"`
		Interface *string `json:"interface"`
	}
	if err := decode(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ch, ok := s.getChannel(w, r)
	if !ok {
		return
	}
	sid := r.PathValue("source_id")
	idx := -1
	for i, x := range ch.Sources {
		if x.ID == sid {
			idx = i
		}
	}
	if idx < 0 {
		writeError(w, r, http.StatusNotFound, "source_not_found", fmt.Sprintf("Fonte '%s' não encontrada.", sid))
		return
	}
	if in.Priority != nil {
		ch.Sources[idx].Priority = *in.Priority
	}
	if in.URL != nil {
		ch.Sources[idx].URL = *in.URL
	}
	if in.Interface != nil {
		ch.Sources[idx].Interface = *in.Interface
	}
	if s.saveChannel(w, r, &ch, "source.update", map[string]any{"source_id": sid}) {
		for _, x := range ch.Sources {
			if x.ID == sid {
				writeJSON(w, http.StatusOK, x)
				return
			}
		}
	}
}

func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.getChannel(w, r)
	if !ok {
		return
	}
	sid := r.PathValue("source_id")
	var keep []model.Source
	for _, x := range ch.Sources {
		if x.ID != sid {
			keep = append(keep, x)
		}
	}
	if len(keep) == len(ch.Sources) {
		writeError(w, r, http.StatusNotFound, "source_not_found", fmt.Sprintf("Fonte '%s' não encontrada.", sid))
		return
	}
	if len(keep) == 0 {
		writeError(w, r, http.StatusConflict, "last_source", "Não é possível remover a única fonte do canal.")
		return
	}
	ch.Sources = keep
	if s.saveChannel(w, r, &ch, "source.delete", map[string]any{"source_id": sid}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL       string  `json:"url"`
		Interface string  `json:"interface"`
		Seconds   float64 `json:"seconds"`
		WorkerID  string  `json:"worker_id"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if _, err := ingest.ParseSourceURL(req.URL); err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_url", err.Error())
		return
	}
	if req.Seconds <= 0 || req.Seconds > 15 {
		req.Seconds = 3
	}
	workers, err := s.st.ListWorkers(r.Context())
	if err != nil {
		s.storeError(w, r, err, "worker")
		return
	}
	wid := req.WorkerID
	if wid == "" {
		for _, wk := range workers {
			if wk.State != model.WorkerOffline && wk.Capabilities.HasInterface(req.Interface) {
				wid = wk.ID
				break
			}
		}
	}
	if wid == "" {
		writeError(w, r, http.StatusServiceUnavailable, "no_worker", "Nenhum worker disponível com acesso a essa interface.")
		return
	}
	cmd := protocol.Command{Type: protocol.CmdProbe, Probe: &protocol.ProbeRequest{URL: req.URL, Interface: req.Interface, Seconds: req.Seconds}}
	rep, err := controller.Send(r.Context(), s.bus, wid, cmd, time.Duration(req.Seconds*float64(time.Second))+15*time.Second)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "worker_command_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"worker_id": wid, "result": rep.Probe})
}

// ---- DVR policies (configuration only until Phase 5) ----

func (s *Server) handleListDVRPolicies(w http.ResponseWriter, r *http.Request) {
	var out []map[string]any
	after := ""
	for {
		page, err := s.st.ListChannels(r.Context(), store.ChannelFilter{Page: store.Page{After: after, Limit: 200}})
		if err != nil {
			s.storeError(w, r, err, "channel")
			return
		}
		for _, c := range page {
			out = append(out, map[string]any{"id": c.ID, "channel_id": c.ID, "group": c.Group, "enabled": c.DVR.Enabled, "retention_hours": c.DVR.RetentionHours})
		}
		if len(page) < 200 {
			break
		}
		after = page[len(page)-1].ID
	}
	if out == nil {
		out = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": nil, "note": "Gravação será implementada na Fase 5; as políticas já podem ser configuradas."})
}

func (s *Server) handlePatchDVRPolicy(w http.ResponseWriter, r *http.Request) {
	var in model.DVRConfig
	if err := decode(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ch, ok := s.getChannel(w, r)
	if !ok {
		return
	}
	ch.DVR = in
	if s.saveChannel(w, r, &ch, "dvr.policy_update", map[string]any{"enabled": in.Enabled, "retention_hours": in.RetentionHours}) {
		writeJSON(w, http.StatusOK, map[string]any{"id": ch.ID, "channel_id": ch.ID, "enabled": ch.DVR.Enabled, "retention_hours": ch.DVR.RetentionHours})
	}
}

// validateDRM normalizes and checks the protection of a channel (ADR 0008).
func validateDRM(ch *model.Channel, fe fieldErrors) {
	d := &ch.DRM
	if !d.Enabled {
		*d = model.DRMConfig{}
		return
	}
	d.Method = strings.ToLower(strings.TrimSpace(d.Method))
	if d.Method == "" {
		d.Method = drm.MethodAES128
	}
	if !slices.Contains(drm.Methods, d.Method) {
		fe.add("drm.method", "método não suportado: use aes-128")
	}
	if d.KeyRotationMinutes == 0 {
		d.KeyRotationMinutes = int(drm.DefaultRotation / time.Minute)
	}
	if d.KeyRotationMinutes < int(drm.MinRotation/time.Minute) || d.KeyRotationMinutes > int(drm.MaxRotation/time.Minute) {
		fe.add("drm.key_rotation_minutes", "entre 1 e 1440 minutos")
	}
	if !ch.Outputs.HLS && !ch.DVR.Enabled {
		fe.add("drm.enabled", "o DRM protege o HLS e o DVR: habilite pelo menos um deles")
	}
	if ch.Outputs.MPEGTSHTTP {
		fe.add("outputs.mpegts_http", "a saída MPEG-TS por HTTP não é criptografada: desative-a para usar DRM")
	}
}

// httpURL reports whether u is an absolute http(s) URL.
func httpURL(u string) bool {
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "http" || p.Scheme == "https") && p.Host != ""
}

// validateAuth normalizes and checks the access rules (ADR 0009).
func validateAuth(ch *model.Channel, fe fieldErrors) {
	a := &ch.Auth
	if !a.Enabled {
		*a = model.AuthConfig{}
		return
	}
	a.BackendURL = strings.TrimSpace(a.BackendURL)
	if a.BackendURL != "" && !httpURL(a.BackendURL) {
		fe.add("auth.backend_url", "use uma URL http:// ou https://")
	}
	clean := func(l []string) []string {
		out := []string{}
		for _, v := range l {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	a.AllowIPs, a.DenyIPs = clean(a.AllowIPs), clean(a.DenyIPs)
	if bad, ok := sessions.ValidIPList(a.AllowIPs); !ok {
		fe.add("auth.allow_ips", "IP ou rede inválida: "+bad)
	}
	if bad, ok := sessions.ValidIPList(a.DenyIPs); !ok {
		fe.add("auth.deny_ips", "IP ou rede inválida: "+bad)
	}
	if a.MaxSessions < 0 || a.MaxSessions > 1_000_000 {
		fe.add("auth.max_sessions", "entre 0 (sem limite) e 1000000")
	}
	if a.CacheSeconds < 0 || a.CacheSeconds > 86400 {
		fe.add("auth.cache_seconds", "entre 0 (padrão de 60 s) e 86400")
	}
}

// validateEPG checks the guide source.
func validateEPG(ch *model.Channel, fe fieldErrors) {
	e := &ch.EPG
	e.URL, e.XMLTVID = strings.TrimSpace(e.URL), strings.TrimSpace(e.XMLTVID)
	if e.URL == "" {
		*e = model.EPGConfig{}
		return
	}
	if !httpURL(e.URL) || len(e.URL) > 2048 {
		fe.add("epg.url", "use a URL http:// ou https:// de um arquivo XMLTV (.xml ou .xml.gz)")
	}
	if len(e.XMLTVID) > 200 {
		fe.add("epg.xmltv_id", "até 200 caracteres")
	}
}

// validateOverlay checks the logo of a channel.
func validateOverlay(ch *model.Channel, fe fieldErrors) {
	o := &ch.Overlay
	o.Image, o.Position = strings.TrimSpace(o.Image), strings.TrimSpace(o.Position)
	if !o.Enabled {
		*o = model.OverlayConfig{}
		return
	}
	if ch.Mode != model.ModeTranscode {
		fe.add("overlay.enabled", "o logo exige o modo transcode: em passthrough o vídeo é copiado, sem decodificar")
	}
	if !model.ValidOverlayImage(o.Image) {
		fe.add("overlay.image", "nome de arquivo .png ou .jpg (sem pastas) que exista na pasta de logos dos servidores de mídia")
	}
	switch o.Position {
	case "":
		o.Position = model.OverlayTopRight
	case model.OverlayTopLeft, model.OverlayTopRight, model.OverlayBottomLeft, model.OverlayBottomRight:
	default:
		fe.add("overlay.position", "top-left, top-right, bottom-left ou bottom-right")
	}
	if o.Margin < 0 || o.Margin > 1000 {
		fe.add("overlay.margin", "entre 0 e 1000 pixels")
	}
	if o.Width < 0 || o.Width > 3840 {
		fe.add("overlay.width", "entre 0 (tamanho original) e 3840 pixels")
	}
	if o.Opacity != 0 && (o.Opacity < 0.05 || o.Opacity > 1) {
		fe.add("overlay.opacity", "entre 0.05 e 1 (0 = opaco)")
	}
}

// validatePush checks the push destinations and DASH.
func validatePush(ch *model.Channel, fe fieldErrors) {
	o := &ch.Outputs
	if o.DASH && ch.DRM.Enabled {
		fe.add("outputs.dash", "o DASH não é criptografado: desative-o para usar DRM")
	}
	var clean []string
	seen := map[string]bool{}
	for i, raw := range o.Push {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		switch {
		case err != nil || u.Host == "" || (u.Scheme != "rtmp" && u.Scheme != "rtmps" && u.Scheme != "srt"):
			fe.add(fmt.Sprintf("outputs.push[%d]", i), "use rtmp://, rtmps:// ou srt:// com servidor")
		case seen[raw]:
			fe.add(fmt.Sprintf("outputs.push[%d]", i), "destino repetido")
		}
		seen[raw] = true
		clean = append(clean, raw)
	}
	if len(clean) > 10 {
		fe.add("outputs.push", "no máximo 10 destinos")
	}
	o.Push = clean
}
