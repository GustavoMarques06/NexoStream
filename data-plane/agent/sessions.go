package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"nexostream/internal/model"
	"nexostream/internal/protocol"
	"nexostream/media-engine/drm"
	"nexostream/media-engine/sessions"
)

// runSessions closes idle play sessions.
func (a *Agent) runSessions(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.sessions.Sweep()
		}
	}
}

// countingWriter counts the bytes sent within a play session and remembers
// the status code, for the viewer-path metrics.
type countingWriter struct {
	http.ResponseWriter
	h      *sessions.Handle
	status int
	bytes  int64
}

func (c *countingWriter) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *countingWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK // an implicit 200 on the first write
	}
	n, err := c.ResponseWriter.Write(b)
	c.h.Add(n)
	c.bytes += int64(n)
	return n, err
}

// ReadFrom keeps the zero-copy path: http.ServeFile copies a segment with
// io.Copy, which uses the destination's ReadFrom when it has one. Hiding it
// behind this wrapper would turn the kernel's sendfile into a copy through the
// Go process for every segment of every viewer.
func (c *countingWriter) ReadFrom(r io.Reader) (int64, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	var n int64
	var err error
	if rf, ok := c.ResponseWriter.(io.ReaderFrom); ok {
		n, err = rf.ReadFrom(r)
	} else {
		n, err = io.Copy(struct{ io.Writer }{c.ResponseWriter}, r)
	}
	c.h.Add(int(n))
	c.bytes += n
	return n, err
}

func (c *countingWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *countingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// statusClass turns a status code into its class label ("2xx").
func statusClass(code int) string {
	if code < 100 || code > 599 {
		return "2xx" // nothing written: net/http answers 200
	}
	return strconv.Itoa(code/100) + "xx"
}

// play admits a viewer request (ADR 0009): it applies the channel access
// rules, attaches the request to its play session and counts its bytes.
// stream=true is a long MPEG-TS connection, which is its own session.
func (a *Agent) play(w http.ResponseWriter, r *http.Request, id, proto string, stream bool, serve func(http.ResponseWriter, *http.Request)) {
	a.mu.Lock()
	spec, ok := a.specs[id]
	a.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	m := a.metrics
	began := time.Now()
	m.playInflight.WithLabelValues(proto).Inc()
	defer m.playInflight.WithLabelValues(proto).Dec()
	token := r.URL.Query().Get("token")
	req := sessions.Request{ChannelID: id, Proto: proto, IP: sessions.ClientIP(r), UserAgent: r.UserAgent(), Token: token}
	if token != "" && len(a.cfg.PlaybackTokenKey) >= drm.MinSecretBytes {
		req.TokenValid = drm.VerifyToken(a.cfg.PlaybackTokenKey, id, token, time.Now()) == nil
	}
	h, err := a.sessions.Begin(r.Context(), spec.Channel.Auth, req, stream)
	m.playAuth.WithLabelValues(proto).Observe(time.Since(began).Seconds())
	if err != nil {
		var d *sessions.DeniedError
		if errors.As(err, &d) {
			w.Header().Set("X-Denied-Reason", d.Reason)
		}
		http.Error(w, err.Error(), http.StatusForbidden)
		m.playRequests.WithLabelValues(proto, "4xx").Inc()
		return
	}
	defer h.Done()
	cw := &countingWriter{ResponseWriter: w, h: h}
	serve(cw, r.WithContext(h.Context))
	m.playRequests.WithLabelValues(proto, statusClass(cw.status)).Inc()
	m.playBytes.WithLabelValues(proto).Add(float64(cw.bytes))
	if !stream {
		m.playSeconds.WithLabelValues(proto).Observe(time.Since(began).Seconds())
	}
}

// sessionLogEvery is how often closed sessions are sent (tests shorten it).
var sessionLogEvery = 10 * time.Second

// runSessionLog sends closed sessions to the controllers in batches (every
// 10 s or 500 sessions) for the session history.
func (a *Agent) runSessionLog(ctx context.Context) {
	t := time.NewTicker(sessionLogEvery)
	defer t.Stop()
	var batch []model.PlaySession
	flush := func() {
		if len(batch) > 0 {
			a.publishJSON(protocol.SessionsClosed, batch)
			batch = nil
		}
	}
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case s := <-a.closedQ:
			batch = append(batch, s)
			if len(batch) >= 500 {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}
