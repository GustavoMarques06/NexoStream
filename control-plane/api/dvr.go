package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"nexostream/control-plane/store"
	"nexostream/media-engine/dvr"

	"nexostream/control-plane/controller"
	"nexostream/internal/model"
	"nexostream/internal/protocol"
)

// The DVR lives on the Media Node that records the channel (like
// Flussonic): these routes ask that node, or point players to it.

func (s *Server) dvrChannel(w http.ResponseWriter, r *http.Request) (model.Channel, string, bool) {
	ch, err := s.st.GetChannel(r.Context(), r.PathValue("channel_id"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "channel_not_found", "Canal '"+r.PathValue("channel_id")+"' não encontrado.")
		return ch, "", false
	}
	if !ch.DVR.Enabled {
		writeError(w, r, http.StatusConflict, "dvr_disabled", "A gravação (DVR) não está ativa neste canal.")
		return ch, "", false
	}
	wid, err := s.assignedWorker(r.Context(), ch.ID)
	if err != nil || wid == "" {
		writeError(w, r, http.StatusConflict, "channel_not_assigned", "O canal não está em nenhum servidor agora.")
		return ch, "", false
	}
	return ch, wid, true
}

func (s *Server) handleDVRRecordings(w http.ResponseWriter, r *http.Request) {
	ch, wid, ok := s.dvrChannel(w, r)
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
		hours := ch.DVR.RetentionHours
		if hours <= 0 {
			hours = 24
		}
		from = now.Add(-time.Duration(hours) * time.Hour)
	}
	rep, err := controller.Send(r.Context(), s.bus, wid, protocol.Command{Type: protocol.CmdDVRRanges, ChannelID: ch.ID, From: from, To: to}, s.cfg.CommandTimeout)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "worker_command_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channel_id": ch.ID, "worker_id": wid, "from": from, "to": to, "dvr": rep.DVR})
}

// handleDVRPlaylist redirects to the playlist served by the recording node.
func (s *Server) handleDVRPlaylist(w http.ResponseWriter, r *http.Request) {
	ch, _, ok := s.dvrChannel(w, r)
	if !ok {
		return
	}
	st, found, _ := s.cache.Get(r.Context(), ch.ID)
	base := ""
	if found && st.Outputs != nil {
		base = st.Outputs["dvr"]
	}
	if base == "" {
		writeError(w, r, http.StatusServiceUnavailable, "dvr_url_unknown",
			"O servidor do canal não publicou o endereço do DVR (NEXOSTREAM_PUBLIC_DVR_BASE).")
		return
	}
	target := strings.TrimRight(base, "/") + "/index.m3u8"
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}
	// The base URL comes from the worker's published status, not from the request.
	http.Redirect(w, r, target, http.StatusFound) //nolint:gosec // G710: see above
}

func (s *Server) handleDVRStorage(w http.ResponseWriter, r *http.Request) {
	ws, err := s.st.ListWorkers(r.Context())
	if err != nil {
		s.storeError(w, r, err, "worker")
		return
	}
	items := []map[string]any{}
	for _, wk := range ws {
		items = append(items, map[string]any{
			"worker_id": wk.ID, "state": wk.State,
			"used_bytes": wk.Resources.DVRUsedBytes, "total_bytes": wk.Resources.DVRTotalBytes,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ---- DVR storages (disks and RAID mode) ----

var validStorageID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func absPath(p string) bool {
	// Unix (/mnt/disk1) or Windows (D:\dvr, D:/dvr) absolute paths.
	return strings.HasPrefix(p, "/") || (len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/'))
}

func validateStorage(v *model.DVRStorage) fieldErrors {
	fe := fieldErrors{}
	if !validStorageID.MatchString(v.ID) {
		fe.add("id", "use de 1 a 64 caracteres [a-z0-9_-]")
	}
	if v.Mode == "" {
		v.Mode = dvr.ModeSingle
		if len(v.Disks) > 1 {
			v.Mode = dvr.ModeRAID0
		}
	}
	for i, d := range v.Disks {
		v.Disks[i] = strings.TrimSpace(d)
		if !absPath(v.Disks[i]) || strings.Contains(v.Disks[i], "..") {
			fe.add(fmt.Sprintf("disks[%d]", i), "use um caminho absoluto no servidor, ex.: /mnt/disk1")
		}
	}
	if err := (dvr.Storage{Mode: v.Mode, Disks: v.Disks}).Validate(); err != nil {
		fe.add("mode", err.Error())
	}
	return fe
}

func (s *Server) handleListStorages(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListDVRStorages(r.Context())
	if err != nil {
		s.storeError(w, r, err, "dvr_storage")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "next_cursor": nil})
}

func (s *Server) handleCreateStorage(w http.ResponseWriter, r *http.Request) {
	var v model.DVRStorage
	if err := decode(r, &v); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if fe := validateStorage(&v); len(fe) > 0 {
		writeErrorDetails(w, r, http.StatusUnprocessableEntity, "invalid_dvr_storage", "Armazenamento inválido.", fe)
		return
	}
	if err := s.st.CreateDVRStorage(r.Context(), v); err != nil {
		s.storeError(w, r, err, "dvr_storage")
		return
	}
	s.audit(r, "dvr_storage.create", v.ID, map[string]any{"mode": v.Mode, "disks": v.Disks})
	got, _ := s.st.GetDVRStorage(r.Context(), v.ID)
	writeJSON(w, http.StatusCreated, got)
}

func (s *Server) handlePatchStorage(w http.ResponseWriter, r *http.Request) {
	v, err := s.st.GetDVRStorage(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, r, err, "dvr_storage")
		return
	}
	var in struct {
		Mode  *string   `json:"mode"`
		Disks *[]string `json:"disks"`
	}
	if err := decode(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if in.Mode != nil {
		v.Mode = *in.Mode
	}
	if in.Disks != nil {
		v.Disks = *in.Disks
	}
	if fe := validateStorage(&v); len(fe) > 0 {
		writeErrorDetails(w, r, http.StatusUnprocessableEntity, "invalid_dvr_storage", "Armazenamento inválido.", fe)
		return
	}
	if err := s.st.UpdateDVRStorage(r.Context(), v); err != nil {
		s.storeError(w, r, err, "dvr_storage")
		return
	}
	if err := s.touchChannelsUsingStorage(r, v.ID); err != nil {
		s.storeError(w, r, err, "channel")
		return
	}
	s.audit(r, "dvr_storage.update", v.ID, map[string]any{"mode": v.Mode, "disks": v.Disks})
	got, _ := s.st.GetDVRStorage(r.Context(), v.ID)
	writeJSON(w, http.StatusOK, got)
}

// touchChannelsUsingStorage bumps the revision of the channels that record
// on a storage, so the controller sends the new disk list to their nodes.
func (s *Server) touchChannelsUsingStorage(r *http.Request, id string) error {
	after := ""
	for {
		page, err := s.st.ListChannels(r.Context(), store.ChannelFilter{Page: store.Page{After: after, Limit: 200}})
		if err != nil {
			return err
		}
		for _, c := range page {
			if c.DVR.Storage == id {
				c := c
				if err := s.st.UpdateChannel(r.Context(), &c); err != nil {
					return err
				}
			}
		}
		if len(page) < 200 {
			break
		}
		after = page[len(page)-1].ID
	}
	s.kick()
	return nil
}

func (s *Server) handleDeleteStorage(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteDVRStorage(r.Context(), r.PathValue("id")); err != nil {
		s.storeError(w, r, err, "dvr_storage")
		return
	}
	s.audit(r, "dvr_storage.delete", r.PathValue("id"), nil)
	w.WriteHeader(http.StatusNoContent)
}
