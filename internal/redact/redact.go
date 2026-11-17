// Package redact hides the secrets that URLs of a channel can carry: SRT
// passphrases, RTSP passwords, RTMP stream keys, tokens in query strings. The
// URL stays recognizable (scheme, host, port, options such as mode=listener),
// so an operator can still tell sources apart.
package redact

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Mask replaces a hidden value.
const Mask = "••••"

// harmlessQuery lists query options whose values are not secrets. Every other
// option is hidden: an unknown name is more likely a token than a setting.
var harmlessQuery = map[string]bool{
	"mode": true, "latency": true, "rcvlatency": true, "peerlatency": true, "pkt_size": true, "ttl": true,
	"localport": true, "reuse": true, "overrun_nonfatal": true, "fifo_size": true, "buffer_size": true,
	"timeout": true, "listen": true, "tcp_nodelay": true, "transtype": true, "maxbw": true,
}

// URL returns raw without its secrets:
//   - user name and password of the address (rtsp://user:pass@camera);
//   - the value of every query option not known to be harmless (passphrase,
//     token, key, streamid, ...);
//   - the last path element of an RTMP/RTMPS address, which is the stream key.
//
// A URL that does not parse is replaced entirely.
func URL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return "(url inválida)"
	}
	u.User = nil
	if u.RawQuery != "" {
		u.RawQuery = maskQuery(u.RawQuery)
	}
	switch strings.ToLower(u.Scheme) {
	case "rtmp", "rtmps", "rtmpt", "rtmpe":
		if i := strings.LastIndex(u.Path, "/"); i > 0 && i < len(u.Path)-1 {
			u.Path = u.Path[:i+1] + Mask
			u.RawPath = ""
		}
	}
	return unescapeMask(u.String())
}

// maskQuery keeps harmless options and hides the others, in a stable order.
func maskQuery(raw string) string {
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return "…"
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		for _, v := range vals[k] {
			if !harmlessQuery[strings.ToLower(k)] {
				v = Mask
			}
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// unescapeMask makes the mask readable again after URL encoding.
func unescapeMask(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, url.QueryEscape(Mask), Mask), url.PathEscape(Mask), Mask)
}

// URLs masks every element of a list.
func URLs(list []string) []string {
	if list == nil {
		return nil
	}
	out := make([]string, len(list))
	for i, v := range list {
		out[i] = URL(v)
	}
	return out
}

// urlInText finds addresses inside a sentence, such as ffmpeg's
// "Error opening output rtmp://host/app/KEY: I/O error".
var urlInText = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)

// Text masks the secrets of every URL found in free text (a log line, an
// error message). Trailing punctuation that belongs to the sentence is kept.
func Text(s string) string {
	return urlInText.ReplaceAllStringFunc(s, func(m string) string {
		trail := ""
		for len(m) > 0 && strings.ContainsRune(".,;:)]}", rune(m[len(m)-1])) {
			trail = string(m[len(m)-1]) + trail
			m = m[:len(m)-1]
		}
		return URL(m) + trail
	})
}
