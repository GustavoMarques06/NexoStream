package agent

import (
	"nexostream/media-engine/packaging"
	"path/filepath"
	"time"
)

// Prometheus series of a channel, updated by the runner loop.

func (r *runner) updateMetrics(now time.Time) {
	m := r.a.metrics
	if rc, ok := r.recv[r.activeIdx]; ok {
		s := rc.an.Snapshot()
		if d := s.Bytes - r.lastBytes; d > 0 {
			m.inputBytes.WithLabelValues(r.id).Add(float64(d))
		}
		if d := s.CCErrors - r.lastCC; d > 0 {
			m.ccErrors.WithLabelValues(r.id).Add(float64(d))
		}
		r.lastBytes, r.lastCC = s.Bytes, s.CCErrors
		if !s.LastPacketAt.IsZero() {
			m.lastPacket.WithLabelValues(r.id).Set(float64(s.LastPacketAt.UnixNano()) / 1e9)
		}
		m.bitrate.WithLabelValues(r.id).Set(rc.rate.Current())
	}
	if d := r.dropped.Load() - r.lastDropped; d > 0 {
		m.droppedBytes.WithLabelValues(r.id).Add(float64(d))
		r.lastDropped += d
	}
	if r.relay != nil {
		if d := r.relay.OutputBytes() - r.lastRelayOut; d > 0 {
			m.outputBytes.WithLabelValues(r.id).Add(float64(d))
			r.lastRelayOut += d
		}
	}
	m.viewers.WithLabelValues(r.id).Set(float64(r.a.sessions.Count(r.id)))
	ch := r.spec.Channel
	if (ch.Outputs.HLS || ch.DVR.Enabled) && now.Sub(r.playlistCheck) >= time.Second {
		r.playlistCheck = now
		if pi, err := packaging.ReadPlaylist(filepath.Join(r.mediaDir(), packaging.PlaylistName)); err == nil && pi.Segments > 0 {
			if r.lastSeq > 0 && pi.LastSequence > r.lastSeq {
				m.segments.WithLabelValues(r.id).Add(float64(pi.LastSequence - r.lastSeq))
			}
			r.lastSeq = pi.LastSequence
		}
	}
	state, _, _ := r.m.State()
	m.setState(r.id, state)
}
