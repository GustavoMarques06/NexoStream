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
	"nexostream/media-engine/pipeline"
)

// TestFFmpegRTMPPublish: an encoder publishes to the node over RTMP (the node
// listens), and the channel goes on air as HLS. An encoder with a wrong key
// must not get in. Skipped without ffmpeg.
func TestFFmpegRTMPPublish(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil || testing.Short() {
		t.Skip("ffmpeg not found in PATH")
	}
	port := freePort(t)
	const key = "publicar-0123456789abcdef"
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

	ch := model.Channel{ID: "rtmp-in", Name: "rtmp-in", Enabled: true, Mode: model.ModePassthrough, Revision: 1,
		Sources: []model.Source{{ID: "1", Priority: 1, URL: fmt.Sprintf("rtmp://127.0.0.1:%d/live/%s?mode=listener", port, key)}},
		Outputs: model.Outputs{HLS: true}, Failover: model.DefaultFailover()}
	if err := a.StartChannel(protocol.ChannelSpec{Channel: ch}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second) // the receiver opens the port

	encoder := func(k string, seconds int) error {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, ff, "-hide_banner", "-loglevel", "error", "-re",
			"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
			"-t", fmt.Sprint(seconds), "-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-c:a", "aac", "-b:a", "96k",
			"-f", "flv", fmt.Sprintf("rtmp://127.0.0.1:%d/live/%s", port, k)).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	// A wrong key is refused, and nothing goes on air.
	if err := encoder("chave-errada-0000000000", 2); err == nil {
		t.Fatal("an encoder with a wrong key was accepted")
	}
	if seg := firstSegment(t, filepath.Join(hlsDir, "rtmp-in"), 2*time.Second); seg != "" {
		t.Fatalf("a refused encoder produced %s", seg)
	}

	// The right key: the channel produces HLS while the encoder publishes.
	pub := make(chan error, 1)
	go func() { pub <- encoder(key, 25) }()
	seg := firstSegment(t, filepath.Join(hlsDir, "rtmp-in"), 40*time.Second)
	if seg == "" {
		st, _ := a.Status("rtmp-in")
		t.Fatalf("no HLS from the RTMP publisher; status=%+v", st)
	}
	if probe, err := exec.LookPath("ffprobe"); err == nil {
		out, err := exec.Command(probe, "-v", "error", "-show_entries", "stream=codec_name,width", "-of", "csv=p=0", seg).CombinedOutput()
		if err != nil || !strings.Contains(string(out), "h264,640") || !strings.Contains(string(out), "aac") {
			t.Fatalf("segment from the RTMP publisher: %q (%v)", out, err)
		}
	}
	st, _ := a.Status("rtmp-in")
	if st.State != model.StateRunning {
		t.Errorf("channel state %s, want RUNNING", st.State)
	}
	if err := <-pub; err != nil {
		t.Fatalf("the encoder with the right key failed: %v", err)
	}
}
