package drm

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Playback token errors.
var (
	ErrTokenMissing = errors.New("playback token required")
	ErrTokenInvalid = errors.New("invalid playback token")
	ErrTokenExpired = errors.New("playback token expired")
)

// Playback tokens authorize one channel until a moment:
//
//	token = <expires> "." hex(HMAC-SHA256(secret, "nexostream-play/v1\n" + channel_id + "\n" + <expires>))
//
// where <expires> is Unix seconds. The format is simple on purpose: the
// operator's middleware (portal, billing) can sign tokens itself with the
// same secret, without calling the API for every viewer.
func tokenMAC(key []byte, channelID, exp string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("nexostream-play/v1\n" + channelID + "\n" + exp))
	return m.Sum(nil)
}

// SignToken returns a playback token for channelID valid until expires.
func SignToken(key []byte, channelID string, expires time.Time) string {
	exp := strconv.FormatInt(expires.Unix(), 10)
	return exp + "." + hex.EncodeToString(tokenMAC(key, channelID, exp))
}

// VerifyToken checks a playback token.
func VerifyToken(key []byte, channelID, token string, now time.Time) error {
	if len(key) < MinSecretBytes {
		return ErrNotConfigured
	}
	if token == "" {
		return ErrTokenMissing
	}
	exp, sig, ok := strings.Cut(token, ".")
	if !ok {
		return ErrTokenInvalid
	}
	n, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || n <= 0 {
		return ErrTokenInvalid
	}
	got, err := hex.DecodeString(sig)
	if err != nil || !hmac.Equal(got, tokenMAC(key, channelID, exp)) {
		return ErrTokenInvalid
	}
	if now.Unix() > n {
		return ErrTokenExpired
	}
	return nil
}
