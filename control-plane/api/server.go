// Package api implements the management REST API (/api/v1) described in
// docs/api.md: authentication, RBAC, channels, sources, profiles, workers,
// events (including Server-Sent Events), audit log and integration tokens.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nexostream/control-plane/settings"
	"nexostream/control-plane/sharedstate"
	"nexostream/control-plane/statuscache"
	"nexostream/control-plane/store"
	"nexostream/internal/bus"
	"nexostream/internal/model"
	"nexostream/internal/protocol"
)

// Config configures the API.
type Config struct {
	JWTKey         []byte
	AccessTTL      time.Duration // 15 min
	RefreshTTL     time.Duration // 12 h
	DefaultProfile string
	CommandTimeout time.Duration
	// PanelDir serves the built web panel (development; nginx in production).
	PanelDir string
	// CORSOrigins allowed for browser clients (development).
	CORSOrigins []string
	// TrustProxy lets the API read the client IP from X-Real-IP or
	// X-Forwarded-For, but only when the TCP peer is loopback or inside
	// TrustedProxies; otherwise any client could forge its address and
	// escape the login rate limit.
	TrustProxy     bool
	TrustedProxies []string // extra proxy IPs or CIDRs besides loopback
	// LoginRatePerMinute limits login attempts per IP.
	LoginRatePerMinute int
	// Account lockout: after LockoutThreshold consecutive failures an account
	// is locked for LockoutBase, doubling at each further multiple of the
	// threshold up to LockoutMax. A negative threshold turns it off. Defaults:
	// 5 failures, 5 minutes, 1 hour.
	LockoutThreshold int
	LockoutBase      time.Duration
	LockoutMax       time.Duration
	// PlaybackTokenKey signs playback tokens of protected channels (same
	// secret as the Media Nodes). Empty disables the token endpoint.
	PlaybackTokenKey []byte
	Version          string
}

// Deps are the API dependencies.
type Deps struct {
	Store store.Store
	Cache statuscache.Cache
	Bus   bus.Bus
	Log   *slog.Logger
	// Shared holds the state every API instance must agree on (login rate
	// limit, idempotent replies). Nil uses this instance's memory.
	Shared sharedstate.State
}

// Server is the HTTP API.
type Server struct {
	cfg         Config
	st          store.Store
	cache       statuscache.Cache
	bus         bus.Bus
	log         *slog.Logger
	mux         *http.ServeMux
	hub         *hub
	shared      sharedstate.State // login limits and idempotent replies, shared by every API instance
	loginPerMin atomic.Int64      // login attempts per minute per address (Settings page)
	proxies     []*net.IPNet
	touch       sync.Map // token id -> last touch time
	now         func() time.Time
}

// New builds the server and subscribes to the bus for live events.
func New(cfg Config, d Deps) (*Server, error) {
	if cfg.AccessTTL == 0 {
		cfg.AccessTTL = 15 * time.Minute
	}
	if cfg.RefreshTTL == 0 {
		cfg.RefreshTTL = 12 * time.Hour
	}
	if cfg.DefaultProfile == "" {
		cfg.DefaultProfile = "hd"
	}
	if cfg.CommandTimeout == 0 {
		cfg.CommandTimeout = 5 * time.Second
	}
	if cfg.LoginRatePerMinute == 0 {
		cfg.LoginRatePerMinute = 10
	}
	if cfg.LockoutThreshold == 0 {
		cfg.LockoutThreshold = defaultLockoutThreshold
	}
	if cfg.LockoutBase == 0 {
		cfg.LockoutBase = defaultLockoutBase
	}
	if cfg.LockoutMax == 0 {
		cfg.LockoutMax = defaultLockoutMax
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	s := &Server{
		cfg: cfg, st: d.Store, cache: d.Cache, bus: d.Bus, log: d.Log.With("component", "api"),
		mux: http.NewServeMux(), shared: d.Shared, now: time.Now,
	}
	if s.shared == nil {
		s.shared = sharedstate.NewMemory() // development and single-instance setups
	}
	s.loginPerMin.Store(int64(cfg.LoginRatePerMinute))
	h, err := newHub(d.Bus, s.log)
	if err != nil {
		return nil, err
	}
	s.hub = h
	if st, saved, err := settings.Load(context.Background(), d.Store); err == nil && saved {
		s.loginPerMin.Store(int64(st.LoginAttemptsPerMinute))
	}
	for _, c := range cfg.TrustedProxies {
		if !strings.Contains(c, "/") {
			if strings.Contains(c, ":") {
				c += "/128"
			} else {
				c += "/32"
			}
		}
		if _, n, err := net.ParseCIDR(strings.TrimSpace(c)); err == nil {
			s.proxies = append(s.proxies, n)
		} else {
			return nil, fmt.Errorf("trusted proxy %q: %w", c, err)
		}
	}
	s.routes()
	return s, nil
}

// Close stops background work.
func (s *Server) Close() { s.hub.close() }

func (s *Server) routes() {
	m := s.mux
	// Public.
	m.HandleFunc("POST /api/v1/auth/token", s.handleLogin)
	m.HandleFunc("GET /api/v1/branding", s.handleBranding)
	m.HandleFunc("POST /api/v1/auth/refresh", s.handleRefresh)
	m.HandleFunc("GET /api/v1/openapi.json", s.handleOpenAPI)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	m.HandleFunc("GET /readyz", s.handleReady)

	// Session.
	m.Handle("GET /api/v1/auth/me", s.authed("", s.handleMe))
	m.Handle("POST /api/v1/auth/logout", s.authed("", s.handleLogout))

	// Channels.
	m.Handle("GET /api/v1/channels", s.authed(model.ScopeChannelsRead, s.handleListChannels))
	m.Handle("POST /api/v1/channels", s.authed(model.ScopeChannelsWrite, s.idempotent(s.handleCreateChannel)))
	m.Handle("GET /api/v1/channels/{id}", s.authed(model.ScopeChannelsRead, s.handleGetChannel))
	m.Handle("PATCH /api/v1/channels/{id}", s.authed(model.ScopeChannelsWrite, s.handlePatchChannel))
	m.Handle("DELETE /api/v1/channels/{id}", s.authed(model.ScopeChannelsWrite, s.handleDeleteChannel))
	m.Handle("POST /api/v1/channels/{id}/start", s.authed(model.ScopeChannelsOperate, s.handleStartChannel))
	m.Handle("POST /api/v1/channels/{id}/stop", s.authed(model.ScopeChannelsOperate, s.handleStopChannel))
	m.Handle("POST /api/v1/channels/{id}/restart", s.authed(model.ScopeChannelsOperate, s.handleRestartChannel))
	m.Handle("POST /api/v1/channels/{id}/switch-source", s.authed(model.ScopeChannelsOperate, s.handleSwitchSource))
	m.Handle("GET /api/v1/channels/{id}/status", s.authed(model.ScopeChannelsRead, s.handleChannelStatus))
	m.Handle("GET /api/v1/channels/{id}/events", s.authed(model.ScopeChannelsRead, s.handleChannelEvents))
	m.Handle("POST /api/v1/channels/{id}/playback-token", s.authed(model.ScopeChannelsRead, s.handlePlaybackToken))
	m.Handle("GET /api/v1/channels/{id}/sessions", s.authed(model.ScopeChannelsRead, s.handleChannelSessions))
	m.Handle("DELETE /api/v1/channels/{id}/sessions/{session_id}", s.authed(model.ScopeChannelsOperate, s.handleKillSession))
	m.Handle("GET /api/v1/sessions", s.authed(model.ScopeChannelsRead, s.handleAllSessions))
	m.Handle("GET /api/v1/sessions/history", s.authed(model.ScopeChannelsRead, s.handleSessionHistory))
	m.Handle("GET /api/v1/alerts", s.authed(model.ScopeChannelsRead, s.handleAlerts))
	m.Handle("GET /api/v1/stats", s.authed(model.ScopeChannelsRead, s.handleStats))
	m.Handle("GET /api/v1/stats/top-channels", s.authed(model.ScopeChannelsRead, s.handleTopChannels))
	m.Handle("GET /api/v1/channels/{id}/epg", s.authed(model.ScopeChannelsRead, s.handleChannelEPG))
	m.Handle("POST /api/v1/channels/{id}/epg/refresh", s.authed(model.ScopeChannelsWrite, s.handleRefreshEPG))
	m.Handle("GET /api/v1/epg.xml", s.authed(model.ScopeChannelsRead, s.handleExportEPG))

	// Sources.
	m.Handle("GET /api/v1/channels/{id}/sources", s.authed(model.ScopeChannelsRead, s.handleListSources))
	m.Handle("POST /api/v1/channels/{id}/sources", s.authed(model.ScopeChannelsWrite, s.handleAddSource))
	m.Handle("PATCH /api/v1/channels/{id}/sources/{source_id}", s.authed(model.ScopeChannelsWrite, s.handlePatchSource))
	m.Handle("DELETE /api/v1/channels/{id}/sources/{source_id}", s.authed(model.ScopeChannelsWrite, s.handleDeleteSource))
	m.Handle("POST /api/v1/sources/probe", s.authed(model.ScopeChannelsWrite, s.handleProbe))

	// Transcode profiles.
	m.Handle("GET /api/v1/transcode-profiles", s.authed(model.ScopeChannelsRead, s.handleListProfiles))
	m.Handle("POST /api/v1/transcode-profiles", s.authed(model.ScopeChannelsWrite, s.handleCreateProfile))
	m.Handle("GET /api/v1/transcode-profiles/{id}", s.authed(model.ScopeChannelsRead, s.handleGetProfile))
	m.Handle("PATCH /api/v1/transcode-profiles/{id}", s.authed(model.ScopeChannelsWrite, s.handlePatchProfile))
	m.Handle("DELETE /api/v1/transcode-profiles/{id}", s.authed(model.ScopeChannelsWrite, s.handleDeleteProfile))

	// Workers.
	m.Handle("GET /api/v1/workers", s.authed(model.ScopeWorkersRead, s.handleListWorkers))
	m.Handle("GET /api/v1/workers/{id}", s.authed(model.ScopeWorkersRead, s.handleGetWorker))
	m.Handle("POST /api/v1/workers/{id}/drain", s.authed(model.ScopeWorkersWrite, s.handleDrain(true)))
	m.Handle("POST /api/v1/workers/{id}/undrain", s.authed(model.ScopeWorkersWrite, s.handleDrain(false)))

	// DVR (Phase 5).
	m.Handle("GET /api/v1/dvr/policies", s.authed(model.ScopeDVRRead, s.handleListDVRPolicies))
	m.Handle("PATCH /api/v1/dvr/policies/{id}", s.authed(model.ScopeDVRWrite, s.handlePatchDVRPolicy))
	m.Handle("GET /api/v1/dvr/storage", s.authed(model.ScopeDVRRead, s.handleDVRStorage))
	m.Handle("GET /api/v1/dvr/storages", s.authed(model.ScopeDVRRead, s.handleListStorages))
	m.Handle("POST /api/v1/dvr/storages", s.authed(model.ScopeDVRWrite, s.handleCreateStorage))
	m.Handle("PATCH /api/v1/dvr/storages/{id}", s.authed(model.ScopeDVRWrite, s.handlePatchStorage))
	m.Handle("DELETE /api/v1/dvr/storages/{id}", s.authed(model.ScopeDVRWrite, s.handleDeleteStorage))
	m.Handle("GET /api/v1/dvr/{channel_id}/recordings", s.authed(model.ScopeDVRRead, s.handleDVRRecordings))
	m.Handle("GET /api/v1/dvr/{channel_id}/playlist.m3u8", s.authed(model.ScopeDVRRead, s.handleDVRPlaylist))

	// Events, audit, administration.
	m.Handle("GET /api/v1/events", s.authed(model.ScopeChannelsRead, s.handleListEvents))
	m.Handle("GET /api/v1/stream/events", s.authed(model.ScopeChannelsRead, s.handleStream))
	m.Handle("GET /api/v1/audit-log", s.authed(model.ScopeAuditRead, s.handleListAudit))
	m.Handle("GET /api/v1/users", s.authed(model.ScopeUsersAdmin, s.handleListUsers))
	m.Handle("POST /api/v1/users", s.authed(model.ScopeUsersAdmin, s.handleCreateUser))
	m.Handle("PATCH /api/v1/users/{id}", s.authed(model.ScopeUsersAdmin, s.handlePatchUser))
	m.Handle("GET /api/v1/api-tokens", s.authed(model.ScopeTokensAdmin, s.handleListTokens))
	m.Handle("POST /api/v1/api-tokens", s.authed(model.ScopeTokensAdmin, s.handleCreateToken))
	m.Handle("DELETE /api/v1/api-tokens/{id}", s.authed(model.ScopeTokensAdmin, s.handleRevokeToken))
	m.Handle("GET /api/v1/system/summary", s.authed(model.ScopeChannelsRead, s.handleSummary))

	// Settings page (Flussonic-style global and per-server options).
	m.Handle("GET /api/v1/settings/defaults", s.authed(model.ScopeChannelsRead, s.handleSettingsDefaults))
	m.Handle("GET /api/v1/settings", s.authed(model.ScopeSettingsAdmin, s.handleGetSettings))
	m.Handle("PUT /api/v1/settings", s.authed(model.ScopeSettingsAdmin, s.handlePutSettings))
	m.Handle("GET /api/v1/settings/servers", s.authed(model.ScopeSettingsAdmin, s.handleListServerSettings))
	m.Handle("PUT /api/v1/settings/servers/{id}", s.authed(model.ScopeSettingsAdmin, s.handlePutServerSettings))

	// Text configuration editor (Flussonic-style).
	m.Handle("GET /api/v1/config", s.authed(model.ScopeChannelsRead, s.handleGetConfig))
	m.Handle("POST /api/v1/config/validate", s.authed(model.ScopeChannelsRead, s.handleValidateConfig))
	m.Handle("POST /api/v1/config/apply", s.authed(model.ScopeChannelsWrite, s.handleApplyConfig))

	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "route_not_found", "Rota não encontrada.")
	})
	if s.cfg.PanelDir != "" {
		m.Handle("/", spa(s.cfg.PanelDir))
	}
}

// Handler returns the root handler with every middleware.
func (s *Server) Handler() http.Handler {
	return s.recover(s.requestID(s.logRequests(s.cors(s.limitBody(s.mux)))))
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.st.Ping(ctx); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "database_unavailable", "Banco de dados indisponível.")
		return
	}
	if err := s.cache.Ping(ctx); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "cache_unavailable", "Cache de status indisponível.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// kick asks the controller to reconcile now.
func (s *Server) kick() { _ = s.bus.Publish(protocol.ReconcileSubject, nil) }

// panelCSP restricts what the panel may load. Media comes from the Media
// Nodes (another origin), hence the http(s) allowance for media and fetches;
// scripts stay same-origin only, hls.js runs its worker from a blob.
const panelCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob: http: https:; media-src 'self' blob: http: https:; " +
	"connect-src 'self' http: https:; worker-src 'self' blob:; font-src 'self' data:; " +
	"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

// spa serves the panel and falls back to index.html for client routes.
func spa(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", panelCSP)
		// Clean("/"+path) cannot climb above "/", so p always stays inside dir.
		p := filepath.Join(dir, filepath.FromSlash(filepath.Clean("/"+r.URL.Path)))
		if st, err := os.Stat(p); err != nil || st.IsDir() { //nolint:gosec // G703: p is confined to dir (see above)
			if !strings.Contains(filepath.Base(r.URL.Path), ".") {
				w.Header().Set("Cache-Control", "no-cache")
				http.ServeFile(w, r, filepath.Join(dir, "index.html"))
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fs.ServeHTTP(w, r)
	})
}
