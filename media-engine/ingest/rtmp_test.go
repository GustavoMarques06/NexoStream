package ingest_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nexostream/media-engine/ingest"
)

const rtmpTestKey = "k3y-0123456789abcdef"

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startRTMPListener runs an RTMP listener source and collects what it
// delivers. It returns the collected bytes and the error Run returned.
func startRTMPListener(t *testing.T, ff string, port int) (data func() []byte, done <-chan error) {
	t.Helper()
	src, err := ingest.Open(ingest.ReceiverConfig{URL: fmt.Sprintf("rtmp://127.0.0.1:%d/live/%s?mode=listener", port, rtmpTestKey), FFmpegPath: ff})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var mu sync.Mutex
	var buf []byte
	ch := make(chan error, 1)
	go func() {
		ch <- src.Run(ctx, func(b []byte) {
			mu.Lock()
			buf = append(buf, b...)
			mu.Unlock()
		})
	}()
	// Wait until the port accepts connections.
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return func() []byte { mu.Lock(); defer mu.Unlock(); return append([]byte(nil), buf...) }, ch
}

func publish(ff string, url string, seconds int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ff, "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", fmt.Sprint(seconds), "-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-c:a", "aac", "-b:a", "64k",
		"-f", "flv", url).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

func TestRTMPListenerAcceptsTheRightKey(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	port := freeTCPPort(t)
	data, done := startRTMPListener(t, ff, port)
	if err := publish(ff, fmt.Sprintf("rtmp://127.0.0.1:%d/live/%s", port, rtmpTestKey), 4); err != nil {
		t.Fatalf("the encoder could not publish with the right key: %v", err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "stopped") {
			t.Fatalf("after the publisher leaves, Run must report it: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after the publisher left")
	}
	ts := data()
	if len(ts) < 188*100 || ts[0] != 0x47 || len(ts)%188 != 0 {
		t.Fatalf("expected aligned MPEG-TS, got %d bytes starting with %#x", len(ts), firstByte(ts))
	}
	// What came out is a playable stream with the published codecs.
	if probe, err := exec.LookPath("ffprobe"); err == nil {
		f := filepath.Join(t.TempDir(), "out.ts")
		_ = os.WriteFile(f, ts, 0o644)
		out, err := exec.Command(probe, "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", f).CombinedOutput()
		if err != nil || !strings.Contains(string(out), "h264") || !strings.Contains(string(out), "aac") {
			t.Fatalf("the remuxed stream: %q (%v)", out, err)
		}
	}
}

func firstByte(b []byte) byte {
	if len(b) == 0 {
		return 0
	}
	return b[0]
}

func TestRTMPListenerRefusesWrongKeyAndApp(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	for name, path := range map[string]string{
		"wrong key":    "live/" + strings.ToUpper(rtmpTestKey),
		"wrong app":    "other/" + rtmpTestKey,
		"key prefix":   "live/" + rtmpTestKey[:len(rtmpTestKey)-1],
		"empty key":    "live/",
		"key as query": "live/x?" + rtmpTestKey,
	} {
		t.Run(name, func(t *testing.T) {
			port := freeTCPPort(t)
			data, done := startRTMPListener(t, ff, port)
			if err := publish(ff, fmt.Sprintf("rtmp://127.0.0.1:%d/%s", port, path), 3); err == nil {
				t.Error("the encoder must be told the publish failed")
			}
			select {
			case err := <-done:
				if !errors.Is(err, ingest.ErrRTMPRefused) {
					t.Fatalf("Run must report the refusal (ErrRTMPRefused), got: %v", err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("Run did not return after a refused publisher")
			}
			if n := len(data()); n != 0 {
				t.Fatalf("a refused publisher delivered %d bytes", n)
			}
		})
	}
}

// While one encoder publishes, a second one is turned away and the first goes on.
func TestRTMPListenerOnePublisherAtATime(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	port := freeTCPPort(t)
	data, done := startRTMPListener(t, ff, port)
	url := fmt.Sprintf("rtmp://127.0.0.1:%d/live/%s", port, rtmpTestKey)
	first := make(chan error, 1)
	go func() { first <- publish(ff, url, 6) }()
	time.Sleep(2 * time.Second)
	if err := publish(ff, url, 2); err == nil {
		t.Error("a second encoder must not be able to publish at the same time")
	}
	if err := <-first; err != nil {
		t.Fatalf("the first encoder was disturbed: %v", err)
	}
	<-done
	if len(data()) < 188*100 {
		t.Fatal("the first encoder's stream did not come through")
	}
}

// Anything that is not RTMP (a scanner, a TCP health check, a browser) is
// dropped at once, and the listener stays open for the real encoder.
func TestRTMPListenerSurvivesGarbageAndProbes(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	port := freeTCPPort(t)
	data, done := startRTMPListener(t, ff, port)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	// Health checks: connect and leave.
	for i := 0; i < 3; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			_ = c.Close()
		}
	}
	// Garbage: the server closes the connection without answering.
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n" + strings.Repeat("x", 2000)))
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := c.Read(make([]byte, 1)); err == nil && n > 0 {
		t.Error("the server answered garbage")
	}
	_ = c.Close()
	select {
	case err := <-done:
		t.Fatalf("probes and garbage must not end the listener: %v", err)
	default:
	}
	// The encoder still gets in.
	if err := publish(ff, fmt.Sprintf("rtmp://%s/live/%s", addr, rtmpTestKey), 3); err != nil {
		t.Fatalf("after probes and garbage the encoder could not publish: %v", err)
	}
	<-done
	if len(data()) < 188*50 {
		t.Fatal("no stream after the probes")
	}
}

func TestRTMPListenerURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"rtmp://0.0.0.0:1935/live/" + rtmpTestKey + "?mode=listener": true,
		"rtmp://0.0.0.0:1935/live?mode=listener":                     false, // no key
		"rtmp://0.0.0.0:1935/?mode=listener":                         false,
		"rtmps://0.0.0.0:443/live/" + rtmpTestKey + "?mode=listener": false, // no TLS listener
		"rtmp://server.example/live/key":                             true,  // pulling stays as before
	} {
		a, err := ingest.ParseSourceURL(raw)
		if (err == nil) != ok {
			t.Errorf("%s: err=%v, want ok=%v", raw, err, ok)
			continue
		}
		if err == nil && strings.Contains(raw, "mode=listener") && (!a.Listener || a.StreamKeyLen != len(rtmpTestKey)) {
			t.Errorf("%s: listener=%v keylen=%d", raw, a.Listener, a.StreamKeyLen)
		}
	}
}
