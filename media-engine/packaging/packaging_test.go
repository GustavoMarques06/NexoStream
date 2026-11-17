package packaging

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTeeEscape(t *testing.T) {
	if got := TeeEscape(`C:\hls|x[1]:y`); got != `C\:\\hls\|x\[1\]\:y` {
		t.Fatalf("got %s", got)
	}
	s := DefaultHLS("/var/lib/nexostream/hls/c1").TeeSlave()
	if !strings.HasPrefix(s, "[f=hls:hls_time=6:hls_list_size=10:hls_flags=delete_segments+append_list") ||
		!strings.HasSuffix(s, "]/var/lib/nexostream/hls/c1/index.m3u8") {
		t.Fatalf("slave %s", s)
	}
	if strings.Contains(s, "discont_start") {
		t.Fatal("discont_start duplicates the discontinuity written by append_list")
	}
	// Windows absolute path: option value escaped twice, file name once.
	w := HLSOptions{Dir: "C:/hls/c1", SegmentSeconds: 6, ListSize: 10}.TeeSlave()
	if !strings.Contains(w, `hls_segment_filename=C\\\:/hls/c1/seg_%06d.ts]C\:/hls/c1/index.m3u8`) {
		t.Fatalf("windows slave %s", w)
	}
	if u := UDPTeeSlave("udp://239.10.0.1:5000?ttl=4"); u != `[f=mpegts:onfail=ignore]udp\://239.10.0.1\:5000?ttl=4&pkt_size=1316` {
		t.Fatalf("udp %s", u)
	}
}

func TestReadPlaylist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "index.m3u8")
	_ = os.WriteFile(p, []byte("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:120\n"+
		"#EXT-X-DISCONTINUITY\n#EXTINF:6.0,\n#EXT-X-PROGRAM-DATE-TIME:2026-11-16T10:29:14.969-0300\nseg_000120.ts\n"+
		"#EXTINF:5.5,\nseg_000121.ts\n"), 0o644)
	pi, err := ReadPlaylist(p)
	if err != nil || pi.MediaSequence != 120 || pi.Segments != 2 || pi.LastSequence != 121 || len(pi.Entries) != 2 {
		t.Fatalf("%+v %v", pi, err)
	}
	e := pi.Entries[0]
	if e.Seq != 120 || e.URI != "seg_000120.ts" || e.Duration != 6*time.Second ||
		!e.PDT.Equal(time.Date(2026, 11, 16, 13, 29, 14, 969_000_000, time.UTC)) {
		t.Fatalf("entry %+v", e)
	}
	if pi.Entries[1].Duration != 5500*time.Millisecond || !pi.Entries[1].PDT.IsZero() {
		t.Fatalf("entry %+v", pi.Entries[1])
	}
}

func TestTSRelay(t *testing.T) {
	r, err := NewTSRelay()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	srv := httptest.NewServer(r)
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "video/mp2t" {
		t.Fatal(resp.Header)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !r.WaitClients(ctx, 1) {
		t.Fatal("client not registered")
	}
	conn, _ := net.Dial("udp4", strings.TrimPrefix(r.Addr(), "udp://"))
	defer conn.Close()
	pkt := make([]byte, 1316)
	pkt[0] = 0x47
	go func() {
		for i := 0; i < 20; i++ {
			_, _ = conn.Write(pkt)
			time.Sleep(10 * time.Millisecond)
		}
	}()
	buf := make([]byte, 1316)
	if _, err := io.ReadFull(resp.Body, buf); err != nil || buf[0] != 0x47 {
		t.Fatalf("relay read: %v", err)
	}
	if r.Viewers() != 1 {
		t.Fatalf("viewers=%d", r.Viewers())
	}
}
