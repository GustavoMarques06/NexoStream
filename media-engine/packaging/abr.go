package packaging

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Variant is one quality of an ABR channel.
type Variant struct {
	Width, Height int
	BitrateBps    int // video + audio, peak
	AvgBitrateBps int
	Profile       string // h264 profile: high, main, baseline
}

// VariantDir is the folder of variant i inside the channel folder.
func VariantDir(i int) string { return "v" + strconv.Itoa(i) }

// ABRTeeSlaves returns one HLS tee entry per variant: variant i takes video
// stream i and every audio stream, and writes <dir>/v<i>/index.m3u8.
func (o HLSOptions) ABRTeeSlaves(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		v := o
		v.Dir = filepath.Join(o.Dir, VariantDir(i))
		s := v.TeeSlave()
		// "[f=hls:..." -> "[select=v\:i,a:f=hls:..." (the value is escaped like hls_segment_filename)
		out = append(out, "[select="+teeOptionEscape("v:"+strconv.Itoa(i)+",a")+":"+s[1:])
	}
	return out
}

// SelectFirstVideo restricts an MPEG-TS tee entry to the top quality, the
// audio and the subtitles.
func SelectFirstVideo(slave string) string {
	return "[select=" + teeOptionEscape("v:0,a,s") + ":" + strings.TrimPrefix(slave, "[")
}

// SelectAV restricts a tee entry to video and audio (outputs that cannot
// carry DVB subtitles or teletext).
func SelectAV(slave string) string {
	return "[select=" + teeOptionEscape("v,a") + ":" + strings.TrimPrefix(slave, "[")
}

func codecs(profile string, height int) string {
	p := "6400" // high
	switch profile {
	case "main":
		p = "4D40"
	case "baseline":
		p = "42E0"
	}
	level := "28" // 4.0
	switch {
	case height <= 480:
		level = "1E" // 3.0
	case height <= 720:
		level = "1F" // 3.1
	}
	return "avc1." + p + level + ",mp4a.40.2"
}

// MasterText builds a master playlist for the given qualities. index lists
// which qualities to include (their positions in vs) and uri gives the
// playlist address of each one.
func MasterText(vs []Variant, index []int, uri func(i int) string) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	for _, i := range index {
		v := vs[i]
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,AVERAGE-BANDWIDTH=%d,RESOLUTION=%dx%d,CODECS=\"%s\"\n%s\n",
			v.BitrateBps, v.AvgBitrateBps, v.Width, v.Height, codecs(v.Profile, v.Height), uri(i))
	}
	return b.String()
}

// WriteMaster writes the master playlist of an ABR channel (atomically).
func WriteMaster(dir string, vs []Variant) error {
	all := make([]int, len(vs))
	for i := range all {
		all[i] = i
	}
	text := MasterText(vs, all, func(i int) string { return VariantDir(i) + "/" + PlaylistName })
	tmp := filepath.Join(dir, ".master.tmp")
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil { //nolint:gosec // read by the HTTP origin
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, PlaylistName))
}

// IsMaster reports whether a playlist file is a master playlist.
func IsMaster(path string) bool {
	b, err := os.ReadFile(path) //nolint:gosec // path built by the worker
	return err == nil && strings.Contains(string(b), "#EXT-X-STREAM-INF")
}
