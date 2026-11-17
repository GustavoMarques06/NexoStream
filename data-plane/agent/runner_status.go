package agent

import (
	"nexostream/internal/model"
	"nexostream/internal/protocol"
	"nexostream/internal/redact"
	"nexostream/media-engine/packaging"
	"strings"
	"time"
)

// Status of a channel on this node: what the runner reports to the Control
// Plane (the panel and the controller read it from NATS).

func (r *runner) updateStatus(now time.Time) {
	state, since, reason := r.m.State()
	st := model.ChannelStatus{
		ChannelID: r.id, State: state, Since: since, Reason: reason,
		WorkerID: r.a.cfg.WorkerID, Revision: r.spec.Channel.Revision,
		LastError: r.lastErr, UpdatedAt: now.UTC(),
	}
	if r.activeIdx >= 0 && r.activeIdx < len(r.sources) && state != model.StateStopped {
		s := r.sources[r.activeIdx]
		st.ActiveSource = &model.SourceRef{Priority: s.Priority, URL: redact.URL(s.URL)}
	}
	if r.procPlan != nil {
		st.Processing = r.procPlan.Processing
		p := r.procPlan
		pi := &model.PipelineInfo{Engine: r.a.engine.Name(), Processing: p.Processing, VideoCopy: p.CopyVideo, AudioCopy: p.CopyAudio,
			AudioTracks: p.AudioTracks, SubtitleTracks: p.SubtitleTracks, Notes: p.Notes, StartedAt: r.procStarted.UTC()}
		if !p.CopyVideo {
			v := p.Video
			pi.Video, pi.Encoder = &v, string(p.Encoder)
		}
		if !p.CopyAudio {
			a := p.Audio
			pi.Audio = &a
		}
		for _, rd := range p.Renditions {
			pi.Renditions = append(pi.Renditions, model.RenditionInfo{Profile: rd.ProfileID, Width: rd.Video.Width,
				Height: rd.Video.Height, BitrateBps: rd.Video.BitrateBps, Encoder: string(rd.Encoder)})
		}
		st.Pipeline = pi
	}
	for i, src := range r.sources {
		ss := model.SourceStatus{Priority: src.Priority, URL: redact.URL(src.URL), Active: i == r.activeIdx && state != model.StateStopped}
		if rc, ok := r.recv[i]; ok {
			ss.Listening = rc.cancel != nil
			ss.BitrateBps = int64(rc.rate.Current())
			if t := rc.an.Snapshot().LastPacketAt; !t.IsZero() {
				t = t.UTC()
				ss.LastPacketAt = &t
			}
			if e := rc.err.Load(); e != nil {
				ss.Error = *e
			}
		}
		st.Sources = append(st.Sources, ss)
	}
	if rc, ok := r.recv[r.activeIdx]; ok {
		s := rc.an.Snapshot()
		r.ccWin.Sample(now, s.CCErrors)
		cc, _ := r.ccWin.Delta()
		in := &model.InputStatus{BitrateBps: int64(rc.rate.Current()), CCErrorsLastMinute: cc, Audio: []model.AudioInfo{}}
		if !s.LastPacketAt.IsZero() {
			t := s.LastPacketAt.UTC()
			in.LastPacketAt = &t
		}
		if s.PMTSeen {
			r.maybeProbe(s.StreamsSignature)
		}
		r.probed.Lock()
		pv, pa, psig, pidx := r.probed.video, r.probed.audio, r.probed.sig, r.probed.idx
		r.probed.Unlock()
		probedHere := pidx == r.activeIdx && psig == s.StreamsSignature
		if v := s.Video(); v != nil {
			in.Video = &model.VideoInfo{Codec: v.Codec}
			if pv != nil && probedHere {
				in.Video = pv
			}
		}
		if pa != nil && probedHere {
			in.Audio = pa
		} else {
			for _, es := range s.Streams {
				if es.Kind == "audio" {
					in.Audio = append(in.Audio, model.AudioInfo{Codec: es.Codec, Language: es.Language})
				}
			}
		}
		for _, es := range s.Streams {
			if es.Codec == "dvb_subtitle" || es.Codec == "teletext" {
				in.Subtitles = append(in.Subtitles, model.SubtitleInfo{Codec: es.Codec, Language: es.Language})
			}
		}
		st.Input = in
	}
	outs := map[string]string{}
	ch := r.spec.Channel
	if ch.Outputs.HLS && r.a.conf().PublicHLSBase != "" {
		outs["hls"] = strings.TrimRight(r.a.conf().PublicHLSBase, "/") + "/" + r.id + "/" + packaging.PlaylistName
	}
	if ch.Outputs.DASH && !ch.DRM.Enabled && r.a.conf().PublicHLSBase != "" {
		outs["dash"] = strings.TrimRight(r.a.conf().PublicHLSBase, "/") + "/" + r.id + "/" + packaging.DASHDir + "/" + packaging.ManifestName
	}
	if ch.Outputs.MPEGTSHTTP && !ch.DRM.Enabled && r.a.conf().PublicTSBase != "" {
		outs["mpegts_http"] = strings.TrimRight(r.a.conf().PublicTSBase, "/") + "/" + r.id + "/stream.ts"
	}
	if (ch.Outputs.HLS || ch.DVR.Enabled) && r.a.conf().PublicHLSBase != "" {
		st.ThumbnailURL = strings.TrimRight(r.a.conf().PublicHLSBase, "/") + "/" + r.id + "/thumb.jpg"
	}
	if ch.DVR.Enabled && r.a.conf().PublicDVRBase != "" {
		outs["dvr"] = strings.TrimRight(r.a.conf().PublicDVRBase, "/") + "/" + r.id + "/"
	}
	if ch.Outputs.MPEGTSUDP != "" {
		outs["mpegts_udp"] = ch.Outputs.MPEGTSUDP
	}
	if len(outs) > 0 {
		st.Outputs = outs
	}
	st.Viewers = r.a.sessions.Count(r.id)
	for _, p := range r.pushers {
		st.Pushes = append(st.Pushes, p.status())
	}
	r.statusMu.Lock()
	r.status = st
	r.statusMu.Unlock()
}

func (r *runner) currentStatus() model.ChannelStatus {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	return r.status
}

func (r *runner) brief() protocol.ChannelBrief {
	st := r.currentStatus()
	return protocol.ChannelBrief{ID: r.id, Revision: st.Revision, State: st.State}
}

// publishStatus sends the status on changes and at least every 5 s.
func (r *runner) publishStatus(force bool) {
	st := r.currentStatus()
	changed := st.State != r.lastPubState || r.activeIdx != r.lastPubActive
	if !force && !changed && time.Since(r.lastPublished) < protocol.HeartbeatInterval {
		return
	}
	r.lastPublished, r.lastPubState, r.lastPubActive = time.Now(), st.State, r.activeIdx
	r.a.publishJSON(protocol.ChannelStatus(r.id), st)
}
