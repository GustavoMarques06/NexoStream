package ingest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"nexostream/internal/procutil"
	"nexostream/internal/redact"
)

// puller receives a non-UDP source (SRT, RTMP, RTSP, HTTP/HLS) by running
// ffmpeg, which remuxes it to MPEG-TS on stdout without re-encoding.
type puller struct {
	cfg  ReceiverConfig
	addr SourceAddr
}

func newPuller(cfg ReceiverConfig, addr SourceAddr) *puller {
	return &puller{cfg: cfg, addr: addr}
}

// protocolWhitelist lists the protocols ffmpeg may open while pulling a source
// of the given scheme, including the transports it uses underneath.
func protocolWhitelist(scheme string) string {
	switch scheme {
	case "rtsp":
		return "rtsp,rtsps,rtp,srtp,udp,tcp,tls,crypto"
	case "rtmp", "rtmps":
		return "rtmp,rtmps,rtmpt,rtmpts,tcp,tls"
	case "srt":
		return "srt,udp"
	default: // http, https (HLS playlists, segments and AES-128 keys)
		return "http,https,tcp,tls,crypto"
	}
}

// PullArgs returns the ffmpeg command line used to pull a source.
func PullArgs(rawURL string, addr SourceAddr) []string {
	// The whitelist keeps a hostile playlist or SDP from making ffmpeg read
	// local files (file:, concat:, subfile:) or reach other protocols.
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-fflags", "+genpts",
		"-protocol_whitelist", protocolWhitelist(addr.Scheme)}
	switch addr.Scheme {
	case "rtsp":
		args = append(args, "-rtsp_transport", "tcp", "-timeout", "10000000")
	case "http", "https":
		args = append(args, "-rw_timeout", "10000000", "-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_delay_max", "5")
		if strings.Contains(strings.ToLower(rawURL), ".m3u8") {
			// Read HLS at native speed (also works for VOD test playlists).
			args = append(args, "-re", "-live_start_index", "-3")
		}
	case "rtmp", "rtmps":
		args = append(args, "-rw_timeout", "10000000")
	}
	// One video and one audio only: an HLS master playlist exposes every
	// variant as separate streams, and unmapped variants are not downloaded.
	return append(args,
		"-i", rawURL,
		"-map", "0:v:0?", "-map", "0:a:0?",
		"-c", "copy", "-f", "mpegts", "-muxdelay", "0", "-mpegts_flags", "+resend_headers",
		"pipe:1")
}

// Run starts ffmpeg and streams its MPEG-TS output until ctx is canceled or
// ffmpeg exits (the runner restarts failed receivers).
func (p *puller) Run(ctx context.Context, onData func([]byte)) error {
	path := p.cfg.FFmpegPath
	if path == "" {
		path = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, path, PullArgs(p.cfg.URL, p.addr)...)
	procutil.SetAttr(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg to pull %s: %w", p.addr.Scheme, err)
	}
	procutil.AfterStart(cmd)

	var mu sync.Mutex
	var last string
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); l != "" {
				mu.Lock()
				last = redact.Text(l)
				mu.Unlock()
			}
		}
	}()

	err = readAligned(stdout, onData)
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	switch {
	case last != "":
		return fmt.Errorf("pull %s: %s", p.addr.Scheme, last)
	case werr != nil:
		return fmt.Errorf("pull %s: %w", p.addr.Scheme, werr)
	case err != nil:
		return fmt.Errorf("pull %s: %w", p.addr.Scheme, err)
	}
	return errors.New("pull " + p.addr.Scheme + ": source ended")
}

// readAligned reads MPEG-TS and delivers whole packets starting on a sync
// byte, resynchronizing if the stream is ever misaligned.
func readAligned(r io.Reader, onData func([]byte)) error {
	buf := make([]byte, 0, PacketSize*64)
	chunk := make([]byte, PacketSize*56)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			start := 0
			for start < len(buf) && buf[start] != syncByte {
				start++
			}
			whole := (len(buf) - start) / PacketSize * PacketSize
			if whole > 0 {
				onData(buf[start : start+whole])
			}
			buf = append(buf[:0], buf[start+whole:]...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
