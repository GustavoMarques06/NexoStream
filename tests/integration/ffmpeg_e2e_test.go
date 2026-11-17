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
	"nexostream/media-engine/dvr"
	"nexostream/media-engine/packaging"
	"nexostream/media-engine/pipeline"
)

// TestFFmpegEndToEnd exercises the real media path: an ffmpeg test source
// (H.264 + MP2 to force audio conversion) over UDP, the agent with the
// FFmpeg engine, HLS on disk and MPEG-TS over HTTP. Skipped when ffmpeg is
// not installed.
func TestFFmpegEndToEnd(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil || testing.Short() {
		t.Skip("ffmpeg not found in PATH")
	}
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := exec.CommandContext(ctx, ff, "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-c:a", "mp2", "-b:a", "128k",
		"-f", "mpegts", fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", port))
	if err := src.Start(); err != nil {
		t.Fatal(err)
	}

	hlsDir := t.TempDir()
	dvrDir := t.TempDir()
	b := bus.NewMemory()
	defer b.Close()
	a, err := agent.New(agent.Config{WorkerID: "e2e", HLSDir: hlsDir, StateDir: t.TempDir(), MediaAddr: "127.0.0.1:0", DVRDir: dvrDir,
		FFmpegPath: ff, FFprobePath: "ffprobe", HLSSegmentSeconds: 2, HLSListSize: 5},
		b, &pipeline.FFmpeg{Path: ff, Logger: logging.Discard()}, logging.Discard())
	if err != nil {
		t.Fatal(err)
	}
	actx, acancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = a.Run(actx) }()
	defer func() { acancel(); <-done }()
	time.Sleep(200 * time.Millisecond)

	ch := model.Channel{ID: "e2e", Name: "e2e", Enabled: true, Mode: model.ModeAuto, Revision: 1,
		Sources: []model.Source{{ID: "1", Priority: 1, URL: fmt.Sprintf("udp://127.0.0.1:%d", port)}},
		Outputs: model.Outputs{HLS: true, MPEGTSHTTP: true}, Failover: model.DefaultFailover(),
		DVR: model.DVRConfig{Enabled: true, RetentionHours: 1}}
	if err := a.StartChannel(protocol.ChannelSpec{Channel: ch}); err != nil {
		t.Fatal(err)
	}

	playlist := filepath.Join(hlsDir, "e2e", packaging.PlaylistName)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if pi, err := packaging.ReadPlaylist(playlist); err == nil && pi.Segments >= 2 {
			break
		}
		if time.Now().After(deadline) {
			st, _ := a.Status("e2e")
			b, _ := os.ReadFile(playlist)
			t.Fatalf("no HLS segments; status=%+v playlist=%s", st, b)
		}
		time.Sleep(500 * time.Millisecond)
	}
	st, _ := a.Status("e2e")
	if st.State != model.StateRunning || st.Processing != pipeline.ProcessingAudioTranscode {
		t.Fatalf("status=%+v", st)
	}

	srv := http.Server{Handler: a.MediaHandler()}
	ln, err := netListen()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	resp, err := http.Get("http://" + ln.Addr().String() + "/live/e2e/stream.ts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 188*100)
	if _, err := io.ReadFull(resp.Body, buf); err != nil || buf[0] != 0x47 {
		t.Fatalf("mpegts over http: %v", err)
	}

	// DVR: the node records the live segments into its own folder and
	// serves the catch-up playlist (Flussonic style).
	deadline = time.Now().Add(15 * time.Second)
	var segs []dvr.Segment
	for time.Now().Before(deadline) {
		segs, _ = dvr.List(dvrDir, "e2e", time.Now().Add(-time.Hour), time.Now().Add(time.Minute))
		if len(segs) >= 2 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(segs) < 2 {
		t.Fatalf("dvr recorded %d segments", len(segs))
	}
	base := "http://" + ln.Addr().String() + "/dvr/e2e/"
	rr, err := http.Get(base + "recordings.json")
	if err != nil || rr.StatusCode != 200 {
		t.Fatalf("recordings: %v %v", err, rr)
	}
	rr.Body.Close()
	from := segs[0].Start.Unix()
	pl, err := http.Get(fmt.Sprintf("%sindex-%d-%d.m3u8", base, from, 3600))
	if err != nil || pl.StatusCode != 200 {
		t.Fatalf("catch-up playlist: %v %v", err, pl)
	}
	body, _ := io.ReadAll(pl.Body)
	pl.Body.Close()
	if !strings.Contains(string(body), "#EXT-X-ENDLIST") || !strings.Contains(string(body), segs[0].Rel) {
		t.Fatalf("playlist:\n%s", body)
	}
	sg, err := http.Get(base + segs[0].Rel)
	if err != nil || sg.StatusCode != 200 {
		t.Fatalf("dvr segment: %v %v", err, sg)
	}
	head := make([]byte, 188)
	_, _ = io.ReadFull(sg.Body, head)
	sg.Body.Close()
	if head[0] != 0x47 {
		t.Fatal("dvr segment is not MPEG-TS")
	}
}
