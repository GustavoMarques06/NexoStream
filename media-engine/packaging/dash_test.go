package packaging

import (
	"strings"
	"testing"
)

func TestDASHSlave(t *testing.T) {
	s := DefaultHLS("/var/lib/nexostream/hls/c1").DASHTeeSlave()
	t.Log(s)
	if !strings.Contains(s, "f=dash") || !strings.HasSuffix(s, "/c1/dash/index.mpd") {
		t.Fatal(s)
	}
	mpd := `<SegmentTemplate initialization="init-$RepresentationID$.m4s" media="chunk-$RepresentationID$-$Number%05d$.m4s"/>`
	got := string(AddTokenMPD([]byte(mpd), "a b"))
	if !strings.Contains(got, `initialization="init-$RepresentationID$.m4s?token=a+b"`) || !strings.Contains(got, `media="chunk-$RepresentationID$-$Number%05d$.m4s?token=a+b"`) {
		t.Fatal(got)
	}
}
