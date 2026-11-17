package api

import (
	"net/http"
	"net/url"
	"time"

	"nexostream/media-engine/drm"
)

// Playback token lifetimes.
const (
	defaultPlaybackTTL = 6 * time.Hour
	minPlaybackTTL     = time.Minute
	maxPlaybackTTL     = 7 * 24 * time.Hour
)

// withToken appends the playback token to an output URL.
func withToken(u, token string) string {
	if u == "" {
		return ""
	}
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	q := p.Query()
	q.Set("token", token)
	p.RawQuery = q.Encode()
	return p.String()
}

// handlePlaybackToken signs a playback token for a protected channel (ADR
// 0008). The panel uses it for previews; in production the operator's
// middleware usually signs its own tokens with the same secret.
func (s *Server) handlePlaybackToken(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.getChannel(w, r)
	if !ok {
		return
	}
	var in struct {
		TTLSeconds int `json:"ttl_seconds"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_body", err.Error())
			return
		}
	}
	if !ch.DRM.Enabled && !ch.Auth.Enabled {
		writeError(w, r, http.StatusConflict, "channel_not_protected", "O canal não tem DRM nem autorização: as saídas não precisam de token.")
		return
	}
	if len(s.cfg.PlaybackTokenKey) < drm.MinSecretBytes {
		writeError(w, r, http.StatusServiceUnavailable, "drm_not_configured",
			"A chave de tokens de reprodução não está configurada na API (NEXOSTREAM_PLAYBACK_TOKEN_KEY_FILE).")
		return
	}
	ttl := defaultPlaybackTTL
	if in.TTLSeconds != 0 {
		ttl = time.Duration(in.TTLSeconds) * time.Second
		if ttl < minPlaybackTTL || ttl > maxPlaybackTTL {
			writeErrorDetails(w, r, http.StatusUnprocessableEntity, "validation_failed", "Duração do token fora do limite.",
				map[string]string{"ttl_seconds": "entre 60 e 604800 segundos"})
			return
		}
	}
	exp := time.Now().Add(ttl).UTC().Truncate(time.Second)
	token := drm.SignToken(s.cfg.PlaybackTokenKey, ch.ID, exp)
	out := map[string]any{"channel_id": ch.ID, "token": token, "expires_at": exp}
	st, found, _ := s.cache.Get(r.Context(), ch.ID)
	if found {
		if u := withToken(st.Outputs["hls"], token); u != "" {
			out["hls_url"] = u
		}
		if u := st.Outputs["dvr"]; u != "" {
			out["dvr_url"] = withToken(u+"index.m3u8", token)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
