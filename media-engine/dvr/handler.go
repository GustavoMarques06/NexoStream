package dvr

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"nexostream/media-engine/drm"
	"nexostream/media-engine/packaging"
)

// Handler serves recordings of the channels on this node, mounted at /dvr/:
//
//	/dvr/<ch>/index.m3u8?from=<t>&to=<t>   catch-up (VOD); without "to": timeshift (EVENT)
//	/dvr/<ch>/index-<from>-<seconds>.m3u8   Flussonic-style catch-up
//	/dvr/<ch>/index-<from>-now.m3u8         Flussonic-style timeshift
//	/dvr/<ch>/archive-<from>-<seconds>.mp4  MP4 export of a recorded interval (copy, no re-encoding)
//	/dvr/<ch>/recordings.json?from=&to=     recorded ranges
//	/dvr/<ch>/[d<n>/]YYYY/MM/DD/HH.ts       hour files (byte ranges; the whole hour plays in VLC)
//
// Times are Unix seconds or RFC 3339. Storage returns where a channel
// records (one or more disks) and whether the channel is on this node.
//
// For channels with DRM (Protect returns a protector), playlists require the
// playback token, pieces are served encrypted at enc/<hour>/<offset>.ts, keys
// at keys/<period>.key, and the clear hour files are not served.
type Handler struct {
	Storage func(channelID string) (Storage, bool)
	Protect func(channelID string) (drm.Protector, bool)
	Now     func() time.Time
	// FFmpeg enables MP4 export (empty = export answers 501). MaxExport is
	// the longest exportable interval and MaxExportJobs how many exports
	// may run at once on the node.
	FFmpeg        string
	MaxExport     time.Duration
	MaxExportJobs int
}

var (
	segRe        = regexp.MustCompile(`^(d[0-9]+/)?[0-9]{4}/[0-9]{2}/[0-9]{2}/[0-9]{2}\.ts$`)
	variantPath  = regexp.MustCompile(`^v([0-9]{1,2})/(.+)$`)
	flussonicRe  = regexp.MustCompile(`^index-([0-9]+)-([0-9]+|now)\.m3u8$`)
	archiveRe    = regexp.MustCompile(`^archive-([0-9]+)-([0-9]+)\.mp4$`)
	channelIDRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	defaultShift = time.Hour
)

func parseTime(v string, def time.Time) (time.Time, bool) {
	if v == "" {
		return def, true
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), true
	}
	t, err := time.Parse(time.RFC3339, v)
	return t.UTC(), err == nil
}

func (h Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	rest := strings.TrimPrefix(r.URL.Path, "/dvr/")
	ch, file, ok := strings.Cut(rest, "/")
	if !ok || !channelIDRe.MatchString(ch) {
		http.NotFound(w, r)
		return
	}
	st, ok := h.Storage(ch)
	if !ok {
		http.Error(w, "channel has no DVR on this node", http.StatusNotFound)
		return
	}
	now := h.now().UTC()
	q := r.URL.Query()
	// "v<N>/..." addresses quality N of an ABR recording (quality 0 is the
	// channel folder itself). DRM and every other rule below apply unchanged.
	key, quality := ch, -1
	if m := variantPath.FindStringSubmatch(file); m != nil {
		n, _ := strconv.Atoi(m[1])
		key, quality, file = VariantKey(ch, n), n, m[2]
	}
	if prot, ok := h.protector(ch); ok {
		if err := prot.Ready(); err != nil {
			drm.WriteError(w, err)
			return
		}
		switch {
		case strings.HasPrefix(file, "keys/"):
			prot.ServeKey(w, r, strings.TrimPrefix(file, "keys/"), now)
			return
		case strings.HasPrefix(file, "enc/"):
			h.piece(w, r, st, key, strings.TrimPrefix(file, "enc/"), prot)
			return
		case segRe.MatchString(file):
			http.Error(w, "protected channel: recordings are served encrypted only", http.StatusForbidden)
			return
		case archiveRe.MatchString(file):
			http.Error(w, "protected channel: MP4 export is not available", http.StatusForbidden)
			return
		case file == "index.m3u8" || flussonicRe.MatchString(file):
			if err := prot.Authorize(r, now); err != nil {
				drm.WriteError(w, err)
				return
			}
		}
	}

	switch {
	case segRe.MatchString(file):
		p, found := st.Locate(key, file)
		if !found {
			http.NotFound(w, r)
			return
		}
		// http.ServeFile honors Range requests (#EXT-X-BYTERANGE). The hour
		// being recorded still grows, so it must not be cached for long.
		w.Header().Set("Content-Type", "video/mp2t")
		if strings.HasSuffix(file, HourRel(now)) {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "max-age=86400")
		}
		http.ServeFile(w, r, p)

	case file == "index.m3u8":
		from, ok1 := parseTime(q.Get("from"), now.Add(-defaultShift))
		to, ok2 := parseTime(q.Get("to"), time.Time{})
		if !ok1 || !ok2 {
			http.Error(w, "bad from/to", http.StatusBadRequest)
			return
		}
		h.playlist(w, r, st, ch, key, quality, file, from, to, now)

	case flussonicRe.MatchString(file):
		m := flussonicRe.FindStringSubmatch(file)
		from, _ := parseTime(m[1], now)
		to := time.Time{}
		if m[2] != "now" {
			secs, _ := strconv.Atoi(m[2])
			to = from.Add(time.Duration(secs) * time.Second)
		}
		h.playlist(w, r, st, ch, key, quality, file, from, to, now)

	case archiveRe.MatchString(file):
		m := archiveRe.FindStringSubmatch(file)
		from, _ := parseTime(m[1], now)
		secs, _ := strconv.Atoi(m[2])
		h.exportMP4(w, r, st, ch, key, from, from.Add(time.Duration(secs)*time.Second))

	case file == "recordings.json":
		from, ok1 := parseTime(q.Get("from"), now.Add(-24*time.Hour))
		to, ok2 := parseTime(q.Get("to"), now.Add(time.Minute))
		if !ok1 || !ok2 {
			http.Error(w, "bad from/to", http.StatusBadRequest)
			return
		}
		segs, _ := st.List(ch, from, to)
		var bytes int64
		for _, s := range segs {
			bytes += s.Size
		}
		ranges := Ranges(segs)
		if ranges == nil {
			ranges = []Range{}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"channel_id": ch, "from": from, "to": to, "ranges": ranges, "segments": len(segs), "bytes": bytes,
			"qualities": max(len(st.ReadVariants(ch)), 1),
		})

	default:
		http.NotFound(w, r)
	}
}

// playlist writes a VOD playlist for [from, to), or an EVENT (timeshift)
// playlist from "from" to now when to is zero.
func (h Handler) protector(ch string) (drm.Protector, bool) {
	if h.Protect == nil {
		return drm.Protector{}, false
	}
	return h.Protect(ch)
}

// playlist serves the playlist of quality key (quality is -1 when the URL
// had no v<N>/ prefix). For an ABR recording asked without a prefix it
// answers a master playlist with the qualities recorded in the interval.
func (h Handler) playlist(w http.ResponseWriter, r *http.Request, st Storage, ch, key string, quality int, file string, from, to, now time.Time) {
	live := to.IsZero()
	end := to
	if live {
		end = now.Add(time.Minute)
	}
	if !from.Before(end) {
		http.Error(w, "empty interval", http.StatusBadRequest)
		return
	}
	if quality < 0 {
		if vs := st.ReadVariants(ch); len(vs) > 1 {
			present := st.recordedVariants(ch, len(vs), from, end)
			switch {
			case len(present) > 1:
				h.master(w, r, vs, present, file, live)
				return
			case len(present) == 1 && present[0] > 0:
				// Only a lower quality was recorded: send the player to it. The
				// target is relative to this playlist (a path only, never a host),
				// and file already matched index.m3u8 or index-<from>-<len>.m3u8.
				target := (&url.URL{Path: "v" + strconv.Itoa(present[0]) + "/" + file, RawQuery: r.URL.RawQuery}).String()
				http.Redirect(w, r, target, http.StatusFound) //nolint:gosec // G710: same-origin relative path from a validated file name
				return
			}
		}
	}
	segs, _ := st.List(key, from, end)
	if len(segs) == 0 {
		http.Error(w, "no recording in this interval", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	if live {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "max-age=10")
	}
	token := r.URL.Query().Get("token")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if p, ok := h.protector(ch); ok {
		//nolint:gosec // G705: an HLS playlist; the echoed token was verified and is URL-escaped
		_, _ = w.Write(packaging.AddToken([]byte(ProtectedPlaylist(segs, live, st.URI, p, token)), token))
		return
	}
	//nolint:gosec // G705: an HLS playlist; the token is URL-escaped
	_, _ = w.Write(packaging.AddToken([]byte(Playlist(segs, live, st.URI)), token))
}

// master writes the master playlist of an ABR recording: one entry per
// recorded quality, each pointing to v<N>/<same playlist>?<same query>.
func (h Handler) master(w http.ResponseWriter, r *http.Request, vs []packaging.Variant, present []int, file string, live bool) {
	query := ""
	if r.URL.RawQuery != "" {
		query = "?" + r.URL.RawQuery
	}
	text := packaging.MasterText(vs, present, func(i int) string { return "v" + strconv.Itoa(i) + "/" + file + query })
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if live {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "max-age=10")
	}
	//nolint:gosec // G705: an HLS playlist; the token is URL-escaped
	_, _ = w.Write(packaging.AddToken([]byte(text), r.URL.Query().Get("token")))
}

// piece serves enc/<hour>/<offset>.ts?s=<sequence>: one recorded piece,
// encrypted with the key of the period it starts in and the IV s.
func (h Handler) piece(w http.ResponseWriter, r *http.Request, st Storage, ch, rest string, p drm.Protector) {
	hour, last := path.Split(rest)
	hourURI := strings.TrimSuffix(hour, "/") + ".ts"
	off, err1 := strconv.ParseInt(strings.TrimSuffix(last, ".ts"), 10, 64)
	seq, err2 := strconv.ParseInt(r.URL.Query().Get("s"), 10, 64)
	if !strings.HasSuffix(last, ".ts") || !segRe.MatchString(hourURI) || err1 != nil || err2 != nil || off < 0 || seq < 0 {
		http.NotFound(w, r)
		return
	}
	file, found := st.Locate(ch, hourURI)
	if !found {
		http.NotFound(w, r)
		return
	}
	start, size, ok := PieceAt(file, off)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(file) //nolint:gosec // file comes from Storage.Locate, which stays inside the disk folder
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	data := make([]byte, size)
	if _, err := f.ReadAt(data, off); err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	enc, err := p.Encrypt(p.Period(start), seq, data)
	if err != nil {
		drm.WriteError(w, err)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(enc) //nolint:gosec // G705: encrypted MPEG-TS bytes, not markup
}
