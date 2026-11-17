package api

import (
	"bytes"
	"net/http"
	"time"

	"nexostream/control-plane/sharedstate"
)

// idempotencyTTL is how long the answer of a POST with an Idempotency-Key is
// replayed.
const idempotencyTTL = 24 * time.Hour

// Buckets of the login rate limit.
const (
	bucketLoginIP   = "login-ip"
	bucketLoginUser = "login-user"
)

// loginLimits returns the attempts per minute allowed per address and per
// user name (half of the address limit, never below 1).
func (s *Server) loginLimits() (perIP, perUser int) {
	perIP = int(s.loginPerMin.Load())
	return perIP, max(perIP/2, 1)
}

// allowLogin counts a login attempt of key in bucket against the shared state,
// so the limit holds across API instances. A failure of the shared state
// never blocks a login: the state falls back to this instance's memory.
func (s *Server) allowLogin(r *http.Request, bucket, key string, limit int) bool {
	ok, err := s.shared.Allow(r.Context(), bucket, key, limit)
	if err != nil {
		s.log.Warn("login rate limit unavailable", "error", err)
		return true
	}
	return ok
}

type recorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (r *recorder) WriteHeader(c int) { r.status = c; r.ResponseWriter.WriteHeader(c) }
func (r *recorder) Write(b []byte) (int, error) {
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}

// idempotent replays the response of a POST with the same Idempotency-Key
// (per actor and path) for a while. The answers live in the shared state, so
// a retry that reaches another API instance is replayed instead of executed
// twice.
func (s *Server) idempotent(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" || len(key) > 128 {
			h(w, r)
			return
		}
		p := principalFrom(r.Context())
		k := p.Actor + "|" + r.URL.Path + "|" + key
		if e, ok, err := s.shared.GetReply(r.Context(), k); err == nil && ok {
			for name, v := range e.Header {
				w.Header()[name] = v
			}
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(e.Status)
			_, _ = w.Write(e.Body)
			return
		}
		rec := &recorder{ResponseWriter: w, status: 200}
		h(rec, r)
		if rec.status < 500 {
			reply := sharedstate.Reply{Status: rec.status, Header: w.Header().Clone(), Body: rec.buf.Bytes()}
			if err := s.shared.PutReply(r.Context(), k, reply, idempotencyTTL); err != nil {
				s.log.Warn("idempotency reply not stored", "error", err)
			}
		}
	}
}
