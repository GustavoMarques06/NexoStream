package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"nexostream/control-plane/settings"
	"nexostream/control-plane/store"
	"nexostream/control-plane/textconfig"
	"nexostream/internal/model"
)

// Text configuration editor (like Flussonic's config): export everything as
// text, validate an edited text and apply it.

type configPlan struct {
	Create    []string `json:"create"`
	Update    []string `json:"update"`
	Delete    []string `json:"delete"`
	Unchanged int      `json:"unchanged"`
}

type configResult struct {
	OK     bool               `json:"ok"`
	Errors []textconfig.Error `json:"errors"`
	// Settings is "update", "unchanged" or "" (no settings block).
	Settings string     `json:"settings"`
	Servers  configPlan `json:"servers"`
	Channels configPlan `json:"channels"`
	Storages configPlan `json:"storages"`
	Applied  bool       `json:"applied"`
}

func settingsFingerprint(st model.Settings) string {
	settings.Normalize(&st)
	st.DefaultFailover = st.DefaultFailover.WithDefaults()
	st.UpdatedAt, st.UpdatedBy = time.Time{}, ""
	b, _ := json.Marshal(st)
	return string(b)
}

func serverFingerprint(o model.ServerSettings) string {
	settings.NormalizeServer(&o)
	o.UpdatedAt, o.UpdatedBy = time.Time{}, ""
	if len(o.IngestInterfaces) == 0 {
		o.IngestInterfaces = nil
	}
	b, _ := json.Marshal(o)
	return string(b)
}

func newPlan() configPlan {
	return configPlan{Create: []string{}, Update: []string{}, Delete: []string{}}
}

// channelFingerprint compares only what the text can express.
func channelFingerprint(c model.Channel) string {
	c.Revision = 0
	c.CreatedAt, c.UpdatedAt = time.Time{}, time.Time{}
	src := c.SortedSources()
	for i := range src {
		src[i].ID = ""
	}
	c.Sources = src
	c.Failover = c.Failover.WithDefaults()
	if c.Profiles == nil {
		c.Profiles = []string{}
	}
	b, _ := json.Marshal(c)
	return string(b)
}

func storageFingerprint(s model.DVRStorage) string {
	b, _ := json.Marshal([]any{s.ID, s.Mode, s.Disks})
	return string(b)
}

func (s *Server) allChannels(ctx context.Context) ([]model.Channel, error) {
	var out []model.Channel
	after := ""
	for {
		page, err := s.st.ListChannels(ctx, store.ChannelFilter{Page: store.Page{After: after, Limit: 200}})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < 200 {
			return out, nil
		}
		after = page[len(page)-1].ID
	}
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	chs, err := s.allChannels(r.Context())
	if err != nil {
		s.storeError(w, r, err, "channel")
		return
	}
	sts, err := s.st.ListDVRStorages(r.Context())
	if err != nil {
		s.storeError(w, r, err, "dvr_storage")
		return
	}
	if !canSeeSecrets(r.Context()) { // a read-only export must not carry stream keys or passphrases
		for i := range chs {
			chs[i] = redactedChannel(chs[i])
		}
	}
	tc := textconfig.Config{Channels: chs, Storages: sts}
	// System and server settings only for those who can change them.
	if p := principalFrom(r.Context()); p != nil && p.Has(model.ScopeSettingsAdmin) {
		st, _ := s.effectiveSettings(r.Context())
		tc.Settings = &st
		servers, err := settings.ListServers(r.Context(), s.st)
		if err != nil {
			s.storeError(w, r, err, "settings")
			return
		}
		for _, id := range settings.ServerIDs(servers) {
			tc.Servers = append(tc.Servers, servers[id])
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="nexostream.conf"`)
	_, _ = w.Write([]byte(textconfig.Format(tc)))
}

type configRequest struct {
	Text  string `json:"text"`
	Prune bool   `json:"prune"` // delete channels and storages missing from the text
}

// plan parses and validates the text and computes the changes.
func (s *Server) plan(ctx context.Context, req configRequest) (textconfig.Config, configResult, map[string]model.Channel, map[string]model.DVRStorage, error) {
	res := configResult{Channels: newPlan(), Storages: newPlan(), Servers: newPlan(), Errors: []textconfig.Error{}}
	cfg, perrs := textconfig.Parse(req.Text)
	res.Errors = append(res.Errors, perrs...)

	existing, err := s.allChannels(ctx)
	if err != nil {
		return cfg, res, nil, nil, err
	}
	exCh := map[string]model.Channel{}
	for _, c := range existing {
		exCh[c.ID] = c
	}
	exStList, err := s.st.ListDVRStorages(ctx)
	if err != nil {
		return cfg, res, nil, nil, err
	}
	exSt := map[string]model.DVRStorage{}
	for _, v := range exStList {
		exSt[v.ID] = v
	}
	profiles, err := s.profileMap(ctx)
	if err != nil {
		return cfg, res, nil, nil, err
	}

	// Storages available after applying: the text's, plus existing ones kept.
	avail := map[string]bool{}
	inText := map[string]bool{}
	for i := range cfg.Storages {
		v := &cfg.Storages[i]
		inText[v.ID] = true
		avail[v.ID] = true
		for field, msg := range validateStorage(v) {
			res.Errors = append(res.Errors, textconfig.Error{Line: cfg.Lines["dvr_storage "+v.ID], Message: "dvr_storage " + v.ID + ": " + field + ": " + msg})
		}
		if old, ok := exSt[v.ID]; !ok {
			res.Storages.Create = append(res.Storages.Create, v.ID)
		} else if storageFingerprint(old) != storageFingerprint(*v) {
			res.Storages.Update = append(res.Storages.Update, v.ID)
		} else {
			res.Storages.Unchanged++
		}
	}
	for id := range exSt {
		if !inText[id] {
			if req.Prune {
				res.Storages.Delete = append(res.Storages.Delete, id)
			} else {
				avail[id] = true
			}
		}
	}

	// System settings and per-server settings.
	canSettings := false
	if p := principalFrom(ctx); p != nil {
		canSettings = p.Has(model.ScopeSettingsAdmin)
	}
	addFE := func(line int, prefix string, fe settings.FieldErrors) {
		keys := make([]string, 0, len(fe))
		for k := range fe {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			res.Errors = append(res.Errors, textconfig.Error{Line: line, Message: prefix + ": " + k + ": " + fe[k]})
		}
	}
	if cfg.Settings != nil {
		line := cfg.Lines["settings"]
		cur, _ := s.effectiveSettings(ctx)
		// The logo is not in the text: keep the one set in the panel.
		if cfg.Settings.BrandLogo == "" {
			cfg.Settings.BrandLogo = cur.BrandLogo
		}
		if settingsFingerprint(cur) == settingsFingerprint(*cfg.Settings) {
			res.Settings = "unchanged"
		} else {
			res.Settings = "update"
			if !canSettings {
				res.Errors = append(res.Errors, textconfig.Error{Line: line, Message: "alterar o bloco settings exige o escopo settings:admin"})
			}
			pset := map[string]bool{}
			for id := range profiles {
				pset[id] = true
			}
			st := *cfg.Settings
			settings.Normalize(&st)
			addFE(line, "settings", settings.Validate(st, pset, avail))
		}
	}
	savedServers, err := settings.ListServers(ctx, s.st)
	if err != nil {
		return cfg, res, nil, nil, err
	}
	for _, o := range cfg.Servers {
		line := cfg.Lines["server "+o.WorkerID]
		old, ok := savedServers[o.WorkerID]
		switch {
		case !ok:
			res.Servers.Create = append(res.Servers.Create, o.WorkerID)
		case serverFingerprint(old) != serverFingerprint(o):
			res.Servers.Update = append(res.Servers.Update, o.WorkerID)
		default:
			res.Servers.Unchanged++
			continue
		}
		if !canSettings {
			res.Errors = append(res.Errors, textconfig.Error{Line: line, Message: "alterar o bloco server exige o escopo settings:admin"})
		}
		o := o
		settings.NormalizeServer(&o)
		addFE(line, "server "+o.WorkerID, settings.ValidateServer(o))
	}

	chInText := map[string]bool{}
	for i := range cfg.Channels {
		c := &cfg.Channels[i]
		chInText[c.ID] = true
		line := cfg.Lines["stream "+c.ID]
		keys := []string{}
		fe := validateChannel(c, profiles, avail)
		for k := range fe {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			res.Errors = append(res.Errors, textconfig.Error{Line: line, Message: "stream " + c.ID + ": " + k + ": " + fe[k]})
		}
		if old, ok := exCh[c.ID]; !ok {
			res.Channels.Create = append(res.Channels.Create, c.ID)
		} else if channelFingerprint(old) != channelFingerprint(*c) {
			res.Channels.Update = append(res.Channels.Update, c.ID)
		} else {
			res.Channels.Unchanged++
		}
	}
	for id := range exCh {
		if !chInText[id] && req.Prune {
			res.Channels.Delete = append(res.Channels.Delete, id)
		}
	}
	for _, p := range []*configPlan{&res.Channels, &res.Storages, &res.Servers} {
		sort.Strings(p.Create)
		sort.Strings(p.Update)
		sort.Strings(p.Delete)
	}
	sort.SliceStable(res.Errors, func(i, j int) bool { return res.Errors[i].Line < res.Errors[j].Line })
	res.OK = len(res.Errors) == 0
	return cfg, res, exCh, exSt, nil
}

func (s *Server) handleValidateConfig(w http.ResponseWriter, r *http.Request) {
	var req configRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	_, res, _, _, err := s.plan(r.Context(), req)
	if err != nil {
		s.storeError(w, r, err, "config")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleApplyConfig(w http.ResponseWriter, r *http.Request) {
	var req configRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if p := principalFrom(r.Context()); !p.Has(model.ScopeDVRWrite) && strings.Contains(req.Text, "dvr_storage") {
		writeError(w, r, http.StatusForbidden, "forbidden", "Alterar armazenamentos de DVR exige o escopo dvr:write.")
		return
	}
	cfg, res, exCh, _, err := s.plan(r.Context(), req)
	if err != nil {
		s.storeError(w, r, err, "config")
		return
	}
	if !res.OK {
		writeJSON(w, http.StatusUnprocessableEntity, res)
		return
	}
	ctx := r.Context()
	fail := func(err error, what string) {
		s.log.Error("config apply", "error", err, "item", what)
		res.OK = false
		res.Errors = append(res.Errors, textconfig.Error{Message: what + ": " + err.Error()})
		writeJSON(w, http.StatusInternalServerError, res)
	}
	// Storages first (channels may reference them), deletions last.
	for _, v := range cfg.Storages {
		var err error
		if contains(res.Storages.Create, v.ID) {
			err = s.st.CreateDVRStorage(ctx, v)
		} else if contains(res.Storages.Update, v.ID) {
			err = s.st.UpdateDVRStorage(ctx, v)
		}
		if err != nil {
			fail(err, "dvr_storage "+v.ID)
			return
		}
	}
	// Settings before channels: new channels take the new defaults.
	if res.Settings == "update" {
		if fe, err := s.saveSettings(r, *cfg.Settings); err != nil || len(fe) > 0 {
			if err == nil {
				err = errors.New("configurações inválidas")
			}
			fail(err, "settings")
			return
		}
	}
	for _, o := range cfg.Servers {
		if !contains(res.Servers.Create, o.WorkerID) && !contains(res.Servers.Update, o.WorkerID) {
			continue
		}
		if fe, err := s.saveServerSettings(r, o); err != nil || len(fe) > 0 {
			if err == nil {
				err = errors.New("configurações do servidor inválidas")
			}
			fail(err, "server "+o.WorkerID)
			return
		}
	}
	for _, c := range cfg.Channels {
		c := c
		var err error
		switch {
		case contains(res.Channels.Create, c.ID):
			err = s.st.CreateChannel(ctx, &c)
		case contains(res.Channels.Update, c.ID):
			old := exCh[c.ID]
			c.CreatedAt = old.CreatedAt
			err = s.st.UpdateChannel(ctx, &c)
		}
		if err != nil {
			fail(err, "stream "+c.ID)
			return
		}
	}
	for _, id := range res.Channels.Delete {
		if err := s.st.DeleteChannel(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			fail(err, "stream "+id)
			return
		}
		_ = s.cache.Delete(ctx, id)
	}
	for _, id := range res.Storages.Delete {
		if err := s.st.DeleteDVRStorage(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			fail(err, "dvr_storage "+id)
			return
		}
	}
	for _, id := range res.Storages.Update {
		if err := s.touchChannelsUsingStorage(r, id); err != nil {
			fail(err, "dvr_storage "+id)
			return
		}
	}
	res.Applied = true
	s.audit(r, "config.apply", "config", map[string]any{
		"channels": res.Channels, "storages": res.Storages, "settings": res.Settings, "servers": res.Servers, "prune": req.Prune,
	})
	s.kick()
	writeJSON(w, http.StatusOK, res)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
