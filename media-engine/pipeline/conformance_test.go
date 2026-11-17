package pipeline_test

import (
	"os/exec"
	"testing"

	"nexostream/internal/logging"
	"nexostream/media-engine/packaging"
	"nexostream/media-engine/pipeline"
	"nexostream/media-engine/pipeline/pipelinetest"
)

// TestFFmpegEngineConforms runs the engine contract suite against the real
// ffmpeg: whatever replaces it later must pass the same checks. Skipped
// without ffmpeg.
func TestFFmpegEngineConforms(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	pipelinetest.RunConformance(t, pipelinetest.Conformance{
		New: func(*testing.T) pipeline.Engine { return &pipeline.FFmpeg{Path: ff, Logger: logging.Discard()} },
		Spec: func(_ *testing.T, dir string) pipeline.Spec {
			hls := packaging.HLSOptions{Dir: dir, SegmentSeconds: 2, ListSize: 5}
			return pipeline.Spec{
				ChannelID: "conformance",
				Plan:      pipeline.Plan{Processing: pipeline.ProcessingPassthrough, Program: 1, HasVideo: true, HasAudio: true, CopyVideo: true, CopyAudio: true},
				HLS:       &hls,
			}
		},
		Input: func(t *testing.T) []byte {
			// 12 s of H.264 + AAC in MPEG-TS, like a live source would send. The
			// engine probes up to 10 s of input before opening its output (to
			// reach a keyframe on a live stream), so the sample must be longer.
			out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error",
				"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
				"-t", "12", "-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-c:a", "aac", "-b:a", "64k",
				"-f", "mpegts", "pipe:1").Output()
			if err != nil || len(out) < 188*100 {
				t.Fatalf("could not generate the sample stream: %v (%d bytes)", err, len(out))
			}
			return out
		},
		ProducesHLS: true,
	})
}
