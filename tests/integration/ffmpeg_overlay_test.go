package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// firstSegment waits until the playlist of dir lists a segment (complete).
func firstSegment(t *testing.T, dir string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pl, err := os.ReadFile(filepath.Join(dir, packaging.PlaylistName)); err == nil {
			for _, l := range strings.Split(string(pl), "\n") {
				if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
					return filepath.Join(dir, l)
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return ""
}

// rgbAt reads one pixel (2x2 crop at even coordinates, 4:2:0 safe).
func rgbAt(t *testing.T, ff, file string, x, y int) (r, g, b byte) {
	t.Helper()
	out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-i", file, "-frames:v", "1",
		"-vf", "crop=2:2:"+strconv.Itoa(x&^1)+":"+strconv.Itoa(y&^1), "-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1").Output()
	if err != nil || len(out) < 3 {
		t.Fatalf("read pixel of %s: %v", file, err)
	}
	return out[0], out[1], out[2]
}

// TestFFmpegOverlay runs two transcoded channels through the agent and the
// real ffmpeg: one with a logo from the node's overlay folder, one whose logo
// file is missing (it must stay on air, without the logo). Skipped without
// ffmpeg.
func TestFFmpegOverlay(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil || testing.Short() {
		t.Skip("ffmpeg not found in PATH")
	}
	overlays := t.TempDir()
	if out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=red:s=80x40",
		"-frames:v", "1", filepath.Join(overlays, "marca.png")).CombinedOutput(); err != nil {
		t.Fatalf("make logo: %v %s", err, out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ports := []int{freePort(t), freePort(t)}
	for _, port := range ports {
		src := exec.CommandContext(ctx, ff, "-hide_banner", "-loglevel", "error", "-re",
			"-f", "lavfi", "-i", "color=c=blue:s=640x360:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
			"-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-c:a", "aac", "-b:a", "64k",
			"-f", "mpegts", fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", port))
		if err := src.Start(); err != nil {
			t.Fatal(err)
		}
	}
	profile := model.TranscodeProfile{ID: "p360", Encoder: model.EncoderCPU,
		Video: model.VideoProfile{Codec: "h264", Profile: "main", Width: 640, Height: 360, BitrateBps: 800_000, GOPSeconds: 2, Preset: "ultrafast"},
		Audio: model.AudioProfile{Codec: "aac", BitrateBps: 64000, SampleRate: 48000, Channels: 2}}

	hlsDir := t.TempDir()
	b := bus.NewMemory()
	defer b.Close()
	a, err := agent.New(agent.Config{WorkerID: "e2e", HLSDir: hlsDir, StateDir: t.TempDir(), DVRDir: t.TempDir(), OverlayDir: overlays,
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

	for i, c := range []struct{ id, image string }{{"com-logo", "marca.png"}, {"logo-ausente", "nao-existe.png"}} {
		ch := model.Channel{ID: c.id, Name: c.id, Enabled: true, Mode: model.ModeTranscode, Revision: 1, Profiles: []string{"p360"},
			Sources: []model.Source{{ID: "1", Priority: 1, URL: fmt.Sprintf("udp://127.0.0.1:%d", ports[i])}},
			Outputs: model.Outputs{HLS: true}, Failover: model.DefaultFailover(),
			Overlay: model.OverlayConfig{Enabled: true, Image: c.image, Position: model.OverlayTopLeft, Margin: 10}}
		if err := a.StartChannel(protocol.ChannelSpec{Channel: ch, Profiles: []model.TranscodeProfile{profile}}); err != nil {
			t.Fatal(err)
		}
	}

	seg := firstSegment(t, filepath.Join(hlsDir, "com-logo"), 45*time.Second)
	if seg == "" {
		st, _ := a.Status("com-logo")
		t.Fatalf("no segment for the channel with a logo; status=%+v", st)
	}
	if r, g, bl := rgbAt(t, ff, seg, 40, 24); r < 200 || g > 60 || bl > 60 {
		t.Fatalf("the logo is not in the picture: rgb(%d,%d,%d) where the logo should be", r, g, bl)
	}
	if r, _, bl := rgbAt(t, ff, seg, 320, 180); bl < 150 || r > 60 {
		t.Fatalf("the rest of the picture changed: r=%d b=%d", r, bl)
	}

	// A missing logo file must not take the channel off the air.
	seg = firstSegment(t, filepath.Join(hlsDir, "logo-ausente"), 45*time.Second)
	if seg == "" {
		st, _ := a.Status("logo-ausente")
		t.Fatalf("a missing logo took the channel off the air; status=%+v", st)
	}
	if r, _, bl := rgbAt(t, ff, seg, 40, 24); bl < 150 || r > 60 {
		t.Fatalf("with no logo file the corner must stay as the source: r=%d b=%d", r, bl)
	}
}
