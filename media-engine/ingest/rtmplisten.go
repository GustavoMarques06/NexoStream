package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"

	"nexostream/internal/procutil"
)

// MinRTMPKey is the shortest stream key accepted for an RTMP listener: the key
// is the only thing that keeps others from publishing into the channel.
const MinRTMPKey = 16

// rtmpListener receives an encoder that publishes to this node
// (rtmp://0.0.0.0:1935/<app>/<key>?mode=listener). One publisher at a time.
type rtmpListener struct {
	cfg      ReceiverConfig
	addr     SourceAddr
	app, key string
}

// splitRTMPPath returns the application and the stream key of an RTMP path:
// "/live/KEY" -> ("live", "KEY"); "/a/b/KEY" -> ("a/b", "KEY").
func splitRTMPPath(p string) (app, key string) {
	p = strings.Trim(p, "/")
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return "", p
	}
	return p[:i], p[i+1:]
}

func newRTMPListener(cfg ReceiverConfig, addr SourceAddr) (*rtmpListener, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil {
		return nil, err
	}
	app, key := splitRTMPPath(u.Path)
	if app == "" || key == "" {
		return nil, errors.New("rtmp listener needs rtmp://host:port/<app>/<key>?mode=listener")
	}
	return &rtmpListener{cfg: cfg, addr: addr, app: app, key: key}, nil
}

// remuxArgs makes ffmpeg turn the FLV on stdin into MPEG-TS on stdout, copying
// the streams. Only the pipe protocol is allowed.
func remuxArgs() []string {
	return []string{"-hide_banner", "-loglevel", "error", "-protocol_whitelist", "pipe",
		"-f", "flv", "-i", "pipe:0",
		"-map", "0:v:0?", "-map", "0:a:0?",
		"-c", "copy", "-f", "mpegts", "-muxdelay", "0", "-mpegts_flags", "+resend_headers", "pipe:1"}
}

// Run waits for a publisher, checks its application and key, and delivers its
// stream as MPEG-TS until it stops. Like the SRT listener (an ffmpeg that exits
// when the sender leaves), it returns when a publishing session ends, or when a
// publisher is refused (the runner logs it and opens the listener again after
// a pause, which also slows down key guessing). Connections that drop before
// asking to publish (port scanners, TCP health checks, broken clients) are
// closed and the listener keeps waiting. While a publisher is on, anyone else
// is turned away.
func (l *rtmpListener) Run(ctx context.Context, onData func([]byte)) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(l.addr.Host, strconv.Itoa(l.addr.Port)))
	if err != nil {
		return fmt.Errorf("rtmp listen: %w", err)
	}
	defer ln.Close()
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()

	conns := make(chan net.Conn)
	var busy atomic.Bool
	go func() {
		defer close(conns)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if !busy.CompareAndSwap(false, true) {
				_ = c.Close() // one publisher at a time
				continue
			}
			select {
			case conns <- c:
			case <-ctx.Done():
				_ = c.Close()
				return
			}
		}
	}()
	for {
		var conn net.Conn
		select {
		case <-ctx.Done():
			return nil
		case c, ok := <-conns:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("rtmp: listener closed")
			}
			conn = c
		}
		published, serr := l.session(ctx, conn, onData)
		remote := conn.RemoteAddr().String()
		_ = conn.Close()
		busy.Store(false)
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.Is(serr, ErrRTMPRefused):
			return fmt.Errorf("%w (from %s)", ErrRTMPRefused, remote)
		case published && serr != nil:
			return fmt.Errorf("rtmp publisher %s: %w", remote, serr)
		case published:
			return errors.New("rtmp: publisher " + remote + " stopped")
		}
		// Dropped before publishing: keep listening.
	}
}

// session serves one connection. published reports whether it got as far as
// publishing (and so whether ffmpeg ran).
func (l *rtmpListener) session(ctx context.Context, conn net.Conn, onData func([]byte)) (published bool, err error) {
	closeConn := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer closeConn()
	var (
		cmd     *exec.Cmd
		stdin   io.WriteCloser
		flv     *flvWriter
		readErr = make(chan error, 1)
	)
	allow := func(app, key string) bool {
		// Both are checked; the key in constant time.
		return app == l.app && keyMatches(l.key, key)
	}
	onPublish := func(rtmpPublish) error {
		path := l.cfg.FFmpegPath
		if path == "" {
			path = "ffmpeg"
		}
		cmd = exec.CommandContext(ctx, path, remuxArgs()...)
		procutil.SetAttr(cmd)
		var err error
		if stdin, err = cmd.StdinPipe(); err != nil {
			return err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start ffmpeg for rtmp: %w", err)
		}
		procutil.AfterStart(cmd)
		published = true
		flv = &flvWriter{w: stdin}
		go func() { readErr <- readAligned(stdout, onData) }()
		return nil
	}
	onMedia := func(typ byte, ts uint32, payload []byte) error {
		return flv.writeTag(typ, ts, payload)
	}
	err = serveRTMP(conn, allow, onPublish, onMedia)
	if published {
		_ = stdin.Close() // EOF: ffmpeg flushes and exits
		if rerr := <-readErr; rerr != nil && err == nil {
			err = fmt.Errorf("remux output: %w", rerr) // the stream broke on the ffmpeg side
		}
		_ = cmd.Wait()
	}
	return published, err
}
