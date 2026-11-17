package integration

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nexostream/data-plane/agent"
	"nexostream/internal/bus"
	"nexostream/internal/logging"
	"nexostream/internal/model"
	"nexostream/internal/protocol"
	"nexostream/media-engine/packaging"
	"nexostream/media-engine/pipeline"
)

// TestFFmpegABR runs a channel with two qualities through the real FFmpeg:
// one decode, two encodes, one HLS playlist per quality plus the master
// playlist, read back by ffprobe as a player. Skipped without ffmpeg.
func TestFFmpegABR(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil || testing.Short() {
		t.Skip("ffmpeg not found in PATH")
	}
	probe, _ := exec.LookPath("ffprobe")
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := exec.CommandContext(ctx, ff, "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-c:a", "aac", "-b:a", "96k",
		"-f", "mpegts", fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", port))
	if err := src.Start(); err != nil {
		t.Fatal(err)
	}
	prof := func(id string, w, h, br int) model.TranscodeProfile {
		return model.TranscodeProfile{ID: id, Encoder: model.EncoderCPU,
			Video: model.VideoProfile{Codec: "h264", Profile: "main", Width: w, Height: h, BitrateBps: br, GOPSeconds: 2, Preset: "ultrafast"},
			Audio: model.AudioProfile{Codec: "aac", BitrateBps: 96000, SampleRate: 48000, Channels: 2}}
	}
	profiles := []model.TranscodeProfile{prof("baixa", 320, 180, 300_000), prof("media", 640, 360, 800_000)}

	hlsDir := t.TempDir()
	b := bus.NewMemory()
	defer b.Close()
	a, err := agent.New(agent.Config{WorkerID: "e2e", HLSDir: hlsDir, StateDir: t.TempDir(), DVRDir: t.TempDir(),
		FFmpegPath: ff, FFprobePath: "-", HLSSegmentSeconds: 2, HLSListSize: 5, Encoders: []string{"cpu"}},
		b, &pipeline.FFmpeg{Path: ff, Logger: logging.Discard()}, logging.Discard())
	if err != nil {
		t.Fatal(err)
	}
	actx, acancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = a.Run(actx) }()
	defer func() { acancel(); <-done }()
	time.Sleep(200 * time.Millisecond)

	ch := model.Channel{ID: "abr", Name: "abr", Enabled: true, Mode: model.ModeTranscode, Revision: 1,
		Profiles: []string{"baixa", "media"},
		Sources:  []model.Source{{ID: "1", Priority: 1, URL: fmt.Sprintf("udp://127.0.0.1:%d", port)}},
		Outputs:  model.Outputs{HLS: true}, Failover: model.DefaultFailover()}
	if err := a.StartChannel(protocol.ChannelSpec{Channel: ch, Profiles: profiles}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(hlsDir, "abr")
	deadline := time.Now().Add(45 * time.Second)
	for {
		p0, e0 := packaging.ReadPlaylist(filepath.Join(dir, "v0", "index.m3u8"))
		p1, e1 := packaging.ReadPlaylist(filepath.Join(dir, "v1", "index.m3u8"))
		if e0 == nil && e1 == nil && p0.Segments >= 2 && p1.Segments >= 2 {
			break
		}
		if time.Now().After(deadline) {
			st, _ := a.Status("abr")
			t.Fatalf("no ABR segments; status=%+v", st)
		}
		time.Sleep(500 * time.Millisecond)
	}
	master, _ := os.ReadFile(filepath.Join(dir, "index.m3u8"))
	if !strings.Contains(string(master), "RESOLUTION=640x360") || !strings.Contains(string(master), "RESOLUTION=320x180") ||
		strings.Index(string(master), "640x360") > strings.Index(string(master), "320x180") {
		t.Fatalf("master playlist (highest first):\n%s", master)
	}
	st, _ := a.Status("abr")
	if st.Pipeline == nil || len(st.Pipeline.Renditions) != 2 || st.Pipeline.Renditions[0].Height != 360 {
		t.Fatalf("pipeline status %+v", st.Pipeline)
	}

	srv := http.Server{Handler: a.MediaHandler()}
	ln, err := netListen()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	base := "http://" + ln.Addr().String() + "/hls/abr/"
	resp, err := http.Get(base + "index.m3u8")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("master over HTTP: %v %v", err, resp)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	// Thumbnail of the top quality, made on request.
	tr, err := http.Get(base + "thumb.jpg")
	if err != nil {
		t.Fatal(err)
	}
	jpg, _ := io.ReadAll(tr.Body)
	tr.Body.Close()
	if tr.StatusCode != 200 || len(jpg) < 1000 || jpg[0] != 0xFF || jpg[1] != 0xD8 || tr.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumbnail: %d, %d bytes", tr.StatusCode, len(jpg))
	}
	if st.ThumbnailURL != "" && !strings.HasSuffix(st.ThumbnailURL, "/abr/thumb.jpg") {
		t.Fatalf("thumbnail url %q", st.ThumbnailURL)
	}
	if probe == "" {
		return
	}
	// Each quality must really have its own resolution.
	for i, want := range []string{"640,360", "320,180"} {
		pctx, pcancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := exec.CommandContext(pctx, probe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height",
			"-of", "csv=p=0", fmt.Sprintf("%sv%d/index.m3u8", base, i)).CombinedOutput()
		pcancel()
		if err != nil || !strings.Contains(string(out), want) {
			t.Fatalf("quality %d: want %s, got %q (%v)", i, want, out, err)
		}
	}
}
