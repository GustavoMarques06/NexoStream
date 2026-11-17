package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"nexostream/media-engine/packaging"
	"nexostream/media-engine/pipeline"
)

// layoutFile records how many qualities a channel folder holds, so a switch
// between one quality and ABR starts from a clean folder (the packager
// appends to existing playlists).
const layoutFile = ".layout"

// prepareLayout creates the channel folder for n qualities (0 or 1 = single).
func prepareLayout(dir string, n int) error {
	want := strconv.Itoa(n)
	if b, err := os.ReadFile(filepath.Join(dir, layoutFile)); err == nil && strings.TrimSpace(string(b)) != want { //nolint:gosec // folder built by the worker
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // the HTTP origin (another user) reads HLS files
		return err
	}
	for i := 0; i < n; i++ {
		if err := os.MkdirAll(filepath.Join(dir, packaging.VariantDir(i)), 0o755); err != nil { //nolint:gosec // as above
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, layoutFile), []byte(want), 0o644) //nolint:gosec // as above
}

// masterVariants describes the qualities of an ABR plan for the master
// playlist (bandwidth = video peak + audio).
func masterVariants(p pipeline.Plan) []packaging.Variant {
	audio := p.Audio.BitrateBps
	if p.CopyAudio || audio == 0 {
		audio = 128000
	}
	audio *= max(p.AudioTracks, 1)
	out := make([]packaging.Variant, 0, len(p.Renditions))
	for _, r := range p.Renditions {
		peak := r.Video.MaxBitrateBps
		if peak == 0 {
			peak = r.Video.BitrateBps
		}
		out = append(out, packaging.Variant{Width: r.Video.Width, Height: r.Video.Height,
			BitrateBps: peak + audio, AvgBitrateBps: r.Video.BitrateBps + audio, Profile: r.Video.Profile})
	}
	return out
}

// mediaDir is the folder whose playlist the DVR records and the metrics
// read: the channel folder, or the top quality of an ABR channel.
func (r *runner) mediaDir() string {
	dir := r.a.hlsDir(r.spec.Channel)
	if r.procPlan != nil && len(r.procPlan.Renditions) > 1 {
		return filepath.Join(dir, packaging.VariantDir(0))
	}
	return dir
}
