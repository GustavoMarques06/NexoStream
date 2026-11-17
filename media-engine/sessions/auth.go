// Package sessions tracks who is playing each channel on a Media Node and
// decides who may play it (ADR 0009), like Flussonic's play sessions and
// on_play auth backend.
//
// A play session is one viewer of one channel: HLS and DVR viewers are
// recognized by their token, or by IP + User-Agent when there is no token;
// each MPEG-TS/HTTP connection is its own session. A session closes after
// some time without requests, when the connection ends, or when an operator
// closes it.
package sessions

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"nexostream/internal/model"
	"nexostream/internal/netguard"
)

// Request describes who asks to play.
type Request struct {
	ChannelID string
	Proto     string // hls | mpegts | dvr
	IP        string
	UserAgent string
	Token     string
	// TokenValid means Token is a playback token signed by this cluster
	// (ADR 0008): it authorizes without calling the backend.
	TokenValid bool
}

// Decision is the answer of the access rules.
type Decision struct {
	Allow  bool
	Reason string
	UserID string
	// Duration is how long the answer stays valid (X-AuthDuration).
	Duration time.Duration
	// MaxSessions limits the sessions of UserID (X-Max-Sessions).
	MaxSessions int
	// Unique closes the other sessions of UserID (X-Unique: true).
	Unique bool
}

// Denial reasons.
const (
	ReasonIPDenied        = "ip_denied"
	ReasonIPNotAllowed    = "ip_not_allowed"
	ReasonBackendDenied   = "backend_denied"
	ReasonBackendError    = "backend_unreachable"
	ReasonMaxSessions     = "max_sessions"
	ReasonUserMaxSessions = "user_max_sessions"
	ReasonKilled          = "killed"
	ReasonBanned          = "closed_by_operator"
)

// DefaultCache is how long a backend answer is reused.
const DefaultCache = time.Minute

// Authorizer applies the access rules of a channel.
type Authorizer struct {
	Client *http.Client
}

func (a *Authorizer) client() *http.Client {
	if a != nil && a.Client != nil {
		return a.Client
	}
	return netguard.Client(3 * time.Second)
}

// ipIn reports whether ip matches one of the IPs or CIDRs.
func ipIn(ip net.IP, list []string) bool {
	for _, v := range list {
		v = strings.TrimSpace(v)
		if strings.Contains(v, "/") {
			if _, n, err := net.ParseCIDR(v); err == nil && n.Contains(ip) {
				return true
			}
		} else if p := net.ParseIP(v); p != nil && p.Equal(ip) {
			return true
		}
	}
	return false
}

// ValidIPList checks a list of IPs and CIDRs, returning the first bad one.
func ValidIPList(list []string) (string, bool) {
	for _, v := range list {
		v = strings.TrimSpace(v)
		if strings.Contains(v, "/") {
			if _, _, err := net.ParseCIDR(v); err != nil {
				return v, false
			}
		} else if net.ParseIP(v) == nil {
			return v, false
		}
	}
	return "", true
}

// CheckIP applies the deny and allow lists; it returns the denial reason
// or "". It runs on every request, not only when a session opens.
func CheckIP(cfg model.AuthConfig, addr string) string {
	ip := net.ParseIP(addr)
	if ip != nil && ipIn(ip, cfg.DenyIPs) {
		return ReasonIPDenied
	}
	if len(cfg.AllowIPs) > 0 && (ip == nil || !ipIn(ip, cfg.AllowIPs)) {
		return ReasonIPNotAllowed
	}
	return ""
}

// Check applies, in order: deny list, allow list, the cluster playback
// token, then the auth backend. Session limits are applied by the Tracker.
func (a *Authorizer) Check(ctx context.Context, cfg model.AuthConfig, req Request, sessionID string) Decision {
	cache := DefaultCache
	if cfg.CacheSeconds > 0 {
		cache = time.Duration(cfg.CacheSeconds) * time.Second
	}
	if reason := CheckIP(cfg, req.IP); reason != "" {
		return Decision{Reason: reason}
	}
	if req.TokenValid || cfg.BackendURL == "" {
		return Decision{Allow: true, Duration: cache}
	}
	return a.backend(ctx, cfg, req, sessionID, cache)
}

// backend calls the auth backend:
//
//	GET <backend_url>?name=<channel>&ip=&token=&proto=&user_agent=&session_id=
//
// 2xx allows; any other status denies. Optional answer headers:
// X-AuthDuration (seconds), X-UserId, X-Max-Sessions, X-Unique (true).
func (a *Authorizer) backend(ctx context.Context, cfg model.AuthConfig, req Request, sessionID string, cache time.Duration) Decision {
	u, err := url.Parse(cfg.BackendURL)
	if err != nil {
		return onError(cfg, cache)
	}
	q := u.Query()
	q.Set("name", req.ChannelID)
	q.Set("ip", req.IP)
	q.Set("token", req.Token)
	q.Set("proto", req.Proto)
	q.Set("user_agent", req.UserAgent)
	q.Set("session_id", sessionID)
	u.RawQuery = q.Encode()
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return onError(cfg, cache)
	}
	resp, err := a.client().Do(hr) //nolint:gosec // G704: the backend URL is set by an administrator, not by the viewer
	if err != nil {
		return onError(cfg, cache)
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Decision{Reason: ReasonBackendDenied}
	}
	d := Decision{Allow: true, Duration: cache, UserID: resp.Header.Get("X-UserId")}
	if n, err := strconv.Atoi(resp.Header.Get("X-AuthDuration")); err == nil && n > 0 {
		d.Duration = time.Duration(n) * time.Second
	}
	if n, err := strconv.Atoi(resp.Header.Get("X-Max-Sessions")); err == nil && n > 0 {
		d.MaxSessions = n
	}
	d.Unique = strings.EqualFold(resp.Header.Get("X-Unique"), "true")
	return d
}

func onError(cfg model.AuthConfig, cache time.Duration) Decision {
	if cfg.AllowOnError {
		// Retry soon: the backend may be back.
		return Decision{Allow: true, Duration: min(cache, 10*time.Second), Reason: ReasonBackendError}
	}
	return Decision{Reason: ReasonBackendError}
}

// ErrDenied is returned when a request may not play.
var ErrDenied = errors.New("access denied")

// DeniedError carries the reason of a denial.
type DeniedError struct{ Reason string }

func (e *DeniedError) Error() string { return "access denied: " + e.Reason }
func (e *DeniedError) Unwrap() error { return ErrDenied }

// ClientIP returns the viewer address. Behind the local nginx (loopback
// peer) it trusts X-Real-IP or the first X-Forwarded-For entry.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(v) != nil {
			return v
		}
		if f, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ","); net.ParseIP(strings.TrimSpace(f)) != nil {
			return strings.TrimSpace(f)
		}
	}
	return host
}
