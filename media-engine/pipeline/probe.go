package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"nexostream/internal/model"
)

// DetectEncoders returns the encoders that really work on this node. The
// presence of an encoder in the ffmpeg build is not enough: NVENC and Quick
// Sync are validated with a short test encode.
func DetectEncoders(ctx context.Context, ffmpegPath string) []string {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	out, err := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", "-encoders").Output()
	if err != nil {
		return nil
	}
	list := string(out)
	var encs []string
	if strings.Contains(list, "libx264") {
		encs = append(encs, string(model.EncoderCPU))
	}
	for enc, name := range map[model.Encoder]string{model.EncoderNVENC: "h264_nvenc", model.EncoderQSV: "h264_qsv"} {
		if !strings.Contains(list, name) {
			continue
		}
		tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := exec.CommandContext(tctx, ffmpegPath, "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "color=black:s=256x144:d=0.2", "-c:v", name, "-f", "null", "-").Run()
		cancel()
		if err == nil {
			encs = append(encs, string(enc))
		}
	}
	return encs
}

// FFprobe analyzes a captured piece of MPEG-TS and returns stream details
// (resolution, FPS, interlacing, audio channels) that the PMT does not carry.
func FFprobe(ctx context.Context, ffprobePath string, ts []byte, program int) (*model.VideoInfo, []model.AudioInfo, error) {
	if ffprobePath == "" {
		ffprobePath = "ffprobe"
	}
	args := []string{"-v", "error", "-show_streams", "-show_programs", "-of", "json", "-f", "mpegts", "-i", "pipe:0"}
	cmd := exec.CommandContext(ctx, ffprobePath, args...)
	cmd.Stdin = bytes.NewReader(ts)
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("ffprobe: %w", err)
	}
	return parseFFprobe(out, program)
}

type probeStream struct {
	Index        int               `json:"index"`
	CodecType    string            `json:"codec_type"`
	CodecName    string            `json:"codec_name"`
	Width        int               `json:"width"`
	Height       int               `json:"height"`
	AvgFrameRate string            `json:"avg_frame_rate"`
	RFrameRate   string            `json:"r_frame_rate"`
	FieldOrder   string            `json:"field_order"`
	Channels     int               `json:"channels"`
	SampleRate   string            `json:"sample_rate"`
	Tags         map[string]string `json:"tags"`
}

type probeOut struct {
	Streams  []probeStream `json:"streams"`
	Programs []struct {
		ProgramNum int           `json:"program_num"`
		Streams    []probeStream `json:"streams"`
	} `json:"programs"`
}

func parseFFprobe(out []byte, program int) (*model.VideoInfo, []model.AudioInfo, error) {
	var po probeOut
	if err := json.Unmarshal(out, &po); err != nil {
		return nil, nil, fmt.Errorf("ffprobe output: %w", err)
	}
	streams := po.Streams
	for _, p := range po.Programs {
		if program > 0 && p.ProgramNum == program && len(p.Streams) > 0 {
			streams = p.Streams
		}
	}
	var video *model.VideoInfo
	audio := []model.AudioInfo{}
	for _, s := range streams {
		switch s.CodecType {
		case "video":
			if video != nil {
				continue
			}
			fps := frameRate(s.AvgFrameRate)
			if fps == 0 {
				fps = frameRate(s.RFrameRate)
			}
			video = &model.VideoInfo{
				Codec: s.CodecName, Width: s.Width, Height: s.Height, FPS: fps,
				Interlaced: s.FieldOrder != "" && s.FieldOrder != "progressive" && s.FieldOrder != "unknown",
			}
		case "audio":
			sr, _ := strconv.Atoi(s.SampleRate)
			audio = append(audio, model.AudioInfo{Codec: s.CodecName, Channels: s.Channels, SampleRate: sr, Language: s.Tags["language"]})
		}
	}
	return video, audio, nil
}

func frameRate(s string) float64 {
	n, d, ok := strings.Cut(s, "/")
	if !ok {
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	num, _ := strconv.ParseFloat(n, 64)
	den, _ := strconv.ParseFloat(d, 64)
	if den == 0 {
		return 0
	}
	return float64(int(num/den*100+0.5)) / 100
}
