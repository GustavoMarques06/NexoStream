package agent

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"nexostream/internal/bus"
	"nexostream/internal/logging"
	"nexostream/internal/model"
	"nexostream/internal/protocol"
	"nexostream/media-engine/pipeline/pipelinetest"
	"nexostream/media-engine/sessions"
)

// metricValue returns the value of a counter or gauge series, or the sample
// count of a histogram, matching every given label.
func metricValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	total := 0.0
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if !hasLabels(m, labels) {
				continue
			}
			switch {
			case m.GetCounter() != nil:
				total += m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				total += m.GetGauge().GetValue()
			case m.GetHistogram() != nil:
				total += float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	return total
}

func hasLabels(m *dto.Metric, want map[string]string) bool {
	for k, v := range want {
		found := false
		for _, l := range m.GetLabel() {
			if l.GetName() == k && l.GetValue() == v {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// The viewer path is measured by protocol: requests by status class, the time
// to answer, the admission time and the bytes sent.
func TestViewerPathMetrics(t *testing.T) {
	hls := t.TempDir()
	b := bus.NewMemory()
	defer b.Close()
	a, err := New(Config{WorkerID: "w1", HLSDir: hls, StateDir: t.TempDir(), DVRDir: t.TempDir(), FFprobePath: "-", Encoders: []string{"cpu"}},
		b, &pipelinetest.Engine{}, logging.Discard())
	if err != nil {
		t.Fatal(err)
	}
	ch := model.Channel{ID: "c1", Enabled: true, Mode: model.ModePassthrough, Revision: 1,
		Sources: []model.Source{{ID: "1", Priority: 1, URL: "udp://" + freeUDP(t)}},
		Outputs: model.Outputs{HLS: true}, Failover: model.DefaultFailover(),
		Auth: model.AuthConfig{Enabled: true, DenyIPs: []string{"10.66.0.0/16"}}}
	if err := a.StartChannel(protocol.ChannelSpec{Channel: ch}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.StopChannel("c1") }()
	dir := filepath.Join(hls, "c1")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "index.m3u8"), []byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.0,\nseg_000001.ts\n"), 0o644)
	segment := bytes.Repeat([]byte{0x47}, 188*100)
	_ = os.WriteFile(filepath.Join(dir, "seg_000001.ts"), segment, 0o644)

	srv := httptest.NewServer(a.MediaHandler())
	defer srv.Close()
	get := func(path string, hdr ...string) int {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	for i := 0; i < 3; i++ {
		if code := get("/hls/c1/seg_000001.ts"); code != 200 {
			t.Fatalf("segment %d", code)
		}
	}
	get("/hls/c1/index.m3u8")
	if code := get("/hls/c1/index.m3u8", "X-Real-IP", "10.66.1.1"); code != 403 { // denied IP via the local proxy
		t.Fatalf("denied IP: %d", code)
	}
	if code := get("/hls/nope/index.m3u8"); code != 404 {
		t.Fatalf("unknown channel: %d", code)
	}

	reg := a.metrics.reg
	if n := metricValue(t, reg, "nexostream_play_requests_total", map[string]string{"proto": "hls", "class": "2xx"}); n != 4 {
		t.Errorf("2xx requests = %v, want 4 (3 segments + 1 playlist)", n)
	}
	if n := metricValue(t, reg, "nexostream_play_requests_total", map[string]string{"proto": "hls", "class": "4xx"}); n != 1 {
		t.Errorf("4xx requests = %v, want 1 (the denied one)", n)
	}
	if n := metricValue(t, reg, "nexostream_play_sent_bytes_total", map[string]string{"proto": "hls"}); n < float64(3*len(segment)) {
		t.Errorf("sent bytes = %v, want at least the 3 segments (%d)", n, 3*len(segment))
	}
	if n := metricValue(t, reg, "nexostream_play_request_duration_seconds", map[string]string{"proto": "hls"}); n != 4 {
		t.Errorf("answer-time samples = %v, want 4 (denied requests are not answers)", n)
	}
	if n := metricValue(t, reg, "nexostream_play_admission_duration_seconds", map[string]string{"proto": "hls"}); n != 5 {
		t.Errorf("admission samples = %v, want 5 (every request that reached the rules)", n)
	}
	if n := metricValue(t, reg, "nexostream_play_inflight_requests", map[string]string{"proto": "hls"}); n != 0 {
		t.Errorf("in flight after all requests = %v, want 0", n)
	}
}

// fakeResponse records whether the zero-copy path was used.
type fakeResponse struct {
	http.ResponseWriter
	readFrom int
	body     bytes.Buffer
}

func (f *fakeResponse) ReadFrom(r io.Reader) (int64, error) {
	f.readFrom++
	return io.Copy(&f.body, r)
}

// The wrapper that counts viewer bytes must not hide ReadFrom: net/http
// serves files through it with the kernel's sendfile. Hiding it makes every
// segment of every viewer pass through the Go process.
func TestCountingWriterKeepsReadFrom(t *testing.T) {
	tr := &sessions.Tracker{WorkerID: "w"}
	h, err := tr.Begin(context.Background(), model.AuthConfig{}, sessions.Request{ChannelID: "c", Proto: "hls", IP: "1.1.1.1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Done()
	under := &fakeResponse{ResponseWriter: httptest.NewRecorder()}
	cw := &countingWriter{ResponseWriter: under, h: h}

	// net/http copies a file with io.CopyN, i.e. through a LimitedReader (which,
	// unlike strings.Reader, has no WriteTo to bypass the destination's ReadFrom).
	n, err := io.Copy(cw, io.LimitReader(strings.NewReader("0123456789"), 10))
	if err != nil || n != 10 {
		t.Fatalf("copy: %d %v", n, err)
	}
	if under.readFrom != 1 {
		t.Fatalf("the underlying ReadFrom was called %d times, want 1 (zero-copy path lost)", under.readFrom)
	}
	if cw.bytes != 10 || cw.status != http.StatusOK || under.body.String() != "0123456789" {
		t.Fatalf("counted %d bytes, status %d, body %q", cw.bytes, cw.status, under.body.String())
	}
	if got := tr.Count("c"); got != 1 {
		t.Fatalf("sessions %d", got)
	}
}

// Without an underlying ReadFrom (a plain writer), copying still works and counts.
func TestCountingWriterWithoutReadFrom(t *testing.T) {
	tr := &sessions.Tracker{WorkerID: "w"}
	h, _ := tr.Begin(context.Background(), model.AuthConfig{}, sessions.Request{ChannelID: "c", Proto: "hls", IP: "1.1.1.1"}, false)
	defer h.Done()
	rec := httptest.NewRecorder()
	cw := &countingWriter{ResponseWriter: rec, h: h}
	if n, err := cw.ReadFrom(strings.NewReader("abc")); err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	if rec.Body.String() != "abc" || cw.bytes != 3 {
		t.Fatalf("body %q bytes %d", rec.Body.String(), cw.bytes)
	}
}
