// Package dvr records, indexes and plays back channels on the Media Node
// itself, the way Flussonic does: each server records into a configured
// folder (per server, optionally per channel) and serves timeshift and
// catch-up playlists.
//
// Recordings are stored in ONE FILE PER HOUR:
//
//	<root>/<channel>/<YYYY>/<MM>/<DD>/<HH>.ts    continuous MPEG-TS (plays in VLC)
//	<root>/<channel>/<YYYY>/<MM>/<DD>/<HH>.idx   one line per piece: start_ms duration_ms offset length
//
// Each live HLS segment is appended to the file of its hour and indexed.
// Playlists address the pieces with #EXT-X-BYTERANGE, so players can seek to
// any moment without splitting the hour file. After a crash, the hour file
// is truncated back to the last indexed piece.
package dvr

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"nexostream/media-engine/drm"
)

// Segment is one recorded piece inside an hour file.
type Segment struct {
	ChannelID string
	Start     time.Time
	Duration  time.Duration
	Rel       string // hour file, relative to the channel folder ("2026/11/16/14.ts")
	Offset    int64  // byte offset of the piece in the hour file
	Size      int64  // byte length of the piece
	Disk      int    // index of the disk holding it (multi-disk storages)
}

// End returns when the segment ends.
func (s Segment) End() time.Time { return s.Start.Add(s.Duration) }

// Range is a continuous recorded interval.
type Range struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// HourRel returns "YYYY/MM/DD/HH.ts" for the hour containing t (UTC).
func HourRel(t time.Time) string {
	t = t.UTC()
	return fmt.Sprintf("%04d/%02d/%02d/%02d.ts", t.Year(), int(t.Month()), t.Day(), t.Hour())
}

func hourPaths(root, channelID string, t time.Time) (ts, idx string) {
	ts = filepath.Join(root, channelID, filepath.FromSlash(HourRel(t)))
	return ts, strings.TrimSuffix(ts, ".ts") + ".idx"
}

type idxLine struct {
	start, dur, off, size int64 // ms, ms, bytes, bytes
}

func parseIdx(r io.Reader) []idxLine {
	var out []idxLine
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 4 {
			continue
		}
		var v [4]int64
		ok := true
		for i := range f {
			n, err := strconv.ParseInt(f[i], 10, 64)
			if err != nil || n < 0 {
				ok = false
				break
			}
			v[i] = n
		}
		if ok && v[1] > 0 && v[3] > 0 {
			out = append(out, idxLine{v[0], v[1], v[2], v[3]})
		}
	}
	return out
}

func readIdx(path string) []idxLine {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseIdx(f)
}

// hourState caches, per open hour file, the last indexed piece, so each new
// piece costs one append and no index read.
type hourState struct {
	lastStart int64 // ms of the newest indexed piece
	size      int64 // bytes covered by the index
}

// openHour returns the state of an hour file, repairing it after a crash:
// bytes written after the last index line are cut off.
func (s *State) openHour(ts, idx string) (*hourState, error) {
	if st, ok := s.hours[ts]; ok {
		return st, nil
	}
	lines := readIdx(idx)
	st := &hourState{}
	valid := lines[:0]
	var fileSize int64
	if fi, err := os.Stat(ts); err == nil {
		fileSize = fi.Size()
	}
	for _, l := range lines {
		if l.off != st.size || l.off+l.size > fileSize {
			break // gap or beyond the file: everything after is unreliable
		}
		valid = append(valid, l)
		st.size = l.off + l.size
		st.lastStart = l.start
	}
	if len(valid) != len(lines) {
		var b strings.Builder
		for _, l := range valid {
			fmt.Fprintf(&b, "%d %d %d %d\n", l.start, l.dur, l.off, l.size)
		}
		if err := os.WriteFile(idx, []byte(b.String()), 0o644); err != nil { //nolint:gosec // served by the origin
			return nil, err
		}
	}
	if fileSize > st.size {
		if err := os.Truncate(ts, st.size); err != nil {
			return nil, err
		}
	}
	s.hours[ts] = st
	// Forget hours that ended long ago (bounded memory).
	if len(s.hours) > 4096 {
		for k := range s.hours {
			delete(s.hours, k)
			if len(s.hours) <= 2048 {
				break
			}
		}
		s.hours[ts] = st
	}
	return st, nil
}

// Record appends the live HLS segment src to the hour file of start and
// indexes it, using the default State. Recording the same start twice is a
// no-op.
func Record(root, channelID, src string, start time.Time, dur time.Duration) (Segment, error) {
	return defaultState.Record(root, channelID, src, start, dur)
}

// Record is the package function Record with this State.
func (s *State) Record(root, channelID, src string, start time.Time, dur time.Duration) (Segment, error) {
	ts, idx := hourPaths(root, channelID, start)
	seg := Segment{ChannelID: channelID, Start: start.UTC(), Duration: dur, Rel: HourRel(start)}
	s.hoursMu.Lock()
	defer s.hoursMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(ts), 0o755); err != nil { //nolint:gosec // served by the origin
		return seg, fmt.Errorf("dvr dir: %w", err)
	}
	st, err := s.openHour(ts, idx)
	if err != nil {
		return seg, fmt.Errorf("dvr open hour: %w", err)
	}
	if start.UnixMilli() <= st.lastStart {
		return seg, nil // already recorded
	}
	in, err := os.Open(src)
	if err != nil {
		return seg, fmt.Errorf("dvr read %s: %w", filepath.Base(src), err)
	}
	defer in.Close()
	out, err := os.OpenFile(ts, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644) //nolint:gosec // served by the origin
	if err != nil {
		return seg, fmt.Errorf("dvr open %s: %w", ts, err)
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil || n == 0 {
		_ = os.Truncate(ts, st.size) // drop the partial piece
		if err == nil {
			err = fmt.Errorf("empty segment %s", filepath.Base(src))
		}
		return seg, fmt.Errorf("dvr append: %w", err)
	}
	line := fmt.Sprintf("%d %d %d %d\n", start.UnixMilli(), dur.Milliseconds(), st.size, n)
	f, err := os.OpenFile(idx, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644) //nolint:gosec // served by the origin
	if err == nil {
		_, err = f.WriteString(line)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		_ = os.Truncate(ts, st.size)
		return seg, fmt.Errorf("dvr index: %w", err)
	}
	seg.Offset, seg.Size = st.size, n
	st.size += n
	st.lastStart = start.UnixMilli()
	return seg, nil
}

// List returns the pieces overlapping [from, to), oldest first.
func List(root, channelID string, from, to time.Time) ([]Segment, error) {
	var out []Segment
	for h := from.UTC().Truncate(time.Hour); h.Before(to); h = h.Add(time.Hour) {
		_, idx := hourPaths(root, channelID, h)
		rel := HourRel(h)
		for _, l := range readIdx(idx) {
			s := Segment{ChannelID: channelID, Start: time.UnixMilli(l.start).UTC(), Duration: time.Duration(l.dur) * time.Millisecond,
				Rel: rel, Offset: l.off, Size: l.size}
			if s.End().After(from) && s.Start.Before(to) {
				out = append(out, s)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// MaxGap is the largest hole between pieces still treated as continuous.
const MaxGap = 2 * time.Second

// Ranges merges consecutive pieces into continuous intervals.
func Ranges(segs []Segment) []Range {
	var out []Range
	for _, s := range segs {
		if n := len(out); n > 0 && s.Start.Sub(out[n-1].To) <= MaxGap {
			if s.End().After(out[n-1].To) {
				out[n-1].To = s.End()
			}
			continue
		}
		out = append(out, Range{From: s.Start, To: s.End()})
	}
	return out
}

// Playlist builds an HLS playlist for segs, addressing each piece of the
// hour files with #EXT-X-BYTERANGE. live=true produces an EVENT playlist
// without ENDLIST (timeshift); otherwise VOD (catch-up). uri maps a segment
// to its URI relative to the playlist.
func Playlist(segs []Segment, live bool, uri func(Segment) string) string {
	return playlist(segs, live, uri, nil, "")
}

// ProtectedPlaylist is Playlist for a channel with DRM: each piece has its
// own URI (enc/<hour>/<offset>.ts?s=<sequence>), encrypted when requested,
// and each key period gets an #EXT-X-KEY line carrying the viewer's token.
func ProtectedPlaylist(segs []Segment, live bool, uri func(Segment) string, p drm.Protector, token string) string {
	return playlist(segs, live, uri, &p, token)
}

func playlist(segs []Segment, live bool, uri func(Segment) string, p *drm.Protector, token string) string {
	var b strings.Builder
	target := 1
	for _, s := range segs {
		if d := int(s.Duration.Seconds() + 0.999); d > target {
			target = d
		}
	}
	typ := "VOD"
	if live {
		typ = "EVENT"
	}
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:%s\n", target, typ)
	if !live {
		b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	}
	var prevEnd time.Time
	key := int64(-1)
	for i, s := range segs {
		if p != nil {
			if period := p.Period(s.Start); period != key {
				b.WriteString(drm.KeyTag(period, token) + "\n")
				key = period
			}
		}
		if i > 0 && s.Start.Sub(prevEnd) > MaxGap {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n#EXTINF:%.3f,\n", s.Start.UTC().Format("2006-01-02T15:04:05.000Z"), s.Duration.Seconds())
		if p != nil {
			// The IV is the media sequence number, i.e. i (MEDIA-SEQUENCE:0).
			fmt.Fprintf(&b, "enc/%s/%d.ts?s=%d\n", strings.TrimSuffix(uri(s), ".ts"), s.Offset, i)
		} else {
			fmt.Fprintf(&b, "#EXT-X-BYTERANGE:%d@%d\n%s\n", s.Size, s.Offset, uri(s))
		}
		prevEnd = s.End()
	}
	if !live {
		b.WriteString("#EXT-X-ENDLIST\n")
	}
	return b.String()
}

// PieceAt finds the piece starting at byte offset in an hour file.
func PieceAt(hourFile string, offset int64) (start time.Time, size int64, ok bool) {
	for _, l := range readIdx(strings.TrimSuffix(hourFile, ".ts") + ".idx") {
		if l.off == offset {
			return time.UnixMilli(l.start).UTC(), l.size, true
		}
	}
	return time.Time{}, 0, false
}

// HourFile is a recorded hour of a channel.
type HourFile struct {
	Path  string // the .ts file
	Start time.Time
}

// ListHours returns the hour files of a channel, oldest first.
func ListHours(root, channelID string) ([]HourFile, error) {
	base := filepath.Join(root, channelID)
	matches, err := filepath.Glob(filepath.Join(base, "[0-9][0-9][0-9][0-9]", "[0-9][0-9]", "[0-9][0-9]", "[0-9][0-9].ts"))
	if err != nil {
		return nil, err
	}
	var out []HourFile
	for _, m := range matches {
		rel, _ := filepath.Rel(base, m)
		t, err := time.Parse("2006/01/02/15.ts", filepath.ToSlash(rel))
		if err != nil {
			continue
		}
		out = append(out, HourFile{Path: m, Start: t.UTC()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// Expired returns the hours entirely outside the retention window.
func Expired(hs []HourFile, now time.Time, retention time.Duration) []HourFile {
	limit := now.Add(-retention)
	var out []HourFile
	for _, h := range hs {
		if !h.Start.Add(time.Hour).After(limit) {
			out = append(out, h)
		}
	}
	return out
}

// Cleanup removes expired hour files (and their index) of a channel, using
// the default State.
func Cleanup(root, channelID string, now time.Time, retention time.Duration) ([]HourFile, error) {
	return defaultState.Cleanup(root, channelID, now, retention)
}

// Cleanup is the package function Cleanup with this State.
func (s *State) Cleanup(root, channelID string, now time.Time, retention time.Duration) ([]HourFile, error) {
	hs, err := ListHours(root, channelID)
	if err != nil {
		return nil, err
	}
	exp := Expired(hs, now, retention)
	s.hoursMu.Lock()
	defer s.hoursMu.Unlock()
	for _, h := range exp {
		if err := os.Remove(h.Path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove %s: %w", h.Path, err)
		}
		_ = os.Remove(strings.TrimSuffix(h.Path, ".ts") + ".idx")
		delete(s.hours, h.Path)
		// Remove day/month/year folders left empty.
		for d := filepath.Dir(h.Path); d != filepath.Join(root, channelID); d = filepath.Dir(d) {
			if os.Remove(d) != nil {
				break
			}
		}
	}
	return exp, nil
}

// StorageEstimate applies the formula of spec 10.5:
// GB = channels x Mbps x 10.8 x days (decimal, no redundancy or margin).
func StorageEstimate(channels int, mbps float64, days float64) float64 {
	return float64(channels) * mbps * 10.8 * days
}
