package packaging

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// PlaylistEntry is one segment of a live playlist.
type PlaylistEntry struct {
	Seq      int64
	URI      string
	Duration time.Duration
	PDT      time.Time // #EXT-X-PROGRAM-DATE-TIME (zero when absent)
}

// PlaylistInfo summarizes a live HLS playlist.
type PlaylistInfo struct {
	MediaSequence int64
	Segments      int
	// LastSequence is the sequence number of the newest segment.
	LastSequence int64
	Entries      []PlaylistEntry
}

// pdtLayouts are the date formats ffmpeg and other packagers write.
var pdtLayouts = []string{"2006-01-02T15:04:05.000-0700", time.RFC3339Nano, "2006-01-02T15:04:05-0700"}

// ReadPlaylist parses a media playlist.
func ReadPlaylist(path string) (PlaylistInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return PlaylistInfo{}, err
	}
	defer f.Close()
	return ParsePlaylist(f)
}

// ParsePlaylist parses a media playlist from r.
func ParsePlaylist(r io.Reader) (PlaylistInfo, error) {
	var pi PlaylistInfo
	var cur PlaylistEntry
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			pi.MediaSequence, _ = strconv.ParseInt(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"), 10, 64)
		case strings.HasPrefix(line, "#EXTINF:"):
			v, _, _ := strings.Cut(strings.TrimPrefix(line, "#EXTINF:"), ",")
			if secs, err := strconv.ParseFloat(v, 64); err == nil {
				cur.Duration = time.Duration(secs * float64(time.Second))
			}
		case strings.HasPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:"):
			v := strings.TrimPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:")
			for _, l := range pdtLayouts {
				if t, err := time.Parse(l, v); err == nil {
					cur.PDT = t.UTC()
					break
				}
			}
		case line != "" && !strings.HasPrefix(line, "#"):
			cur.URI = line
			cur.Seq = pi.MediaSequence + int64(pi.Segments)
			pi.Entries = append(pi.Entries, cur)
			pi.Segments++
			cur = PlaylistEntry{}
		}
	}
	pi.LastSequence = pi.MediaSequence + int64(pi.Segments) - 1
	return pi, sc.Err()
}
