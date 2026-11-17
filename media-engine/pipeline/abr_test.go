package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nexostream/internal/model"
	"nexostream/media-engine/packaging"
)

func ladderProfile(id string, w, h, br int) model.TranscodeProfile {
	return model.TranscodeProfile{ID: id, Encoder: model.EncoderCPU,
		Video: model.VideoProfile{Codec: "h264", Profile: "main", Width: w, Height: h, BitrateBps: br, GOPSeconds: 2},
		Audio: model.AudioProfile{Codec: "aac", BitrateBps: 96000, SampleRate: 48000, Channels: 2}}
}

func TestABRArgs(t *testing.T) {
	sd, hd := ladderProfile("sd", 640, 360, 800_000), ladderProfile("hd", 1280, 720, 2_500_000)
	p, err := Decide(model.ModeTranscode, Input{VideoCodec: "h264", AudioCodecs: []string{"aac", "mp2"}}, &sd,
		Options{Encoders: []string{"cpu"}, Ladder: []model.TranscodeProfile{sd, hd}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Renditions) != 2 || p.Renditions[0].ProfileID != "hd" || p.Video.Height != 720 {
		t.Fatalf("ladder must be highest first: %+v", p.Renditions)
	}
	dir := t.TempDir()
	h := packaging.DefaultHLS(filepath.ToSlash(dir))
	args, err := BuildArgs(Spec{Plan: p, HLS: &h, TSRelayURL: "udp://127.0.0.1:9000"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := strings.Join(args, " ")
	for _, want := range []string{
		"-filter_complex [0:v:0]split=2[s0][s1];[s0]yadif",
		"scale=1280:720", "scale=640:360",
		"-map [v0] -map [v1] -map 0:a",
		"-c:v:0 libx264", "-b:v:0 2500000", "-c:v:1 libx264", "-b:v:1 800000", "-profile:v:1 main",
		`[select=v\\\:0,a:f=hls`, `[select=v\\\:1,a:f=hls`, "/v1/index.m3u8",
		`[select=v\\\:0,a,s:f=mpegts`, // the TS relay carries the top quality and the subtitles
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("missing %q in:\n%s", want, cmd)
		}
	}

	if err := packaging.WriteMaster(dir, []packaging.Variant{
		{Width: 1280, Height: 720, BitrateBps: 2_700_000, AvgBitrateBps: 2_700_000, Profile: "main"},
		{Width: 640, Height: 360, BitrateBps: 1_000_000, AvgBitrateBps: 1_000_000, Profile: "main"},
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "index.m3u8"))
	m := string(b)
	if !strings.Contains(m, `BANDWIDTH=2700000,AVERAGE-BANDWIDTH=2700000,RESOLUTION=1280x720,CODECS="avc1.4D401F,mp4a.40.2"`+"\nv0/index.m3u8") ||
		!strings.Contains(m, "RESOLUTION=640x360") || !packaging.IsMaster(filepath.Join(dir, "index.m3u8")) {
		t.Fatalf("master:\n%s", m)
	}
	// ABR needs mode transcode: in auto mode the ladder is ignored.
	p, _ = Decide(model.ModeAuto, Input{VideoCodec: "h264", AudioCodec: "aac"}, &sd, Options{Ladder: []model.TranscodeProfile{sd, hd}})
	if len(p.Renditions) != 0 {
		t.Fatal("auto mode must not build a ladder")
	}
}
