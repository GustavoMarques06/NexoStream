package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"nexostream/internal/bus"
	"nexostream/internal/model"
	"nexostream/internal/protocol"
)

// hub fans out bus events to Server-Sent Events clients.
type hub struct {
	mu      sync.Mutex
	clients map[*sseClient]struct{}
	subs    []bus.Subscription
	log     *slog.Logger
}

type sseClient struct {
	ch     chan sseMsg
	types  map[string]bool // empty = all
	status bool
}

type sseMsg struct {
	event string
	data  []byte
}

func newHub(b bus.Bus, log *slog.Logger) (*hub, error) {
	h := &hub{clients: map[*sseClient]struct{}{}, log: log}
	onEvent := func(_ string, d []byte) {
		var ev model.Event
		if json.Unmarshal(d, &ev) != nil || ev.Type == "" {
			return
		}
		h.broadcast(sseMsg{event: ev.Type, data: d}, false)
	}
	for _, subj := range protocol.EventSubjects {
		s, err := b.Subscribe(subj, onEvent)
		if err != nil {
			return nil, fmt.Errorf("subscribe %s: %w", subj, err)
		}
		h.subs = append(h.subs, s)
	}
	s, err := b.Subscribe(protocol.AllChannelStatus, func(_ string, d []byte) {
		h.broadcast(sseMsg{event: "channel.status", data: d}, true)
	})
	if err != nil {
		return nil, err
	}
	h.subs = append(h.subs, s)
	return h, nil
}

func (h *hub) broadcast(m sseMsg, isStatus bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if isStatus && !c.status {
			continue
		}
		if !isStatus && len(c.types) > 0 && !c.types[m.event] {
			continue
		}
		select {
		case c.ch <- m:
		default: // slow client: drop the message, it will resync from the API
		}
	}
}

func (h *hub) add(c *sseClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *hub) remove(c *sseClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (h *hub) close() {
	for _, s := range h.subs {
		_ = s.Unsubscribe()
	}
}

// handleStream serves GET /stream/events as text/event-stream.
// Query: types=channel.state_changed,worker.offline ; status=true adds
// "channel.status" updates (live bitrate for the panel).
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, http.StatusInternalServerError, "streaming_unsupported", "Streaming não suportado.")
		return
	}
	c := &sseClient{ch: make(chan sseMsg, 256), types: map[string]bool{}, status: r.URL.Query().Get("status") == "true"}
	for _, t := range strings.Split(r.URL.Query().Get("types"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			c.types[t] = true
		}
	}
	s.hub.add(c)
	defer s.hub.remove(c)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // nginx: do not buffer
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "retry: 3000\n: connected\n\n")
	flusher.Flush()

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case m := <-c.ch:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m.event, strings.TrimSpace(string(m.data)))
			flusher.Flush()
		case <-ping.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
