package packaging

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// TSRelay receives the MPEG-TS produced by the pipeline on a loopback UDP
// port and fans it out to HTTP clients (MPEG-TS over HTTP). Slow clients are
// disconnected instead of slowing everyone down. Using UDP between the
// pipeline and the relay means the pipeline never blocks on viewers.
type TSRelay struct {
	conn      *net.UDPConn
	mu        sync.Mutex
	clients   map[*relayClient]struct{}
	outBytes  atomic.Int64
	closed    chan struct{}
	closeOnce sync.Once
}

type relayClient struct {
	ch       chan []byte
	internal bool // push feeds: drop packets when full, never disconnect
}

// NewTSRelay listens on 127.0.0.1 using a free port.
func NewTSRelay() (*TSRelay, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return nil, fmt.Errorf("ts relay listen: %w", err)
	}
	_ = conn.SetReadBuffer(4 << 20)
	r := &TSRelay{conn: conn, clients: map[*relayClient]struct{}{}, closed: make(chan struct{})}
	go r.loop()
	return r, nil
}

// Addr is where the pipeline must send its MPEG-TS ("udp://127.0.0.1:port").
func (r *TSRelay) Addr() string {
	return "udp://" + r.conn.LocalAddr().String()
}

// Viewers returns the number of connected HTTP clients.
func (r *TSRelay) Viewers() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for c := range r.clients {
		if !c.internal {
			n++
		}
	}
	return n
}

// Subscribe returns a feed of the MPEG-TS for an internal consumer (push)
// and the function that ends it.
func (r *TSRelay) Subscribe() (<-chan []byte, func()) {
	c := &relayClient{ch: make(chan []byte, 4096), internal: true}
	r.mu.Lock()
	r.clients[c] = struct{}{}
	r.mu.Unlock()
	var once sync.Once
	return c.ch, func() {
		once.Do(func() {
			r.mu.Lock()
			if _, ok := r.clients[c]; ok {
				delete(r.clients, c)
				close(c.ch)
			}
			r.mu.Unlock()
		})
	}
}

// OutputBytes returns the bytes written to clients.
func (r *TSRelay) OutputBytes() int64 { return r.outBytes.Load() }

func (r *TSRelay) loop() {
	buf := make([]byte, 65536)
	for {
		n, _, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-r.closed:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		pkt := append([]byte(nil), buf[:n]...)
		r.mu.Lock()
		for c := range r.clients {
			select {
			case c.ch <- pkt:
			default:
				if c.internal {
					continue // a slow push loses packets, it is not dropped
				}
				// Client too slow: drop it.
				delete(r.clients, c)
				close(c.ch)
			}
		}
		r.mu.Unlock()
	}
}

// ServeHTTP streams MPEG-TS to one client until it disconnects.
func (r *TSRelay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	c := &relayClient{ch: make(chan []byte, 2048)}
	r.mu.Lock()
	r.clients[c] = struct{}{}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if _, ok := r.clients[c]; ok {
			delete(r.clients, c)
			close(c.ch)
		}
		r.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	ctx := req.Context()
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.closed:
			return
		case pkt, ok := <-c.ch:
			if !ok {
				return
			}
			if _, err := w.Write(pkt); err != nil {
				return
			}
			r.outBytes.Add(int64(len(pkt)))
		case <-t.C:
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// Close stops the relay and disconnects clients.
func (r *TSRelay) Close() error {
	r.closeOnce.Do(func() { close(r.closed) })
	return r.conn.Close()
}

// WaitClients is a test helper that waits for n clients.
func (r *TSRelay) WaitClients(ctx context.Context, n int) bool {
	for {
		if r.Viewers() >= n {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
}
