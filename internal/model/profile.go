package model

// Encoder selects where video is encoded.
type Encoder string

const (
	EncoderAuto  Encoder = "auto"
	EncoderCPU   Encoder = "cpu"
	EncoderNVENC Encoder = "nvenc"
	EncoderQSV   Encoder = "qsv"
)

// Valid reports whether e is a known encoder.
func (e Encoder) Valid() bool {
	return e == EncoderAuto || e == EncoderCPU || e == EncoderNVENC || e == EncoderQSV
}

// TranscodeProfile describes one output rendition (spec section 6.1).
type TranscodeProfile struct {
	ID      string       `json:"id"`
	Video   VideoProfile `json:"video"`
	Audio   AudioProfile `json:"audio"`
	Encoder Encoder      `json:"encoder"`
}

// VideoProfile holds the video encoding parameters.
type VideoProfile struct {
	Codec         string  `json:"codec"`   // h264 in v1
	Profile       string  `json:"profile"` // high | main | baseline
	Width         int     `json:"width"`
	Height        int     `json:"height"`
	BitrateBps    int     `json:"bitrate_bps"`
	MaxBitrateBps int     `json:"max_bitrate_bps"`
	GOPSeconds    float64 `json:"gop_seconds"`
	Deinterlace   string  `json:"deinterlace"`      // auto | on | off
	Preset        string  `json:"preset,omitempty"` // encoder preset, e.g. veryfast
}

// AudioProfile holds the audio encoding parameters.
type AudioProfile struct {
	Codec      string `json:"codec"` // aac in v1
	BitrateBps int    `json:"bitrate_bps"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
}

// DefaultProfiles returns the initial profiles from spec section 6.1.
func DefaultProfiles() []TranscodeProfile {
	mk := func(id, vprof string, w, h, vbr, abr int) TranscodeProfile {
		return TranscodeProfile{
			ID: id,
			Video: VideoProfile{
				Codec: "h264", Profile: vprof, Width: w, Height: h,
				BitrateBps: vbr, MaxBitrateBps: vbr, GOPSeconds: 2, Deinterlace: "auto",
				Preset: "veryfast",
			},
			Audio:   AudioProfile{Codec: "aac", BitrateBps: abr, SampleRate: 48000, Channels: 2},
			Encoder: EncoderAuto,
		}
	}
	return []TranscodeProfile{
		mk("fhd", "high", 1920, 1080, 5_000_000, 128_000),
		mk("hd", "high", 1280, 720, 3_000_000, 128_000),
		mk("sd", "main", 854, 480, 1_200_000, 96_000),
	}
}
