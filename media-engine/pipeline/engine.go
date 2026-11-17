package pipeline

import (
	"context"
	"io"
	"time"

	"nexostream/media-engine/packaging"
)

// Spec is everything an engine needs to run one channel.
type Spec struct {
	ChannelID string
	Plan      Plan
	HLS       *packaging.HLSOptions // nil = no HLS output
	// DASH writes MPEG-DASH into DASH.Dir/dash (nil = no DASH output).
	DASH *packaging.HLSOptions
	// TSRelayURL receives MPEG-TS for the HTTP relay (udp://127.0.0.1:port).
	TSRelayURL string
	// UDPOutput is an optional external MPEG-TS/UDP destination.
	UDPOutput string
	// Overlay burns a logo into the video; ignored when the video is copied.
	Overlay *Overlay
}

// Overlay is a logo placed over the video.
type Overlay struct {
	Image    string  // absolute path of a PNG or JPEG on the node
	Position string  // top-left, top-right, bottom-left, bottom-right
	Margin   int     // pixels from the edges of the source picture
	Width    int     // logo width in source pixels (0 = as is)
	Opacity  float64 // 0 < opacity <= 1
}

// Progress is reported by the engine while it runs.
type Progress struct {
	OutTime     time.Duration // media time produced
	TotalSize   int64         // bytes written
	Speed       float64       // 1.0 = real time
	UpdatedAt   time.Time
	FramesTotal int64
}

// Engine starts media processes.
//
// The behaviors the agent relies on are executable: any implementation must
// pass pipelinetest.RunConformance (see the FFmpeg engine's test).
type Engine interface {
	Name() string
	// Start launches the pipeline. The caller writes the input MPEG-TS to
	// Process.Input(). Canceling ctx ends the process like Stop does.
	Start(ctx context.Context, spec Spec) (Process, error)
}

// Process is one running pipeline. Every method is safe for concurrent use,
// also after the process ended.
type Process interface {
	// Input is where the MPEG-TS goes. Writes fail (they never block) once
	// the process ended.
	Input() io.Writer
	// Done is closed when the process exits.
	Done() <-chan struct{}
	// Err returns why the process exited (nil if stopped on request).
	Err() error
	// Stop asks the process to finish and kills it after the timeout. It may
	// be called more than once.
	Stop(timeout time.Duration)
	// Progress follows the media written; it is zero until output starts.
	Progress() Progress
	// LastLog returns the last lines written by the engine (diagnostics).
	LastLog() []string
	PID() int
}
