package agent

import (
	"path/filepath"
	"time"

	"nexostream/media-engine/dvr"
	"nexostream/media-engine/packaging"
)

// dvrRecorder copies the live HLS segments of one channel into the DVR. It
// has its own step in the runner loop and its own state, so changes to the
// metrics or the status cannot affect what is recorded.
//
// An ABR channel with dvr.all_qualities records every quality; otherwise only
// the highest (quality 0).
type dvrRecorder struct {
	r *runner

	checked   time.Time     // last time the playlists were looked at
	last      map[int]int64 // newest live segment already recorded, per quality
	qualities int           // qualities announced in the DVR folder (-1 = nothing written yet)
	err       string        // last recording error, to log each distinct one once
}

func newDVRRecorder(r *runner) *dvrRecorder {
	return &dvrRecorder{r: r, last: map[int]int64{}, qualities: -1}
}

// tick records what is new, at most once per second.
func (d *dvrRecorder) tick(now time.Time) {
	r := d.r
	if !r.spec.Channel.DVR.Enabled || now.Sub(d.checked) < time.Second {
		return
	}
	d.checked = now
	pi, err := packaging.ReadPlaylist(filepath.Join(r.mediaDir(), packaging.PlaylistName))
	if err != nil || pi.Segments == 0 {
		return
	}
	d.record(pi, now)
}

// record copies the new live segments into the DVR folder. pi is the playlist
// of quality 0; the other qualities are read here.
func (d *dvrRecorder) record(pi packaging.PlaylistInfo, now time.Time) {
	r := d.r
	st := r.a.dvrStorage(r.spec)
	n := 1
	if r.procPlan != nil && len(r.procPlan.Renditions) > 1 && r.spec.Channel.DVR.AllQualities {
		n = len(r.procPlan.Renditions)
	}
	if n != d.qualities {
		var vs []packaging.Variant
		if n > 1 {
			vs = masterVariants(*r.procPlan)
		}
		if err := st.WriteVariants(r.id, vs); err != nil {
			r.log.Error("dvr qualities file", "error", err)
		} else {
			d.qualities = n
		}
	}
	d.recordQuality(st, 0, pi, now)
	for i := 1; i < n; i++ {
		q, err := packaging.ReadPlaylist(filepath.Join(r.a.hlsDir(r.spec.Channel), packaging.VariantDir(i), packaging.PlaylistName))
		if err != nil || q.Segments == 0 {
			continue
		}
		d.recordQuality(st, i, q, now)
	}
}

// recordQuality records the new segments of quality i (0 = highest).
func (d *dvrRecorder) recordQuality(st dvr.Storage, i int, pi packaging.PlaylistInfo, now time.Time) {
	r := d.r
	last, ok := d.last[i]
	if !ok {
		last = -1
	}
	if pi.LastSequence < last-2 {
		last = -1 // numbering restarted (playlist recreated)
	}
	dir := r.mediaDir()
	if i > 0 {
		dir = filepath.Join(r.a.hlsDir(r.spec.Channel), packaging.VariantDir(i))
	}
	key := dvr.VariantKey(r.id, i)
	for _, e := range pi.Entries {
		if e.Seq <= last || e.Duration <= 0 {
			continue
		}
		start := e.PDT
		if start.IsZero() {
			start = now.Add(-e.Duration)
		}
		src := filepath.Join(dir, filepath.FromSlash(e.URI))
		if _, err := st.Record(key, src, start, e.Duration); err != nil {
			if err.Error() != d.err {
				r.log.Error("dvr record failed", "error", err, "mode", st.Mode, "disks", st.Disks, "quality", i)
			}
			d.err = err.Error()
			continue
		}
		d.err = ""
		r.a.metrics.dvrSegments.WithLabelValues(r.id).Inc()
		last = e.Seq
	}
	d.last[i] = last
}
