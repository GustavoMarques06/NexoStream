// Package transcoding turns transcode profiles into encoder arguments for
// CPU (libx264), NVIDIA NVENC and Intel Quick Sync. Hardware encoder
// arguments must be validated on the lab hardware (spec section 6).
package transcoding

import (
	"fmt"
	"strconv"
	"strings"

	"nexostream/internal/model"
)

// ResolveEncoder picks the encoder for a profile given what the node offers.
// "auto" prefers NVENC, then Quick Sync, then CPU.
func ResolveEncoder(requested model.Encoder, available []string) (model.Encoder, error) {
	has := func(e model.Encoder) bool {
		for _, a := range available {
			if a == string(e) {
				return true
			}
		}
		return false
	}
	if requested == "" || requested == model.EncoderAuto {
		for _, e := range []model.Encoder{model.EncoderNVENC, model.EncoderQSV, model.EncoderCPU} {
			if has(e) {
				return e, nil
			}
		}
		return model.EncoderCPU, nil
	}
	if !has(requested) {
		return "", fmt.Errorf("encoder %q is not available on this node (available: %v)", requested, available)
	}
	return requested, nil
}

// ValidateProfile checks a profile before it is saved.
func ValidateProfile(p model.TranscodeProfile) error {
	v, a := p.Video, p.Audio
	switch {
	case p.ID == "":
		return fmt.Errorf("id is required")
	case v.Codec != "h264":
		return fmt.Errorf("video.codec must be h264 in v1")
	case v.Profile != "" && v.Profile != "high" && v.Profile != "main" && v.Profile != "baseline":
		return fmt.Errorf("video.profile must be high, main or baseline")
	case v.Width <= 0 || v.Height <= 0 || v.Width%2 != 0 || v.Height%2 != 0:
		return fmt.Errorf("video.width and video.height must be positive even numbers")
	case v.BitrateBps < 100_000:
		return fmt.Errorf("video.bitrate_bps must be at least 100000")
	case v.MaxBitrateBps != 0 && v.MaxBitrateBps < v.BitrateBps:
		return fmt.Errorf("video.max_bitrate_bps must be >= video.bitrate_bps")
	case v.GOPSeconds <= 0 || v.GOPSeconds > 10:
		return fmt.Errorf("video.gop_seconds must be between 0 and 10")
	case v.Deinterlace != "" && v.Deinterlace != "auto" && v.Deinterlace != "on" && v.Deinterlace != "off":
		return fmt.Errorf("video.deinterlace must be auto, on or off")
	case a.Codec != "aac":
		return fmt.Errorf("audio.codec must be aac in v1")
	case a.BitrateBps < 16_000:
		return fmt.Errorf("audio.bitrate_bps must be at least 16000")
	case a.SampleRate != 0 && a.SampleRate != 44100 && a.SampleRate != 48000:
		return fmt.Errorf("audio.sample_rate must be 44100 or 48000")
	case a.Channels < 0 || a.Channels > 6:
		return fmt.Errorf("audio.channels must be between 1 and 6")
	case !p.Encoder.Valid() && p.Encoder != "":
		return fmt.Errorf("encoder must be auto, cpu, nvenc or qsv")
	}
	return nil
}

// VideoFilter returns the -vf chain: deinterlace, scale keeping the aspect
// ratio with padding, square pixels.
func VideoFilter(v model.VideoProfile) string {
	f := ""
	switch v.Deinterlace {
	case "on":
		f = "yadif=mode=send_frame:deint=all,"
	case "off":
	default: // auto: only frames flagged as interlaced
		f = "yadif=mode=send_frame:deint=interlaced,"
	}
	w, h := strconv.Itoa(v.Width), strconv.Itoa(v.Height)
	f += "scale=" + w + ":" + h + ":force_original_aspect_ratio=decrease," +
		"pad=" + w + ":" + h + ":(ow-iw)/2:(oh-ih)/2,setsar=1"
	return f
}

// VideoArgs returns the encoder arguments (without -map and -vf).
// Keyframes are forced every GOP seconds independently of the input FPS, and
// scene-cut keyframes are disabled, so HLS segments stay aligned (spec 6.1).
func VideoArgs(v model.VideoProfile, enc model.Encoder) []string {
	br := strconv.Itoa(v.BitrateBps)
	maxr := v.MaxBitrateBps
	if maxr == 0 {
		maxr = v.BitrateBps
	}
	mr := strconv.Itoa(maxr)
	buf := strconv.Itoa(maxr * 2)
	gop := strconv.FormatFloat(v.GOPSeconds, 'f', -1, 64)
	prof := v.Profile
	if prof == "" {
		prof = "high"
	}
	kf := []string{"-force_key_frames", "expr:gte(t,n_forced*" + gop + ")"}
	switch enc {
	case model.EncoderNVENC:
		preset := "p4"
		if v.Preset != "" && v.Preset[0] == 'p' {
			preset = v.Preset
		}
		args := []string{"-c:v", "h264_nvenc", "-preset", preset, "-profile:v", prof,
			"-rc", "cbr", "-b:v", br, "-maxrate", mr, "-bufsize", buf,
			"-no-scenecut", "1", "-forced-idr", "1"}
		return append(args, kf...)
	case model.EncoderQSV:
		args := []string{"-c:v", "h264_qsv", "-profile:v", prof,
			"-b:v", br, "-maxrate", mr, "-bufsize", buf, "-look_ahead", "0", "-forced_idr", "1"}
		return append(args, kf...)
	default:
		preset := v.Preset
		if preset == "" {
			preset = "veryfast"
		}
		args := []string{"-c:v", "libx264", "-preset", preset, "-profile:v", prof,
			"-b:v", br, "-maxrate", mr, "-bufsize", buf,
			"-sc_threshold", "0", "-pix_fmt", "yuv420p"}
		return append(args, kf...)
	}
}

// AudioArgs returns the AAC encoder arguments.
func AudioArgs(a model.AudioProfile) []string {
	sr := a.SampleRate
	if sr == 0 {
		sr = 48000
	}
	ch := a.Channels
	if ch == 0 {
		ch = 2
	}
	br := a.BitrateBps
	if br == 0 {
		br = 128000
	}
	return []string{"-c:a", "aac", "-b:a", strconv.Itoa(br), "-ar", strconv.Itoa(sr), "-ac", strconv.Itoa(ch)}
}

// DefaultAudio is used when only the audio must be converted to AAC and the
// channel has no profile (e.g. H.264 + MP2 in auto mode).
var DefaultAudio = model.AudioProfile{Codec: "aac", BitrateBps: 128_000, SampleRate: 48_000, Channels: 2}

// VideoArgsFor returns VideoArgs for output video stream i of an ABR ladder:
// every option gets the stream specifier (-b:v:1, -preset:v:1, ...).
func VideoArgsFor(v model.VideoProfile, enc model.Encoder, i int) []string {
	args := VideoArgs(v, enc)
	idx := strconv.Itoa(i)
	for k := 0; k < len(args); k += 2 {
		name := args[k]
		switch {
		case strings.HasSuffix(name, ":v"):
			args[k] = name + ":" + idx
		default:
			args[k] = name + ":v:" + idx
		}
	}
	return args
}
