package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"nexostream/internal/bus"
	"nexostream/internal/logging"
	"nexostream/internal/model"
	"nexostream/internal/protocol"
	"nexostream/media-engine/pipeline/pipelinetest"
)

// The status a node publishes (read by the panel and by every read-only user)
// must not carry the passwords and keys of the sources.
func TestStatusHidesSourceSecrets(t *testing.T) {
	const pass, key = "hunter2-camera-pw", "stream-key-abcdef"
	b := bus.NewMemory()
	defer b.Close()
	a, err := New(Config{WorkerID: "w1", HLSDir: t.TempDir(), StateDir: t.TempDir(), DVRDir: t.TempDir(), FFprobePath: "-", FFmpegPath: "ffmpeg-not-installed-here",
		Encoders: []string{"cpu"}}, b, &pipelinetest.Engine{}, logging.Discard())
	if err != nil {
		t.Fatal(err)
	}
	ch := model.Channel{ID: "seg", Enabled: true, Mode: model.ModePassthrough, Revision: 1,
		Sources: []model.Source{
			{ID: "1", Priority: 1, URL: "rtsp://admin:" + pass + "@127.0.0.1:1/stream"},
			{ID: "2", Priority: 2, URL: "rtmp://127.0.0.1:1/live/" + key},
		},
		Outputs: model.Outputs{HLS: true}, Failover: model.DefaultFailover()}
	if err := a.StartChannel(protocol.ChannelSpec{Channel: ch}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.StopChannel("seg") }()

	var raw string
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, ok := a.Status("seg")
		if ok && len(st.Sources) == 2 {
			j, _ := json.Marshal(st)
			raw = string(j)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no status with both sources")
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, secret := range []string{pass, key} {
		if strings.Contains(raw, secret) {
			t.Errorf("the status leaks %q:\n%s", secret, raw)
		}
	}
	if !strings.Contains(raw, "rtsp://127.0.0.1:1/stream") || !strings.Contains(raw, "rtmp://127.0.0.1:1/live/••••") {
		t.Errorf("the addresses must stay recognizable:\n%s", raw)
	}
}
