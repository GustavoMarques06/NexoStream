package drm

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	keySecret   = bytes.Repeat([]byte("k"), 32)
	tokenSecret = bytes.Repeat([]byte("t"), 32)
)

func protector() Protector {
	return Protector{ChannelID: "canal-1", Keys: LocalKeys{Secret: keySecret}, TokenKey: tokenSecret, Rotation: time.Hour}
}

// decrypt is what a player does with an AES-128 segment.
func decrypt(t *testing.T, key [16]byte, seq int64, data []byte) []byte {
	t.Helper()
	block, _ := aes.NewCipher(key[:])
	var iv [16]byte
	binary.BigEndian.PutUint64(iv[8:], uint64(seq))
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(out, data)
	pad := int(out[len(out)-1])
	if pad < 1 || pad > 16 {
		t.Fatalf("bad padding %d", pad)
	}
	return out[:len(out)-pad]
}

func TestToken(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tok := SignToken(tokenSecret, "canal-1", now.Add(time.Hour))
	if err := VerifyToken(tokenSecret, "canal-1", tok, now); err != nil {
		t.Fatal(err)
	}
	cases := map[string]error{
		"":               ErrTokenMissing,
		"abc":            ErrTokenInvalid,
		tok[:len(tok)-1]: ErrTokenInvalid,
		"1." + tok[2:]:   ErrTokenInvalid,
		SignToken(tokenSecret, "outro", now.Add(time.Hour)):      ErrTokenInvalid,
		SignToken(tokenSecret, "canal-1", now.Add(-time.Second)): ErrTokenExpired,
	}
	for tk, want := range cases {
		if err := VerifyToken(tokenSecret, "canal-1", tk, now); !errors.Is(err, want) {
			t.Errorf("token %q: got %v, want %v", tk, err, want)
		}
	}
	if err := VerifyToken(nil, "canal-1", tok, now); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no secret: %v", err)
	}
}

func TestKeysAndEncryption(t *testing.T) {
	p := protector()
	k1, _ := p.Keys.ContentKey("canal-1", 3600)
	k2, _ := p.Keys.ContentKey("canal-1", 7200)
	k3, _ := p.Keys.ContentKey("canal-2", 3600)
	again, _ := LocalKeys{Secret: keySecret}.ContentKey("canal-1", 3600)
	if k1 == k2 || k1 == k3 || k1 != again {
		t.Fatal("keys must depend on channel and period, and be stable")
	}
	if _, err := (LocalKeys{Secret: []byte("short")}).ContentKey("c", 0); !errors.Is(err, ErrNotConfigured) {
		t.Fatal("short secret accepted")
	}
	for _, n := range []int{0, 1, 15, 16, 188 * 7} {
		clear := bytes.Repeat([]byte{0x47}, n)
		enc, err := p.Encrypt(3600, 42, clear)
		if err != nil || len(enc)%16 != 0 || len(enc) <= n {
			t.Fatalf("n=%d: len %d err %v", n, len(enc), err)
		}
		if got := decrypt(t, k1, 42, enc); !bytes.Equal(got, clear) {
			t.Fatalf("n=%d: round trip failed", n)
		}
	}
	if got := p.Period(time.Unix(7199, 0)); got != 3600 {
		t.Fatalf("period %d", got)
	}
	if got := (Protector{Rotation: time.Second}).Period(time.Unix(130, 0)); got != 120 {
		t.Fatalf("rotation below the minimum must be raised to 1 min, got %d", got)
	}
}

func TestServeKey(t *testing.T) {
	p := protector()
	now := time.Unix(10_000, 0)
	tok := SignToken(tokenSecret, "canal-1", now.Add(time.Hour))
	get := func(name, token string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		p.ServeKey(w, httptest.NewRequest("GET", "/x?token="+token, nil), name, now)
		return w
	}
	w := get("7200.key", tok)
	want, _ := p.Keys.ContentKey("canal-1", 7200)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), want[:]) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("key: %d %q", w.Code, w.Body.String())
	}
	if w := get("7200.key", ""); w.Code != 403 {
		t.Fatalf("without token: %d", w.Code)
	}
	if w := get("90000.key", tok); w.Code != 404 {
		t.Fatalf("far future key served: %d", w.Code)
	}
	if w := get("abc.key", tok); w.Code != 404 {
		t.Fatalf("bad name: %d", w.Code)
	}
	w = httptest.NewRecorder()
	Protector{ChannelID: "canal-1"}.ServeKey(w, httptest.NewRequest("GET", "/x?token="+tok, nil), "7200.key", now)
	if w.Code != 503 {
		t.Fatalf("node without secrets: %d", w.Code)
	}
}

const livePlaylist = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:120
#EXT-X-DISCONTINUITY
#EXT-X-PROGRAM-DATE-TIME:2026-11-16T13:59:54.000+0000
#EXTINF:6.000000,
seg_000120.ts
#EXT-X-PROGRAM-DATE-TIME:2026-11-16T14:00:00.000+0000
#EXTINF:6.000000,
seg_000121.ts
#EXTINF:6.000000,
seg_000122.ts
`

func TestLive(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.m3u8"), []byte(livePlaylist), 0o600); err != nil {
		t.Fatal(err)
	}
	clear := bytes.Repeat([]byte{0x47, 1, 2, 3}, 100)
	for _, n := range []string{"seg_000120.ts", "seg_000121.ts", "seg_000122.ts"} {
		_ = os.WriteFile(filepath.Join(dir, n), clear, 0o600)
	}
	p := protector()
	var l Live
	now := time.Date(2026, 11, 16, 14, 0, 10, 0, time.UTC)
	out, err := l.Playlist(dir, p, "tok en", now)
	if err != nil {
		t.Fatal(err)
	}
	p1 := time.Date(2026, 11, 16, 13, 0, 0, 0, time.UTC).Unix()
	p2 := time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC).Unix()
	want := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:120\n" +
		KeyTag(p1, "tok en") + "\n#EXT-X-DISCONTINUITY\n#EXT-X-PROGRAM-DATE-TIME:2026-11-16T13:59:54.000+0000\n#EXTINF:6.000000,\nenc/seg_000120.ts\n" +
		KeyTag(p2, "tok en") + "\n#EXT-X-PROGRAM-DATE-TIME:2026-11-16T14:00:00.000+0000\n#EXTINF:6.000000,\nenc/seg_000121.ts\n" +
		"#EXTINF:6.000000,\nenc/seg_000122.ts\n"
	if string(out) != want {
		t.Fatalf("playlist:\n%s\nwant:\n%s", out, want)
	}
	if !strings.Contains(string(out), fmt.Sprintf(`URI="keys/%d.key?token=tok+en"`, p1)) {
		t.Fatalf("key URI must carry the escaped token:\n%s", out)
	}
	// seg 122 has no PDT: it starts where 121 ends (period p2), IV = 122.
	enc, err := l.Segment(dir, "seg_000122.ts", p, now)
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := p.Keys.ContentKey("canal-1", p2)
	if got := decrypt(t, k2, 122, enc); !bytes.Equal(got, clear) {
		t.Fatal("segment does not decrypt with the key of its period and its sequence")
	}
	// A fresh Live (node restarted) reads the playlist on demand.
	var l2 Live
	enc, err = l2.Segment(dir, "seg_000120.ts", p, now)
	k1, _ := p.Keys.ContentKey("canal-1", p1)
	if err != nil || !bytes.Equal(decrypt(t, k1, 120, enc), clear) {
		t.Fatalf("on-demand lookup: %v", err)
	}
	for _, bad := range []string{"index.m3u8", "../x.ts", "seg_999999.ts"} {
		if _, err := l.Segment(dir, bad, p, now); err == nil {
			t.Errorf("%s served", bad)
		}
	}
}
