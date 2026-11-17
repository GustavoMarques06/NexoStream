package agent

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"nexostream/internal/model"
	"nexostream/media-engine/drm"
	"nexostream/media-engine/packaging"
)

// protectedDir holds the clear HLS of protected channels, inside the HLS
// folder but outside what nginx publishes (channel ids never start with a
// dot). The worker serves those channels itself, encrypted (ADR 0008).
const protectedDir = ".drm"

// variantRe matches the folder of one ABR quality.
var variantRe = regexp.MustCompile(`^v[0-9]{1,2}$`)

// hlsDir is where the packager writes the live HLS of a channel.
func (a *Agent) hlsDir(ch model.Channel) string {
	if ch.DRM.Enabled {
		return filepath.Join(a.cfg.HLSDir, protectedDir, ch.ID)
	}
	return filepath.Join(a.cfg.HLSDir, ch.ID)
}

// otherHLSDir is the folder the channel would use with the opposite
// protection setting.
func (a *Agent) otherHLSDir(ch model.Channel) string {
	ch.DRM.Enabled = !ch.DRM.Enabled
	return a.hlsDir(ch)
}

// protector returns the DRM protector of a channel running here.
func (a *Agent) protector(id string) (drm.Protector, bool) {
	a.mu.Lock()
	spec, ok := a.specs[id]
	a.mu.Unlock()
	if !ok || !spec.Channel.DRM.Enabled {
		return drm.Protector{}, false
	}
	p := drm.Protector{ChannelID: id, TokenKey: a.cfg.PlaybackTokenKey}
	if len(a.cfg.DRMKey) >= drm.MinSecretBytes {
		p.Keys = drm.LocalKeys{Secret: a.cfg.DRMKey}
	}
	if m := spec.Channel.DRM.KeyRotationMinutes; m > 0 {
		p.Rotation = time.Duration(m) * time.Minute
	}
	return p, true
}

// serveHLS answers /hls/<channel>/...:
//
//   - protected channels: index.m3u8 (token required), enc/seg_N.ts
//     (encrypted segments) and keys/<period>.key (token required);
//     nginx proxies these to the worker;
//   - other channels: the files themselves.
//
// Every request goes through the play session and access rules (ADR 0009);
// nginx proxies /<channel>/... here.
func (a *Agent) serveHLS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	rest := strings.TrimPrefix(r.URL.Path, "/hls/")
	id, file, ok := strings.Cut(rest, "/")
	if !ok || id == "" || strings.HasPrefix(id, ".") || !filepath.IsLocal(id) || !filepath.IsLocal(filepath.FromSlash(file)) {
		http.NotFound(w, r)
		return
	}
	if file == "thumb.jpg" {
		a.serveThumb(w, r, id) // a picture for the panel, not a viewer
		return
	}
	a.play(w, r, id, "hls", false, func(w http.ResponseWriter, r *http.Request) {
		if p, protected := a.protector(id); protected {
			a.serveProtected(w, r, id, file, p)
			return
		}
		path := filepath.Join(a.cfg.HLSDir, id, filepath.FromSlash(file))
		if strings.HasSuffix(file, ".mpd") {
			b, err := os.ReadFile(path) //nolint:gosec // G304: id and file checked with filepath.IsLocal
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/dash+xml")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			_, _ = w.Write(packaging.AddTokenMPD(b, r.URL.Query().Get("token"))) //nolint:gosec // G705: a DASH manifest; the token is URL-escaped
			return
		}
		if strings.HasSuffix(file, ".m4s") {
			w.Header().Set("Content-Type", "video/iso.segment")
		}
		if strings.HasSuffix(file, ".m3u8") {
			b, err := os.ReadFile(path) //nolint:gosec // G304: id and file checked with filepath.IsLocal
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			_, _ = w.Write(packaging.AddToken(b, r.URL.Query().Get("token"))) //nolint:gosec // G705: an HLS playlist; the token is URL-escaped
			return
		}
		http.ServeFile(w, r, path) //nolint:gosec // G703: id and file checked with filepath.IsLocal
	})
}

func (a *Agent) serveProtected(w http.ResponseWriter, r *http.Request, id, file string, p drm.Protector) {
	if err := p.Ready(); err != nil {
		drm.WriteError(w, err)
		return
	}
	now := time.Now()
	dir := filepath.Join(a.cfg.HLSDir, protectedDir, id)
	// ABR: v<N>/... is one quality; index.m3u8 is then the master playlist.
	if v, rest, ok := strings.Cut(file, "/"); ok && variantRe.MatchString(v) {
		dir, file = filepath.Join(dir, v), rest
	} else if file == packaging.PlaylistName && packaging.IsMaster(filepath.Join(dir, file)) {
		if err := p.Authorize(r, now); err != nil {
			drm.WriteError(w, err)
			return
		}
		b, err := os.ReadFile(filepath.Join(dir, file)) //nolint:gosec // folder built by the worker
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(packaging.AddToken(b, r.URL.Query().Get("token"))) //nolint:gosec // G705: an HLS playlist; the token was verified and is URL-escaped
		return
	}
	switch {
	case file == packaging.PlaylistName:
		if err := p.Authorize(r, now); err != nil {
			drm.WriteError(w, err)
			return
		}
		b, err := a.live.Playlist(dir, p, r.URL.Query().Get("token"), now)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		b = packaging.AddToken(b, r.URL.Query().Get("token"))
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(b) //nolint:gosec // G705: an HLS playlist; the echoed token was verified and is URL-escaped
	case strings.HasPrefix(file, "enc/"):
		b, err := a.live.Segment(dir, strings.TrimPrefix(file, "enc/"), p, now)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.NotFound(w, r)
			} else {
				drm.WriteError(w, err)
			}
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(b) //nolint:gosec // G705: encrypted MPEG-TS bytes, not markup
	case strings.HasPrefix(file, "keys/"):
		p.ServeKey(w, r, strings.TrimPrefix(file, "keys/"), now)
	default:
		// Never the clear segments.
		http.NotFound(w, r)
	}
}
