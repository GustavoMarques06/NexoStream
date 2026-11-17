package ingest_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nexostream/media-engine/ingest"
	"nexostream/media-engine/ingest/tstest"
)

func TestParseSourceURL(t *testing.T) {
	good := map[string]bool{
		"udp://239.1.1.1:1234": true,
		"udp://@:5000":         false,
		"udp://127.0.0.1:5000": false,
		"rtp://239.1.1.2:5004": true,
	}
	for u, mc := range good {
		a, err := ingest.ParseSourceURL(u)
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		if a.Multicast != mc {
			t.Errorf("%s multicast=%v", u, a.Multicast)
		}
	}
	for _, u := range []string{"ftp://1.2.3.4:21", "udp://host:1", "udp://1.2.3.4", "udp://1.2.3.4:99999", "::bad",
		"srt://:9000", "rtmp:///live/key", "http://:8080/x"} {
		if _, err := ingest.ParseSourceURL(u); err == nil {
			t.Errorf("%s: expected error", u)
		}
	}
	pulled := map[string]int{
		"srt://10.0.0.5:9000":                 9000,
		"srt://:9000?mode=listener":           9000,
		"rtmp://server.example/live/key":      1935,
		"rtsp://camera.local/stream1":         554,
		"https://cdn.example/live/index.m3u8": 443,
		"http://10.1.1.1:8080/canal.ts":       8080,
	}
	for u, port := range pulled {
		a, err := ingest.ParseSourceURL(u)
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		if !a.Pulled || a.Port != port {
			t.Errorf("%s: pulled=%v port=%d", u, a.Pulled, a.Port)
		}
	}
}

func TestPullArgs(t *testing.T) {
	a, _ := ingest.ParseSourceURL("https://cdn.example/live/index.m3u8")
	args := strings.Join(ingest.PullArgs("https://cdn.example/live/index.m3u8", a), " ")
	for _, w := range []string{"-re", "-reconnect 1", "-i https://cdn.example/live/index.m3u8", "-c copy -f mpegts", "pipe:1"} {
		if !strings.Contains(args, w) {
			t.Errorf("missing %q in %s", w, args)
		}
	}
	r, _ := ingest.ParseSourceURL("rtsp://cam/s")
	if args := strings.Join(ingest.PullArgs("rtsp://cam/s", r), " "); !strings.Contains(args, "-rtsp_transport tcp") || strings.Contains(args, "-re ") {
		t.Errorf("rtsp args %s", args)
	}
}

// TestPullArgsRestrictProtocols: a hostile playlist must not make ffmpeg
// open local files, so every pull passes a protocol whitelist before -i.
func TestPullArgsRestrictProtocols(t *testing.T) {
	for _, u := range []string{"https://cdn/x.m3u8", "http://a/b.ts", "rtsp://cam/s", "rtmp://a/live/k", "rtmps://a/live/k", "srt://h:9000?mode=caller"} {
		a, err := ingest.ParseSourceURL(u)
		if err != nil {
			t.Fatal(u, err)
		}
		args := ingest.PullArgs(u, a)
		wl, in := -1, -1
		for i, x := range args {
			switch x {
			case "-protocol_whitelist":
				wl = i
			case "-i":
				in = i
			}
		}
		if wl < 0 || in < 0 || wl > in {
			t.Fatalf("%s: whitelist missing or after -i: %v", u, args)
		}
		for _, banned := range []string{"file", "concat", "subfile", "data", "pipe", "ftp"} {
			for _, p := range strings.Split(args[wl+1], ",") {
				if p == banned {
					t.Errorf("%s: whitelist allows %s", u, banned)
				}
			}
		}
	}
}

// TestHostileHLSCannotReadLocalFiles runs the real ffmpeg against an HTTP
// playlist whose segment is a local file: with the whitelist ffmpeg must
// refuse it, and without it (control run) the refusal message must be absent,
// which shows the test detects the difference.
func TestHostileHLSCannotReadLocalFiles(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	secret := filepath.Join(t.TempDir(), "secret.ts")
	if err := os.WriteFile(secret, []byte("local-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	playlist := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\nfile:" + filepath.ToSlash(secret) + "\n#EXT-X-ENDLIST\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(playlist))
	}))
	defer srv.Close()
	u := srv.URL + "/evil.m3u8"
	a, _ := ingest.ParseSourceURL(u)

	// Keep PullArgs' input options and send the media to a null muxer.
	inputOnly := func(args []string) []string {
		for i, x := range args {
			if x == "-map" {
				return args[:i]
			}
		}
		return args
	}
	run := func(args []string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(ctx, ff, append(args, "-f", "null", "-")...).CombinedOutput() //nolint:gosec // test: ffmpeg path from LookPath
		return string(out)
	}
	safe := run(inputOnly(ingest.PullArgs(u, a)))
	if !strings.Contains(safe, "whitelist") {
		t.Fatalf("whitelist did not stop the local file segment; ffmpeg said:\n%s", safe)
	}
	// Control: same playlist with the file protocol allowed must not be refused for that reason.
	open := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-protocol_whitelist", "http,https,tcp,tls,crypto,file", "-allowed_extensions", "ALL", "-i", u}
	if loose := run(open); strings.Contains(loose, "whitelist") {
		t.Fatalf("control run was also refused by the whitelist:\n%s", loose)
	}
}

// TestPullerHTTP pulls a continuous MPEG-TS over HTTP through ffmpeg.
func TestPullerHTTP(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	ts := filepath.Join(t.TempDir(), "src.ts")
	gen := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440", "-t", "6", "-c:v", "libx264", "-preset", "ultrafast", "-g", "25",
		"-c:a", "aac", "-f", "mpegts", ts)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v %s", err, out)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		http.ServeFile(w, r, ts)
	}))
	defer srv.Close()

	src, err := ingest.Open(ingest.ReceiverConfig{URL: srv.URL + "/canal.ts", FFmpegPath: ff})
	if err != nil {
		t.Fatal(err)
	}
	an := ingest.NewAnalyzer(0)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = src.Run(ctx, func(b []byte) { an.Feed(b, time.Now()) })
	s := an.Snapshot()
	if !s.PMTSeen || s.Video() == nil || s.Video().Codec != "h264" || s.Audio() == nil || s.Audio().Codec != "aac" {
		t.Fatalf("snapshot=%+v err=%v", s, err)
	}
	if s.CCErrors != 0 || s.SyncErrors != 0 {
		t.Fatalf("cc=%d sync=%d", s.CCErrors, s.SyncErrors)
	}
	// The file ends: the puller must report it so the runner can retry.
	if err == nil {
		t.Fatal("expected an error when the source ends")
	}
}

func TestAnalyzerPATPMT(t *testing.T) {
	g := tstest.New()
	a := ingest.NewAnalyzer(0)
	now := time.Now()
	a.Feed(g.Next(200), now)
	s := a.Snapshot()
	if !s.PMTSeen {
		t.Fatal("PMT not parsed")
	}
	if s.Program != 1 || len(s.Streams) != 2 {
		t.Fatalf("program=%d streams=%v", s.Program, s.Streams)
	}
	if v := s.Video(); v == nil || v.Codec != "h264" {
		t.Fatalf("video=%v", v)
	}
	if au := s.Audio(); au == nil || au.Codec != "aac" {
		t.Fatalf("audio=%v", au)
	}
	if s.CCErrors != 0 {
		t.Fatalf("cc errors=%d", s.CCErrors)
	}
	if s.LastVideoAt.IsZero() || s.LastAudioAt.IsZero() || s.LastPCRAdvanceAt.IsZero() {
		t.Fatalf("presence not tracked: %+v", s)
	}
	if s.Bytes != 200*188 {
		t.Fatalf("bytes=%d", s.Bytes)
	}
}

func TestAnalyzerCodecs(t *testing.T) {
	g := tstest.New()
	g.VideoType, g.AudioType = 0x02, 0x03 // MPEG-2 + MP2
	a := ingest.NewAnalyzer(0)
	a.Feed(g.Next(50), time.Now())
	s := a.Snapshot()
	if s.Video().Codec != "mpeg2video" || s.Audio().Codec != "mp2" {
		t.Fatalf("streams=%v", s.Streams)
	}
}

func TestAnalyzerCCErrors(t *testing.T) {
	g := tstest.New()
	a := ingest.NewAnalyzer(0)
	buf := g.Next(100)
	// Drop packets 50..52 to break the continuity of the ES PIDs.
	broken := append(append([]byte{}, buf[:50*188]...), buf[53*188:]...)
	a.Feed(broken, time.Now())
	if a.Snapshot().CCErrors == 0 {
		t.Fatal("expected continuity errors")
	}
}

func TestAnalyzerCorruptSectionIgnored(t *testing.T) {
	g := tstest.New()
	pat := g.PAT()
	pat[10] ^= 0xFF // break CRC
	a := ingest.NewAnalyzer(0)
	a.Feed(pat, time.Now())
	a.Feed(g.PMT(), time.Now())
	if a.Snapshot().PMTSeen {
		t.Fatal("PMT must not be found through a corrupted PAT")
	}
}

func TestReceiverUDPAndRTP(t *testing.T) {
	// Pick a free port.
	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.LocalAddr().(*net.UDPAddr).Port
	l.Close()

	r, err := ingest.NewReceiver(ingest.ReceiverConfig{URL: fmt.Sprintf("udp://127.0.0.1:%d", port)})
	if err != nil {
		t.Fatal(err)
	}
	a := ingest.NewAnalyzer(0)
	var got atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx, func(b []byte) { got.Add(int64(len(b))); a.Feed(b, time.Now()) })
	}()
	time.Sleep(100 * time.Millisecond)

	conn, err := net.Dial("udp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	g := tstest.New()
	for i := 0; i < 20; i++ {
		_, _ = conn.Write(g.Next(7))
	}
	// RTP-wrapped datagram.
	rtp := append([]byte{0x80, 33, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1}, g.Next(7)...)
	_, _ = conn.Write(rtp)

	deadline := time.Now().Add(3 * time.Second)
	for got.Load() < 21*7*188 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got.Load() != 21*7*188 {
		t.Fatalf("received %d bytes", got.Load())
	}
	if !a.Snapshot().PMTSeen {
		t.Fatal("PMT not parsed from UDP")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("receiver did not stop")
	}
}

func TestParseSourceURLListenerAndPassphrase(t *testing.T) {
	for raw, want := range map[string]struct {
		listener bool
		passLen  int
	}{
		"srt://:9000?mode=listener&passphrase=correct-horse-1": {true, 15},
		"srt://:9000?mode=listener":                            {true, 0},
		"srt://cdn.example:9000?mode=caller&passphrase=abc":    {false, 3},
		"srt://cdn.example:9000":                               {false, 0},
		"rtmp://server/live/key":                               {false, 0},
		// RTMP also listens (the key length rule lives in the API, like the
		// SRT passphrase rule); the passphrase field is SRT only.
		"rtmp://:1935/live/key?mode=listener": {true, 0},
	} {
		a, err := ingest.ParseSourceURL(raw)
		if err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		if a.Listener != want.listener || a.PassphraseLen != want.passLen {
			t.Errorf("%s: listener=%v passphrase=%d, want %v/%d", raw, a.Listener, a.PassphraseLen, want.listener, want.passLen)
		}
	}
}
