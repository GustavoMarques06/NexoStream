package ingest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"golang.org/x/net/ipv4"
)

// ReceiverConfig configures a source receiver.
type ReceiverConfig struct {
	URL       string
	Interface string // network interface for multicast joins (e.g. eno2)
	// ReadBuffer is the socket receive buffer (SO_RCVBUF). The kernel caps it
	// at net.core.rmem_max (see deploy/configs/sysctl).
	ReadBuffer int
	// FFmpegPath pulls non-UDP sources (SRT, RTMP, RTSP, HTTP/HLS).
	FFmpegPath string
}

// Receiver reads MPEG-TS datagrams from a UDP source.
type Receiver struct {
	cfg     ReceiverConfig
	addr    SourceAddr
	dgrams  atomic.Int64
	strayed atomic.Int64
}

// NewReceiver validates the configuration.
func NewReceiver(cfg ReceiverConfig) (*Receiver, error) {
	addr, err := ParseSourceURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	if addr.Pulled {
		return nil, fmt.Errorf("%s is not received over UDP: use ingest.Open", addr.Scheme)
	}
	if cfg.ReadBuffer <= 0 {
		cfg.ReadBuffer = 4 << 20
	}
	return &Receiver{cfg: cfg, addr: addr}, nil
}

// Datagrams returns how many datagrams were received.
func (r *Receiver) Datagrams() int64 { return r.dgrams.Load() }

// Run receives until ctx is canceled. onData is called with whole TS
// packets (RTP headers stripped); the slice is only valid during the call.
func (r *Receiver) Run(ctx context.Context, onData func([]byte)) error {
	conn, err := r.listen()
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		_ = conn.SetReadDeadline(time.Now())
	}()

	buf := make([]byte, 65536)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return fmt.Errorf("read %s: %w", r.cfg.URL, err)
		}
		r.dgrams.Add(1)
		if ts := tsPayload(buf[:n]); len(ts) > 0 {
			onData(ts)
		} else {
			r.strayed.Add(1)
		}
	}
}

func (r *Receiver) listen() (net.PacketConn, error) {
	conn, err := listenSocket(r.addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", r.cfg.URL, err)
	}
	if uc, ok := conn.(*net.UDPConn); ok {
		_ = uc.SetReadBuffer(r.cfg.ReadBuffer)
	}
	if r.addr.Multicast {
		var ifi *net.Interface
		if r.cfg.Interface != "" {
			ifi, err = net.InterfaceByName(r.cfg.Interface)
			if err != nil {
				_ = conn.Close()
				return nil, fmt.Errorf("interface %q: %w", r.cfg.Interface, err)
			}
		}
		pc := ipv4.NewPacketConn(conn)
		if err := pc.JoinGroup(ifi, &net.UDPAddr{IP: r.addr.IP}); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("join multicast %s on %q: %w", r.addr.IP, r.cfg.Interface, err)
		}
	}
	return conn, nil
}

// tsPayload returns the aligned TS packets of a datagram, stripping an RTP
// header when present.
func tsPayload(b []byte) []byte {
	if len(b) >= PacketSize && b[0] == syncByte {
		return b[:len(b)-len(b)%PacketSize]
	}
	// RTP: version 2, payload type 33 (MP2T) or dynamic.
	if len(b) >= 12 && b[0]>>6 == 2 {
		h := 12 + 4*int(b[0]&0x0F)
		if b[0]&0x10 != 0 && len(b) >= h+4 {
			h += 4 + 4*(int(b[h+2])<<8|int(b[h+3]))
		}
		if len(b) >= h+PacketSize && b[h] == syncByte {
			p := b[h:]
			return p[:len(p)-len(p)%PacketSize]
		}
	}
	return nil
}
