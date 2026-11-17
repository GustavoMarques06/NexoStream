package integration

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"nexostream/data-plane/agent"
	"nexostream/internal/bus"
	"nexostream/internal/logging"
	"nexostream/internal/model"
	"nexostream/internal/protocol"
	"nexostream/media-engine/dvr"
	"nexostream/media-engine/pipeline"
)

// TestFFmpegABRDVR records a channel with two qualities for about 20 seconds
// into a temporary folder (removed by the test) and plays both qualities
// back through the DVR handler, with ffprobe as the player. Skipped without
// ffmpeg.
func TestFFmpegABRDVR(t *testing.T) { abrDVR(t, true) }

// TestFFmpegABRDVRTopOnly is the default: an ABR channel records only its
// highest quality, so nothing changes for existing channels.
func TestFFmpegABRDVRTopOnly(t *testing.T) { abrDVR(t, false) }

func abrDVR(t *testing.T, all bool) {
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

	dvrDir := t.TempDir() // removed when the test ends
	b := bus.NewMemory()
	defer b.Close()
	a, err := agent.New(agent.Config{WorkerID: "e2e", HLSDir: t.TempDir(), StateDir: t.TempDir(), DVRDir: dvrDir,
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
		Outputs:  model.Outputs{HLS: true}, DVR: model.DVRConfig{Enabled: true, RetentionHours: 1, AllQualities: all}, Failover: model.DefaultFailover()}
	if err := a.StartChannel(protocol.ChannelSpec{Channel: ch, Profiles: profiles}); err != nil {
		t.Fatal(err)
	}

	// Wait until both qualities have at least 3 recorded pieces.
	st := dvr.Single(dvrDir)
	var q0, q1 []dvr.Segment
	deadline := time.Now().Add(60 * time.Second)
	for {
		q0, _ = st.List("abr", time.Now().Add(-time.Hour), time.Now().Add(time.Minute))
		q1, _ = st.List("abr/v1", time.Now().Add(-time.Hour), time.Now().Add(time.Minute))
		if len(q0) >= 3 && (len(q1) >= 3 || !all) {
			break
		}
		if time.Now().After(deadline) {
			s, _ := a.Status("abr")
			t.Fatalf("DVR did not record both qualities (q0=%d q1=%d); status=%+v", len(q0), len(q1), s)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !all {
		// Quality 0 is recorded and nothing else: no folder, no qualities file.
		if len(q1) != 0 || st.ReadVariants("abr") != nil {
			t.Fatalf("top-only recorded extra qualities: q1=%d variants=%v", len(q1), st.ReadVariants("abr"))
		}
		if keys := st.VariantKeys("abr"); len(keys) != 1 {
			t.Fatalf("variant folders %v", keys)
		}
		srv := http.Server{Handler: a.MediaHandler()}
		ln, err := netListen()
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = srv.Serve(ln) }()
		defer srv.Close()
		base := "http://" + ln.Addr().String() + "/dvr/abr/"
		list := "index-" + strconv.FormatInt(q0[0].Start.Unix(), 10) + "-30.m3u8"
		resp, err := http.Get(base + list)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || strings.Contains(string(body), "#EXT-X-STREAM-INF") || !strings.Contains(string(body), "#EXTINF") {
			t.Fatalf("top-only playlist must be a plain media playlist, got %d:\n%s", resp.StatusCode, body)
		}
		if r2, err := http.Get(base + "v1/" + list); err == nil {
			r2.Body.Close()
			if r2.StatusCode == 200 {
				t.Fatal("quality 1 served although it was not recorded")
			}
		}
		return
	}
	if vs := st.ReadVariants("abr"); len(vs) != 2 || vs[0].Height != 360 || vs[1].Height != 180 {
		t.Fatalf("qualities file: %+v", vs)
	}

	srv := http.Server{Handler: a.MediaHandler()}
	ln, err := netListen()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	base := "http://" + ln.Addr().String() + "/dvr/abr/"
	from := strconv.FormatInt(q0[0].Start.Unix(), 10)
	list := "index-" + from + "-30.m3u8"

	resp, err := http.Get(base + list)
	if err != nil {
		t.Fatal(err)
	}
	master, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || strings.Count(string(master), "#EXT-X-STREAM-INF") != 2 ||
		strings.Index(string(master), "640x360") > strings.Index(string(master), "320x180") || !strings.Contains(string(master), "v1/"+list) {
		t.Fatalf("DVR master %d:\n%s", resp.StatusCode, master)
	}
	if probe == "" {
		return
	}
	// Each recorded quality plays back at its own resolution.
	for i, want := range []string{"640,360", "320,180"} {
		pctx, pcancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := exec.CommandContext(pctx, probe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height",
			"-of", "csv=p=0", fmt.Sprintf("%sv%d/%s", base, i, list)).CombinedOutput()
		pcancel()
		if err != nil || !strings.Contains(string(out), want) {
			t.Fatalf("recorded quality %d: want %s, got %q (%v)", i, want, out, err)
		}
	}
}
