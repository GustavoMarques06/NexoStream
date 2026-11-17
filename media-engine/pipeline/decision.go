// Package pipeline decides how a channel is processed and runs the media
// engine (FFmpeg in v1, behind the Engine interface so GStreamer can be added
// later; see docs/adr/0003).
package pipeline

import (
	"fmt"
	"sort"
	"strings"

	"nexostream/internal/model"
	"nexostream/media-engine/transcoding"
)

// Processing values reported in the channel status.
const (
	ProcessingPassthrough    = "passthrough"
	ProcessingTranscode      = "transcode"
	ProcessingAudioTranscode = "audio_transcode"
)

// Plan is the processing decision for the current input.
type Plan struct {
	Processing string
	Program    int
	HasVideo   bool
	HasAudio   bool
	CopyVideo  bool
	CopyAudio  bool
	// AudioTracks is how many audio tracks are kept (all of the input).
	AudioTracks int
	// SourceVideoCodec is the input video codec (MP4 tag when copied).
	SourceVideoCodec string
	// SubtitleTracks are DVB subtitles and teletext, copied to the MPEG-TS
	// outputs (HLS, DASH and FLV cannot carry them).
	SubtitleTracks int
	Video          model.VideoProfile // used when !CopyVideo
	Audio          model.AudioProfile // used when !CopyAudio
	Encoder        model.Encoder      // resolved encoder when !CopyVideo
	Notes          []string           // why the decision was taken
	// Renditions is the ABR ladder (2 or more qualities, highest first).
	// When set, Video/Encoder describe the first one.
	Renditions []Rendition
}

// Rendition is one quality of an ABR ladder.
type Rendition struct {
	ProfileID string
	Video     model.VideoProfile
	Encoder   model.Encoder
}

// Options tunes the automatic decision.
type Options struct {
	// AllowHEVCPassthrough keeps HEVC as is in auto mode. Off by default
	// because client support is not guaranteed (spec 5.4).
	AllowHEVCPassthrough bool
	// DefaultProfile is used in auto mode when video must be transcoded and
	// the channel has no profile.
	DefaultProfile *model.TranscodeProfile
	// Encoders available on the node.
	Encoders []string
	// Ladder are the profiles of an ABR channel (mode transcode with two or
	// more profiles); profile is then the first of them.
	Ladder []model.TranscodeProfile
}

// Input is what the analysis found in the source.
type Input struct {
	Program    int
	VideoCodec string // "" when there is no video
	AudioCodec string // "" when there is no audio
	// AudioCodecs lists every audio track (all are kept, like Flussonic).
	// Empty means one track with AudioCodec.
	AudioCodecs []string
	// SubtitleTracks counts DVB subtitle and teletext tracks.
	SubtitleTracks int
}

// Decide applies the rules of docs/architecture.md section 2.2:
//   - H.264 + AAC: passthrough.
//   - H.264 + MP2/AC-3: copy the video, convert only the audio to AAC.
//   - MPEG-2 or HEVC: transcode the video to H.264.
//   - The operator can force passthrough or transcode.
func Decide(mode model.ProcessingMode, in Input, profile *model.TranscodeProfile, opt Options) (Plan, error) {
	if len(in.AudioCodecs) == 0 && in.AudioCodec != "" {
		in.AudioCodecs = []string{in.AudioCodec}
	}
	if in.AudioCodec == "" && len(in.AudioCodecs) > 0 {
		in.AudioCodec = in.AudioCodecs[0]
	}
	p := Plan{Program: in.Program, HasVideo: in.VideoCodec != "", HasAudio: in.AudioCodec != "", AudioTracks: len(in.AudioCodecs),
		SubtitleTracks: in.SubtitleTracks, SourceVideoCodec: in.VideoCodec}
	if !p.HasVideo && !p.HasAudio {
		return p, fmt.Errorf("source has neither video nor audio")
	}
	if mode == "" {
		mode = model.ModeAuto
	}
	if mode == model.ModeTranscode && profile == nil {
		return p, fmt.Errorf("mode transcode requires a profile")
	}

	switch mode {
	case model.ModePassthrough:
		p.CopyVideo, p.CopyAudio = true, true
		p.Notes = append(p.Notes, "passthrough forced by the operator")
	case model.ModeTranscode:
		p.CopyVideo, p.CopyAudio = false, false
		p.Notes = append(p.Notes, "transcode forced by the operator")
	default:
		p.CopyVideo = videoCompatible(in.VideoCodec, opt)
		p.CopyAudio = true
		var convert []string
		for _, c := range in.AudioCodecs {
			if !audioCompatible(c) {
				p.CopyAudio = false
				convert = append(convert, c)
			}
		}
		if p.HasVideo && !p.CopyVideo {
			p.Notes = append(p.Notes, "video "+in.VideoCodec+" is transcoded to h264")
		}
		if p.HasAudio && !p.CopyAudio {
			p.Notes = append(p.Notes, "audio "+strings.Join(convert, ", ")+" is converted to aac")
		}
		if len(in.AudioCodecs) > 1 {
			p.Notes = append(p.Notes, fmt.Sprintf("%d audio tracks kept", len(in.AudioCodecs)))
		}
	}
	if !p.HasVideo {
		p.CopyVideo = true // nothing to encode
	}
	if !p.HasAudio {
		p.CopyAudio = true
	}

	if !p.CopyVideo {
		prof := profile
		if prof == nil {
			prof = opt.DefaultProfile
		}
		if prof == nil {
			return p, fmt.Errorf("video %s needs transcoding but no profile is configured", in.VideoCodec)
		}
		enc, err := transcoding.ResolveEncoder(prof.Encoder, opt.Encoders)
		if err != nil {
			return p, err
		}
		p.Video, p.Encoder = prof.Video, enc
		if profile != nil {
			p.Audio = profile.Audio
		}
	}
	if !p.CopyAudio {
		switch {
		case profile != nil:
			p.Audio = profile.Audio
		case p.Audio.Codec == "":
			p.Audio = transcoding.DefaultAudio
		}
	}

	if mode == model.ModeTranscode && len(opt.Ladder) > 1 && p.HasVideo {
		ladder := append([]model.TranscodeProfile(nil), opt.Ladder...)
		sort.SliceStable(ladder, func(i, j int) bool { return ladder[i].Video.Height > ladder[j].Video.Height })
		for _, lp := range ladder {
			enc, err := transcoding.ResolveEncoder(lp.Encoder, opt.Encoders)
			if err != nil {
				return p, fmt.Errorf("profile %s: %w", lp.ID, err)
			}
			p.Renditions = append(p.Renditions, Rendition{ProfileID: lp.ID, Video: lp.Video, Encoder: enc})
		}
		p.Video, p.Encoder = p.Renditions[0].Video, p.Renditions[0].Encoder
		p.Notes = append(p.Notes, fmt.Sprintf("ABR: %d qualities", len(p.Renditions)))
	}

	switch {
	case p.CopyVideo && p.CopyAudio:
		p.Processing = ProcessingPassthrough
	case p.CopyVideo:
		p.Processing = ProcessingAudioTranscode
	default:
		p.Processing = ProcessingTranscode
	}
	return p, nil
}

func videoCompatible(codec string, opt Options) bool {
	switch codec {
	case "", "h264":
		return true
	case "hevc":
		return opt.AllowHEVCPassthrough
	}
	return false
}

func audioCompatible(codec string) bool {
	return codec == "" || codec == "aac"
}
