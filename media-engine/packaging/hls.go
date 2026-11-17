// Package packaging builds the outputs of a channel: HLS written to disk
// (served by nginx), MPEG-TS over UDP, and MPEG-TS over HTTP through a local
// relay.
package packaging

import (
	"path/filepath"
	"strconv"
	"strings"
)

// HLSOptions configures the live HLS output.
type HLSOptions struct {
	Dir            string // channel directory, e.g. /var/lib/nexostream/hls/canal-001
	SegmentSeconds int    // multiple of the GOP (6 s with GOP 2 s)
	ListSize       int    // segments in the live playlist
}

// DefaultHLS returns the starting values of spec section 6.2.
func DefaultHLS(dir string) HLSOptions {
	return HLSOptions{Dir: dir, SegmentSeconds: 6, ListSize: 10}
}

// PlaylistName is the live playlist file name.
const PlaylistName = "index.m3u8"

// TeeSlave returns the tee muxer entry for HLS. Playlists are written
// atomically (temp_file) and continue across pipeline restarts
// (append_list). append_list already writes one #EXT-X-DISCONTINUITY per
// pipeline start; discont_start would add a second one (verified with
// ffmpeg 9.0), so it is not used.
func (o HLSOptions) TeeSlave() string {
	seg := o.SegmentSeconds
	if seg <= 0 {
		seg = 6
	}
	size := o.ListSize
	if size <= 0 {
		size = 10
	}
	opts := []string{
		"f=hls",
		"hls_time=" + strconv.Itoa(seg),
		"hls_list_size=" + strconv.Itoa(size),
		"hls_flags=delete_segments+append_list+program_date_time+omit_endlist+temp_file",
		"hls_segment_type=mpegts",
		"hls_segment_filename=" + teeOptionEscape(filepath.ToSlash(filepath.Join(o.Dir, "seg_%06d.ts"))),
	}
	return "[" + strings.Join(opts, ":") + "]" + TeeEscape(filepath.ToSlash(filepath.Join(o.Dir, PlaylistName)))
}

// UDPTeeSlave returns the tee entry for an MPEG-TS/UDP destination. A failing
// UDP output must not stop HLS (onfail=ignore).
func UDPTeeSlave(url string) string {
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	return "[f=mpegts:onfail=ignore]" + TeeEscape(url+sep+"pkt_size=1316")
}

// teeOptionEscape escapes an option value inside "[...]". The tee muxer
// unescapes it twice (slave list, then option list), so it needs two levels;
// e.g. "C:/hls" must become `C\\\:/hls` (verified with ffmpeg 9.0).
func teeOptionEscape(s string) string { return TeeEscape(TeeEscape(s)) }

// TeeEscape escapes characters that are special in tee muxer specs (one
// level, enough for the output file name after "]").
func TeeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `:`, `\:`, `|`, `\|`, `[`, `\[`, `]`, `\]`, `'`, `\'`)
	return r.Replace(s)
}
