// Package textconfig reads and writes the whole configuration as text, in a
// syntax close to Flussonic's. Besides the blocks below there are
// "settings { }" and "server <id> { }" (see settings.go):
//
//	dvr_storage principal {
//	  mode raid1;
//	  disk /mnt/disk1;
//	  disk /mnt/disk2;
//	}
//
//	stream canal-001 {
//	  title "Canal Exemplo";
//	  group Noticias;
//	  input udp://239.1.1.1:1234 interface=eno2;   # primary
//	  input udp://239.1.2.1:1234;                  # backup (order = priority)
//	  mode auto;                                   # auto | passthrough | transcode
//	  transcode hd;                                # force transcoding with a profile
//	  profile hd;                                  # profile used only if needed (auto)
//	  output hls;
//	  output mpegts_http;
//	  output udp://239.10.0.1:5000;
//	  dvr 3d storage=principal;                    # or: dvr /storage 3d;  dvr off;
//	  drm aes-128 rotate=1h;                       # HLS AES-128 with token; drm off;
//	  auth http://auth/check max_sessions=500 allow=10.0.0.0/8 deny=10.9.0.0/16 cache=60 on_error=deny;
//	  epg http://epg.local/guide.xml.gz id=canal.br;   # XMLTV guide; epg off;
//	  failover no_packets=3 confirm=5 return=60;   # return=off: stay on backup
//	  disabled;
//	}
package textconfig

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"nexostream/internal/model"
)

// Config is the parsed text.
type Config struct {
	// Settings is nil when the text has no settings block.
	Settings *model.Settings
	Servers  []model.ServerSettings
	Channels []model.Channel
	Storages []model.DVRStorage
	// Lines maps "settings", "server <id>", "stream <id>" and
	// "dvr_storage <id>" to the line of the block.
	Lines map[string]int
}

// Error is a parse error at a line.
type Error struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

func (e Error) Error() string { return fmt.Sprintf("linha %d: %s", e.Line, e.Message) }

// ---- tokenizer ----

type token struct {
	text   string
	line   int
	quoted bool
}

func tokenize(src string) ([]token, []Error) {
	var toks []token
	var errs []Error
	line := 1
	rs := []rune(src)
	for i := 0; i < len(rs); {
		c := rs[i]
		switch {
		case c == '\n':
			line++
			i++
		case unicode.IsSpace(c):
			i++
		case c == '#':
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
		case c == '{' || c == '}' || c == ';':
			toks = append(toks, token{text: string(c), line: line})
			i++
		case c == '"':
			start := line
			var b strings.Builder
			i++
			closed := false
			for i < len(rs) {
				if rs[i] == '\\' && i+1 < len(rs) {
					b.WriteRune(rs[i+1])
					i += 2
					continue
				}
				if rs[i] == '"' {
					closed = true
					i++
					break
				}
				if rs[i] == '\n' {
					line++
				}
				b.WriteRune(rs[i])
				i++
			}
			if !closed {
				errs = append(errs, Error{start, "aspas não fechadas"})
			}
			toks = append(toks, token{text: b.String(), line: start, quoted: true})
		default:
			var b strings.Builder
			for i < len(rs) && !unicode.IsSpace(rs[i]) && rs[i] != ';' && rs[i] != '{' && rs[i] != '}' && rs[i] != '#' {
				b.WriteRune(rs[i])
				i++
			}
			toks = append(toks, token{text: b.String(), line: line})
		}
	}
	return toks, errs
}

// ---- parser ----

type parser struct {
	toks []token
	pos  int
	errs []Error
}

func (p *parser) peek() *token {
	if p.pos < len(p.toks) {
		return &p.toks[p.pos]
	}
	return nil
}

func (p *parser) next() *token {
	t := p.peek()
	if t != nil {
		p.pos++
	}
	return t
}

func (p *parser) fail(line int, format string, a ...any) {
	p.errs = append(p.errs, Error{line, fmt.Sprintf(format, a...)})
}

// statement reads words until ";" (or "}" which is left for the caller).
func (p *parser) statement() ([]token, bool) {
	var words []token
	for {
		t := p.peek()
		if t == nil {
			return words, false
		}
		if !t.quoted && t.text == ";" {
			p.pos++
			return words, true
		}
		if !t.quoted && (t.text == "}" || t.text == "{") {
			return words, false
		}
		words = append(words, *t)
		p.pos++
	}
}

// Parse reads a configuration. It reports every error it can find.
func Parse(src string) (Config, []Error) {
	toks, errs := tokenize(src)
	p := &parser{toks: toks, errs: errs}
	cfg := Config{Lines: map[string]int{}}
	seenCh, seenSt, seenSrv := map[string]int{}, map[string]int{}, map[string]int{}
	for p.peek() != nil {
		kw := p.next()
		if !kw.quoted && kw.text == "settings" {
			open := p.next()
			if open == nil || open.quoted || open.text != "{" {
				p.fail(kw.line, "esperado '{' depois de settings")
				p.skipBlock()
				continue
			}
			if l, dup := cfg.Lines["settings"]; dup {
				p.fail(kw.line, "settings repetido (já definido na linha %d)", l)
			}
			cfg.Lines["settings"] = kw.line
			st := p.settings()
			cfg.Settings = &st
			continue
		}
		if kw.quoted || (kw.text != "stream" && kw.text != "dvr_storage" && kw.text != "server") {
			p.fail(kw.line, "esperado 'settings', 'server', 'stream' ou 'dvr_storage', encontrado %q", kw.text)
			p.skipBlock()
			continue
		}
		name := p.next()
		if name == nil || (!name.quoted && (name.text == "{" || name.text == ";")) {
			p.fail(kw.line, "%s sem nome", kw.text)
			p.skipBlock()
			continue
		}
		open := p.next()
		if open == nil || open.quoted || open.text != "{" {
			p.fail(name.line, "esperado '{' depois de %s %s", kw.text, name.text)
			p.skipBlock()
			continue
		}
		if kw.text == "server" {
			if l, dup := seenSrv[name.text]; dup {
				p.fail(name.line, "server %s repetido (já definido na linha %d)", name.text, l)
			}
			seenSrv[name.text] = name.line
			cfg.Lines["server "+name.text] = name.line
			cfg.Servers = append(cfg.Servers, p.server(name.text))
			continue
		}
		if kw.text == "stream" {
			if l, dup := seenCh[name.text]; dup {
				p.fail(name.line, "stream %s repetido (já definido na linha %d)", name.text, l)
			}
			seenCh[name.text] = name.line
			cfg.Lines["stream "+name.text] = name.line
			cfg.Channels = append(cfg.Channels, p.stream(name.text))
		} else {
			if l, dup := seenSt[name.text]; dup {
				p.fail(name.line, "dvr_storage %s repetido (já definido na linha %d)", name.text, l)
			}
			seenSt[name.text] = name.line
			cfg.Lines["dvr_storage "+name.text] = name.line
			cfg.Storages = append(cfg.Storages, p.storage(name.text))
		}
	}
	sort.SliceStable(p.errs, func(i, j int) bool { return p.errs[i].Line < p.errs[j].Line })
	return cfg, p.errs
}

func (p *parser) skipBlock() {
	depth := 0
	for t := p.next(); t != nil; t = p.next() {
		if t.quoted {
			continue
		}
		switch t.text {
		case "{":
			depth++
		case "}":
			depth--
			if depth <= 0 {
				return
			}
		}
	}
}

// body calls fn for each statement until the closing "}".
func (p *parser) body(fn func(words []token)) {
	for {
		t := p.peek()
		if t == nil {
			p.fail(p.toks[len(p.toks)-1].line, "bloco sem '}' de fechamento")
			return
		}
		if !t.quoted && t.text == "}" {
			p.pos++
			return
		}
		words, ok := p.statement()
		if len(words) == 0 {
			if !ok {
				bad := p.next()
				p.fail(bad.line, "%q inesperado", bad.text)
			}
			continue
		}
		if !ok {
			p.fail(words[len(words)-1].line, "falta ';' no fim de %q", words[0].text)
		}
		fn(words)
	}
}

func kv(words []token) (map[string]string, []token) {
	opts := map[string]string{}
	var pos []token
	for _, w := range words {
		if k, v, ok := strings.Cut(w.text, "="); ok && !w.quoted && !strings.Contains(k, "/") {
			opts[k] = v
			continue
		}
		pos = append(pos, w)
	}
	return opts, pos
}

// duration parses retention like "3d", "72h"; returns hours.
func retentionHours(s string) (int, bool) {
	s = strings.ToLower(s)
	mult := 1
	switch {
	case strings.HasSuffix(s, "d"):
		mult, s = 24, strings.TrimSuffix(s, "d")
	case strings.HasSuffix(s, "h"):
		s = strings.TrimSuffix(s, "h")
	default:
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n * mult, true
}

func isAbs(p string) bool {
	return strings.HasPrefix(p, "/") || (len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/'))
}

func (p *parser) stream(id string) model.Channel {
	ch := model.Channel{ID: id, Name: id, Enabled: true, Mode: model.ModeAuto, Profiles: []string{}}
	fo := model.FailoverPolicy{}
	outputs := 0
	p.body(func(w []token) {
		key, args := w[0].text, w[1:]
		line := w[0].line
		need := func(n int) bool {
			if len(args) < n {
				p.fail(line, "%s precisa de %d valor(es)", key, n)
				return false
			}
			return true
		}
		// extra reports values beyond the expected ones: usually a missing ";".
		extra := func(ts []token, n int) {
			if len(ts) > n {
				p.fail(ts[n].line, "valor inesperado %q em %s (falta ';' no fim da linha anterior?)", ts[n].text, key)
			}
		}
		sameLine := func(ts []token) {
			for _, t := range ts {
				if t.line != line {
					p.fail(t.line, "valor inesperado %q em %s (falta ';' no fim da linha anterior?)", t.text, key)
					return
				}
			}
		}
		switch key {
		case "title":
			if need(1) {
				sameLine(args)
				ch.Name = joinWords(args)
			}
		case "group":
			if need(1) {
				sameLine(args)
				ch.Group = joinWords(args)
			}
		case "input":
			if !need(1) {
				return
			}
			opts, pos := kv(args)
			if len(pos) == 0 {
				p.fail(line, "input precisa da URL")
				return
			}
			extra(pos, 1)
			src := model.Source{Priority: len(ch.Sources) + 1, URL: pos[0].text, Interface: opts["interface"]}
			src.ID = strconv.Itoa(src.Priority)
			for k := range opts {
				if k != "interface" {
					p.fail(line, "opção desconhecida em input: %s", k)
				}
			}
			ch.Sources = append(ch.Sources, src)
		case "mode":
			if need(1) {
				extra(args, 1)
				m := model.ProcessingMode(args[0].text)
				if !m.Valid() {
					p.fail(line, "mode deve ser auto, passthrough ou transcode")
				}
				ch.Mode = m
			}
		case "transcode":
			// transcode hd;  or an ABR ladder: transcode fhd hd sd;
			if need(1) {
				ch.Profiles = []string{}
				for _, a := range args {
					ch.Profiles = append(ch.Profiles, a.text)
				}
				if ch.Mode == model.ModeAuto {
					ch.Mode = model.ModeTranscode
				}
			}
		case "profile":
			// Profile used only if the source needs transcoding (auto mode).
			if need(1) {
				extra(args, 1)
				ch.Profiles = []string{args[0].text}
			}
		case "passthrough":
			ch.Mode = model.ModePassthrough
		case "output":
			if !need(1) {
				return
			}
			outputs++
			extra(args, 1)
			switch v := args[0].text; {
			case v == "hls":
				ch.Outputs.HLS = true
			case v == "mpegts_http" || v == "mpegts":
				ch.Outputs.MPEGTSHTTP = true
			case v == "dash":
				ch.Outputs.DASH = true
			case strings.HasPrefix(v, "udp://"):
				ch.Outputs.MPEGTSUDP = v
			default:
				p.fail(line, "output deve ser hls, dash, mpegts_http ou udp://host:porta")
			}
		case "dvr":
			if !need(1) {
				return
			}
			if args[0].text == "off" {
				ch.DVR = model.DVRConfig{}
				return
			}
			opts, pos := kv(args)
			ch.DVR.Enabled = true
			for _, a := range pos {
				if h, ok := retentionHours(a.text); ok {
					ch.DVR.RetentionHours = h
				} else if isAbs(a.text) {
					ch.DVR.Path = a.text
				} else {
					p.fail(line, "dvr: %q não é retenção (3d, 72h) nem pasta absoluta", a.text)
				}
			}
			for k, v := range opts {
				switch k {
				case "storage":
					ch.DVR.Storage = v
				case "path":
					ch.DVR.Path = v
				case "qualities":
					switch v {
					case "all":
						ch.DVR.AllQualities = true
					case "top":
						ch.DVR.AllQualities = false
					default:
						p.fail(line, "dvr: qualities deve ser all ou top")
					}
				default:
					p.fail(line, "opção desconhecida em dvr: %s", k)
				}
			}
			if ch.DVR.RetentionHours == 0 {
				p.fail(line, "dvr precisa da retenção, ex.: dvr 3d;")
			}
		case "push":
			// push rtmp://a.rtmp.youtube.com/live2/KEY;  push srt://host:9000;
			if need(1) {
				extra(args, 1)
				ch.Outputs.Push = append(ch.Outputs.Push, args[0].text)
			}
		case "drm":
			if !need(1) {
				return
			}
			if args[0].text == "off" {
				extra(args, 1)
				ch.DRM = model.DRMConfig{}
				return
			}
			opts, pos := kv(args)
			ch.DRM = model.DRMConfig{Enabled: true}
			for _, a := range pos {
				if a.text != "aes-128" {
					p.fail(line, "drm: método %q não suportado (use aes-128)", a.text)
				}
				ch.DRM.Method = a.text
			}
			for k, v := range opts {
				switch k {
				case "rotate":
					m, ok := minutes(v)
					if !ok {
						p.fail(line, "drm: rotate=%s inválido (ex.: 30m, 1h)", v)
					}
					ch.DRM.KeyRotationMinutes = m
				default:
					p.fail(line, "opção desconhecida em drm: %s", k)
				}
			}
		case "auth":
			if !need(1) {
				return
			}
			if args[0].text == "off" {
				extra(args, 1)
				ch.Auth = model.AuthConfig{}
				return
			}
			opts, pos := kv(args)
			ch.Auth = model.AuthConfig{Enabled: true}
			for _, a := range pos {
				switch {
				case a.text == "on":
				case strings.HasPrefix(a.text, "http://") || strings.HasPrefix(a.text, "https://"):
					ch.Auth.BackendURL = a.text
				default:
					p.fail(line, "auth: %q não é on, off nem URL do backend", a.text)
				}
			}
			list := func(v string) []string {
				var out []string
				for _, x := range strings.Split(v, ",") {
					if x = strings.TrimSpace(x); x != "" {
						out = append(out, x)
					}
				}
				return out
			}
			for k, v := range opts {
				switch k {
				case "backend":
					ch.Auth.BackendURL = v
				case "allow":
					ch.Auth.AllowIPs = list(v)
				case "deny":
					ch.Auth.DenyIPs = list(v)
				case "max_sessions":
					n, err := strconv.Atoi(v)
					if err != nil || n < 0 {
						p.fail(line, "auth: max_sessions=%s inválido", v)
					}
					ch.Auth.MaxSessions = n
				case "cache":
					n, err := strconv.Atoi(strings.TrimSuffix(v, "s"))
					if err != nil || n < 0 {
						p.fail(line, "auth: cache=%s inválido (segundos)", v)
					}
					ch.Auth.CacheSeconds = n
				case "on_error":
					if v != "allow" && v != "deny" {
						p.fail(line, "auth: on_error deve ser allow ou deny")
					}
					ch.Auth.AllowOnError = v == "allow"
				default:
					p.fail(line, "opção desconhecida em auth: %s", k)
				}
			}
		case "epg":
			if !need(1) {
				return
			}
			if args[0].text == "off" {
				extra(args, 1)
				ch.EPG = model.EPGConfig{}
				return
			}
			opts, pos := kv(args)
			for _, a := range pos {
				ch.EPG.URL = a.text
			}
			for k, v := range opts {
				switch k {
				case "id":
					ch.EPG.XMLTVID = v
				case "url":
					ch.EPG.URL = v
				default:
					p.fail(line, "opção desconhecida em epg: %s", k)
				}
			}
			if ch.EPG.URL == "" {
				p.fail(line, "epg precisa da URL do guia XMLTV")
			}
		case "overlay":
			// overlay logo.png position=top-right margin=24 width=200 opacity=0.8;  overlay off;
			if !need(1) {
				return
			}
			if args[0].text == "off" {
				extra(args, 1)
				ch.Overlay = model.OverlayConfig{}
				return
			}
			opts, pos := kv(args)
			ch.Overlay = model.OverlayConfig{Enabled: true}
			for _, a := range pos {
				ch.Overlay.Image = a.text
			}
			for k, v := range opts {
				switch k {
				case "position":
					ch.Overlay.Position = v
				case "margin", "width":
					n, err := strconv.Atoi(v)
					if err != nil {
						p.fail(line, "overlay: %s=%s não é um número", k, v)
						continue
					}
					if k == "margin" {
						ch.Overlay.Margin = n
					} else {
						ch.Overlay.Width = n
					}
				case "opacity":
					f, err := strconv.ParseFloat(v, 64)
					if err != nil {
						p.fail(line, "overlay: opacity=%s não é um número", v)
						continue
					}
					ch.Overlay.Opacity = f
				default:
					p.fail(line, "opção desconhecida em overlay: %s", k)
				}
			}
			if ch.Overlay.Image == "" {
				p.fail(line, "overlay precisa do nome do arquivo do logo, ex.: overlay logo.png;")
			}
		case "failover":
			p.failover(line, args, &fo)
		case "disabled":
			ch.Enabled = false
		default:
			p.fail(line, "diretiva desconhecida em stream: %s", key)
		}
	})
	if outputs == 0 {
		ch.Outputs.HLS = true
	}
	ch.Failover = fo.WithDefaults()
	return ch
}

func (p *parser) storage(id string) model.DVRStorage {
	st := model.DVRStorage{ID: id}
	p.body(func(w []token) {
		key, args := w[0].text, w[1:]
		switch key {
		case "mode":
			if len(args) != 1 {
				p.fail(w[0].line, "mode precisa de um valor (single, raid0, raid1)")
				return
			}
			st.Mode = args[0].text
		case "disk":
			if len(args) != 1 {
				p.fail(w[0].line, "disk precisa de um caminho")
				return
			}
			st.Disks = append(st.Disks, args[0].text)
		default:
			p.fail(w[0].line, "diretiva desconhecida em dvr_storage: %s", key)
		}
	})
	return st
}

func joinWords(ts []token) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = t.text
	}
	return strings.Join(parts, " ")
}

// ---- writer ----

func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\";{}#\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func retention(h int) string {
	if h%24 == 0 {
		return strconv.Itoa(h/24) + "d"
	}
	return strconv.Itoa(h) + "h"
}

// Format writes the configuration. Parse(Format(c)) yields the same config.
func Format(c Config) string {
	var b strings.Builder
	b.WriteString("# NexoStream — configuração do sistema, servidores, armazenamentos de DVR e canais.\n")
	b.WriteString("# Edite e aplique; a ordem dos inputs define a prioridade (1º = principal).\n\n")
	if c.Settings != nil {
		formatSettings(&b, *c.Settings)
	}
	servers := append([]model.ServerSettings(nil), c.Servers...)
	sort.Slice(servers, func(i, j int) bool { return servers[i].WorkerID < servers[j].WorkerID })
	for _, o := range servers {
		formatServer(&b, o)
	}
	storages := append([]model.DVRStorage(nil), c.Storages...)
	sort.Slice(storages, func(i, j int) bool { return storages[i].ID < storages[j].ID })
	for _, st := range storages {
		fmt.Fprintf(&b, "dvr_storage %s {\n  mode %s;\n", st.ID, st.Mode)
		for _, d := range st.Disks {
			fmt.Fprintf(&b, "  disk %s;\n", quote(d))
		}
		b.WriteString("}\n\n")
	}
	chs := append([]model.Channel(nil), c.Channels...)
	sort.Slice(chs, func(i, j int) bool { return chs[i].ID < chs[j].ID })
	def := model.DefaultFailover()
	for _, ch := range chs {
		fmt.Fprintf(&b, "stream %s {\n", ch.ID)
		if ch.Name != "" && ch.Name != ch.ID {
			fmt.Fprintf(&b, "  title %s;\n", quote(ch.Name))
		}
		if ch.Group != "" {
			fmt.Fprintf(&b, "  group %s;\n", quote(ch.Group))
		}
		for _, s := range ch.SortedSources() {
			if s.Interface != "" {
				fmt.Fprintf(&b, "  input %s interface=%s;\n", s.URL, s.Interface)
			} else {
				fmt.Fprintf(&b, "  input %s;\n", s.URL)
			}
		}
		switch {
		case ch.Mode == model.ModeTranscode && len(ch.Profiles) > 0:
			fmt.Fprintf(&b, "  transcode %s;\n", strings.Join(ch.Profiles, " "))
		case ch.Mode == model.ModeTranscode:
			b.WriteString("  mode transcode;\n")
		case ch.Mode == model.ModePassthrough:
			b.WriteString("  mode passthrough;\n")
		}
		if ch.Mode != model.ModeTranscode && len(ch.Profiles) > 0 {
			fmt.Fprintf(&b, "  profile %s;\n", ch.Profiles[0])
		}
		// Only HLS is the default and needs no output lines.
		if !ch.Outputs.HLS || ch.Outputs.MPEGTSHTTP || ch.Outputs.MPEGTSUDP != "" || ch.Outputs.DASH {
			if ch.Outputs.HLS {
				b.WriteString("  output hls;\n")
			}
			if ch.Outputs.MPEGTSHTTP {
				b.WriteString("  output mpegts_http;\n")
			}
			if ch.Outputs.DASH {
				b.WriteString("  output dash;\n")
			}
			if ch.Outputs.MPEGTSUDP != "" {
				fmt.Fprintf(&b, "  output %s;\n", ch.Outputs.MPEGTSUDP)
			}
		}
		for _, p := range ch.Outputs.Push {
			fmt.Fprintf(&b, "  push %s;\n", quote(p))
		}
		if ch.DVR.Enabled {
			line := "  dvr " + retention(ch.DVR.RetentionHours)
			if ch.DVR.Storage != "" {
				line += " storage=" + ch.DVR.Storage
			} else if ch.DVR.Path != "" {
				line += " path=" + quote(ch.DVR.Path)
			}
			if ch.DVR.AllQualities {
				line += " qualities=all"
			}
			b.WriteString(line + ";\n")
		}
		if ch.DRM.Enabled {
			line := "  drm " + ch.DRM.Method
			if ch.DRM.Method == "" {
				line = "  drm aes-128"
			}
			if m := ch.DRM.KeyRotationMinutes; m > 0 {
				if m%60 == 0 {
					line += fmt.Sprintf(" rotate=%dh", m/60)
				} else {
					line += fmt.Sprintf(" rotate=%dm", m)
				}
			}
			b.WriteString(line + ";\n")
		}
		if a := ch.Auth; a.Enabled {
			line := "  auth on"
			if a.BackendURL != "" {
				line = "  auth " + quote(a.BackendURL)
			}
			if len(a.AllowIPs) > 0 {
				line += " allow=" + strings.Join(a.AllowIPs, ",")
			}
			if len(a.DenyIPs) > 0 {
				line += " deny=" + strings.Join(a.DenyIPs, ",")
			}
			if a.MaxSessions > 0 {
				line += fmt.Sprintf(" max_sessions=%d", a.MaxSessions)
			}
			if a.CacheSeconds > 0 {
				line += fmt.Sprintf(" cache=%d", a.CacheSeconds)
			}
			if a.AllowOnError {
				line += " on_error=allow"
			}
			b.WriteString(line + ";\n")
		}
		if ch.EPG.URL != "" {
			line := "  epg " + quote(ch.EPG.URL)
			if ch.EPG.XMLTVID != "" {
				line += " id=" + quote(ch.EPG.XMLTVID)
			}
			b.WriteString(line + ";\n")
		}
		if o := ch.Overlay; o.Enabled {
			line := "  overlay " + quote(o.Image)
			if o.Position != "" {
				line += " position=" + o.Position
			}
			if o.Margin != 0 {
				line += " margin=" + strconv.Itoa(o.Margin)
			}
			if o.Width != 0 {
				line += " width=" + strconv.Itoa(o.Width)
			}
			if o.Opacity != 0 {
				line += " opacity=" + strconv.FormatFloat(o.Opacity, 'f', -1, 64)
			}
			b.WriteString(line + ";\n")
		}
		if f := failoverText(ch.Failover, &def); f != "" {
			fmt.Fprintf(&b, "  failover %s;\n", f)
		}
		if !ch.Enabled {
			b.WriteString("  disabled;\n")
		}
		b.WriteString("}\n\n")
	}
	return b.String()
}
