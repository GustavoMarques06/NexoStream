package dvr

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nexostream/media-engine/drm"
	"nexostream/media-engine/packaging"
)

// recordQuality writes n 6 s pieces under key; every byte pair is
// {0x47, marker} so a piece tells which quality it came from.
func recordQuality(t *testing.T, st Storage, key string, start time.Time, n int, marker byte) {
	t.Helper()
	at := start
	for i := 0; i < n; i++ {
		p := filepath.Join(t.TempDir(), "seg.ts")
		if err := os.WriteFile(p, bytes.Repeat([]byte{0x47, marker}, 94), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Record(key, p, at, 6*time.Second); err != nil {
			t.Fatal(err)
		}
		at = at.Add(6 * time.Second)
	}
}

var testVariants = []packaging.Variant{
	{Width: 1920, Height: 1080, BitrateBps: 5_000_000, AvgBitrateBps: 4_500_000, Profile: "high"},
	{Width: 1280, Height: 720, BitrateBps: 2_800_000, AvgBitrateBps: 2_500_000, Profile: "main"},
	{Width: 640, Height: 360, BitrateBps: 900_000, AvgBitrateBps: 800_000, Profile: "main"},
}

func httpGet(t *testing.T, url string) (int, string, *http.Response) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp
}

func TestABRRecordingAndPlayback(t *testing.T) {
	root := t.TempDir()
	st := Single(root)
	now := time.Date(2026, 11, 16, 14, 30, 0, 0, time.UTC)
	start := now.Add(-10 * time.Minute)
	for i := range testVariants {
		recordQuality(t, st, VariantKey("c1", i), start, 20, byte(0xA0+i))
	}
	if err := st.WriteVariants("c1", testVariants); err != nil {
		t.Fatal(err)
	}
	// Quality 0 keeps the original layout; the others live in v<N>/.
	for _, p := range []string{"c1/2026/11/16/14.ts", "c1/v1/2026/11/16/14.ts", "c1/v2/2026/11/16/14.idx"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); err != nil {
			t.Fatalf("missing %s", p)
		}
	}
	if got := st.VariantKeys("c1"); strings.Join(got, ",") != "c1,c1/v1,c1/v2" {
		t.Fatalf("variant keys %v", got)
	}

	srv := httptest.NewServer(Handler{Storage: func(ch string) (Storage, bool) { return st, ch == "c1" }, Now: func() time.Time { return now }})
	defer srv.Close()
	from := strconv.FormatInt(start.Unix(), 10)

	// Master: every quality, in order, pointing at v<N>/ with the same playlist name.
	code, master, _ := httpGet(t, srv.URL+"/dvr/c1/index-"+from+"-60.m3u8")
	if code != 200 || strings.Count(master, "#EXT-X-STREAM-INF") != 3 ||
		!strings.Contains(master, "RESOLUTION=1920x1080") || !strings.Contains(master, "RESOLUTION=640x360") ||
		!strings.Contains(master, "\nv0/index-"+from+"-60.m3u8\n") || !strings.Contains(master, "\nv2/index-"+from+"-60.m3u8\n") {
		t.Fatalf("master %d:\n%s", code, master)
	}
	// The query (from/to) is carried to each quality.
	to := strconv.FormatInt(start.Add(30*time.Second).Unix(), 10)
	_, master, _ = httpGet(t, srv.URL+"/dvr/c1/index.m3u8?from="+from+"&to="+to)
	if !strings.Contains(master, "\nv1/index.m3u8?from="+from+"&to="+to+"\n") {
		t.Fatalf("query not carried:\n%s", master)
	}

	// Each quality has its own playlist and serves its own bytes.
	for i := range testVariants {
		prefix := "/dvr/c1/v" + strconv.Itoa(i)
		code, pl, _ := httpGet(t, srv.URL+prefix+"/index-"+from+"-60.m3u8")
		if code != 200 || strings.Count(pl, "#EXTINF") != 10 || !strings.Contains(pl, "\n2026/11/16/14.ts\n") {
			t.Fatalf("v%d playlist %d:\n%s", i, code, pl)
		}
		code, hour, _ := httpGet(t, srv.URL+prefix+"/2026/11/16/14.ts")
		if code != 200 || len(hour) != 20*188 || hour[1] != byte(0xA0+i) {
			t.Fatalf("v%d hour file %d len=%d marker=%x", i, code, len(hour), hour[1])
		}
	}
	// A quality that was never recorded does not exist; nor does path escaping.
	for _, bad := range []string{"/dvr/c1/v7/2026/11/16/14.ts", "/dvr/c1/v1/../2026/11/16/14.ts", "/dvr/c1/v1/v2/2026/11/16/14.ts", "/dvr/c1/v100/index.m3u8"} {
		if code, _, _ := httpGet(t, srv.URL+bad); code == 200 {
			t.Errorf("%s served", bad)
		}
	}
	// recordings.json reports the number of qualities.
	if _, body, _ := httpGet(t, srv.URL+"/dvr/c1/recordings.json"); !strings.Contains(body, `"qualities":3`) {
		t.Fatalf("recordings.json: %s", body)
	}
}

// A quality missing in the requested interval is left out of the master; when
// only one is left the player is sent to it.
func TestABRMasterOnlyListsRecordedQualities(t *testing.T) {
	root := t.TempDir()
	st := Single(root)
	now := time.Date(2026, 11, 16, 14, 30, 0, 0, time.UTC)
	early := now.Add(-20 * time.Minute) // 10 pieces = 1 min
	late := now.Add(-10 * time.Minute)
	recordQuality(t, st, "c1/v1", early, 10, 0xB1) // ABR started with only a lower quality recorded
	recordQuality(t, st, "c1", late, 10, 0xB0)
	recordQuality(t, st, "c1/v1", late, 10, 0xB1)
	if err := st.WriteVariants("c1", testVariants[:2]); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler{Storage: func(string) (Storage, bool) { return st, true }, Now: func() time.Time { return now }})
	defer srv.Close()

	// Where both exist: master with two entries.
	code, master, _ := httpGet(t, srv.URL+"/dvr/c1/index-"+strconv.FormatInt(late.Unix(), 10)+"-60.m3u8")
	if code != 200 || strings.Count(master, "#EXT-X-STREAM-INF") != 2 {
		t.Fatalf("both qualities: %d\n%s", code, master)
	}
	// Where only quality 1 exists: redirected to it, and the playlist is its own.
	code, pl, resp := httpGet(t, srv.URL+"/dvr/c1/index-"+strconv.FormatInt(early.Unix(), 10)+"-60.m3u8")
	if code != 200 || !strings.Contains(resp.Request.URL.Path, "/c1/v1/") || strings.Count(pl, "#EXTINF") != 10 {
		t.Fatalf("single quality: %d path=%s\n%s", code, resp.Request.URL.Path, pl)
	}
	// Nothing at all: 404.
	if code, _, _ := httpGet(t, srv.URL+"/dvr/c1/index-"+strconv.FormatInt(now.Add(-48*time.Hour).Unix(), 10)+"-60.m3u8"); code != 404 {
		t.Fatalf("empty interval: %d", code)
	}
}

func TestVariantsFile(t *testing.T) {
	st := Single(t.TempDir())
	if st.ReadVariants("c1") != nil {
		t.Fatal("variants of an unknown channel")
	}
	if err := st.WriteVariants("c1", testVariants); err != nil {
		t.Fatal(err)
	}
	if got := st.ReadVariants("c1"); len(got) != 3 || got[1].Height != 720 || got[2].Profile != "main" {
		t.Fatalf("read back %+v", got)
	}
	// Going back to a single quality removes the file, so the master disappears.
	if err := st.WriteVariants("c1", testVariants[:1]); err != nil {
		t.Fatal(err)
	}
	if st.ReadVariants("c1") != nil {
		t.Fatal("variants file still there after going single")
	}
	if err := st.WriteVariants("never-existed", nil); err != nil {
		t.Fatalf("removing a missing file must not fail: %v", err)
	}
}

func TestABRCleanupAndVerifyCoverEveryQuality(t *testing.T) {
	root := t.TempDir()
	st := Single(root)
	old := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 11, 16, 14, 30, 0, 0, time.UTC)
	for _, k := range []string{"c1", "c1/v1", "c1/v2"} {
		recordQuality(t, st, k, old, 5, 1)
		recordQuality(t, st, k, recent, 5, 1)
	}
	// Corrupt a closed hour of quality 2: bytes without index.
	bad := filepath.Join(root, "c1", "v2", "2026", "11", "16", "10.ts")
	f, err := os.OpenFile(bad, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("junk"))
	f.Close()
	probs := st.Verify("c1", recent.Add(-time.Hour), now, now)
	if len(probs) != 1 || probs[0].File != bad {
		t.Fatalf("verify did not find the corrupt hour of quality 2: %+v", probs)
	}

	removed, err := st.Cleanup("c1", now, 48*time.Hour)
	if err != nil || len(removed) != 3 {
		t.Fatalf("cleanup removed %d hours (want 3, one per quality): %v", len(removed), err)
	}
	for _, p := range []string{"c1/2026/11/10", "c1/v1/2026/11/10", "c1/v2/2026/11/10"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); err == nil {
			t.Errorf("%s survived retention", p)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "c1", "v1", "2026", "11", "16", "10.ts")); err != nil {
		t.Fatalf("recent hour of quality 1 was removed: %v", err)
	}
}

// With DRM, the master needs the token, and each quality's playlist, keys and
// encrypted pieces resolve under its own v<N>/ folder.
func TestABRProtectedPlayback(t *testing.T) {
	root := t.TempDir()
	st := Single(root)
	now := time.Date(2026, 11, 16, 14, 30, 0, 0, time.UTC)
	start := now.Add(-10 * time.Minute)
	for i := 0; i < 2; i++ {
		recordQuality(t, st, VariantKey("c1", i), start, 20, byte(0xC0+i))
	}
	_ = st.WriteVariants("c1", testVariants[:2])
	tokenKey := bytes.Repeat([]byte("t"), 32)
	prot := drm.Protector{ChannelID: "c1", Keys: drm.LocalKeys{Secret: bytes.Repeat([]byte("k"), 32)}, TokenKey: tokenKey, Rotation: time.Minute}
	srv := httptest.NewServer(Handler{
		Storage: func(ch string) (Storage, bool) { return st, ch == "c1" },
		Protect: func(string) (drm.Protector, bool) { return prot, true },
		Now:     func() time.Time { return now },
	})
	defer srv.Close()
	tok := drm.SignToken(tokenKey, "c1", now.Add(time.Hour))
	list := "index-" + strconv.FormatInt(start.Unix(), 10) + "-120.m3u8"

	if code, _, _ := httpGet(t, srv.URL+"/dvr/c1/"+list); code != 403 {
		t.Fatalf("master without token: %d", code)
	}
	if code, _, _ := httpGet(t, srv.URL+"/dvr/c1/v1/"+list); code != 403 {
		t.Fatalf("quality playlist without token: %d", code)
	}
	if code, _, _ := httpGet(t, srv.URL+"/dvr/c1/v1/2026/11/16/14.ts"); code != 403 {
		t.Fatalf("clear hour file of a protected quality: %d", code)
	}
	code, master, _ := httpGet(t, srv.URL+"/dvr/c1/"+list+"?token="+tok)
	if code != 200 || !strings.Contains(master, "v1/"+list+"?token=") {
		t.Fatalf("master %d:\n%s", code, master)
	}

	// Play quality 1 like a player would.
	code, body, _ := httpGet(t, srv.URL+"/dvr/c1/v1/"+list+"?token="+tok)
	if code != 200 || strings.Count(body, "#EXT-X-KEY") != 2 {
		t.Fatalf("quality playlist %d:\n%s", code, body)
	}
	var keyURI string
	seq := 0
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if u, ok := strings.CutPrefix(line, `#EXT-X-KEY:METHOD=AES-128,URI="`); ok {
			keyURI = strings.TrimSuffix(u, `"`)
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		code, key, _ := httpGet(t, srv.URL+"/dvr/c1/v1/"+keyURI)
		if code != 200 || len(key) != 16 {
			t.Fatalf("key %s: %d", keyURI, code)
		}
		code, enc, _ := httpGet(t, srv.URL+"/dvr/c1/v1/"+line)
		if code != 200 {
			t.Fatalf("piece %s: %d", line, code)
		}
		block, _ := aes.NewCipher([]byte(key))
		var iv [16]byte
		binary.BigEndian.PutUint64(iv[8:], uint64(seq))
		b := []byte(enc)
		cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(b, b)
		clear := b[:len(b)-int(b[len(b)-1])]
		if len(clear) != 188 || clear[1] != 0xC1 {
			t.Fatalf("piece %d did not decrypt to quality 1 bytes (marker %x)", seq, clear[1])
		}
		seq++
	}
	if seq != 20 {
		t.Fatalf("played %d pieces", seq)
	}
}
