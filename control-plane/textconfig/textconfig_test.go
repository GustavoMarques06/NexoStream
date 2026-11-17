package textconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"nexostream/internal/model"
)

const sample = `
# comentário
dvr_storage principal {
  mode raid1;
  disk /mnt/disk1;
  disk "/mnt/disk 2";
}

stream canal-001 {
  title "Canal Exemplo";
  group Noticias;
  input udp://239.1.1.1:1234 interface=eno2;   # principal
  input udp://239.1.2.1:1234;                  # backup
  transcode hd;
  output hls;
  output mpegts_http;
  dvr 3d storage=principal qualities=all;
  failover no_packets=2 confirm=4 return=off;
}

stream futebol { input srt://10.0.0.5:9000; profile sd; dvr /storage/dvr 72h; drm aes-128 rotate=30m; disabled; }
stream so-hls { input https://cdn.example/live/index.m3u8; mode passthrough;
  auth http://auth.local/check?k=1 allow=10.0.0.0/8,192.168.1.5 deny=10.9.0.0/16 max_sessions=50 cache=30 on_error=allow;
  epg "http://epg.local/guia.xml.gz" id=canal.br; }
`

func TestParseSample(t *testing.T) {
	cfg, errs := Parse(sample)
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(cfg.Storages) != 1 || cfg.Storages[0].Mode != "raid1" || cfg.Storages[0].Disks[1] != "/mnt/disk 2" {
		t.Fatalf("storages %+v", cfg.Storages)
	}
	if len(cfg.Channels) != 3 {
		t.Fatalf("channels %d", len(cfg.Channels))
	}
	c := cfg.Channels[0]
	if c.Name != "Canal Exemplo" || c.Group != "Noticias" || len(c.Sources) != 2 || c.Sources[0].Interface != "eno2" ||
		c.Sources[1].Priority != 2 || c.Mode != model.ModeTranscode || c.Profiles[0] != "hd" ||
		!c.Outputs.HLS || !c.Outputs.MPEGTSHTTP || !c.DVR.Enabled || c.DVR.RetentionHours != 72 || c.DVR.Storage != "principal" || !c.DVR.AllQualities ||
		c.Failover.NoPacketsSeconds != 2 || c.Failover.ReturnsToPrimary() || !c.Enabled {
		t.Fatalf("canal-001 %+v", c)
	}
	e := cfg.Channels[1]
	if e.Enabled || e.Mode != model.ModeAuto || e.Profiles[0] != "sd" || e.DVR.Path != "/storage/dvr" || e.DVR.RetentionHours != 72 ||
		!e.Outputs.HLS || e.DRM != (model.DRMConfig{Enabled: true, Method: "aes-128", KeyRotationMinutes: 30}) {
		t.Fatalf("futebol %+v", e)
	}
	if c.DRM.Enabled {
		t.Fatalf("canal-001 has no drm line: %+v", c.DRM)
	}
	h := cfg.Channels[2]
	if h.Mode != model.ModePassthrough || !h.Auth.Enabled || h.Auth.BackendURL != "http://auth.local/check?k=1" ||
		len(h.Auth.AllowIPs) != 2 || h.Auth.DenyIPs[0] != "10.9.0.0/16" || h.Auth.MaxSessions != 50 || h.Auth.CacheSeconds != 30 ||
		!h.Auth.AllowOnError || h.EPG.URL != "http://epg.local/guia.xml.gz" || h.EPG.XMLTVID != "canal.br" {
		t.Fatalf("so-hls %+v", h)
	}
}

func TestRoundTrip(t *testing.T) {
	cfg, errs := Parse(sample)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	text := Format(cfg)
	again, errs := Parse(text)
	if len(errs) > 0 {
		t.Fatalf("formatted text has errors %v:\n%s", errs, text)
	}
	a, _ := json.Marshal(cfg.Storages)
	b, _ := json.Marshal(again.Storages)
	if string(a) != string(b) {
		t.Fatalf("storages differ:\n%s\n%s", a, b)
	}
	byID := map[string]model.Channel{}
	for _, c := range again.Channels {
		byID[c.ID] = c
	}
	for _, c := range cfg.Channels {
		x, _ := json.Marshal(c)
		y, _ := json.Marshal(byID[c.ID])
		if string(x) != string(y) {
			t.Fatalf("channel %s differs:\n%s\n%s\n--- text:\n%s", c.ID, x, y, text)
		}
	}
}

func TestErrorsHaveLines(t *testing.T) {
	src := "stream a {\n  input udp://1.1.1.1:1\n  bogus 1;\n}\nfoo b { }\nstream a { input udp://1.1.1.1:2; dvr 3x; }\n"
	_, errs := Parse(src)
	got := map[int]string{}
	for _, e := range errs {
		got[e.Line] += e.Message + "|"
	}
	for line, want := range map[int]string{3: "falta ';'", 5: "esperado 'settings'", 6: "repetido"} {
		if !strings.Contains(got[line], want) {
			t.Errorf("line %d: want %q in %q (all: %v)", line, want, got[line], errs)
		}
	}
	if !strings.Contains(got[6], "dvr") {
		t.Errorf("dvr 3x not reported: %v", errs)
	}
}

func TestDRMErrors(t *testing.T) {
	src := "stream a { input udp://1.1.1.1:1; drm widevine; }\n" +
		"stream b { input udp://1.1.1.1:2; drm aes-128 rotate=xx; }\n" +
		"stream c { input udp://1.1.1.1:3; drm aes-128 key=1; }\n"
	_, errs := Parse(src)
	got := map[int]string{}
	for _, e := range errs {
		got[e.Line] += e.Message + "|"
	}
	for line, want := range map[int]string{1: "não suportado", 2: "rotate", 3: "opção desconhecida"} {
		if !strings.Contains(got[line], want) {
			t.Errorf("line %d: want %q in %q", line, want, got[line])
		}
	}
	cfg, errs := Parse("stream a { input udp://1.1.1.1:1; drm aes-128; drm off; }")
	if len(errs) > 0 || cfg.Channels[0].DRM.Enabled {
		t.Fatalf("drm off: %v %+v", errs, cfg.Channels)
	}
}

func TestOverlayDirective(t *testing.T) {
	src := "stream ov { input udp://239.1.1.1:1234; transcode hd; overlay logo.png position=bottom-left margin=20 width=160 opacity=0.8; }"
	cfg, errs := Parse(src)
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	want := model.OverlayConfig{Enabled: true, Image: "logo.png", Position: "bottom-left", Margin: 20, Width: 160, Opacity: 0.8}
	if got := cfg.Channels[0].Overlay; got != want {
		t.Fatalf("overlay %+v", got)
	}
	// Writing it back and reading again gives the same channel.
	text := Format(cfg)
	if !strings.Contains(text, "overlay logo.png position=bottom-left margin=20 width=160 opacity=0.8;") {
		t.Fatalf("formatted:\n%s", text)
	}
	again, errs := Parse(text)
	if len(errs) > 0 || again.Channels[0].Overlay != want {
		t.Fatalf("round trip: %+v %v", again.Channels[0].Overlay, errs)
	}
	if cfg, _ := Parse("stream ov { input udp://239.1.1.1:1234; overlay off; }"); cfg.Channels[0].Overlay.Enabled {
		t.Fatal("overlay off must disable it")
	}
	for _, bad := range []string{
		"stream ov { input udp://239.1.1.1:1234; overlay position=top-left; }", // no file
		"stream ov { input udp://239.1.1.1:1234; overlay logo.png margin=x; }", // not a number
		"stream ov { input udp://239.1.1.1:1234; overlay logo.png size=2; }",   // unknown option
	} {
		if _, errs := Parse(bad); len(errs) == 0 {
			t.Errorf("no error for %q", bad)
		}
	}
}
