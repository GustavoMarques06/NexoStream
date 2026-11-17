package drm

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"nexostream/media-engine/packaging"
)

// segNameRe matches the live segment names written by the packager.
var segNameRe = regexp.MustCompile(`^seg_[0-9]+\.ts$`)

// liveSeg is what encrypting a live segment needs: its media sequence number
// (the IV) and its start (the key period). Both are fixed the first time the
// segment is seen, so the playlist and the segment always agree.
type liveSeg struct {
	seq   int64
	start time.Time
}

// Live serves the live HLS window of protected channels. Each channel folder
// holds the clear playlist and segments written by the packager.
type Live struct {
	mu   sync.Mutex
	dirs map[string]map[string]liveSeg // folder -> segment name -> info
}

// update parses a playlist and remembers its segments.
func (l *Live) update(dir string, raw []byte, now time.Time) map[string]liveSeg {
	pi, _ := packaging.ParsePlaylist(bytes.NewReader(raw))
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dirs == nil {
		l.dirs = map[string]map[string]liveSeg{}
	}
	m := l.dirs[dir]
	if m == nil {
		m = map[string]liveSeg{}
		l.dirs[dir] = m
	}
	var next time.Time
	for _, e := range pi.Entries {
		start := e.PDT
		if start.IsZero() {
			start = next
		}
		if start.IsZero() {
			start = now
		}
		next = start.Add(e.Duration)
		if old, ok := m[e.URI]; !ok || old.seq != e.Seq {
			m[e.URI] = liveSeg{seq: e.Seq, start: start}
		}
	}
	// Keep a margin of segments that already left the playlist: players may
	// still ask for them.
	if n := int64(len(pi.Entries)); n > 0 {
		limit := pi.MediaSequence - 2*n - 5
		for k, s := range m {
			if s.seq < limit {
				delete(m, k)
			}
		}
	}
	out := make(map[string]liveSeg, len(pi.Entries))
	for _, e := range pi.Entries {
		out[e.URI] = m[e.URI]
	}
	return out
}

func (l *Live) lookup(dir, name string) (liveSeg, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s, ok := l.dirs[dir][name]
	return s, ok
}

// Forget drops what is known about a folder (channel stopped).
func (l *Live) Forget(dir string) {
	l.mu.Lock()
	delete(l.dirs, dir)
	l.mu.Unlock()
}

// Playlist returns the live playlist of dir for one viewer: segments point to
// enc/<name> and each key period gets an #EXT-X-KEY line with the viewer's
// token.
func (l *Live) Playlist(dir string, p Protector, token string, now time.Time) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(dir, packaging.PlaylistName))
	if err != nil {
		return nil, err
	}
	return Rewrite(raw, l.update(dir, raw, now), p, token), nil
}

// headerTags are playlist-level tags, written where they are; every other
// tag belongs to the next segment and goes after its key line.
var headerTags = []string{
	"#EXTM3U", "#EXT-X-VERSION", "#EXT-X-TARGETDURATION", "#EXT-X-MEDIA-SEQUENCE",
	"#EXT-X-DISCONTINUITY-SEQUENCE", "#EXT-X-PLAYLIST-TYPE", "#EXT-X-INDEPENDENT-SEGMENTS",
	"#EXT-X-ALLOW-CACHE", "#EXT-X-ENDLIST",
}

func isHeader(line string) bool {
	for _, t := range headerTags {
		if line == t || strings.HasPrefix(line, t+":") {
			return true
		}
	}
	return false
}

// Rewrite turns a clear live playlist into a protected one.
func Rewrite(raw []byte, segs map[string]liveSeg, p Protector, token string) []byte {
	var b bytes.Buffer
	var pending []string
	cur := int64(-1)
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXT-X-KEY"):
			// never pass on a key line from the source
		case strings.HasPrefix(line, "#"):
			if isHeader(line) {
				b.WriteString(line + "\n")
			} else {
				pending = append(pending, line)
			}
		default:
			s, ok := segs[line]
			if !ok || !segNameRe.MatchString(line) {
				pending = pending[:0] // unknown entry: drop it rather than serve it in clear
				continue
			}
			if period := p.Period(s.start); period != cur {
				b.WriteString(KeyTag(period, token) + "\n")
				cur = period
			}
			for _, t := range pending {
				b.WriteString(t + "\n")
			}
			pending = pending[:0]
			b.WriteString("enc/" + line + "\n")
		}
	}
	for _, t := range pending {
		b.WriteString(t + "\n")
	}
	return b.Bytes()
}

// Segment returns a live segment of dir encrypted for the playlist.
func (l *Live) Segment(dir, name string, p Protector, now time.Time) ([]byte, error) {
	if !segNameRe.MatchString(name) {
		return nil, os.ErrNotExist
	}
	s, ok := l.lookup(dir, name)
	if !ok {
		raw, err := os.ReadFile(filepath.Join(dir, packaging.PlaylistName))
		if err != nil {
			return nil, err
		}
		l.update(dir, raw, now)
		if s, ok = l.lookup(dir, name); !ok {
			return nil, os.ErrNotExist
		}
	}
	clear, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	return p.Encrypt(p.Period(s.start), s.seq, clear)
}
