// Package settings loads, validates and saves the panel-managed settings
// (Settings page and the "settings" / "server" blocks of the text config).
// They live in the store as JSON documents: "global" and "server:<id>".
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"nexostream/control-plane/store"
	"nexostream/internal/model"
)

const (
	GlobalKey    = "global"
	ServerPrefix = "server:"
)

// Store is the part of store.Store used here.
type Store interface {
	GetSetting(ctx context.Context, key string) ([]byte, error)
	PutSetting(ctx context.Context, key string, value []byte) error
	ListSettings(ctx context.Context, prefix string) (map[string][]byte, error)
}

// Load returns the global settings. saved is false until someone saves them
// in the panel; callers then keep their .env values.
func Load(ctx context.Context, st Store) (s model.Settings, saved bool, err error) {
	b, err := st.GetSetting(ctx, GlobalKey)
	if errors.Is(err, store.ErrNotFound) {
		return model.DefaultSettings(), false, nil
	}
	if err != nil {
		return model.DefaultSettings(), false, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return model.DefaultSettings(), false, fmt.Errorf("parse settings: %w", err)
	}
	return s.WithDefaults(), true, nil
}

// Save stores the global settings.
func Save(ctx context.Context, st Store, s model.Settings) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return st.PutSetting(ctx, GlobalKey, b)
}

// LoadServer returns a node's overrides (empty when never saved).
func LoadServer(ctx context.Context, st Store, id string) (model.ServerSettings, error) {
	b, err := st.GetSetting(ctx, ServerPrefix+id)
	if errors.Is(err, store.ErrNotFound) {
		return model.ServerSettings{WorkerID: id}, nil
	}
	if err != nil {
		return model.ServerSettings{WorkerID: id}, err
	}
	var o model.ServerSettings
	if err := json.Unmarshal(b, &o); err != nil {
		return model.ServerSettings{WorkerID: id}, fmt.Errorf("parse server settings: %w", err)
	}
	o.WorkerID = id
	return o, nil
}

// SaveServer stores a node's overrides.
func SaveServer(ctx context.Context, st Store, o model.ServerSettings) error {
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	return st.PutSetting(ctx, ServerPrefix+o.WorkerID, b)
}

// ListServers returns every node with saved overrides, by id.
func ListServers(ctx context.Context, st Store) (map[string]model.ServerSettings, error) {
	all, err := st.ListSettings(ctx, ServerPrefix)
	if err != nil {
		return nil, err
	}
	out := make(map[string]model.ServerSettings, len(all))
	for k, b := range all {
		var o model.ServerSettings
		if err := json.Unmarshal(b, &o); err != nil {
			continue
		}
		o.WorkerID = strings.TrimPrefix(k, ServerPrefix)
		out[o.WorkerID] = o
	}
	return out, nil
}

// ServerIDs returns the ids of ListServers sorted.
func ServerIDs(m map[string]model.ServerSettings) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// FieldErrors maps a field to a message (Portuguese, shown in the panel).
type FieldErrors map[string]string

func (fe FieldErrors) add(f, msg string) { fe[f] = msg }

func (fe FieldErrors) rangeI(f string, v, lo, hi int) {
	if v < lo || v > hi {
		fe.add(f, fmt.Sprintf("entre %d e %d", lo, hi))
	}
}

func (fe FieldErrors) rangeF(f string, v, lo, hi float64) {
	if v < lo || v > hi {
		fe.add(f, fmt.Sprintf("entre %g e %g", lo, hi))
	}
}

// Validate checks the global settings. profiles and storages are the known
// ids (nil skips the check).
func Validate(s model.Settings, profiles, storages map[string]bool) FieldErrors {
	fe := FieldErrors{}
	if n := len(strings.TrimSpace(s.SystemName)); n == 0 || n > 64 {
		fe.add("system_name", "de 1 a 64 caracteres")
	}
	if len(s.BrandTagline) > 64 {
		fe.add("brand_tagline", "até 64 caracteres")
	}
	if s.BrandColor != "" && !colorRe.MatchString(s.BrandColor) {
		fe.add("brand_color", "use uma cor #RRGGBB")
	}
	if len(s.BrandLoginMessage) > 300 {
		fe.add("brand_login_message", "até 300 caracteres")
	}
	if l := s.BrandLogo; l != "" {
		ok := false
		for _, t := range logoTypes {
			if strings.HasPrefix(l, "data:"+t+";base64,") {
				ok = true
			}
		}
		switch {
		case !ok:
			fe.add("brand_logo", "use PNG, SVG, JPEG ou WebP")
		case len(l) > MaxLogoChars:
			fe.add("brand_logo", "imagem grande demais (até 256 KB)")
		}
	}
	if profiles != nil && !profiles[s.DefaultProfile] {
		fe.add("default_profile", "perfil inexistente: "+s.DefaultProfile)
	}
	fe.rangeI("udp_read_buffer_bytes", s.UDPReadBufferBytes, 64<<10, 256<<20)
	fo := s.DefaultFailover
	fe.rangeF("default_failover.no_packets_seconds", fo.NoPacketsSeconds, 0.5, 60)
	fe.rangeF("default_failover.confirm_window_seconds", fo.ConfirmWindowSeconds, 0, 120)
	fe.rangeF("default_failover.low_bitrate_ratio", fo.LowBitrateRatio, 0.01, 0.99)
	fe.rangeF("default_failover.low_bitrate_seconds", fo.LowBitrateSeconds, 1, 600)
	fe.rangeF("default_failover.missing_stream_seconds", fo.MissingStreamSeconds, 1, 120)
	fe.rangeF("default_failover.primary_stable_seconds", fo.PrimaryStableSeconds, 1, 3600)
	fe.rangeI("hls_segment_seconds", s.HLSSegmentSeconds, 1, 30)
	fe.rangeI("hls_list_size", s.HLSListSize, 3, 60)
	fe.rangeI("dvr_default_retention_hours", s.DVRDefaultRetentionHours, 1, 24*365)
	if storages != nil && s.DVRDefaultStorage != "" && !storages[s.DVRDefaultStorage] {
		fe.add("dvr_default_storage", "armazenamento inexistente: "+s.DVRDefaultStorage)
	}
	fe.rangeF("scheduler_max_cpu_percent", s.SchedulerMaxCPUPercent, 10, 100)
	fe.rangeF("cost_passthrough", s.CostPassthrough, 0.01, 100)
	fe.rangeF("cost_transcode_per_megapixel", s.CostTranscodePerMegapixel, 0.01, 100)
	fe.rangeF("cost_gpu_offload", s.CostGPUOffload, 1, 100)
	fe.rangeI("worker_offline_seconds", s.WorkerOfflineSeconds, 10, 600)
	fe.rangeI("drain_migrations_per_pass", s.DrainMigrationsPerPass, 1, 100)
	fe.rangeI("access_token_minutes", s.AccessTokenMinutes, 1, 24*60)
	fe.rangeI("refresh_token_hours", s.RefreshTokenHours, 1, 24*30)
	fe.rangeI("login_attempts_per_minute", s.LoginAttemptsPerMinute, 1, 1000)
	return fe
}

var ifaceRe = regexp.MustCompile(`^[A-Za-z0-9._:@-]{1,32}$`)

// ValidateServer checks a node's overrides.
func ValidateServer(o model.ServerSettings) FieldErrors {
	fe := FieldErrors{}
	if !ifaceRe.MatchString(o.WorkerID) {
		fe.add("worker_id", "identificador de servidor inválido")
	}
	for i, n := range o.IngestInterfaces {
		if !ifaceRe.MatchString(n) {
			fe.add(fmt.Sprintf("ingest_interfaces[%d]", i), "nome de interface inválido")
		}
	}
	if p := o.DVRDir; p != "" && (!absPath(p) || strings.Contains(p, "..") || len(p) > 255) {
		fe.add("dvr_dir", "use um caminho absoluto no servidor de mídia, ex.: /storage/dvr")
	}
	for i, r := range o.Roles {
		if !slices.Contains(model.Roles, r) {
			fe.add(fmt.Sprintf("roles[%d]", i), "papel inválido: use passthrough, transcode ou dvr")
		}
	}
	for f, v := range map[string]string{"public_hls_base": o.PublicHLSBase, "public_ts_base": o.PublicTSBase, "public_dvr_base": o.PublicDVRBase} {
		if v == "" {
			continue
		}
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			fe.add(f, "use uma URL http(s)://host/caminho")
		}
	}
	return fe
}

// absPath accepts Unix paths and Windows drive paths (development).
func absPath(p string) bool {
	if path.IsAbs(p) {
		return true
	}
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z'))
}

// Branding limits.
var (
	colorRe   = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	logoTypes = []string{"image/png", "image/svg+xml", "image/jpeg", "image/webp"}
)

// MaxLogoChars bounds the logo data URL (256 KB of image in base64).
const MaxLogoChars = (256 << 10) * 4 / 3

// Normalize trims text fields and drops empty interface names.
func Normalize(s *model.Settings) {
	s.SystemName = strings.TrimSpace(s.SystemName)
	s.BrandTagline = strings.TrimSpace(s.BrandTagline)
	s.BrandColor = strings.ToLower(strings.TrimSpace(s.BrandColor))
	s.BrandLoginMessage = strings.TrimSpace(s.BrandLoginMessage)
	s.DefaultProfile = strings.TrimSpace(s.DefaultProfile)
	s.DVRDefaultStorage = strings.TrimSpace(s.DVRDefaultStorage)
}

// NormalizeServer trims fields and drops empty interface names.
func NormalizeServer(o *model.ServerSettings) {
	o.WorkerID = strings.TrimSpace(o.WorkerID)
	ifs := o.IngestInterfaces[:0:0]
	for _, n := range o.IngestInterfaces {
		if n = strings.TrimSpace(n); n != "" {
			ifs = append(ifs, n)
		}
	}
	o.IngestInterfaces = ifs
	o.DVRDir = strings.TrimSpace(o.DVRDir)
	o.PublicHLSBase = strings.TrimSpace(o.PublicHLSBase)
	o.PublicTSBase = strings.TrimSpace(o.PublicTSBase)
	o.PublicDVRBase = strings.TrimSpace(o.PublicDVRBase)
	var roles []string
	for _, r := range o.Roles {
		if r = strings.ToLower(strings.TrimSpace(r)); r != "" && !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	o.Roles = roles
}
