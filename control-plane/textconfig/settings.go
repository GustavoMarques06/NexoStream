package textconfig

import (
	"fmt"
	"strconv"
	"strings"

	"nexostream/internal/model"
)

// Global and per-server blocks, like the top of a Flussonic config:
//
//	settings {
//	  name "NexoStream";
//	  default_profile hd;
//	  hevc_passthrough off;
//	  udp_buffer 4M;
//	  failover no_packets=3 confirm=5 return=60;   # default for new streams
//	  hls segment=6 window=10;
//	  dvr_retention 3d;
//	  dvr_storage principal;                       # default for new recordings
//	  scheduler max_cpu=80 passthrough=0.2 transcode_mp=4 gpu=4;
//	  worker_offline 15;
//	  drain_per_pass 5;
//	  session access=15m refresh=12h;
//	  login_limit 10;
//	}
//
//	server media-01 {
//	  interface eno2;
//	  dvr_dir /storage/dvr;
//	  public_hls https://origin.example/hls;
//	  public_ts https://origin.example/live;
//	  public_dvr https://origin.example/dvr;
//	}

// failover reads "key=value" failover options into fo.
func (p *parser) failover(line int, args []token, fo *model.FailoverPolicy) {
	opts, pos := kv(args)
	for _, a := range pos {
		p.fail(line, "failover usa chave=valor, encontrado %q", a.text)
	}
	for k, v := range opts {
		if k == "return" && v == "off" {
			f := false
			fo.ReturnToPrimary = &f
			continue
		}
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			p.fail(line, "failover %s: número inválido %q", k, v)
			continue
		}
		switch k {
		case "no_packets":
			fo.NoPacketsSeconds = n
		case "confirm":
			fo.ConfirmWindowSeconds = n
		case "return":
			t := true
			fo.ReturnToPrimary = &t
			fo.PrimaryStableSeconds = n
		case "missing_stream":
			fo.MissingStreamSeconds = n
		case "low_bitrate":
			fo.LowBitrateRatio = n
		case "low_bitrate_for":
			fo.LowBitrateSeconds = n
		default:
			p.fail(line, "opção desconhecida em failover: %s", k)
		}
	}
}

// failoverText writes the failover options that differ from def (all of
// them when def is nil).
func failoverText(fo model.FailoverPolicy, def *model.FailoverPolicy) string {
	fo = fo.WithDefaults()
	var f []string
	add := func(k string, v, d float64) {
		if def == nil || v != d {
			f = append(f, k+"="+num(v))
		}
	}
	var d model.FailoverPolicy
	if def != nil {
		d = *def
	}
	add("no_packets", fo.NoPacketsSeconds, d.NoPacketsSeconds)
	add("confirm", fo.ConfirmWindowSeconds, d.ConfirmWindowSeconds)
	if !fo.ReturnsToPrimary() {
		f = append(f, "return=off")
	} else {
		add("return", fo.PrimaryStableSeconds, d.PrimaryStableSeconds)
	}
	add("missing_stream", fo.MissingStreamSeconds, d.MissingStreamSeconds)
	add("low_bitrate", fo.LowBitrateRatio, d.LowBitrateRatio)
	add("low_bitrate_for", fo.LowBitrateSeconds, d.LowBitrateSeconds)
	return strings.Join(f, " ")
}

// size parses "4M", "512K" or a number of bytes.
func size(s string) (int, bool) {
	mult := 1
	switch u := strings.ToUpper(s); {
	case strings.HasSuffix(u, "M"):
		mult, s = 1<<20, s[:len(s)-1]
	case strings.HasSuffix(u, "K"):
		mult, s = 1<<10, s[:len(s)-1]
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n * mult, true
}

func sizeText(n int) string {
	switch {
	case n%(1<<20) == 0:
		return strconv.Itoa(n>>20) + "M"
	case n%(1<<10) == 0:
		return strconv.Itoa(n>>10) + "K"
	}
	return strconv.Itoa(n)
}

// minutes parses "15m", "12h", "1d" or a number of minutes.
func minutes(s string) (int, bool) {
	mult := 1
	switch {
	case strings.HasSuffix(s, "m"):
		s = strings.TrimSuffix(s, "m")
	case strings.HasSuffix(s, "h"):
		mult, s = 60, strings.TrimSuffix(s, "h")
	case strings.HasSuffix(s, "d"):
		mult, s = 24*60, strings.TrimSuffix(s, "d")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n * mult, true
}

func onOff(s string) (bool, bool) {
	switch s {
	case "on", "yes", "true":
		return true, true
	case "off", "no", "false":
		return false, true
	}
	return false, false
}

func (p *parser) settings() model.Settings {
	st := model.DefaultSettings()
	p.body(func(w []token) {
		key, args := w[0].text, w[1:]
		line := w[0].line
		one := func() (string, bool) {
			if len(args) != 1 {
				p.fail(line, "%s precisa de um valor", key)
				return "", false
			}
			return args[0].text, true
		}
		integer := func(dst *int) {
			if v, ok := one(); ok {
				n, err := strconv.Atoi(v)
				if err != nil {
					p.fail(line, "%s: número inteiro inválido %q", key, v)
					return
				}
				*dst = n
			}
		}
		options := func(set map[string]func(string) bool) {
			opts, pos := kv(args)
			for _, a := range pos {
				p.fail(line, "%s usa chave=valor, encontrado %q", key, a.text)
			}
			for k, v := range opts {
				f, ok := set[k]
				if !ok {
					p.fail(line, "opção desconhecida em %s: %s", key, k)
					continue
				}
				if !f(v) {
					p.fail(line, "%s %s: valor inválido %q", key, k, v)
				}
			}
		}
		intOpt := func(dst *int) func(string) bool {
			return func(v string) bool {
				n, err := strconv.Atoi(v)
				*dst = n
				return err == nil
			}
		}
		floatOpt := func(dst *float64) func(string) bool {
			return func(v string) bool {
				n, err := strconv.ParseFloat(v, 64)
				*dst = n
				return err == nil
			}
		}
		switch key {
		case "name":
			if len(args) == 0 {
				p.fail(line, "name precisa de um valor")
				return
			}
			st.SystemName = joinWords(args)
		case "tagline":
			st.BrandTagline = joinWords(args)
		case "brand_color":
			// "#" starts a comment: write "#00aa00" in quotes, or 00aa00.
			if v, ok := one(); ok {
				if len(v) == 6 && !strings.HasPrefix(v, "#") {
					v = "#" + v
				}
				st.BrandColor = v
			}
		case "login_message":
			st.BrandLoginMessage = joinWords(args)
		case "default_profile":
			if v, ok := one(); ok {
				st.DefaultProfile = v
			}
		case "hevc_passthrough":
			if v, ok := one(); ok {
				b, valid := onOff(v)
				if !valid {
					p.fail(line, "hevc_passthrough deve ser on ou off")
				}
				st.AllowHEVCPassthrough = b
			}
		case "udp_buffer":
			if v, ok := one(); ok {
				n, valid := size(v)
				if !valid {
					p.fail(line, "udp_buffer: tamanho inválido %q (ex.: 4M, 512K)", v)
				}
				st.UDPReadBufferBytes = n
			}
		case "failover":
			fo := model.FailoverPolicy{}
			p.failover(line, args, &fo)
			st.DefaultFailover = fo.WithDefaults()
		case "hls":
			options(map[string]func(string) bool{
				"segment": intOpt(&st.HLSSegmentSeconds),
				"window":  intOpt(&st.HLSListSize),
			})
		case "dvr_retention":
			if v, ok := one(); ok {
				h, valid := retentionHours(v)
				if !valid {
					p.fail(line, "dvr_retention: use 3d ou 72h")
				}
				st.DVRDefaultRetentionHours = h
			}
		case "dvr_storage":
			if v, ok := one(); ok {
				if v == "none" {
					v = ""
				}
				st.DVRDefaultStorage = v
			}
		case "scheduler":
			options(map[string]func(string) bool{
				"max_cpu":      floatOpt(&st.SchedulerMaxCPUPercent),
				"passthrough":  floatOpt(&st.CostPassthrough),
				"transcode_mp": floatOpt(&st.CostTranscodePerMegapixel),
				"gpu":          floatOpt(&st.CostGPUOffload),
			})
		case "worker_offline":
			integer(&st.WorkerOfflineSeconds)
		case "drain_per_pass":
			integer(&st.DrainMigrationsPerPass)
		case "session":
			options(map[string]func(string) bool{
				"access": func(v string) bool {
					n, ok := minutes(v)
					st.AccessTokenMinutes = n
					return ok
				},
				"refresh": func(v string) bool {
					n, ok := minutes(v)
					st.RefreshTokenHours = n / 60
					return ok && n%60 == 0
				},
			})
		case "login_limit":
			integer(&st.LoginAttemptsPerMinute)
		default:
			p.fail(line, "diretiva desconhecida em settings: %s", key)
		}
	})
	return st
}

func (p *parser) server(id string) model.ServerSettings {
	o := model.ServerSettings{WorkerID: id, IngestInterfaces: []string{}}
	p.body(func(w []token) {
		key, args := w[0].text, w[1:]
		if key == "roles" {
			// roles transcode dvr;
			for _, a := range args {
				o.Roles = append(o.Roles, a.text)
			}
			return
		}
		if len(args) != 1 {
			p.fail(w[0].line, "%s precisa de um valor", key)
			return
		}
		v := args[0].text
		switch key {
		case "interface":
			o.IngestInterfaces = append(o.IngestInterfaces, v)
		case "dvr_dir":
			o.DVRDir = v
		case "public_hls":
			o.PublicHLSBase = v
		case "public_ts":
			o.PublicTSBase = v
		case "public_dvr":
			o.PublicDVRBase = v
		default:
			p.fail(w[0].line, "diretiva desconhecida em server: %s", key)
		}
	})
	return o
}

func formatSettings(b *strings.Builder, st model.Settings) {
	st = st.WithDefaults()
	b.WriteString("settings {\n")
	fmt.Fprintf(b, "  name %s;\n", quote(st.SystemName))
	if st.BrandTagline != "" {
		fmt.Fprintf(b, "  tagline %s;\n", quote(st.BrandTagline))
	}
	if st.BrandColor != "" {
		fmt.Fprintf(b, "  brand_color %s;\n", quote(st.BrandColor))
	}
	if st.BrandLoginMessage != "" {
		fmt.Fprintf(b, "  login_message %s;\n", quote(st.BrandLoginMessage))
	}
	// The logo (an image) is managed only in the panel.
	fmt.Fprintf(b, "  default_profile %s;\n", st.DefaultProfile)
	if st.AllowHEVCPassthrough {
		b.WriteString("  hevc_passthrough on;\n")
	} else {
		b.WriteString("  hevc_passthrough off;\n")
	}
	fmt.Fprintf(b, "  udp_buffer %s;\n", sizeText(st.UDPReadBufferBytes))
	fmt.Fprintf(b, "  failover %s;\n", failoverText(st.DefaultFailover, nil))
	fmt.Fprintf(b, "  hls segment=%d window=%d;\n", st.HLSSegmentSeconds, st.HLSListSize)
	fmt.Fprintf(b, "  dvr_retention %s;\n", retention(st.DVRDefaultRetentionHours))
	if st.DVRDefaultStorage != "" {
		fmt.Fprintf(b, "  dvr_storage %s;\n", st.DVRDefaultStorage)
	} else {
		b.WriteString("  dvr_storage none;\n")
	}
	fmt.Fprintf(b, "  scheduler max_cpu=%s passthrough=%s transcode_mp=%s gpu=%s;\n",
		num(st.SchedulerMaxCPUPercent), num(st.CostPassthrough), num(st.CostTranscodePerMegapixel), num(st.CostGPUOffload))
	fmt.Fprintf(b, "  worker_offline %d;\n", st.WorkerOfflineSeconds)
	fmt.Fprintf(b, "  drain_per_pass %d;\n", st.DrainMigrationsPerPass)
	fmt.Fprintf(b, "  session access=%dm refresh=%dh;\n", st.AccessTokenMinutes, st.RefreshTokenHours)
	fmt.Fprintf(b, "  login_limit %d;\n", st.LoginAttemptsPerMinute)
	b.WriteString("}\n\n")
}

func formatServer(b *strings.Builder, o model.ServerSettings) {
	fmt.Fprintf(b, "server %s {\n", o.WorkerID)
	for _, i := range o.IngestInterfaces {
		fmt.Fprintf(b, "  interface %s;\n", i)
	}
	if len(o.Roles) > 0 {
		fmt.Fprintf(b, "  roles %s;\n", strings.Join(o.Roles, " "))
	}
	for _, kv := range [][2]string{{"dvr_dir", o.DVRDir}, {"public_hls", o.PublicHLSBase}, {"public_ts", o.PublicTSBase}, {"public_dvr", o.PublicDVRBase}} {
		if kv[1] != "" {
			fmt.Fprintf(b, "  %s %s;\n", kv[0], quote(kv[1]))
		}
	}
	b.WriteString("}\n\n")
}
