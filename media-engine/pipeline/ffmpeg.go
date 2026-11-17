package pipeline

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"nexostream/internal/procutil"
	"nexostream/internal/redact"
	"nexostream/media-engine/packaging"
	"nexostream/media-engine/transcoding"
)

// FFmpeg runs one ffmpeg process per channel. The agent writes the received
// MPEG-TS to the process stdin; ffmpeg remuxes or transcodes it and writes
// every output through the tee muxer, so the input is decoded and encoded
// only once.
type FFmpeg struct {
	Path   string // ffmpeg binary
	Logger *slog.Logger
}

// Name implements Engine.
func (f *FFmpeg) Name() string { return "ffmpeg" }

// overlayGraph returns the filters that put the logo (input 1) on video.
func overlayGraph(video string, o Overlay) string {
	logo := "[1:v]"
	var chain []string
	if o.Width > 0 {
		chain = append(chain, "scale="+strconv.Itoa(o.Width)+":-1")
	}
	if o.Opacity > 0 && o.Opacity < 1 {
		chain = append(chain, "format=rgba", "colorchannelmixer=aa="+strconv.FormatFloat(o.Opacity, 'f', 2, 64))
	}
	g := ""
	if len(chain) > 0 {
		g = logo + strings.Join(chain, ",") + "[logo];"
		logo = "[logo]"
	}
	m := strconv.Itoa(max(o.Margin, 0))
	x, y := "W-w-"+m, m // top-right
	switch o.Position {
	case "top-left":
		x, y = m, m
	case "bottom-left":
		x, y = m, "H-h-"+m
	case "bottom-right":
		x, y = "W-w-"+m, "H-h-"+m
	}
	return g + video + logo + "overlay=" + x + ":" + y
}

// BuildArgs returns the ffmpeg command line for a spec.
func BuildArgs(spec Spec) ([]string, error) {
	p := spec.Plan
	args := []string{
		"-hide_banner", "-loglevel", "warning", "-nostats",
		"-progress", "pipe:1", "-stats_period", "1",
		"-fflags", "+genpts+discardcorrupt",
		// The agent starts the pipeline as soon as the PMT is seen, usually
		// mid-GOP: give ffmpeg time to reach a keyframe (SPS/PPS) before it
		// writes the output headers, or it fails with "Could not write header".
		"-analyzeduration", "10000000", "-probesize", "20000000",
		"-f", "mpegts", "-i", "pipe:0",
	}
	sel := "0:"
	if p.Program > 0 {
		sel = "0:p:" + strconv.Itoa(p.Program) + ":"
	}
	abr := len(p.Renditions) > 1
	// The logo needs decoded frames: it only applies when the video is encoded.
	overlay := spec.Overlay != nil && p.HasVideo && (abr || !p.CopyVideo)
	video := "[" + sel + "v:0]" // the picture the encoders start from
	var pre string              // filter graph that puts the logo on it
	if overlay {
		// A one-frame input: overlay keeps showing its last frame (repeatlast),
		// so the image is decoded once, whatever the timestamps of the live input.
		args = append(args, "-i", spec.Overlay.Image)
		pre = overlayGraph(video, *spec.Overlay) + "[ov]"
		video = "[ov]"
	}
	if abr {
		// One decode, one scaler chain per quality.
		var fc strings.Builder
		if pre != "" {
			fc.WriteString(pre + ";")
		}
		fmt.Fprintf(&fc, "%ssplit=%d", video, len(p.Renditions))
		for i := range p.Renditions {
			fmt.Fprintf(&fc, "[s%d]", i)
		}
		for i, r := range p.Renditions {
			fmt.Fprintf(&fc, ";[s%d]%s[v%d]", i, transcoding.VideoFilter(r.Video), i)
		}
		args = append(args, "-filter_complex", fc.String())
		for i := range p.Renditions {
			args = append(args, "-map", "[v"+strconv.Itoa(i)+"]")
		}
	} else if overlay {
		args = append(args, "-filter_complex", pre+";"+video+transcoding.VideoFilter(p.Video)+"[vout]", "-map", "[vout]")
	} else if p.HasVideo {
		args = append(args, "-map", sel+"v:0")
	}
	if p.HasAudio {
		if p.AudioTracks > 1 {
			args = append(args, "-map", sel+"a") // every audio track (languages, AD)
		} else {
			args = append(args, "-map", sel+"a:0")
		}
	}
	if p.SubtitleTracks > 0 {
		args = append(args, "-map", sel+"s?", "-c:s", "copy")
	}
	if abr {
		for i, r := range p.Renditions {
			args = append(args, transcoding.VideoArgsFor(r.Video, r.Encoder, i)...)
		}
	} else if p.HasVideo {
		switch {
		case p.CopyVideo:
			args = append(args, "-c:v", "copy")
		case overlay: // the scaler is already in the filter graph
			args = append(args, transcoding.VideoArgs(p.Video, p.Encoder)...)
		default:
			args = append(args, "-vf", transcoding.VideoFilter(p.Video))
			args = append(args, transcoding.VideoArgs(p.Video, p.Encoder)...)
		}
	}
	if p.HasAudio {
		if p.CopyAudio {
			args = append(args, "-c:a", "copy")
		} else {
			args = append(args, transcoding.AudioArgs(p.Audio)...)
		}
	}
	if spec.DASH != nil {
		// Copied streams keep their MPEG-TS codec tag, which the MP4 inside
		// DASH rejects; MPEG-TS outputs ignore the tag (verified with ffmpeg 9.0).
		vtag := "avc1"
		if p.CopyVideo && p.SourceVideoCodec == "hevc" {
			vtag = "hvc1"
		}
		if p.HasVideo {
			args = append(args, "-tag:v", vtag)
		}
		if p.HasAudio {
			args = append(args, "-tag:a", "mp4a")
		}
	}
	var slaves []string
	ts := func(s string) string {
		if abr {
			return packaging.SelectFirstVideo(s) // MPEG-TS carries the top quality (and the subtitles)
		}
		return s
	}
	if spec.HLS != nil {
		switch {
		case abr:
			slaves = append(slaves, spec.HLS.ABRTeeSlaves(len(p.Renditions))...)
		case p.SubtitleTracks > 0:
			slaves = append(slaves, packaging.SelectAV(spec.HLS.TeeSlave()))
		default:
			slaves = append(slaves, spec.HLS.TeeSlave())
		}
	}
	if spec.DASH != nil {
		slaves = append(slaves, spec.DASH.DASHTeeSlave())
	}
	if spec.TSRelayURL != "" {
		slaves = append(slaves, ts(packaging.UDPTeeSlave(spec.TSRelayURL)))
	}
	if spec.UDPOutput != "" {
		slaves = append(slaves, ts(packaging.UDPTeeSlave(spec.UDPOutput)))
	}
	if len(slaves) == 0 {
		return nil, errors.New("no output enabled")
	}
	args = append(args, "-max_muxing_queue_size", "1024", "-f", "tee", strings.Join(slaves, "|"))
	return args, nil
}

// Start implements Engine.
func (f *FFmpeg) Start(ctx context.Context, spec Spec) (Process, error) {
	args, err := BuildArgs(spec)
	if err != nil {
		return nil, err
	}
	path := f.Path
	if path == "" {
		path = "ffmpeg"
	}
	lg := f.Logger
	if lg == nil {
		lg = slog.Default()
	}
	lg = lg.With("channel_id", spec.ChannelID)

	cmd := exec.Command(path, args...)
	procutil.SetAttr(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	procutil.AfterStart(cmd)
	lg.Info("pipeline started", "pid", cmd.Process.Pid, "processing", spec.Plan.Processing, "args", strings.Join(args, " "))

	p := &ffProcess{cmd: cmd, stdin: stdin, done: make(chan struct{}), log: lg}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); p.readProgress(stdout) }()
	go func() { defer wg.Done(); p.readLog(stderr) }()
	go func() {
		wg.Wait() // pipes must be drained before Wait
		err := cmd.Wait()
		p.mu.Lock()
		if !p.stopping && err == nil {
			err = errors.New("ffmpeg exited")
		}
		if p.stopping {
			err = nil
		}
		p.err = err
		p.mu.Unlock()
		close(p.done)
	}()
	// Canceling ctx ends the pipeline like Stop does (Engine contract).
	go func() {
		select {
		case <-ctx.Done():
			p.Stop(5 * time.Second)
		case <-p.done:
		}
	}()
	return p, nil
}

type ffProcess struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	done     chan struct{}
	log      *slog.Logger
	mu       sync.Mutex
	err      error
	stopping bool
	progress Progress
	lines    []string
}

func (p *ffProcess) Input() io.Writer      { return p.stdin }
func (p *ffProcess) Done() <-chan struct{} { return p.done }
func (p *ffProcess) PID() int              { return p.cmd.Process.Pid }

func (p *ffProcess) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *ffProcess) Progress() Progress {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.progress
}

func (p *ffProcess) LastLog() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.lines...)
}

func (p *ffProcess) Stop(timeout time.Duration) {
	p.mu.Lock()
	p.stopping = true
	p.mu.Unlock()
	_ = p.stdin.Close() // EOF: ffmpeg flushes and exits
	select {
	case <-p.done:
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// readProgress parses "-progress" key=value blocks.
func (p *ffProcess) readProgress(r io.Reader) {
	sc := bufio.NewScanner(r)
	var cur Progress
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "out_time_us", "out_time_ms": // both are microseconds in ffmpeg
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				cur.OutTime = time.Duration(n) * time.Microsecond
			}
		case "total_size":
			cur.TotalSize, _ = strconv.ParseInt(v, 10, 64)
		case "frame":
			cur.FramesTotal, _ = strconv.ParseInt(v, 10, 64)
		case "speed":
			cur.Speed, _ = strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "x"), 64)
		case "progress":
			cur.UpdatedAt = time.Now()
			p.mu.Lock()
			p.progress = cur
			p.mu.Unlock()
		}
	}
}

// readLog keeps the last lines of stderr and forwards them to the logger
// with a rate limit, so a broken input cannot flood the journal.
func (p *ffProcess) readLog(r io.Reader) {
	sc := bufio.NewScanner(r)
	windowStart := time.Now()
	n := 0
	for sc.Scan() {
		// ffmpeg repeats the addresses it opens, stream keys included.
		line := redact.Text(strings.TrimSpace(sc.Text()))
		if line == "" {
			continue
		}
		p.mu.Lock()
		p.lines = append(p.lines, line)
		if len(p.lines) > 50 {
			p.lines = p.lines[len(p.lines)-50:]
		}
		p.mu.Unlock()
		if time.Since(windowStart) > 10*time.Second {
			windowStart, n = time.Now(), 0
		}
		if n++; n <= 20 {
			p.log.Warn("ffmpeg", "line", line)
		}
	}
}
