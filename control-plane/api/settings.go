package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"

	"nexostream/control-plane/settings"
	"nexostream/internal/model"
)

// Settings page (like the global options of a Flussonic config).

// effectiveSettings returns the saved settings, or, before the first save,
// the defaults with this API's .env values on top.
func (s *Server) effectiveSettings(ctx context.Context) (model.Settings, bool) {
	st, saved, err := settings.Load(ctx, s.st)
	if err != nil {
		s.log.Error("load settings", "error", err)
	}
	if !saved {
		st.AccessTokenMinutes = int(s.cfg.AccessTTL.Minutes())
		st.RefreshTokenHours = int(s.cfg.RefreshTTL.Hours())
		st.LoginAttemptsPerMinute = s.cfg.LoginRatePerMinute
		st.DefaultProfile = s.cfg.DefaultProfile
		st = st.WithDefaults()
	}
	return st, saved
}

type settingsView struct {
	Settings model.Settings `json:"settings"`
	Defaults model.Settings `json:"defaults"`
	// Saved is false while the values still come from the .env files.
	Saved bool `json:"saved"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st, saved := s.effectiveSettings(r.Context())
	writeJSON(w, http.StatusOK, settingsView{Settings: st, Defaults: model.DefaultSettings(), Saved: saved})
}

// changedFields lists the JSON fields that differ (for the audit log).
func changedFields(a, b any) []string {
	var ma, mb map[string]any
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	_ = json.Unmarshal(ja, &ma)
	_ = json.Unmarshal(jb, &mb)
	var out []string
	for k, v := range mb {
		if k == "updated_at" || k == "updated_by" {
			continue
		}
		if !reflect.DeepEqual(ma[k], v) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Server) profileSet(ctx context.Context) map[string]bool {
	ps, err := s.profileMap(ctx)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for id := range ps {
		out[id] = true
	}
	return out
}

// saveSettings validates and stores new global settings; it returns the
// field errors (nil when saved).
func (s *Server) saveSettings(r *http.Request, in model.Settings) (settings.FieldErrors, error) {
	settings.Normalize(&in)
	in.DefaultFailover = in.DefaultFailover.WithDefaults()
	if fe := settings.Validate(in, s.profileSet(r.Context()), s.storageSet(r.Context())); len(fe) > 0 {
		return fe, nil
	}
	old, _ := s.effectiveSettings(r.Context())
	in.UpdatedAt = s.now().UTC()
	if p := principalFrom(r.Context()); p != nil {
		in.UpdatedBy = p.Actor
	}
	if err := settings.Save(r.Context(), s.st, in); err != nil {
		return nil, err
	}
	s.loginPerMin.Store(int64(in.LoginAttemptsPerMinute))
	s.audit(r, "settings.update", "global", map[string]any{"changed": changedFields(old, in)})
	s.kick()
	return nil, nil
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var in model.Settings
	if err := decode(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	fe, err := s.saveSettings(r, in)
	if err != nil {
		s.storeError(w, r, err, "settings")
		return
	}
	if len(fe) > 0 {
		writeErrorDetails(w, r, http.StatusUnprocessableEntity, "invalid_settings", "Configurações inválidas.", fe)
		return
	}
	s.handleGetSettings(w, r)
}

type serverView struct {
	WorkerID   string               `json:"worker_id"`
	Known      bool                 `json:"known"` // the node has sent heartbeats
	State      model.WorkerState    `json:"state,omitempty"`
	Interfaces []string             `json:"interfaces"`
	Settings   model.ServerSettings `json:"settings"`
	Runtime    model.WorkerRuntime  `json:"runtime"`
}

func (s *Server) serverViews(ctx context.Context) ([]serverView, error) {
	ws, err := s.st.ListWorkers(ctx)
	if err != nil {
		return nil, err
	}
	overrides, err := settings.ListServers(ctx, s.st)
	if err != nil {
		return nil, err
	}
	glob, _ := s.effectiveSettings(ctx)
	views := map[string]*serverView{}
	for _, wk := range ws {
		views[wk.ID] = &serverView{WorkerID: wk.ID, Known: true, State: wk.State, Interfaces: wk.Capabilities.Interfaces}
	}
	for id, o := range overrides {
		v, ok := views[id]
		if !ok {
			v = &serverView{WorkerID: id}
			views[id] = v
		}
		v.Settings = o
	}
	out := make([]serverView, 0, len(views))
	for id, v := range views {
		if v.Settings.WorkerID == "" {
			v.Settings.WorkerID = id
		}
		if v.Settings.IngestInterfaces == nil {
			v.Settings.IngestInterfaces = []string{}
		}
		if v.Interfaces == nil {
			v.Interfaces = []string{}
		}
		v.Runtime = glob.Runtime(v.Settings)
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkerID < out[j].WorkerID })
	return out, nil
}

func (s *Server) handleListServerSettings(w http.ResponseWriter, r *http.Request) {
	out, err := s.serverViews(r.Context())
	if err != nil {
		s.storeError(w, r, err, "settings")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) saveServerSettings(r *http.Request, in model.ServerSettings) (settings.FieldErrors, error) {
	settings.NormalizeServer(&in)
	if fe := settings.ValidateServer(in); len(fe) > 0 {
		return fe, nil
	}
	old, _ := settings.LoadServer(r.Context(), s.st, in.WorkerID)
	in.UpdatedAt = s.now().UTC()
	if p := principalFrom(r.Context()); p != nil {
		in.UpdatedBy = p.Actor
	}
	if err := settings.SaveServer(r.Context(), s.st, in); err != nil {
		return nil, err
	}
	s.audit(r, "settings.server_update", in.WorkerID, map[string]any{"changed": changedFields(old, in)})
	s.kick()
	return nil, nil
}

func (s *Server) handlePutServerSettings(w http.ResponseWriter, r *http.Request) {
	var in model.ServerSettings
	if err := decode(r, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	in.WorkerID = r.PathValue("id")
	fe, err := s.saveServerSettings(r, in)
	if err != nil {
		s.storeError(w, r, err, "settings")
		return
	}
	if len(fe) > 0 {
		writeErrorDetails(w, r, http.StatusUnprocessableEntity, "invalid_settings", "Configurações do servidor inválidas.", fe)
		return
	}
	views, err := s.serverViews(r.Context())
	if err != nil {
		s.storeError(w, r, err, "settings")
		return
	}
	for _, v := range views {
		if v.WorkerID == in.WorkerID {
			writeJSON(w, http.StatusOK, v)
			return
		}
	}
	writeJSON(w, http.StatusOK, in)
}

// handleSettingsDefaults returns what new channels start with (readable by
// everyone who sees channels: the channel form uses it).
func (s *Server) handleSettingsDefaults(w http.ResponseWriter, r *http.Request) {
	st, _ := s.effectiveSettings(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"system_name":                 st.SystemName,
		"default_profile":             st.DefaultProfile,
		"default_failover":            st.DefaultFailover,
		"dvr_default_retention_hours": st.DVRDefaultRetentionHours,
		"dvr_default_storage":         st.DVRDefaultStorage,
	})
}
