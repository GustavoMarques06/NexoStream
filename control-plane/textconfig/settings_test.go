package textconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"nexostream/internal/model"
)

const settingsSample = `
settings {
  name "Operadora X";
  default_profile sd;
  hevc_passthrough on;
  udp_buffer 8M;
  failover no_packets=2 confirm=4 return=off;
  hls segment=4 window=6;
  dvr_retention 7d;
  dvr_storage principal;
  scheduler max_cpu=70 passthrough=0.5 transcode_mp=3 gpu=5;
  worker_offline 20;
  drain_per_pass 2;
  session access=30m refresh=24h;
  login_limit 5;
}

server media-01 {
  interface eno2;
  interface eno3;
  dvr_dir /storage/dvr;
  public_hls https://origin.example/hls;
}
`

func TestSettingsBlocks(t *testing.T) {
	cfg, errs := Parse(settingsSample)
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	st := cfg.Settings
	if st == nil {
		t.Fatal("settings block not parsed")
	}
	f := false
	want := model.Settings{
		SystemName: "Operadora X", BrandTagline: "Media Server", DefaultProfile: "sd", AllowHEVCPassthrough: true,
		UDPReadBufferBytes: 8 << 20,
		DefaultFailover: model.FailoverPolicy{NoPacketsSeconds: 2, ConfirmWindowSeconds: 4, ReturnToPrimary: &f,
			LowBitrateRatio: 0.3, LowBitrateSeconds: 10, MissingStreamSeconds: 5, PrimaryStableSeconds: 60},
		HLSSegmentSeconds: 4, HLSListSize: 6, DVRDefaultRetentionHours: 168, DVRDefaultStorage: "principal",
		SchedulerMaxCPUPercent: 70, CostPassthrough: 0.5, CostTranscodePerMegapixel: 3, CostGPUOffload: 5,
		WorkerOfflineSeconds: 20, DrainMigrationsPerPass: 2, AccessTokenMinutes: 30, RefreshTokenHours: 24,
		LoginAttemptsPerMinute: 5,
	}
	a, _ := json.Marshal(st)
	b, _ := json.Marshal(want)
	if string(a) != string(b) {
		t.Fatalf("settings\n got %s\nwant %s", a, b)
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].WorkerID != "media-01" ||
		strings.Join(cfg.Servers[0].IngestInterfaces, ",") != "eno2,eno3" ||
		cfg.Servers[0].DVRDir != "/storage/dvr" || cfg.Servers[0].PublicHLSBase != "https://origin.example/hls" {
		t.Fatalf("servers %+v", cfg.Servers)
	}
	if cfg.Lines["settings"] != 2 || cfg.Lines["server media-01"] != 18 {
		t.Fatalf("lines %v", cfg.Lines)
	}

	// Round trip.
	again, errs := Parse(Format(cfg))
	if len(errs) > 0 {
		t.Fatalf("round trip errors: %v\n%s", errs, Format(cfg))
	}
	c, _ := json.Marshal(again.Settings)
	if string(c) != string(a) {
		t.Fatalf("round trip\n got %s\nwant %s", c, a)
	}
	d, _ := json.Marshal(again.Servers)
	e, _ := json.Marshal(cfg.Servers)
	if string(d) != string(e) {
		t.Fatalf("servers round trip %s != %s", d, e)
	}
}

func TestSettingsDefaultsRoundTrip(t *testing.T) {
	def := model.DefaultSettings()
	out := Format(Config{Settings: &def})
	cfg, errs := Parse(out)
	if len(errs) > 0 {
		t.Fatalf("errors: %v\n%s", errs, out)
	}
	a, _ := json.Marshal(cfg.Settings)
	b, _ := json.Marshal(def)
	if string(a) != string(b) {
		t.Fatalf("got %s\nwant %s", a, b)
	}
}

func TestSettingsErrors(t *testing.T) {
	_, errs := Parse(`settings {
  hls segment=x;
  udp_buffer 4G;
  bogus 1;
  session refresh=90m;
}
settings {
}
server {
}
`)
	got := []string{}
	for _, e := range errs {
		got = append(got, e.Error())
	}
	all := strings.Join(got, "\n")
	for _, want := range []string{
		"linha 2: hls segment: valor inválido",
		"linha 3: udp_buffer: tamanho inválido",
		"linha 4: diretiva desconhecida em settings: bogus",
		"linha 5: session refresh: valor inválido",
		"linha 7: settings repetido",
		"linha 9:",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
}

func TestBrandingRoundTrip(t *testing.T) {
	st := model.DefaultSettings()
	st.SystemName, st.BrandTagline, st.BrandColor, st.BrandLoginMessage = "TV Sul", "IPTV", "#0a7cff", "Suporte: 0800; 24h"
	text := Format(Config{Settings: &st})
	cfg, errs := Parse(text)
	if len(errs) > 0 || cfg.Settings.BrandColor != "#0a7cff" || cfg.Settings.BrandLoginMessage != "Suporte: 0800; 24h" || cfg.Settings.BrandTagline != "IPTV" {
		t.Fatalf("%v %+v\n%s", errs, cfg.Settings, text)
	}
}
