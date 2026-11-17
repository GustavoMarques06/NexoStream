package dvr

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/json"
	"fmt"
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
)

// piece writes a fake live segment whose bytes identify it.
func piece(t *testing.T, i int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), fmt.Sprintf("seg_%d.ts", i))
	body := bytes.Repeat([]byte{0x47, byte(i)}, 94) // 188 bytes, distinct per piece
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// record writes n 6 s pieces starting at start, with a 30 s hole after
// piece gapAfter (-1 = none), through storage st.
func record(t *testing.T, st Storage, ch string, start time.Time, n, gapAfter int) {
	t.Helper()
	at := start
	for i := 0; i < n; i++ {
		if _, err := st.Record(ch, piece(t, i), at, 6*time.Second); err != nil {
			t.Fatal(err)
		}
		at = at.Add(6 * time.Second)
		if i == gapAfter {
			at = at.Add(30 * time.Second)
		}
	}
}

func TestHourFiles(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 11, 16, 13, 59, 30, 0, time.UTC) // crosses an hour boundary
	record(t, Single(root), "c1", start, 10, 4)

	// One .ts + .idx per hour, nothing else.
	h13 := filepath.Join(root, "c1", "2026", "11", "16", "13.ts")
	h14 := filepath.Join(root, "c1", "2026", "11", "16", "14.ts")
	for _, p := range []string{h13, h14, strings.TrimSuffix(h13, ".ts") + ".idx"} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing %s", p)
		}
	}
	files, _ := filepath.Glob(filepath.Join(root, "c1", "2026", "11", "16", "*"))
	if len(files) != 4 {
		t.Fatalf("files %v", files)
	}
	if fi, _ := os.Stat(h13); fi.Size() != 5*188 { // pieces 0-4 start in hour 13
		t.Fatalf("hour 13 size %d", fi.Size())
	}

	// Recording the same start again is a no-op.
	if _, err := Record(root, "c1", piece(t, 99), start, 6*time.Second); err != nil {
		t.Fatal(err)
	}
	segs, _ := List(root, "c1", start.Add(-time.Hour), start.Add(time.Hour))
	if len(segs) != 10 || segs[0].Rel != "2026/11/16/13.ts" || segs[5].Rel != "2026/11/16/14.ts" {
		t.Fatalf("segs=%d %+v", len(segs), segs)
	}
	for i, s := range segs[:5] {
		if s.Offset != int64(i*188) || s.Size != 188 {
			t.Fatalf("piece %d offset=%d size=%d", i, s.Offset, s.Size)
		}
	}
	if r := Ranges(segs); len(r) != 2 || !r[0].To.Equal(start.Add(30*time.Second)) {
		t.Fatalf("ranges=%+v", r)
	}
	// [15 s, 25 s) overlaps the pieces 12-18, 18-24 and 24-30.
	if part, _ := List(root, "c1", start.Add(15*time.Second), start.Add(25*time.Second)); len(part) != 3 {
		t.Fatalf("partial=%d", len(part))
	}
}

func TestCrashRecovery(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)
	record(t, Single(root), "c1", start, 3, -1)
	ts := filepath.Join(root, "c1", "2026", "11", "16", "14.ts")
	idx := strings.TrimSuffix(ts, ".ts") + ".idx"
	// Crash: half a piece written without index, plus a torn index line.
	f, _ := os.OpenFile(ts, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.Write(bytes.Repeat([]byte{0xAA}, 100))
	f.Close()
	g, _ := os.OpenFile(idx, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = g.WriteString("1794844818000 6000 999999 188\n")
	g.Close()
	defaultState.forget(ts) // the process restarted

	if _, err := Record(root, "c1", piece(t, 3), start.Add(18*time.Second), 6*time.Second); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(ts)
	if fi.Size() != 4*188 {
		t.Fatalf("hour file size %d, want %d (partial bytes must be cut)", fi.Size(), 4*188)
	}
	segs, _ := List(root, "c1", start, start.Add(time.Hour))
	if len(segs) != 4 || segs[3].Offset != 3*188 {
		t.Fatalf("after recovery %+v", segs)
	}
}

func TestPlaylist(t *testing.T) {
	start := time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)
	segs := []Segment{
		{Start: start, Duration: 6 * time.Second, Rel: "2026/11/16/14.ts", Offset: 0, Size: 188},
		{Start: start.Add(6 * time.Second), Duration: 6500 * time.Millisecond, Rel: "2026/11/16/14.ts", Offset: 188, Size: 376},
		{Start: start.Add(60 * time.Second), Duration: 6 * time.Second, Rel: "2026/11/16/14.ts", Offset: 564, Size: 188},
	}
	uri := func(s Segment) string { return s.Rel }
	vod := Playlist(segs, false, uri)
	for _, w := range []string{"#EXT-X-VERSION:4", "#EXT-X-PLAYLIST-TYPE:VOD", "#EXT-X-TARGETDURATION:7", "#EXT-X-ENDLIST",
		"#EXT-X-PROGRAM-DATE-TIME:2026-11-16T14:00:00.000Z", "#EXTINF:6.500,\n#EXT-X-BYTERANGE:376@188\n2026/11/16/14.ts"} {
		if !strings.Contains(vod, w) {
			t.Errorf("vod missing %q:\n%s", w, vod)
		}
	}
	if strings.Count(vod, "#EXT-X-DISCONTINUITY") != 1 {
		t.Errorf("expected one discontinuity:\n%s", vod)
	}
	live := Playlist(segs, true, uri)
	if !strings.Contains(live, "#EXT-X-PLAYLIST-TYPE:EVENT") || strings.Contains(live, "ENDLIST") {
		t.Errorf("timeshift playlist:\n%s", live)
	}
}

func TestHandler(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 16, 14, 30, 0, 0, time.UTC)
	start := now.Add(-10 * time.Minute)
	record(t, Single(root), "c1", start, 20, -1)
	h := Handler{Storage: func(ch string) (Storage, bool) { return Single(root), ch == "c1" }, Now: func() time.Time { return now }}
	srv := httptest.NewServer(h)
	defer srv.Close()
	get := func(path string, hdr ...string) (int, string) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	from := strconv.FormatInt(start.Unix(), 10)

	code, body := get("/dvr/c1/index.m3u8?from=" + from + "&to=" + strconv.FormatInt(start.Add(30*time.Second).Unix(), 10))
	if code != 200 || strings.Count(body, "#EXTINF") != 5 || strings.Count(body, "#EXT-X-BYTERANGE") != 5 || !strings.Contains(body, "ENDLIST") {
		t.Fatalf("catch-up %d:\n%s", code, body)
	}
	if code, body := get("/dvr/c1/index-" + from + "-60.m3u8"); code != 200 || strings.Count(body, "#EXTINF") != 10 {
		t.Fatalf("flussonic catch-up %d:\n%s", code, body)
	}
	if code, body := get("/dvr/c1/index-" + from + "-now.m3u8"); code != 200 || !strings.Contains(body, "EVENT") || strings.Contains(body, "ENDLIST") {
		t.Fatalf("flussonic timeshift %d:\n%s", code, body)
	}
	code, body = get("/dvr/c1/recordings.json")
	var rec struct {
		Ranges   []Range `json:"ranges"`
		Segments int     `json:"segments"`
	}
	_ = json.Unmarshal([]byte(body), &rec)
	if code != 200 || rec.Segments != 20 || len(rec.Ranges) != 1 {
		t.Fatalf("recordings %d %s", code, body)
	}

	// The whole hour file, and a byte range of one piece (piece 3).
	code, whole := get("/dvr/c1/2026/11/16/14.ts")
	if code != 200 || len(whole) != 20*188 {
		t.Fatalf("hour file %d len=%d", code, len(whole))
	}
	code, part := get("/dvr/c1/2026/11/16/14.ts", "Range", fmt.Sprintf("bytes=%d-%d", 3*188, 4*188-1))
	if code != 206 || len(part) != 188 || part[1] != 3 {
		t.Fatalf("byte range %d len=%d", code, len(part))
	}
	for _, bad := range []string{"/dvr/c1/../../etc/passwd", "/dvr/c1/2026/11/16/../14.ts", "/dvr/other/index.m3u8",
		"/dvr/C1/index.m3u8", "/dvr/c1/2026/11/16/14.idx", "/dvr/c1/d5/2026/11/16/14.ts"} {
		if code, _ := get(bad); code == 200 {
			t.Errorf("%s served", bad)
		}
	}
	if code, _ := get("/dvr/c1/index.m3u8?from=" + strconv.FormatInt(now.Add(-48*time.Hour).Unix(), 10) + "&to=" + strconv.FormatInt(now.Add(-47*time.Hour).Unix(), 10)); code != 404 {
		t.Fatalf("empty interval: %d", code)
	}
}

func TestCleanup(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 16, 14, 30, 0, 0, time.UTC)
	for _, h := range []time.Time{now.Add(-75 * time.Hour), now.Add(-73 * time.Hour), now.Add(-71 * time.Hour), now} {
		record(t, Single(root), "c1", h, 1, -1)
	}
	removed, err := Cleanup(root, "c1", now, 72*time.Hour)
	if err != nil || len(removed) != 2 {
		t.Fatalf("removed %v %v", removed, err)
	}
	left, _ := ListHours(root, "c1")
	if len(left) != 2 {
		t.Fatalf("left %v", left)
	}
	if idx, _ := filepath.Glob(filepath.Join(root, "c1", "*", "*", "*", "*.idx")); len(idx) != 2 {
		t.Fatalf("index files left %v", idx)
	}
}

func TestRAID(t *testing.T) {
	start := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

	// RAID-0: each hour entirely on one disk; hours spread over the disks.
	d1, d2 := t.TempDir(), t.TempDir()
	r0 := Storage{Mode: ModeRAID0, Disks: []string{d1, d2}}
	for h := 0; h < 12; h++ {
		record(t, r0, "c1", start.Add(time.Duration(h)*time.Hour), 3, -1)
	}
	a, _ := ListHours(d1, "c1")
	b, _ := ListHours(d2, "c1")
	if len(a) == 0 || len(b) == 0 || len(a)+len(b) != 12 {
		t.Fatalf("raid0 hours per disk: %d + %d", len(a), len(b))
	}
	all, _ := r0.List("c1", start, start.Add(12*time.Hour))
	if len(all) != 36 {
		t.Fatalf("raid0 list=%d", len(all))
	}
	for _, s := range all {
		if _, ok := r0.Locate("c1", r0.URI(s)); !ok {
			t.Fatalf("raid0 locate %s", r0.URI(s))
		}
	}

	// RAID-1: identical hour files on both disks.
	m1, m2 := t.TempDir(), t.TempDir()
	r1 := Storage{Mode: ModeRAID1, Disks: []string{m1, m2}}
	record(t, r1, "c1", start, 4, -1)
	f1, _ := os.ReadFile(filepath.Join(m1, "c1", "2026", "11", "16", "00.ts"))
	f2, _ := os.ReadFile(filepath.Join(m2, "c1", "2026", "11", "16", "00.ts"))
	if len(f1) != 4*188 || !bytes.Equal(f1, f2) {
		t.Fatalf("raid1 mirror: %d vs %d bytes", len(f1), len(f2))
	}
	if merged, _ := r1.List("c1", start, start.Add(time.Hour)); len(merged) != 4 || !strings.HasPrefix(r1.URI(merged[0]), "d0/") {
		t.Fatalf("raid1 merged %+v", merged)
	}

	// Degraded: one mirror disk fails, recording continues on the other.
	bad := filepath.Join(t.TempDir(), "not-a-dir")
	_ = os.WriteFile(bad, []byte("x"), 0o644)
	deg := Storage{Mode: ModeRAID1, Disks: []string{bad, m2}}
	if _, err := deg.Record("c1", piece(t, 9), start.Add(time.Minute), 6*time.Second); err != nil {
		t.Fatalf("degraded raid1 must still record: %v", err)
	}
	if !DiskDown(bad) {
		t.Fatal("failed disk not marked down")
	}
	if err := (Storage{Mode: ModeRAID0, Disks: []string{d1}}).Validate(); err == nil {
		t.Fatal("raid0 with one disk must be invalid")
	}
	if err := (Storage{Mode: ModeRAID1, Disks: []string{d1, d1}}).Validate(); err == nil {
		t.Fatal("duplicate disks must be invalid")
	}
}

func TestStorageEstimate(t *testing.T) {
	// Spec 10.5: 1,000 channels at 4 Mbps for 1 day = 43.2 TB.
	if got := StorageEstimate(1000, 4, 1); got != 43200 {
		t.Fatalf("got %v GB", got)
	}
}

func TestProtectedHandler(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 16, 14, 30, 0, 0, time.UTC)
	start := now.Add(-10 * time.Minute)
	record(t, Single(root), "c1", start, 20, -1) // 2 min: 2 key periods of 1 min
	tokenKey := bytes.Repeat([]byte("t"), 32)
	prot := drm.Protector{ChannelID: "c1", Keys: drm.LocalKeys{Secret: bytes.Repeat([]byte("k"), 32)}, TokenKey: tokenKey, Rotation: time.Minute}
	h := Handler{
		Storage: func(ch string) (Storage, bool) { return Single(root), ch == "c1" },
		Protect: func(string) (drm.Protector, bool) { return prot, true },
		Now:     func() time.Time { return now },
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	get := func(path string) (int, []byte) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	tok := drm.SignToken(tokenKey, "c1", now.Add(time.Hour))
	list := "/dvr/c1/index-" + strconv.FormatInt(start.Unix(), 10) + "-120.m3u8"
	if code, _ := get(list); code != 403 {
		t.Fatalf("playlist without token: %d", code)
	}
	code, body := get(list + "?token=" + tok)
	if code != 200 || strings.Contains(string(body), "BYTERANGE") || strings.Count(string(body), "#EXT-X-KEY") != 2 {
		t.Fatalf("protected playlist %d:\n%s", code, body)
	}
	// Play it like a player: IV = media sequence (0, 1, ...), key from the last key line.
	var keyURI string
	seq := 0
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if u, ok := strings.CutPrefix(line, `#EXT-X-KEY:METHOD=AES-128,URI="`); ok {
			keyURI = strings.TrimSuffix(u, `"`)
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		code, key := get("/dvr/c1/" + keyURI)
		if code != 200 || len(key) != 16 {
			t.Fatalf("key %s: %d", keyURI, code)
		}
		code, enc := get("/dvr/c1/" + line)
		if code != 200 {
			t.Fatalf("piece %s: %d", line, code)
		}
		block, _ := aes.NewCipher(key)
		var iv [16]byte
		binary.BigEndian.PutUint64(iv[8:], uint64(seq))
		cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(enc, enc)
		clear := enc[:len(enc)-int(enc[len(enc)-1])]
		if len(clear) != 188 || clear[1] != byte(seq) {
			t.Fatalf("piece %d does not decrypt to the recorded bytes", seq)
		}
		seq++
	}
	if seq != 20 {
		t.Fatalf("played %d pieces", seq)
	}
	if code, _ := get("/dvr/c1/2026/11/16/14.ts"); code != 403 {
		t.Fatalf("clear hour file of a protected channel: %d", code)
	}
	if code, _ := get("/dvr/c1/" + strings.Split(keyURI, "?")[0]); code != 403 {
		t.Fatalf("key without token: %d", code)
	}
	for _, bad := range []string{"enc/2026/11/16/14/1.ts?s=0", "enc/2026/11/16/14/0.ts", "enc/../x/0.ts?s=0"} {
		if code, _ := get("/dvr/c1/" + bad); code == 200 {
			t.Errorf("%s served", bad)
		}
	}
	h.Protect = func(string) (drm.Protector, bool) { return drm.Protector{ChannelID: "c1"}, true }
	srv2 := httptest.NewServer(h)
	defer srv2.Close()
	resp, err := http.Get(srv2.URL + "/dvr/c1/2026/11/16/14.ts")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("node without DRM secrets must refuse: %d", resp.StatusCode)
	}
}

func TestVerify(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	now := start.Add(2 * time.Hour)
	record(t, Single(root), "c1", start, 5, -1)
	if p := Single(root).Verify("c1", start.Add(-time.Hour), now, now); len(p) != 0 {
		t.Fatalf("a clean hour has problems: %+v", p)
	}
	ts := filepath.Join(root, "c1", "2026", "11", "16", "10.ts")
	// Garbage appended after the last indexed piece, and a broken sync byte.
	f, _ := os.OpenFile(ts, os.O_RDWR, 0o644)
	_, _ = f.WriteAt([]byte{0x00}, 188)
	fi, _ := f.Stat()
	_, _ = f.WriteAt([]byte("lixo"), fi.Size())
	f.Close()
	p := Single(root).Verify("c1", start.Add(-time.Hour), now, now)
	joined := fmt.Sprint(p)
	if len(p) != 2 || !strings.Contains(joined, "sem índice") || !strings.Contains(joined, "sincronismo") {
		t.Fatalf("problems: %+v", p)
	}
	// The hour being recorded is not checked.
	if p := Single(root).Verify("c1", start, now, start.Add(30*time.Minute)); len(p) != 0 {
		t.Fatalf("open hour verified: %+v", p)
	}
}

// Each Media Node agent owns its DVR state: a disk failure seen by one State,
// or its export slots, say nothing about another. (They used to be
// process-wide globals.)
func TestStatesAreIndependent(t *testing.T) {
	s1, s2 := NewState(), NewState()
	a := Storage{Mode: ModeRAID1, Disks: []string{t.TempDir(), t.TempDir()}, State: s1}
	b := Storage{Mode: ModeRAID1, Disks: []string{t.TempDir(), t.TempDir()}, State: s2}

	s1.markDown(a.Disks[0], true)
	if !s1.DiskDown(a.Disks[0]) || s2.DiskDown(a.Disks[0]) || DiskDown(a.Disks[0]) {
		t.Fatal("a failed disk must only be known to the State that saw it")
	}
	s1.markDown(a.Disks[0], false)

	if !s1.acquireExport(1) || s1.acquireExport(1) {
		t.Fatal("State 1 has one export slot")
	}
	if !s2.acquireExport(1) {
		t.Fatal("State 2's slots are its own")
	}
	s1.releaseExport()
	s2.releaseExport()

	// Both record at the same time without sharing locks or caches.
	start := time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)
	done := make(chan struct{})
	go func() { defer close(done); record(t, a, "c1", start, 10, -1) }()
	record(t, b, "c1", start, 10, -1)
	<-done
	for _, st := range []Storage{a, b} {
		segs, _ := st.List("c1", start, start.Add(time.Hour))
		if len(segs) != 10 {
			t.Fatalf("recorded %d pieces, want 10", len(segs))
		}
	}
}
