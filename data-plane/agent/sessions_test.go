package agent

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nexostream/internal/bus"
	"nexostream/internal/logging"
	"nexostream/internal/model"
	"nexostream/internal/protocol"
	"nexostream/media-engine/drm"
	"nexostream/media-engine/pipeline/pipelinetest"
)

func freeUDP(t *testing.T) string {
	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.LocalAddr().String()
}

// TestPlaySessions checks the access rules and play sessions on the HLS
// served by the node: backend, token carried to the segments, listing and
// closing a session.
func TestPlaySessions(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "assinante-1" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("X-UserId", "u1")
	}))
	defer backend.Close()

	tokenKey := bytes.Repeat([]byte("p"), 32)
	hls := t.TempDir()
	b := bus.NewMemory()
	defer b.Close()
	a, err := New(Config{WorkerID: "w1", HLSDir: hls, StateDir: t.TempDir(), DVRDir: t.TempDir(), FFprobePath: "-", Encoders: []string{"cpu"},
		PlaybackTokenKey: tokenKey}, b, &pipelinetest.Engine{}, logging.Discard())
	if err != nil {
		t.Fatal(err)
	}
	ch := model.Channel{ID: "c1", Enabled: true, Mode: model.ModePassthrough, Revision: 1,
		Sources: []model.Source{{ID: "1", Priority: 1, URL: "udp://" + freeUDP(t)}},
		Outputs: model.Outputs{HLS: true}, Failover: model.DefaultFailover(),
		Auth: model.AuthConfig{Enabled: true, BackendURL: backend.URL, DenyIPs: []string{"10.66.0.0/16"}}}
	if err := a.StartChannel(protocol.ChannelSpec{Channel: ch}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.StopChannel("c1") }()
	dir := filepath.Join(hls, "c1")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "index.m3u8"), []byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:1\n#EXTINF:6.0,\nseg_000001.ts\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "seg_000001.ts"), bytes.Repeat([]byte{0x47}, 188*10), 0o644)

	srv := httptest.NewServer(a.MediaHandler())
	defer srv.Close()
	get := func(path string, hdr ...string) (int, string) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("User-Agent", "STB/1.0")
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	if code, _ := get("/hls/c1/index.m3u8?token=outro"); code != 403 {
		t.Fatalf("backend denial: %d", code)
	}
	code, body := get("/hls/c1/index.m3u8?token=assinante-1")
	if code != 200 || !strings.Contains(body, "seg_000001.ts?token=assinante-1") {
		t.Fatalf("playlist %d:\n%s", code, body)
	}
	if code, _ := get("/hls/c1/seg_000001.ts?token=assinante-1"); code != 200 {
		t.Fatalf("segment %d", code)
	}
	// The cluster playback token (panel previews, middleware) skips the backend.
	signed := drm.SignToken(tokenKey, "c1", time.Now().Add(time.Hour))
	if code, _ := get("/hls/c1/index.m3u8?token=" + signed); code != 200 {
		t.Fatalf("signed token %d", code)
	}
	// Behind the local nginx the viewer IP comes from X-Real-IP.
	if code, _ := get("/hls/c1/index.m3u8?token=assinante-1", "X-Real-IP", "10.66.1.1"); code != 403 {
		t.Fatalf("denied IP via nginx: %d", code)
	}

	var rep protocol.Reply
	if err := a.execute(protocol.Command{Type: protocol.CmdSessions, ChannelID: "c1"}, &rep); err != nil {
		t.Fatal(err)
	}
	var sub *model.PlaySession
	for i, s := range rep.Sessions.Open {
		if s.UserID == "u1" {
			sub = &rep.Sessions.Open[i]
		}
	}
	if len(rep.Sessions.Open) != 2 || sub == nil || sub.Requests != 2 || sub.Bytes < 188*10 || sub.Proto != "hls" || sub.WorkerID != "w1" {
		t.Fatalf("sessions %+v", rep.Sessions.Open)
	}
	if st, _ := a.Status("c1"); st.Viewers != 2 {
		// status refreshes every second
		time.Sleep(1500 * time.Millisecond)
		if st, _ = a.Status("c1"); st.Viewers != 2 {
			t.Fatalf("viewers %d", st.Viewers)
		}
	}
	if err := a.execute(protocol.Command{Type: protocol.CmdKillSession, SessionID: sub.ID}, &rep); err != nil {
		t.Fatal(err)
	}
	if code, _ := get("/hls/c1/index.m3u8?token=assinante-1"); code != 403 {
		t.Fatalf("a closed session is refused for a while: %d", code)
	}
	_ = a.execute(protocol.Command{Type: protocol.CmdSessions, ChannelID: "c1"}, &rep)
	if len(rep.Sessions.Closed) != 1 || rep.Sessions.Closed[0].Reason != "killed" {
		t.Fatalf("closed %+v", rep.Sessions.Closed)
	}
	if err := a.execute(protocol.Command{Type: protocol.CmdKillSession, SessionID: "nao-existe"}, &rep); err == nil {
		t.Fatal("unknown session")
	}
	if code, _ := get("/hls/outro/index.m3u8"); code != 404 {
		t.Fatalf("channel not on this node: %d", code)
	}
}
