// Package ingest receives MPEG-TS and analyzes it: PAT/PMT, continuity
// counters, PCR progression, bitrate and stream presence. UDP/RTP is read
// directly; SRT, RTMP, RTSP and HTTP (HLS or continuous TS) are pulled by an
// ffmpeg process that remuxes them to MPEG-TS, so every protocol goes
// through the same analysis, health monitoring and failover.
package ingest

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// SourceAddr is a parsed source URL.
type SourceAddr struct {
	Scheme    string
	Host      string // empty = all local addresses
	Port      int
	Multicast bool
	IP        net.IP
	// Pulled is true for protocols received through ffmpeg (not UDP/RTP).
	Pulled bool
	// Listener is true when the node waits for the sender (SRT mode=listener)
	// instead of connecting to it: the port is open to whoever can reach it.
	Listener bool
	// PassphraseLen is the length of the SRT passphrase in the URL (0 = none).
	// SRT accepts 10 to 79 characters, authenticates the sender with it and
	// encrypts the stream. The value itself is never kept here.
	PassphraseLen int
	// StreamKeyLen is the length of the key of an RTMP listener
	// (rtmp://0.0.0.0:1935/<app>/<key>?mode=listener); the key itself is not kept.
	StreamKeyLen int
}

// SRT limits for a passphrase.
const (
	MinSRTPassphrase = 10
	MaxSRTPassphrase = 79
)

// SupportedSchemes are the accepted input protocols.
var SupportedSchemes = []string{"udp", "rtp", "srt", "rtmp", "rtmps", "rtsp", "http", "https"}

var defaultPorts = map[string]int{"rtmp": 1935, "rtmps": 443, "rtsp": 554, "http": 80, "https": 443}

// IsPulled reports whether a scheme is received through ffmpeg.
func IsPulled(scheme string) bool {
	return scheme != "udp" && scheme != "rtp"
}

// ParseSourceURL validates and parses a source URL such as
// "udp://239.1.1.1:1234", "udp://@:5000", "srt://10.0.0.5:9000",
// "srt://:9000?mode=listener", "rtmp://server/live/key",
// "rtsp://camera/stream" or "https://cdn/live/index.m3u8".
func ParseSourceURL(raw string) (SourceAddr, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return SourceAddr{}, fmt.Errorf("invalid url: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	ok := false
	for _, s := range SupportedSchemes {
		ok = ok || s == scheme
	}
	if !ok {
		return SourceAddr{}, fmt.Errorf("unsupported protocol %q (supported: %s)", u.Scheme, strings.Join(SupportedSchemes, ", "))
	}
	if IsPulled(scheme) {
		return parsePulled(scheme, u)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		return SourceAddr{}, fmt.Errorf("url must contain host:port: %w", err)
	}
	host = strings.TrimPrefix(host, "@")
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return SourceAddr{}, fmt.Errorf("invalid port %q", portStr)
	}
	sa := SourceAddr{Scheme: scheme, Host: host, Port: port}
	if host != "" {
		ip := net.ParseIP(host)
		if ip == nil {
			return SourceAddr{}, fmt.Errorf("host must be an IP address, got %q", host)
		}
		if ip.To4() == nil {
			return SourceAddr{}, fmt.Errorf("IPv6 sources are not supported yet")
		}
		sa.IP = ip.To4()
		sa.Multicast = ip.IsMulticast()
	}
	return sa, nil
}

func parsePulled(scheme string, u *url.URL) (SourceAddr, error) {
	sa := SourceAddr{Scheme: scheme, Pulled: true, Host: u.Hostname()}
	listener := (scheme == "srt" || scheme == "rtmp") && u.Query().Get("mode") == "listener"
	sa.Listener = listener
	switch {
	case scheme == "srt":
		sa.PassphraseLen = len(u.Query().Get("passphrase"))
	case scheme == "rtmp" && listener:
		app, key := splitRTMPPath(u.Path)
		if app == "" || key == "" {
			return SourceAddr{}, fmt.Errorf("rtmp listener needs rtmp://0.0.0.0:1935/<app>/<key>?mode=listener")
		}
		sa.StreamKeyLen = len(key)
	case scheme == "rtmps" && u.Query().Get("mode") == "listener":
		return SourceAddr{}, fmt.Errorf("rtmps has no listener mode: put a TLS proxy in front of an rtmp listener, or use SRT with a passphrase")
	}
	if sa.Host == "" && !listener {
		return SourceAddr{}, fmt.Errorf("%s url needs a host", scheme)
	}
	if p := u.Port(); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil || port <= 0 || port > 65535 {
			return SourceAddr{}, fmt.Errorf("invalid port %q", p)
		}
		sa.Port = port
	} else if d, ok := defaultPorts[scheme]; ok {
		sa.Port = d
	} else {
		return SourceAddr{}, fmt.Errorf("%s url needs a port", scheme)
	}
	return sa, nil
}

// String returns the canonical form.
func (s SourceAddr) String() string {
	return fmt.Sprintf("%s://%s:%d", s.Scheme, s.Host, s.Port)
}

// Source delivers aligned MPEG-TS packets until ctx is canceled.
type Source interface {
	Run(ctx context.Context, onData func([]byte)) error
}

// Open returns the receiver for a source URL.
func Open(cfg ReceiverConfig) (Source, error) {
	addr, err := ParseSourceURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	if addr.Listener && addr.Scheme == "rtmp" {
		return newRTMPListener(cfg, addr)
	}
	if addr.Pulled {
		return newPuller(cfg, addr), nil
	}
	return NewReceiver(cfg)
}
