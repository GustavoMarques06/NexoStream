package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"nexostream/control-plane/store"
	"nexostream/internal/model"
)

func TestSettingsAPI(t *testing.T) {
	e := newTestEnv(t)

	// Before the first save the values come from the .env (here: defaults).
	code, out, _ := e.do("GET", "/api/v1/settings", e.tok, nil)
	if code != 200 || out["saved"] != false {
		t.Fatalf("%d %v", code, out)
	}
	st := out["settings"].(map[string]any)
	if st["hls_segment_seconds"].(float64) != 6 || st["access_token_minutes"].(float64) != 15 {
		t.Fatalf("defaults %v", st)
	}

	// Validation reports every bad field.
	bad := model.DefaultSettings()
	bad.HLSSegmentSeconds = 99
	bad.DefaultProfile = "nao-existe"
	bad.DVRDefaultStorage = "nenhum"
	code, out, _ = e.do("PUT", "/api/v1/settings", e.tok, bad)
	if code != 422 || errCode(out) != "invalid_settings" {
		t.Fatalf("%d %v", code, out)
	}
	details := out["error"].(map[string]any)["details"].(map[string]any)
	for _, f := range []string{"hls_segment_seconds", "default_profile", "dvr_default_storage"} {
		if _, ok := details[f]; !ok {
			t.Errorf("missing error for %s: %v", f, details)
		}
	}

	// Save.
	good := model.DefaultSettings()
	good.SystemName = "Operadora X"
	good.AccessTokenMinutes = 30
	good.DVRDefaultRetentionHours = 24
	good.DefaultFailover.NoPacketsSeconds = 2
	code, out, _ = e.do("PUT", "/api/v1/settings", e.tok, good)
	if code != 200 || out["saved"] != true {
		t.Fatalf("%d %v", code, out)
	}
	if out["settings"].(map[string]any)["updated_by"] != "admin" {
		t.Fatalf("updated_by %v", out["settings"])
	}

	// Token lifetime follows the settings.
	_, tok, _ := e.do("POST", "/api/v1/auth/token", "", map[string]string{"username": "admin", "password": "admin-password-123"})
	if tok["expires_in"].(float64) != 1800 {
		t.Fatalf("expires_in %v", tok["expires_in"])
	}

	// New channels take the default failover and DVR retention.
	body := map[string]any{
		"id": "c1", "name": "C1", "mode": "passthrough",
		"sources": []map[string]any{{"priority": 1, "url": "udp://239.1.1.1:1234"}},
		"outputs": map[string]any{"hls": true},
		"dvr":     map[string]any{"enabled": true},
	}
	code, out, _ = e.do("POST", "/api/v1/channels", e.tok, body)
	if code != 201 {
		t.Fatalf("create %d %v", code, out)
	}
	if fo := out["failover"].(map[string]any); fo["no_packets_seconds"].(float64) != 2 {
		t.Fatalf("failover %v", fo)
	}
	if dv := out["dvr"].(map[string]any); dv["retention_hours"].(float64) != 24 {
		t.Fatalf("dvr %v", dv)
	}

	// Audit.
	entries, _ := e.st.ListAudit(t.Context(), storeAuditFilter())
	found := false
	for _, a := range entries {
		if a.Action == "settings.update" {
			found = true
			if !strings.Contains(strings.Join(toStrings(a.Details["changed"]), ","), "system_name") {
				t.Fatalf("changed %v", a.Details)
			}
		}
	}
	if !found {
		t.Fatal("no settings.update audit entry")
	}

	// Per-server settings.
	code, out, _ = e.do("PUT", "/api/v1/settings/servers/media-01", e.tok, map[string]any{
		"ingest_interfaces": []string{"eno2", " "}, "dvr_dir": "/storage/dvr", "public_hls_base": "https://origin.example/hls",
	})
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	rt := out["runtime"].(map[string]any)
	if rt["dvr_dir"] != "/storage/dvr" || rt["hls_segment_seconds"].(float64) != 6 {
		t.Fatalf("runtime %v", rt)
	}
	if ifs := out["settings"].(map[string]any)["ingest_interfaces"].([]any); len(ifs) != 1 {
		t.Fatalf("interfaces %v", ifs)
	}
	code, out, _ = e.do("PUT", "/api/v1/settings/servers/media-01", e.tok, map[string]any{"dvr_dir": "relativo", "public_ts_base": "ftp://x"})
	if code != 422 {
		t.Fatalf("%d %v", code, out)
	}
	code, out, _ = e.do("GET", "/api/v1/settings/servers", e.tok, nil)
	if items := out["items"].([]any); code != 200 || len(items) != 1 {
		t.Fatalf("%d %v", code, out)
	}

	// Only admins.
	if code, _, _ := e.do("POST", "/api/v1/users", e.tok, map[string]string{"username": "op", "password": "senha-forte-123", "role": "operator"}); code != 201 {
		t.Fatal("create operator")
	}
	op := e.login("op", "senha-forte-123")
	if code, _, _ := e.do("GET", "/api/v1/settings", op, nil); code != 403 {
		t.Fatalf("operator settings: %d", code)
	}
	code, out, _ = e.do("GET", "/api/v1/settings/defaults", op, nil)
	if code != 200 || out["system_name"] != "Operadora X" || out["dvr_default_retention_hours"].(float64) != 24 {
		t.Fatalf("defaults for operators: %d %v", code, out)
	}
}

func TestConfigEditorSettings(t *testing.T) {
	e := newTestEnv(t)
	text := e.getText("/api/v1/config")
	if !strings.Contains(text, "settings {") || !strings.Contains(text, "hls segment=6 window=10;") {
		t.Fatalf("export without settings:\n%s", text)
	}
	text = strings.Replace(text, "hls segment=6 window=10;", "hls segment=4 window=8;", 1)
	text += "server media-01 {\n  dvr_dir /storage/dvr;\n}\n"
	code, out, _ := e.do("POST", "/api/v1/config/validate", e.tok, map[string]any{"text": text})
	if code != 200 || out["ok"] != true || out["settings"] != "update" || len(plan(out, "servers", "create")) != 1 {
		t.Fatalf("%d %v", code, out)
	}
	code, out, _ = e.do("POST", "/api/v1/config/apply", e.tok, map[string]any{"text": text})
	if code != 200 || out["applied"] != true {
		t.Fatalf("%d %v", code, out)
	}
	_, out, _ = e.do("GET", "/api/v1/settings", e.tok, nil)
	if st := out["settings"].(map[string]any); st["hls_segment_seconds"].(float64) != 4 || st["hls_list_size"].(float64) != 8 {
		t.Fatalf("not applied %v", st)
	}
	// Re-applying the export changes nothing.
	again := e.getText("/api/v1/config")
	_, out, _ = e.do("POST", "/api/v1/config/validate", e.tok, map[string]any{"text": again})
	if out["settings"] != "unchanged" || plan(out, "servers", "update") != nil && len(plan(out, "servers", "update")) > 0 {
		t.Fatalf("export not stable: %v\n%s", out, again)
	}

	// Errors point to the settings line.
	bad := "settings {\n  hls segment=0 window=8;\n}\n"
	_, out, _ = e.do("POST", "/api/v1/config/validate", e.tok, map[string]any{"text": bad})
	errs, _ := out["errors"].([]any)
	if out["ok"] != false || len(errs) == 0 || errs[0].(map[string]any)["line"].(float64) != 1 {
		t.Fatalf("%v", out)
	}

	// Operators get no settings block and cannot send one.
	if code, _, _ := e.do("POST", "/api/v1/users", e.tok, map[string]string{"username": "op", "password": "senha-forte-123", "role": "operator"}); code != 201 {
		t.Fatal("create operator")
	}
	op := e.login("op", "senha-forte-123")
	e2 := *e
	e2.tok = op
	if strings.Contains(e2.getText("/api/v1/config"), "settings {") {
		t.Fatal("operator export has settings")
	}
	_, out, _ = e.do("POST", "/api/v1/config/validate", op, map[string]any{"text": "settings {\n  login_limit 3;\n}\n"})
	if out["ok"] != false || !strings.Contains(toJSON(out["errors"]), "settings:admin") {
		t.Fatalf("%v", out)
	}
}

func storeAuditFilter() store.AuditFilter { return store.AuditFilter{Limit: 100} }

func toStrings(v any) []string {
	var out []string
	switch l := v.(type) {
	case []string:
		out = l
	case []any:
		for _, x := range l {
			out = append(out, fmt.Sprint(x))
		}
	}
	return out
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
