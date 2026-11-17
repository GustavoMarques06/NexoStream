package integration

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
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
	"nexostream/media-engine/drm"
	"nexostream/media-engine/packaging"
	"nexostream/media-engine/pipeline"
)

// TestFFmpegDRM checks a protected channel end to end: the packager writes
// clear HLS outside the published folder, the worker serves it encrypted
// (AES-128) with keys behind the playback token, and ffmpeg — as a player —
// plays it. Skipped when ffmpeg is not installed.
func TestFFmpegDRM(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil || testing.Short() {
		t.Skip("ffmpeg not found in PATH")
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

	tokenKey := bytes.Repeat([]byte("p"), 32)
	hlsDir := t.TempDir()
	b := bus.NewMemory()
	defer b.Close()
	a, err := agent.New(agent.Config{WorkerID: "e2e", HLSDir: hlsDir, StateDir: t.TempDir(), MediaAddr: "127.0.0.1:0", DVRDir: t.TempDir(),
		FFmpegPath: ff, FFprobePath: "-", HLSSegmentSeconds: 2, HLSListSize: 5,
		DRMKey: bytes.Repeat([]byte("k"), 32), PlaybackTokenKey: tokenKey},
		b, &pipeline.FFmpeg{Path: ff, Logger: logging.Discard()}, logging.Discard())
	if err != nil {
		t.Fatal(err)
	}
	actx, acancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = a.Run(actx) }()
	defer func() { acancel(); <-done }()
	time.Sleep(200 * time.Millisecond)

	// A stale clear folder (channel published before DRM) must disappear.
	stale := filepath.Join(hlsDir, "protegido")
	_ = os.MkdirAll(stale, 0o755)
	_ = os.WriteFile(filepath.Join(stale, "index.m3u8"), []byte("#EXTM3U\n"), 0o644)

	ch := model.Channel{ID: "protegido", Name: "protegido", Enabled: true, Mode: model.ModePassthrough, Revision: 1,
		Sources: []model.Source{{ID: "1", Priority: 1, URL: fmt.Sprintf("udp://127.0.0.1:%d", port)}},
		Outputs: model.Outputs{HLS: true}, Failover: model.DefaultFailover(),
		DRM: model.DRMConfig{Enabled: true, Method: drm.MethodAES128, KeyRotationMinutes: 1}}
	if err := a.StartChannel(protocol.ChannelSpec{Channel: ch}); err != nil {
		t.Fatal(err)
	}
	playlist := filepath.Join(hlsDir, ".drm", "protegido", packaging.PlaylistName)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if pi, err := packaging.ReadPlaylist(playlist); err == nil && pi.Segments >= 3 {
			break
		}
		if time.Now().After(deadline) {
			st, _ := a.Status("protegido")
			t.Fatalf("no HLS segments; status=%+v", st)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("the clear folder of a protected channel was left in the published HLS folder")
	}

	srv := http.Server{Handler: a.MediaHandler()}
	ln, err := netListen()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	base := "http://" + ln.Addr().String() + "/hls/protegido/"
	get := func(u string) (int, []byte) {
		t.Helper()
		resp, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}

	if code, _ := get(base + "index.m3u8"); code != 403 {
		t.Fatalf("playlist without token: %d", code)
	}
	if code, _ := get(base + "seg_000001.ts"); code != 404 {
		t.Fatalf("clear segment served: %d", code)
	}
	tok := drm.SignToken(tokenKey, "protegido", time.Now().Add(time.Hour))
	code, body := get(base + "index.m3u8?token=" + tok)
	if code != 200 || !strings.Contains(string(body), "#EXT-X-KEY:METHOD=AES-128") || !strings.Contains(string(body), "enc/seg_") {
		t.Fatalf("protected playlist %d:\n%s", code, body)
	}

	// Decrypt one segment by hand, as the HLS spec says.
	pi, _ := packaging.ParsePlaylist(bytes.NewReader(body))
	var keyURI string
	for _, line := range strings.Split(string(body), "\n") {
		if u, ok := strings.CutPrefix(line, `#EXT-X-KEY:METHOD=AES-128,URI="`); ok {
			keyURI = strings.TrimSuffix(u, `"`)
		}
		if line == pi.Entries[0].URI {
			break
		}
	}
	if code, _ := get(base + strings.Split(keyURI, "?")[0]); code != 403 {
		t.Fatalf("key without token: %d", code)
	}
	code, key := get(base + keyURI)
	if code != 200 || len(key) != 16 {
		t.Fatalf("key %s: %d", keyURI, code)
	}
	code, enc := get(base + pi.Entries[0].URI)
	if code != 200 || len(enc)%16 != 0 {
		t.Fatalf("segment %d len %d", code, len(enc))
	}
	block, _ := aes.NewCipher(key)
	var iv [16]byte
	binary.BigEndian.PutUint64(iv[8:], uint64(pi.Entries[0].Seq))
	cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(enc, enc)
	clear := enc[:len(enc)-int(enc[len(enc)-1])]
	if len(clear)%188 != 0 || clear[0] != 0x47 || clear[188] != 0x47 {
		t.Fatal("decrypted segment is not MPEG-TS")
	}

	// And ffmpeg, as a player, fetches the key and decodes the video.
	pctx, pcancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer pcancel()
	out, err := exec.CommandContext(pctx, ff, "-hide_banner", "-loglevel", "error", "-live_start_index", "0",
		"-i", base+"index.m3u8?token="+tok, "-map", "0:v:0", "-frames:v", strconv.Itoa(75), "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg could not play the protected channel: %v\n%s", err, out)
	}
}
