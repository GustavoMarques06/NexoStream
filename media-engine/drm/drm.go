// Package drm protects the HLS outputs of a channel (ADR 0008).
//
// The method implemented is HLS AES-128: the Media Node writes clear
// segments to a folder nginx does not publish, and encrypts each segment
// when it is requested (AES-128-CBC, PKCS#7, IV = media sequence number).
// The content key changes every key period. Keys are served by the Media
// Node only to requests carrying a valid playback token, so the player gets
// the token once (in the playlist URL) and the node adds it to the key URIs.
//
// Content keys come from a KeyProvider. LocalKeys derives them from a
// secret shared by the Media Nodes, so any node (and the DVR, years later)
// computes the same key for the same channel and period without a key
// database. A commercial DRM (Widevine, PlayReady, FairPlay through a key
// provider using CPIX/SPEKE) would be another KeyProvider plus fMP4/CMAF
// packaging; it is not implemented.
package drm

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MethodAES128 is HLS AES-128 with keys from this cluster.
const MethodAES128 = "aes-128"

// Methods lists the supported protection methods.
var Methods = []string{MethodAES128}

// DefaultRotation is how long one content key is used.
const DefaultRotation = time.Hour

// MinRotation and MaxRotation bound the key period.
const (
	MinRotation = time.Minute
	MaxRotation = 24 * time.Hour
)

// MinSecretBytes is the minimum size of the key and token secrets.
const MinSecretBytes = 32

// ErrNotConfigured means this Media Node has no DRM secrets: protected
// channels are not served at all (never in clear).
var ErrNotConfigured = errors.New("DRM is not configured on this Media Node (NEXOSTREAM_DRM_KEY_FILE, NEXOSTREAM_PLAYBACK_TOKEN_KEY_FILE)")

// KeyProvider returns the content key of a channel for a key period
// (period = Unix time, in seconds, when the period starts).
type KeyProvider interface {
	ContentKey(channelID string, period int64) ([16]byte, error)
}

// LocalKeys derives content keys with HMAC-SHA256 from a secret that every
// Media Node of the cluster shares.
type LocalKeys struct{ Secret []byte }

// ContentKey implements KeyProvider.
func (k LocalKeys) ContentKey(channelID string, period int64) ([16]byte, error) {
	var out [16]byte
	if len(k.Secret) < MinSecretBytes {
		return out, ErrNotConfigured
	}
	m := hmac.New(sha256.New, k.Secret)
	m.Write([]byte("nexostream/hls-aes-128/v1\x00" + channelID + "\x00" + strconv.FormatInt(period, 10)))
	copy(out[:], m.Sum(nil))
	return out, nil
}

// Protector protects one channel.
type Protector struct {
	ChannelID string
	Keys      KeyProvider // nil: the node has no key secret
	TokenKey  []byte      // playback token secret
	Rotation  time.Duration
}

// Ready reports whether the node can serve the channel.
func (p Protector) Ready() error {
	if p.Keys == nil || len(p.TokenKey) < MinSecretBytes {
		return ErrNotConfigured
	}
	return nil
}

func (p Protector) rotation() int64 {
	r := p.Rotation
	if r <= 0 {
		r = DefaultRotation
	}
	r = min(max(r, MinRotation), MaxRotation)
	return int64(r / time.Second)
}

// Period returns the key period of a moment (Unix seconds of its start).
func (p Protector) Period(t time.Time) int64 {
	u, r := t.Unix(), p.rotation()
	m := u % r
	if m < 0 {
		m += r
	}
	return u - m
}

// KeyTag returns the #EXT-X-KEY line of a period. The key URI is relative to
// the playlist and carries the viewer's token.
func KeyTag(period int64, token string) string {
	uri := "keys/" + strconv.FormatInt(period, 10) + ".key"
	if token != "" {
		uri += "?token=" + url.QueryEscape(token)
	}
	return `#EXT-X-KEY:METHOD=AES-128,URI="` + uri + `"`
}

// Encrypt encrypts a segment with the key of period, using the media
// sequence number as IV (the HLS default when the key tag has no IV).
func (p Protector) Encrypt(period, seq int64, clear []byte) ([]byte, error) {
	if p.Keys == nil {
		return nil, ErrNotConfigured
	}
	key, err := p.Keys.ContentKey(p.ChannelID, period)
	if err != nil {
		return nil, err
	}
	return EncryptAES128(key, seq, clear)
}

// EncryptAES128 encrypts data as an HLS AES-128 segment: AES-128-CBC with
// PKCS#7 padding and the sequence number, big-endian, as IV.
func EncryptAES128(key [16]byte, seq int64, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	pad := aes.BlockSize - len(data)%aes.BlockSize
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	var iv [aes.BlockSize]byte
	binary.BigEndian.PutUint64(iv[8:], uint64(seq)) //nolint:gosec // sequence numbers are never negative
	cipher.NewCBCEncrypter(block, iv[:]).CryptBlocks(out, out)
	return out, nil
}

// Authorize checks the playback token of a request (query "token").
func (p Protector) Authorize(r *http.Request, now time.Time) error {
	return VerifyToken(p.TokenKey, p.ChannelID, r.URL.Query().Get("token"), now)
}

// ServeKey answers keys/<period>.key. The token is required, and keys of
// future periods beyond the next one are refused, so a viewer whose token
// expires cannot stock up keys in advance.
func (p Protector) ServeKey(w http.ResponseWriter, r *http.Request, name string, now time.Time) {
	if err := p.Ready(); err != nil {
		WriteError(w, err)
		return
	}
	if err := p.Authorize(r, now); err != nil {
		WriteError(w, err)
		return
	}
	v, ok := strings.CutSuffix(name, ".key")
	period, err := strconv.ParseInt(v, 10, 64)
	if !ok || err != nil || period < 0 || period > now.Unix()+p.rotation() {
		http.NotFound(w, r)
		return
	}
	key, err := p.Keys.ContentKey(p.ChannelID, period)
	if err != nil {
		WriteError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(key[:])
}

// WriteError maps DRM errors to HTTP answers.
func WriteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotConfigured):
		http.Error(w, "protected channel: DRM is not configured on this server", http.StatusServiceUnavailable)
	case errors.Is(err, ErrTokenMissing), errors.Is(err, ErrTokenInvalid), errors.Is(err, ErrTokenExpired):
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
