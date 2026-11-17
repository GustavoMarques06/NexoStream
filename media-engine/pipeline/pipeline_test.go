package pipeline

import (
	"strings"
	"testing"

	"nexostream/internal/model"
	"nexostream/media-engine/packaging"
)

func profile(id string) *model.TranscodeProfile {
	for _, p := range model.DefaultProfiles() {
		if p.ID == id {
			return &p
		}
	}
	return nil
}

func TestDecideAuto(t *testing.T) {
	opt := Options{DefaultProfile: profile("hd"), Encoders: []string{"cpu"}}
	cases := []struct {
		v, a string
		want string
	}{
		{"h264", "aac", ProcessingPassthrough},
		{"h264", "mp2", ProcessingAudioTranscode},
		{"h264", "ac3", ProcessingAudioTranscode},
		{"mpeg2video", "mp2", ProcessingTranscode},
		{"hevc", "aac", ProcessingTranscode},
		{"", "aac", ProcessingPassthrough},
		{"h264", "", ProcessingPassthrough},
	}
	for _, c := range cases {
		p, err := Decide(model.ModeAuto, Input{VideoCodec: c.v, AudioCodec: c.a}, nil, opt)
		if err != nil {
			t.Fatalf("%s/%s: %v", c.v, c.a, err)
		}
		if p.Processing != c.want {
			t.Errorf("%s/%s: %s want %s", c.v, c.a, p.Processing, c.want)
		}
	}
	p, _ := Decide(model.ModeAuto, Input{VideoCodec: "hevc", AudioCodec: "aac"}, nil, Options{AllowHEVCPassthrough: true})
	if p.Processing != ProcessingPassthrough {
		t.Errorf("hevc passthrough allowed: %s", p.Processing)
	}
}

func TestDecideForcedAndErrors(t *testing.T) {
	p, err := Decide(model.ModePassthrough, Input{VideoCodec: "mpeg2video", AudioCodec: "mp2"}, nil, Options{})
	if err != nil || p.Processing != ProcessingPassthrough {
		t.Fatalf("%v %v", p.Processing, err)
	}
	p, err = Decide(model.ModeTranscode, Input{VideoCodec: "h264", AudioCodec: "aac"}, profile("sd"), Options{Encoders: []string{"cpu"}})
	if err != nil || p.Processing != ProcessingTranscode || p.Video.Width != 854 || p.Encoder != model.EncoderCPU {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := Decide(model.ModeTranscode, Input{VideoCodec: "h264"}, nil, Options{}); err == nil {
		t.Fatal("transcode without profile must fail")
	}
	if _, err := Decide(model.ModeAuto, Input{VideoCodec: "mpeg2video"}, nil, Options{}); err == nil {
		t.Fatal("auto transcode without default profile must fail")
	}
	if _, err := Decide(model.ModeAuto, Input{}, nil, Options{}); err == nil {
		t.Fatal("empty input must fail")
	}
	nv := *profile("hd")
	nv.Encoder = model.EncoderNVENC
	if _, err := Decide(model.ModeTranscode, Input{VideoCodec: "h264"}, &nv, Options{Encoders: []string{"cpu"}}); err == nil {
		t.Fatal("nvenc on a CPU-only node must fail")
	}
}

func TestBuildArgsPassthrough(t *testing.T) {
	hls := packaging.DefaultHLS("/var/lib/nexostream/hls/canal-001")
	args, err := BuildArgs(Spec{
		ChannelID:  "canal-001",
		Plan:       Plan{Processing: ProcessingPassthrough, Program: 1, HasVideo: true, HasAudio: true, CopyVideo: true, CopyAudio: true},
		HLS:        &hls,
		TSRelayURL: "udp://127.0.0.1:40000",
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := strings.Join(args, " ")
	for _, want := range []string{
		"-f mpegts -i pipe:0",
		"-map 0:p:1:v:0 -map 0:p:1:a:0",
		"-c:v copy", "-c:a copy",
		"-f tee [f=hls:hls_time=6:hls_list_size=10:",
		"hls_segment_filename=/var/lib/nexostream/hls/canal-001/seg_%06d.ts]/var/lib/nexostream/hls/canal-001/index.m3u8",
		"|[f=mpegts:onfail=ignore]udp\\://127.0.0.1\\:40000?pkt_size=1316",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("missing %q in\n%s", want, cmd)
		}
	}
}

func TestBuildArgsTranscode(t *testing.T) {
	hd := profile("hd")
	hls := packaging.DefaultHLS("/hls/c1")
	for _, enc := range []model.Encoder{model.EncoderCPU, model.EncoderNVENC, model.EncoderQSV} {
		args, err := BuildArgs(Spec{
			Plan: Plan{Processing: ProcessingTranscode, HasVideo: true, HasAudio: true, Video: hd.Video, Audio: hd.Audio, Encoder: enc},
			HLS:  &hls,
		})
		if err != nil {
			t.Fatal(err)
		}
		cmd := strings.Join(args, " ")
		want := map[model.Encoder]string{model.EncoderCPU: "libx264", model.EncoderNVENC: "h264_nvenc", model.EncoderQSV: "h264_qsv"}[enc]
		for _, w := range []string{want, "-b:v 3000000", "-maxrate 3000000", "-bufsize 6000000",
			"expr:gte(t,n_forced*2)", "scale=1280:720", "yadif", "-c:a aac -b:a 128000 -ar 48000 -ac 2", "-map 0:v:0"} {
			if !strings.Contains(cmd, w) {
				t.Errorf("%s: missing %q in %s", enc, w, cmd)
			}
		}
	}
	if _, err := BuildArgs(Spec{Plan: Plan{HasVideo: true, CopyVideo: true}}); err == nil {
		t.Fatal("no output must fail")
	}
}

func TestParseFFprobe(t *testing.T) {
	out := `{"programs":[],"streams":[
	 {"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"avg_frame_rate":"25/1","field_order":"tt"},
	 {"index":1,"codec_type":"audio","codec_name":"mp2","channels":2,"sample_rate":"48000","tags":{"language":"por"}}]}`
	v, a, err := parseFFprobe([]byte(out), 0)
	if err != nil {
		t.Fatal(err)
	}
	if v.Width != 1920 || v.FPS != 25 || !v.Interlaced {
		t.Fatalf("video=%+v", v)
	}
	if len(a) != 1 || a[0].Codec != "mp2" || a[0].SampleRate != 48000 || a[0].Language != "por" {
		t.Fatalf("audio=%+v", a)
	}
	if frameRate("30000/1001") != 29.97 {
		t.Fatalf("fps=%v", frameRate("30000/1001"))
	}
}

func TestAllAudioTracks(t *testing.T) {
	p, err := Decide(model.ModeAuto, Input{VideoCodec: "h264", AudioCodecs: []string{"aac", "ac3"}}, nil, Options{})
	if err != nil || p.CopyAudio || p.AudioTracks != 2 || p.Processing != ProcessingAudioTranscode {
		t.Fatalf("%+v %v", p, err)
	}
	args, _ := BuildArgs(Spec{Plan: p, UDPOutput: "udp://239.1.1.1:1"})
	if !strings.Contains(strings.Join(args, " "), "-map 0:a -c:v copy") {
		t.Fatalf("every audio track must be mapped: %v", args)
	}
	p, _ = Decide(model.ModeAuto, Input{VideoCodec: "h264", AudioCodecs: []string{"aac", "aac"}}, nil, Options{})
	if !p.CopyAudio || p.Processing != ProcessingPassthrough {
		t.Fatalf("two AAC tracks are copied: %+v", p)
	}
	p, _ = Decide(model.ModeAuto, Input{VideoCodec: "h264", AudioCodec: "aac"}, nil, Options{})
	args, _ = BuildArgs(Spec{Plan: p, UDPOutput: "udp://239.1.1.1:1"})
	if !strings.Contains(strings.Join(args, " "), "-map 0:a:0") {
		t.Fatalf("one track: %v", args)
	}
}
