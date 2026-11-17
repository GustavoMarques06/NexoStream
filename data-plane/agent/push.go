package agent

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"nexostream/internal/model"
	"nexostream/internal/procutil"
	"nexostream/internal/redact"
	"nexostream/media-engine/packaging"
)

// Push: each destination has its own ffmpeg, fed from the channel's TS relay
// and restarted with a growing wait (1 s to 30 s) when it fails, so a remote
// server that is down never affects the channel or the other destinations.
const (
	pushRunningAfter = 3 * time.Second
	pushMaxBackoff   = 30 * time.Second
)

type pusher struct {
	url    string
	relay  *packaging.TSRelay
	ffmpeg string
	log    *slog.Logger

	mu sync.Mutex
	st model.PushStatus

	cancel context.CancelFunc
	done   chan struct{}
}

// MaskURL hides the stream key (last path element and query) of a push URL.
func MaskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(url inválida)"
	}
	if u.RawQuery != "" {
		u.RawQuery = "…"
	}
	if i := strings.LastIndex(u.Path, "/"); i >= 0 && i < len(u.Path)-1 && strings.Count(u.Path, "/") > 1 {
		u.Path = u.Path[:i+1] + "••••"
	}
	u.User = nil
	return u.String()
}

// pushArgs returns the ffmpeg output for a destination.
func pushArgs(dst string) []string {
	switch {
	case strings.HasPrefix(dst, "rtmp://"), strings.HasPrefix(dst, "rtmps://"):
		// FLV carries one video and one audio track.
		return []string{"-map", "0:v:0?", "-map", "0:a:0?", "-c", "copy", "-f", "flv", "-flvflags", "no_duration_filesize", dst}
	default: // srt://
		if !strings.Contains(dst, "pkt_size=") {
			sep := "?"
			if strings.Contains(dst, "?") {
				sep = "&"
			}
			dst += sep + "pkt_size=1316"
		}
		return []string{"-map", "0", "-c", "copy", "-f", "mpegts", dst}
	}
}

func startPusher(dst string, relay *packaging.TSRelay, ffmpegPath string, log *slog.Logger) *pusher {
	ctx, cancel := context.WithCancel(context.Background())
	p := &pusher{url: dst, relay: relay, ffmpeg: ffmpegPath, log: log.With("push", MaskURL(dst)),
		cancel: cancel, done: make(chan struct{}),
		st: model.PushStatus{URL: MaskURL(dst), State: "connecting", Since: time.Now().UTC()}}
	go p.run(ctx)
	return p
}

func (p *pusher) set(f func(*model.PushStatus)) {
	p.mu.Lock()
	f(&p.st)
	p.mu.Unlock()
}

func (p *pusher) status() model.PushStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st
}

func (p *pusher) stop() {
	p.cancel()
	<-p.done
}

func (p *pusher) run(ctx context.Context) {
	defer close(p.done)
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := p.once(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		msg := "conexão encerrada"
		if err != nil {
			msg = err.Error()
		}
		p.log.Warn("push failed, retrying", "error", msg, "retry_in", backoff.String())
		p.set(func(s *model.PushStatus) {
			s.State, s.Since, s.LastError = "retrying", time.Now().UTC(), msg
			s.Restarts++
		})
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, pushMaxBackoff)
		p.set(func(s *model.PushStatus) { s.State, s.Since = "connecting", time.Now().UTC() })
	}
}

// once runs one ffmpeg until it exits.
func (p *pusher) once(ctx context.Context) error {
	ff := p.ffmpeg
	if ff == "" {
		ff = "ffmpeg"
	}
	args := append([]string{"-hide_banner", "-loglevel", "error", "-fflags", "+genpts+discardcorrupt", "-f", "mpegts", "-i", "pipe:0"}, pushArgs(p.url)...)
	cmd := exec.CommandContext(ctx, ff, args...) //nolint:gosec // G204: destination set by an operator, ffmpeg path from the node configuration
	procutil.SetAttr(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	procutil.AfterStart(cmd)
	feed, unsubscribe := p.relay.Subscribe()
	var lastLine string
	var wg sync.WaitGroup
	wg.Add(2)
	started := time.Now()
	go func() { // relay -> ffmpeg
		defer wg.Done()
		defer stdin.Close()
		for b := range feed {
			if _, err := stdin.Write(b); err != nil {
				for range feed { // drain until unsubscribed
				}
				return
			}
			// Running = data has been flowing to the destination for a while.
			p.set(func(s *model.PushStatus) {
				s.Bytes += int64(len(b))
				if s.State != "running" && time.Since(started) > pushRunningAfter {
					s.State, s.Since, s.LastError = "running", time.Now().UTC(), ""
				}
			})
		}
	}()
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); l != "" {
				lastLine = redact.Text(l) // the destination, with its stream key, is often in the message
			}
		}
		_, _ = io.Copy(io.Discard, stderr)
	}()
	err = cmd.Wait()
	unsubscribe()
	wg.Wait()
	if lastLine != "" {
		return &pushError{msg: lastLine}
	}
	return err
}

type pushError struct{ msg string }

func (e *pushError) Error() string { return e.msg }
