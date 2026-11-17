package dvr

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"

	"nexostream/internal/procutil"
)

// Export limits. Exporting copies packets (no re-encoding), but it still
// reads disk and holds a connection for as long as the client downloads.
const (
	DefaultMaxExport     = 2 * time.Hour
	DefaultMaxExportJobs = 2
)

// firstContinuous keeps the pieces up to the first hole: the timestamps of a
// recording restart after a gap, and one MP4 cannot carry that.
func firstContinuous(segs []Segment) []Segment {
	for i := 1; i < len(segs); i++ {
		if segs[i].Start.Sub(segs[i-1].End()) > MaxGap {
			return segs[:i]
		}
	}
	return segs
}

// pieceReader streams the pieces of an export, in order, from their hour
// files (each piece is a byte range of one file).
type pieceReader struct {
	segs   []Segment
	locate func(Segment) (string, bool)
	cur    io.Reader
	file   *os.File
}

func (p *pieceReader) Read(b []byte) (int, error) {
	for {
		if p.cur != nil {
			n, err := p.cur.Read(b)
			if err == io.EOF {
				p.closeCurrent()
				if n > 0 {
					return n, nil
				}
				continue
			}
			return n, err
		}
		if len(p.segs) == 0 {
			return 0, io.EOF
		}
		s := p.segs[0]
		p.segs = p.segs[1:]
		path, ok := p.locate(s)
		if !ok {
			continue // the hour was removed by the retention meanwhile
		}
		f, err := os.Open(path) //nolint:gosec // path comes from Storage.Locate, which stays inside the disk folder
		if err != nil {
			continue
		}
		p.file = f
		p.cur = io.NewSectionReader(f, s.Offset, s.Size)
	}
}

func (p *pieceReader) closeCurrent() {
	if p.file != nil {
		_ = p.file.Close()
	}
	p.file, p.cur = nil, nil
}

func (p *pieceReader) Close() { p.closeCurrent() }

// firstWrite sends the response headers when the first byte of the MP4 is
// ready, so an ffmpeg failure before that can still answer a proper error.
type firstWrite struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	headers func()
	started bool
}

// writeTimeout is how long a client may take to accept one block of the
// export; a stalled download must not hold an export slot and an ffmpeg.
const writeTimeout = 30 * time.Second

func (f *firstWrite) Write(b []byte) (int, error) {
	if !f.started {
		f.started = true
		f.headers()
	}
	_ = f.rc.SetWriteDeadline(time.Now().Add(writeTimeout)) // not supported by every writer; then no deadline
	n, err := f.w.Write(b)                                  //nolint:gosec // G705: MP4 bytes, not markup
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}

// exportMP4 streams the continuous part of [from, to) of quality key as a
// fragmented MP4, copying the recorded packets.
func (h Handler) exportMP4(w http.ResponseWriter, r *http.Request, st Storage, ch, key string, from, to time.Time) {
	if h.FFmpeg == "" {
		http.Error(w, "export is not available on this node", http.StatusNotImplemented)
		return
	}
	maxLen := h.MaxExport
	if maxLen <= 0 {
		maxLen = DefaultMaxExport
	}
	if !to.After(from) || to.Sub(from) > maxLen {
		http.Error(w, fmt.Sprintf("interval must be between 1 second and %s", maxLen), http.StatusBadRequest)
		return
	}
	segs, _ := st.List(key, from, to)
	segs = firstContinuous(segs)
	if len(segs) == 0 {
		http.Error(w, "no recording in this interval", http.StatusNotFound)
		return
	}
	jobs := h.MaxExportJobs
	if jobs <= 0 {
		jobs = DefaultMaxExportJobs
	}
	if !st.state().acquireExport(jobs) {
		w.Header().Set("Retry-After", "10")
		http.Error(w, "too many exports running, try again shortly", http.StatusServiceUnavailable)
		return
	}
	defer st.state().releaseExport()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, h.FFmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", //nolint:gosec // fixed arguments; ffmpeg path from the node configuration
		"-f", "mpegts", "-i", "pipe:0", "-map", "0:v?", "-map", "0:a?", "-c", "copy",
		// Recorded audio is always AAC (the HLS packager converts anything
		// else); MP4 needs it without the ADTS headers MPEG-TS carries.
		"-bsf:a", "aac_adtstoasc",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1")
	procutil.SetAttr(cmd)
	in := &pieceReader{segs: segs, locate: func(s Segment) (string, bool) { return st.Locate(key, st.URI(s)) }}
	defer in.Close()
	cmd.Stdin = in
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "export failed", http.StatusInternalServerError)
		return
	}
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		http.Error(w, "export failed", http.StatusInternalServerError)
		return
	}
	procutil.AfterStart(cmd)

	name := ch + "-" + strconv.FormatInt(segs[0].Start.Unix(), 10) + ".mp4"
	out := &firstWrite{w: w, rc: http.NewResponseController(w), headers: func() {
		hd := w.Header()
		hd.Set("Content-Type", "video/mp4")
		hd.Set("Content-Disposition", `attachment; filename="`+name+`"`)
		hd.Set("Cache-Control", "no-store")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Export-From", segs[0].Start.UTC().Format(time.RFC3339))
		hd.Set("X-Export-To", segs[len(segs)-1].End().UTC().Format(time.RFC3339))
	}}
	if _, err := io.Copy(out, stdout); err != nil {
		cancel() // the client is gone or too slow: stop ffmpeg instead of waiting on a full pipe
	}
	_ = cmd.Wait()
	if !out.started {
		http.Error(w, "export failed: "+stderr.String(), http.StatusInternalServerError)
	}
	// Once the headers are sent, a late failure can only truncate the download.
}

// limitedBuffer keeps the first bytes of ffmpeg's error output.
type limitedBuffer struct{ b []byte }

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := 512 - len(l.b); room > 0 {
		l.b = append(l.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string { return string(l.b) }
