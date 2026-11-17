package integration

import (
	"context"
	"fmt"
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

// TestCompatMatrix fills the software part of the compatibility matrix
// (spec 5.4): for each input codec pair the auto mode must produce HLS that
// players read as H.264 + AAC. It runs the real FFmpeg on this machine; it
// says nothing about capacity or about real set-top boxes.
func TestCompatMatrix(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	probe, perr := exec.LookPath("ffprobe")
	if err != nil || perr != nil || testing.Short() {
		t.Skip("ffmpeg/ffprobe not found in PATH")
	}
	cases := []struct {
		name, vcodec, acodec string
		vargs                []string
		processing           string
	}{
		{"H.264 + AAC", "libx264", "aac", []string{"-preset", "ultrafast", "-g", "50"}, pipeline.ProcessingPassthrough},
		{"H.264 + MP2", "libx264", "mp2", []string{"-preset", "ultrafast", "-g", "50"}, pipeline.ProcessingAudioTranscode},
		{"H.264 + AC-3", "libx264", "ac3", []string{"-preset", "ultrafast", "-g", "50"}, pipeline.ProcessingAudioTranscode},
		{"MPEG-2 + MP2", "mpeg2video", "mp2", []string{"-b:v", "3M", "-g", "25"}, pipeline.ProcessingTranscode},
		{"HEVC + AAC", "libx265", "aac", []string{"-preset", "ultrafast", "-g", "50", "-x265-params", "log-level=error"}, pipeline.ProcessingTranscode},
	}
	var report []string
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			port := freePort(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			args := []string{"-hide_banner", "-loglevel", "error", "-re",
				"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
				"-c:v", tc.vcodec}
			args = append(args, tc.vargs...)
			args = append(args, "-c:a", tc.acodec, "-f", "mpegts", fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", port))
			src := exec.CommandContext(ctx, ff, args...)
			if err := src.Start(); err != nil {
				t.Fatal(err)
			}
			hlsDir := t.TempDir()
			b := bus.NewMemory()
			defer b.Close()
			sd := model.TranscodeProfile{ID: "sd", Encoder: model.EncoderCPU,
				Video: model.VideoProfile{Codec: "h264", Profile: "main", Width: 640, Height: 360, BitrateBps: 800_000, GOPSeconds: 2, Preset: "ultrafast"},
				Audio: model.AudioProfile{Codec: "aac", BitrateBps: 96000, SampleRate: 48000, Channels: 2}}
			a, err := agent.New(agent.Config{WorkerID: "m", HLSDir: hlsDir, StateDir: t.TempDir(), DVRDir: t.TempDir(),
				FFmpegPath: ff, FFprobePath: "-", HLSSegmentSeconds: 2, HLSListSize: 5, Encoders: []string{"cpu"}, DefaultProfile: "sd"},
				b, &pipeline.FFmpeg{Path: ff, Logger: logging.Discard()}, logging.Discard())
			if err != nil {
				t.Fatal(err)
			}
			actx, acancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); _ = a.Run(actx) }()
			defer func() { acancel(); <-done }()
			ch := model.Channel{ID: "m", Enabled: true, Mode: model.ModeAuto, Revision: 1,
				Sources: []model.Source{{ID: "1", Priority: 1, URL: fmt.Sprintf("udp://127.0.0.1:%d", port)}},
				Outputs: model.Outputs{HLS: true}, Failover: model.DefaultFailover()}
			if err := a.StartChannel(protocol.ChannelSpec{Channel: ch, Profiles: []model.TranscodeProfile{sd}}); err != nil {
				t.Fatal(err)
			}
			pl := filepath.Join(hlsDir, "m", packaging.PlaylistName)
			deadline := time.Now().Add(40 * time.Second)
			var pi packaging.PlaylistInfo
			for {
				if pi, err = packaging.ReadPlaylist(pl); err == nil && pi.Segments >= 2 {
					break
				}
				if time.Now().After(deadline) {
					st, _ := a.Status("m")
					t.Fatalf("no HLS: %+v", st)
				}
				time.Sleep(500 * time.Millisecond)
			}
			st, _ := a.Status("m")
			seg := filepath.Join(hlsDir, "m", pi.Entries[len(pi.Entries)-1].URI)
			out, err := exec.Command(probe, "-v", "error", "-show_entries", "stream=codec_type,codec_name", "-of", "csv=p=0", seg).CombinedOutput()
			got := strings.Join(strings.Fields(string(out)), " ")
			if err != nil || !strings.Contains(got, "h264,video") || !strings.Contains(got, "aac,audio") {
				t.Fatalf("output is not H.264 + AAC: %q (%v)", got, err)
			}
			if st.Processing != tc.processing {
				t.Errorf("processing %q, want %q", st.Processing, tc.processing)
			}
			report = append(report, fmt.Sprintf("%-14s -> %-16s saída %s", tc.name, st.Processing, got))
		})
	}
	t.Log("matriz (software, esta máquina):\n" + strings.Join(report, "\n"))
}
