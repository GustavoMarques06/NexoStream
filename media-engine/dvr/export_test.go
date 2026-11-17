package dvr

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nexostream/media-engine/drm"
)

// realSegments makes four 2-second HLS segments (video + audio) with the real
// ffmpeg, like the live packager does, and returns their paths.
func realSegments(t *testing.T, ff string) []string {
	t.Helper()
	dir := t.TempDir()
	out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "8", "-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-keyint_min", "50", "-sc_threshold", "0", "-c:a", "aac", "-b:a", "64k",
		"-f", "hls", "-hls_time", "2", "-hls_playlist_type", "vod", "-hls_segment_filename", filepath.Join(dir, "seg%d.ts"),
		filepath.Join(dir, "index.m3u8")).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	segs, _ := filepath.Glob(filepath.Join(dir, "seg*.ts"))
	if len(segs) != 4 {
		t.Fatalf("want 4 segments, got %v", segs)
	}
	return segs
}

// probe reads an exported MP4 like a player: the span of its video packets
// (the header duration of a fragmented MP4 only describes the first fragment)
// and which kinds of streams it holds.
func probe(t *testing.T, ffprobe string, mp4 []byte) (seconds float64, streams string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "out.mp4")
	if err := os.WriteFile(p, mp4, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_type", "-of", "csv=p=0", p).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe streams: %v\n%s", err, out)
	}
	streams = strings.Join(strings.Fields(string(out)), ",")
	out, err = exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "packet=pts_time", "-of", "csv=p=0", p).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe packets: %v\n%s", err, out)
	}
	first, last := -1.0, 0.0
	for _, f := range strings.Fields(string(out)) {
		if v, err := strconv.ParseFloat(strings.TrimSuffix(f, ","), 64); err == nil {
			if first < 0 {
				first = v
			}
			last = max(last, v)
		}
	}
	return last - first + 0.04, streams
}

func TestExportMP4(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not found in PATH")
	}
	segs := realSegments(t, ff)
	root := t.TempDir()
	st := Single(root)
	start := time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)
	for i, s := range segs[:2] { // 14:00:00 and 14:00:02, then a 30 s hole
		if _, err := st.Record("c1", s, start.Add(time.Duration(i)*2*time.Second), 2*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	for i, s := range segs[2:] {
		if _, err := st.Record("c1", s, start.Add(34*time.Second+time.Duration(i)*2*time.Second), 2*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	now := start.Add(time.Hour)
	h := Handler{Storage: func(ch string) (Storage, bool) { return st, ch == "c1" }, Now: func() time.Time { return now }, FFmpeg: ff}
	srv := httptest.NewServer(h)
	defer srv.Close()
	from := strconv.FormatInt(start.Unix(), 10)
	get := func(path string) (int, []byte, http.Header) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b, resp.Header
	}

	// The first continuous stretch (2 pieces = 4 s) is exported; the hole ends it.
	code, body, hdr := get("/dvr/c1/archive-" + from + "-60.mp4")
	if code != 200 || hdr.Get("Content-Type") != "video/mp4" || !strings.Contains(hdr.Get("Content-Disposition"), "c1-"+from+".mp4") {
		t.Fatalf("export %d %v\n%s", code, hdr, body)
	}
	if hdr.Get("X-Export-From") != "2026-11-16T14:00:00Z" || hdr.Get("X-Export-To") != "2026-11-16T14:00:04Z" {
		t.Fatalf("exported range %q..%q", hdr.Get("X-Export-From"), hdr.Get("X-Export-To"))
	}
	secs, streams := probe(t, ffprobe, body)
	if secs < 3 || secs > 5.5 || !strings.Contains(streams, "video") || !strings.Contains(streams, "audio") {
		t.Fatalf("exported file: %.2f s, streams %q", secs, streams)
	}

	// After the hole, the second stretch exports on its own.
	from2 := strconv.FormatInt(start.Add(34*time.Second).Unix(), 10)
	code, body, _ = get("/dvr/c1/archive-" + from2 + "-60.mp4")
	if secs, _ := probe(t, ffprobe, body); code != 200 || secs < 3 || secs > 5.5 {
		t.Fatalf("second stretch: %d, %.2f s", code, secs)
	}

	// Rules.
	if code, _, _ := get("/dvr/c1/archive-" + strconv.FormatInt(start.Add(-48*time.Hour).Unix(), 10) + "-60.mp4"); code != 404 {
		t.Fatalf("no recording: %d", code)
	}
	if code, _, _ := get("/dvr/c1/archive-" + from + "-0.mp4"); code != 400 {
		t.Fatalf("zero length: %d", code)
	}
	if code, _, _ := get("/dvr/c1/archive-" + from + "-" + strconv.Itoa(3*3600) + ".mp4"); code != 400 {
		t.Fatalf("longer than the maximum: %d", code)
	}
	for i := 0; i < DefaultMaxExportJobs; i++ {
		if !st.state().acquireExport(DefaultMaxExportJobs) {
			t.Fatal("could not take a slot")
		}
	}
	code, _, hdr = get("/dvr/c1/archive-" + from + "-60.mp4")
	if code != 503 || hdr.Get("Retry-After") == "" {
		t.Fatalf("slots exhausted: %d", code)
	}
	for i := 0; i < DefaultMaxExportJobs; i++ {
		st.state().releaseExport()
	}
	if code, _, _ := get("/dvr/c1/archive-" + from + "-60.mp4"); code != 200 {
		t.Fatalf("slots must be released: %d", code)
	}

	// Not enabled without ffmpeg; refused for protected channels.
	h.FFmpeg = ""
	srv2 := httptest.NewServer(h)
	defer srv2.Close()
	if resp, err := http.Get(srv2.URL + "/dvr/c1/archive-" + from + "-60.mp4"); err != nil || resp.StatusCode != 501 {
		t.Fatalf("export without ffmpeg: %v %v", resp, err)
	}
	h.FFmpeg = ff
	h.Protect = func(string) (drm.Protector, bool) {
		return drm.Protector{ChannelID: "c1", Keys: drm.LocalKeys{Secret: bytes.Repeat([]byte("k"), 32)}, TokenKey: bytes.Repeat([]byte("t"), 32), Rotation: time.Minute}, true
	}
	srv3 := httptest.NewServer(h)
	defer srv3.Close()
	resp, err := http.Get(srv3.URL + "/dvr/c1/archive-" + from + "-60.mp4")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("protected channel must not export: %d", resp.StatusCode)
	}
}

// A quality of an ABR recording exports through its own v<N>/ path.
func TestExportMP4OfAQuality(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	segs := realSegments(t, ff)
	st := Single(t.TempDir())
	start := time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)
	for i, s := range segs {
		if _, err := st.Record("c1/v1", s, start.Add(time.Duration(i)*2*time.Second), 2*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(Handler{Storage: func(string) (Storage, bool) { return st, true }, FFmpeg: ff, Now: func() time.Time { return start.Add(time.Hour) }})
	defer srv.Close()
	from := strconv.FormatInt(start.Unix(), 10)
	status := func(path string) int {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if code := status("/dvr/c1/v1/archive-" + from + "-30.mp4"); code != 200 {
		t.Fatalf("quality 1 export: %d", code)
	}
	if code := status("/dvr/c1/archive-" + from + "-30.mp4"); code != 404 {
		t.Fatalf("quality 0 was not recorded: %d", code)
	}
}
