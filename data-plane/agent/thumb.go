package agent

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"nexostream/internal/procutil"
	"nexostream/media-engine/drm"
	"nexostream/media-engine/packaging"
)

// Thumbnails are made on request from the newest live segment, at most once
// per channel every thumbMaxAge, so nothing runs for channels nobody looks at.
const (
	thumbMaxAge  = 10 * time.Second
	thumbTimeout = 10 * time.Second
	thumbWidth   = 320
)

type thumbEntry struct {
	mu  sync.Mutex // one ffmpeg per channel at a time
	at  time.Time
	img []byte
}

type thumbs struct {
	mu sync.Mutex
	m  map[string]*thumbEntry
}

func (t *thumbs) entry(id string) *thumbEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]*thumbEntry{}
	}
	e := t.m[id]
	if e == nil {
		e = &thumbEntry{}
		t.m[id] = e
	}
	return e
}

func (t *thumbs) forget(id string) {
	t.mu.Lock()
	delete(t.m, id)
	t.mu.Unlock()
}

// segmentDir is where the newest playable segments of a channel are: the
// channel folder, or the top quality of an ABR channel.
func (a *Agent) segmentDir(id string) (string, bool) {
	a.mu.Lock()
	spec, ok := a.specs[id]
	a.mu.Unlock()
	if !ok {
		return "", false
	}
	dir := a.hlsDir(spec.Channel)
	if v0 := filepath.Join(dir, packaging.VariantDir(0)); fileExists(filepath.Join(v0, packaging.PlaylistName)) {
		dir = v0
	}
	return dir, true
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// serveThumb answers /hls/<channel>/thumb.jpg. Protected channels (DRM or
// access rules) need a playback token signed by the cluster.
func (a *Agent) serveThumb(w http.ResponseWriter, r *http.Request, id string) {
	dir, ok := a.segmentDir(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	a.mu.Lock()
	ch := a.specs[id].Channel
	a.mu.Unlock()
	if ch.DRM.Enabled || ch.Auth.Enabled {
		if err := drm.VerifyToken(a.cfg.PlaybackTokenKey, id, r.URL.Query().Get("token"), time.Now()); err != nil {
			drm.WriteError(w, err)
			return
		}
	}
	e := a.thumbs.entry(id)
	e.mu.Lock()
	defer e.mu.Unlock()
	if time.Since(e.at) > thumbMaxAge || e.img == nil {
		img, err := a.makeThumb(r.Context(), dir)
		if err != nil {
			if e.img == nil {
				http.Error(w, "no picture yet", http.StatusServiceUnavailable)
				return
			}
		} else {
			e.img, e.at = img, time.Now()
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "max-age=10")
	_, _ = w.Write(e.img)
}

// makeThumb decodes one frame of the newest complete segment.
func (a *Agent) makeThumb(ctx context.Context, dir string) ([]byte, error) {
	pi, err := packaging.ReadPlaylist(filepath.Join(dir, packaging.PlaylistName))
	if err != nil || len(pi.Entries) == 0 {
		return nil, os.ErrNotExist
	}
	seg := filepath.Join(dir, filepath.FromSlash(pi.Entries[len(pi.Entries)-1].URI))
	ff := a.cfg.FFmpegPath
	if ff == "" {
		ff = "ffmpeg"
	}
	ctx, cancel := context.WithTimeout(ctx, thumbTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ff, "-hide_banner", "-loglevel", "error", "-i", seg, //nolint:gosec // G204: ffmpeg path from the node configuration, segment path built by the worker
		"-frames:v", "1", "-vf", "scale=320:-2", "-c:v", "mjpeg", "-q:v", "6", "-f", "image2", "pipe:1")
	procutil.SetAttr(cmd)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if out.Len() == 0 {
		return nil, os.ErrNotExist
	}
	return out.Bytes(), nil
}
