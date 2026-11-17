package integration

import (
	"context"
	"fmt"
	"net"
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
	"nexostream/media-engine/pipeline"
)

func freeTCP(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestFFmpegPushAndDASH: a channel pushes to an SRT and an RTMP server (local
// ffmpeg listeners; the RTMP one starts late, so the push must retry) and
// publishes MPEG-DASH read back by ffprobe. Skipped without ffmpeg.
func TestFFmpegPushAndDASH(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	probe, perr := exec.LookPath("ffprobe")
	if err != nil || perr != nil || testing.Short() {
		t.Skip("ffmpeg/ffprobe not found in PATH")
	}
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
	out := t.TempDir()
	srtPort := freePort(t)
	srtOut := filepath.Join(out, "srt.ts")
	srt := exec.CommandContext(ctx, ff, "-hide_banner", "-loglevel", "error", "-y",
		"-i", fmt.Sprintf("srt://127.0.0.1:%d?mode=listener", srtPort), "-t", "6", "-c", "copy", "-f", "mpegts", srtOut)
	if err := srt.Start(); err != nil {
		t.Fatal(err)
	}
	rtmpPort := freeTCP(t)
	rtmpURL := fmt.Sprintf("rtmp://127.0.0.1:%d/live/chave-secreta", rtmpPort)

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
	ch := model.Channel{ID: "push", Enabled: true, Mode: model.ModePassthrough, Revision: 1,
		Sources:  []model.Source{{ID: "1", Priority: 1, URL: fmt.Sprintf("udp://127.0.0.1:%d", port)}},
		Outputs:  model.Outputs{HLS: true, DASH: true, Push: []string{fmt.Sprintf("srt://127.0.0.1:%d", srtPort), rtmpURL}},
		Failover: model.DefaultFailover()}
	if err := a.StartChannel(protocol.ChannelSpec{Channel: ch}); err != nil {
		t.Fatal(err)
	}

	// The RTMP server is down at first: the push must be retrying.
	deadline := time.Now().Add(20 * time.Second)
	for {
		st, _ := a.Status("push")
		if len(st.Pushes) == 2 && st.Pushes[1].State == "retrying" {
			if strings.Contains(st.Pushes[1].URL, "chave-secreta") {
				t.Fatalf("stream key shown in status: %s", st.Pushes[1].URL)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rtmp push never retried: %+v (channel %s %s)", st.Pushes, st.State, st.LastError)
		}
		time.Sleep(300 * time.Millisecond)
	}
	rtmpOut := filepath.Join(out, "rtmp.flv")
	rtmp := exec.CommandContext(ctx, ff, "-hide_banner", "-loglevel", "error", "-y", "-listen", "1",
		"-i", rtmpURL, "-t", "4", "-c", "copy", rtmpOut)
	if err := rtmp.Start(); err != nil {
		t.Fatal(err)
	}
	waitFile := func(cmd *exec.Cmd, path string) {
		t.Helper()
		errc := make(chan error, 1)
		go func() { errc <- cmd.Wait() }()
		select {
		case <-errc:
		case <-time.After(60 * time.Second):
			t.Fatalf("receiver of %s did not finish", path)
		}
		fi, err := os.Stat(path)
		if err != nil || fi.Size() < 50_000 {
			t.Fatalf("%s: nothing received (%v)", path, err)
		}
		pctx, pcancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer pcancel()
		info, err := exec.CommandContext(pctx, probe, "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", path).CombinedOutput()
		if err != nil || !strings.Contains(string(info), "h264") || !strings.Contains(string(info), "aac") {
			t.Fatalf("%s: %q %v", path, info, err)
		}
	}
	waitFile(srt, srtOut)
	waitFile(rtmp, rtmpOut)
	st, _ := a.Status("push")
	if st.Pushes[1].Restarts == 0 {
		t.Fatalf("rtmp push should have retried: %+v", st.Pushes[1])
	}

	// MPEG-DASH through the media server.
	srv := http.Server{Handler: a.MediaHandler()}
	ln, err := netListen()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	mpd := "http://" + ln.Addr().String() + "/hls/push/dash/index.mpd"
	deadline = time.Now().Add(30 * time.Second)
	for {
		pctx, pcancel := context.WithTimeout(context.Background(), 20*time.Second)
		info, err := exec.CommandContext(pctx, probe, "-v", "error", "-show_entries", "stream=codec_name,width", "-of", "csv=p=0", mpd).CombinedOutput()
		pcancel()
		if err == nil && strings.Contains(string(info), "h264,640") && strings.Contains(string(info), "aac") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("DASH: %q %v", info, err)
		}
		time.Sleep(time.Second)
	}
	// The MPEG-TS relay exists for the push, but MPEG-TS over HTTP is off.
	resp, err := http.Get("http://" + ln.Addr().String() + "/live/push/stream.ts")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("MPEG-TS over HTTP served while off: %d", resp.StatusCode)
	}
}
