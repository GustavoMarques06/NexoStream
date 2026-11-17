package pipeline_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nexostream/internal/logging"
	"nexostream/internal/model"
	"nexostream/media-engine/packaging"
	"nexostream/media-engine/pipeline"
)

func TestOverlayArgs(t *testing.T) {
	video := model.VideoProfile{Codec: "h264", Profile: "main", Width: 1280, Height: 720, BitrateBps: 3_000_000, GOPSeconds: 2}
	audio := model.AudioProfile{Codec: "aac", BitrateBps: 128000, SampleRate: 48000, Channels: 2}
	hls := packaging.DefaultHLS("/hls/c1")
	ov := &pipeline.Overlay{Image: "/srv/overlays/logo.png", Position: "bottom-left", Margin: 20, Width: 160, Opacity: 0.8}

	// Single quality: the logo goes in before the scaler, in one filter graph.
	args, err := pipeline.BuildArgs(pipeline.Spec{
		Plan: pipeline.Plan{Processing: pipeline.ProcessingTranscode, Program: 1, HasVideo: true, HasAudio: true, Video: video, Audio: audio, Encoder: model.EncoderCPU},
		HLS:  &hls, Overlay: ov,
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := strings.Join(args, " ")
	for _, want := range []string{
		"-i pipe:0 -i /srv/overlays/logo.png",
		"[1:v]scale=160:-1,format=rgba,colorchannelmixer=aa=0.80[logo];[0:p:1:v:0][logo]overlay=20:H-h-20[ov];[ov]",
		"-map [vout]",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("missing %q in\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, " -vf ") {
		t.Errorf("the scaler must be in the filter graph, not in -vf:\n%s", cmd)
	}

	// ABR: one logo, before the split, so every quality carries it.
	args, err = pipeline.BuildArgs(pipeline.Spec{
		Plan: pipeline.Plan{Processing: pipeline.ProcessingTranscode, HasVideo: true, HasAudio: true, Audio: audio,
			Renditions: []pipeline.Rendition{{Video: video, Encoder: model.EncoderCPU}, {Video: model.VideoProfile{Codec: "h264", Width: 640, Height: 360, BitrateBps: 800_000, GOPSeconds: 2}, Encoder: model.EncoderCPU}}},
		HLS: &hls, Overlay: &pipeline.Overlay{Image: "/o/l.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd = strings.Join(args, " ")
	if !strings.Contains(cmd, "[0:v:0][1:v]overlay=W-w-0:0[ov];[ov]split=2") {
		t.Errorf("ABR must split after the overlay (top-right by default):\n%s", cmd)
	}

	// Copied video cannot carry a logo: the overlay is ignored, nothing else changes.
	args, _ = pipeline.BuildArgs(pipeline.Spec{
		Plan: pipeline.Plan{Processing: pipeline.ProcessingPassthrough, HasVideo: true, HasAudio: true, CopyVideo: true, CopyAudio: true},
		HLS:  &hls, Overlay: ov,
	})
	if cmd := strings.Join(args, " "); strings.Contains(cmd, "logo.png") || strings.Contains(cmd, "overlay=") {
		t.Errorf("passthrough must not take the overlay:\n%s", cmd)
	}
}

// pixel returns the RGB of one pixel of the first frame of a media file. It
// crops 2x2 at even coordinates: with 4:2:0 chroma a 1x1 crop rounds to nothing.
func pixel(t *testing.T, ff, file string, x, y int) (r, g, b byte) {
	t.Helper()
	out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-i", file, "-frames:v", "1",
		"-vf", "crop=2:2:"+strconv.Itoa(x&^1)+":"+strconv.Itoa(y&^1), "-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1").Output()
	if err != nil || len(out) < 3 {
		var stderr []byte
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = ee.Stderr
		}
		t.Fatalf("read pixel of %s: %v (%d bytes): %s", file, err, len(out), stderr)
	}
	return out[0], out[1], out[2]
}

// The logo really ends up in the picture: a solid red logo in the top-left
// corner turns those pixels red, and the rest of the picture is untouched.
func TestOverlayWithFFmpeg(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	dir := t.TempDir()
	logo := filepath.Join(dir, "logo.png")
	if out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=red:s=80x40", "-frames:v", "1", logo).CombinedOutput(); err != nil {
		t.Fatalf("make logo: %v %s", err, out)
	}
	src, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=blue:s=640x360:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "12", "-c:v", "libx264", "-preset", "ultrafast", "-g", "50",
		"-c:a", "aac", "-b:a", "64k", "-f", "mpegts", "pipe:1").Output()
	if err != nil {
		t.Fatalf("make source: %v", err)
	}
	out := filepath.Join(dir, "hls")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	hls := packaging.HLSOptions{Dir: out, SegmentSeconds: 2, ListSize: 5}
	video := model.VideoProfile{Codec: "h264", Profile: "main", Width: 640, Height: 360, BitrateBps: 800_000, GOPSeconds: 2, Preset: "ultrafast"}
	spec := pipeline.Spec{ChannelID: "ov", HLS: &hls,
		Plan: pipeline.Plan{Processing: pipeline.ProcessingTranscode, Program: 1, HasVideo: true, HasAudio: true, Video: video,
			Audio: model.AudioProfile{Codec: "aac", BitrateBps: 64000, SampleRate: 48000, Channels: 2}, Encoder: model.EncoderCPU},
		Overlay: &pipeline.Overlay{Image: logo, Position: "top-left", Margin: 10},
	}
	p, err := (&pipeline.FFmpeg{Path: ff, Logger: logging.Discard()}).Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop(5 * time.Second)
	go func() { _, _ = p.Input().Write(src) }()
	var seg string
	deadline := time.Now().Add(30 * time.Second)
	for seg == "" {
		// A segment is complete once the playlist lists it.
		if pl, err := os.ReadFile(filepath.Join(out, packaging.PlaylistName)); err == nil {
			for _, line := range strings.Split(string(pl), "\n") {
				if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
					seg = filepath.Join(out, line)
					break
				}
			}
		}
		if seg != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no segment; ffmpeg said: %v", p.LastLog())
		}
		time.Sleep(200 * time.Millisecond)
	}

	if r, g, b := pixel(t, ff, seg, 40, 25); r < 200 || g > 60 || b > 60 { // inside the logo (10..90 x 10..50)
		t.Fatalf("inside the logo the picture must be red, got rgb(%d,%d,%d)", r, g, b)
	}
	if r, g, b := pixel(t, ff, seg, 320, 180); b < 150 || r > 60 { // center: the blue source
		t.Fatalf("outside the logo the picture must stay blue, got rgb(%d,%d,%d)", r, g, b)
	}
	if r, _, b := pixel(t, ff, seg, 5, 5); b < 150 || r > 60 { // inside the margin
		t.Fatalf("the margin must stay blue, got r=%d b=%d", r, b)
	}
}
