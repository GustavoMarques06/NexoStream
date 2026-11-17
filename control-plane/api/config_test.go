package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func (e *tenv) getText(path string) string {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+e.tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		e.t.Fatalf("GET %s: %d %s", path, resp.StatusCode, b)
	}
	return string(b)
}

func plan(out map[string]any, kind, op string) []any {
	p, _ := out[kind].(map[string]any)
	l, _ := p[op].([]any)
	return l
}

func TestConfigEditor(t *testing.T) {
	e := newTestEnv(t)
	text := `
dvr_storage principal {
  mode raid1;
  disk /mnt/disk1;
  disk /mnt/disk2;
}
stream canal-a {
  title "Canal A";
  input udp://239.1.1.1:1234;
  input srt://10.0.0.5:9000;
  dvr 3d storage=principal;
}
stream canal-b { input https://cdn.example/live/index.m3u8; transcode sd; disabled; }
`
	// Validate: plan without applying.
	code, out, _ := e.do("POST", "/api/v1/config/validate", e.tok, map[string]any{"text": text})
	if code != 200 || out["ok"] != true || len(plan(out, "channels", "create")) != 2 || len(plan(out, "storages", "create")) != 1 {
		t.Fatalf("validate %d %v", code, out)
	}
	if n, _, _ := e.st.CountChannels(context.Background()); n != 0 {
		t.Fatal("validate must not change anything")
	}

	// Apply.
	code, out, _ = e.do("POST", "/api/v1/config/apply", e.tok, map[string]any{"text": text})
	if code != 200 || out["applied"] != true {
		t.Fatalf("apply %d %v", code, out)
	}
	a, err := e.st.GetChannel(context.Background(), "canal-a")
	if err != nil || len(a.Sources) != 2 || a.DVR.Storage != "principal" || a.DVR.RetentionHours != 72 {
		t.Fatalf("canal-a %+v %v", a, err)
	}
	st, _ := e.st.GetDVRStorage(context.Background(), "principal")
	if st.Mode != "raid1" || len(st.Disks) != 2 {
		t.Fatalf("storage %+v", st)
	}

	// Export, then re-apply the export: nothing changes.
	exported := e.getText("/api/v1/config")
	if !strings.Contains(exported, "stream canal-a {") || !strings.Contains(exported, "dvr_storage principal {") {
		t.Fatalf("export:\n%s", exported)
	}
	_, out, _ = e.do("POST", "/api/v1/config/validate", e.tok, map[string]any{"text": exported})
	if out["ok"] != true || len(plan(out, "channels", "update")) != 0 || len(plan(out, "channels", "create")) != 0 {
		t.Fatalf("re-validate export: %v\n%s", out, exported)
	}

	// Errors point to lines; nothing is applied.
	bad := "stream canal-c {\n  input ftp://x:1;\n  dvr 3d storage=fantasma;\n}\n"
	code, out, _ = e.do("POST", "/api/v1/config/apply", e.tok, map[string]any{"text": bad})
	errs, _ := out["errors"].([]any)
	if code != 422 || len(errs) < 2 || errs[0].(map[string]any)["line"].(float64) != 1 {
		t.Fatalf("errors %d %v", code, out)
	}
	if _, err := e.st.GetChannel(context.Background(), "canal-c"); err == nil {
		t.Fatal("invalid config was applied")
	}

	// Prune removes what is not in the text.
	only := "stream canal-a { input udp://239.1.1.1:1234; }\n"
	code, out, _ = e.do("POST", "/api/v1/config/apply", e.tok, map[string]any{"text": only, "prune": true})
	if code != 200 || len(plan(out, "channels", "delete")) != 1 || len(plan(out, "storages", "delete")) != 1 ||
		len(plan(out, "channels", "update")) != 1 {
		t.Fatalf("prune %d %v", code, out)
	}
	if n, _, _ := e.st.CountChannels(context.Background()); n != 1 {
		t.Fatalf("channels after prune: %d", n)
	}

	// Storages need dvr:write.
	if code, _, _ := e.do("POST", "/api/v1/users", e.tok, map[string]string{"username": "viewer2", "password": "senha-forte-123", "role": "viewer"}); code != 201 {
		t.Fatal("user")
	}
	viewer := e.login("viewer2", "senha-forte-123")
	if code, _, _ := e.do("POST", "/api/v1/config/apply", viewer, map[string]any{"text": only}); code != 403 {
		t.Fatalf("viewer apply: %d", code)
	}
}

func TestDVRStoragesAPI(t *testing.T) {
	e := newTestEnv(t)
	code, out, _ := e.do("POST", "/api/v1/dvr/storages", e.tok, map[string]any{"id": "raid", "mode": "raid0", "disks": []string{"/mnt/a"}})
	if code != 422 {
		t.Fatalf("raid0 with one disk: %d %v", code, out)
	}
	code, out, _ = e.do("POST", "/api/v1/dvr/storages", e.tok, map[string]any{"id": "raid", "mode": "raid0", "disks": []string{"/mnt/a", "/mnt/b"}})
	if code != 201 || out["mode"] != "raid0" {
		t.Fatalf("create %d %v", code, out)
	}
	body := channelBody("canal-r")
	body["dvr"] = map[string]any{"enabled": true, "retention_hours": 24, "storage": "raid"}
	if code, out, _ := e.do("POST", "/api/v1/channels", e.tok, body); code != 201 {
		t.Fatalf("channel %d %v", code, out)
	}
	rev := func() float64 {
		_, c, _ := e.do("GET", "/api/v1/channels/canal-r", e.tok, nil)
		return c["revision"].(float64)
	}
	before := rev()
	if code, out, _ := e.do("PATCH", "/api/v1/dvr/storages/raid", e.tok, map[string]any{"disks": []string{"/mnt/a", "/mnt/b", "/mnt/c"}}); code != 200 {
		t.Fatalf("patch %d %v", code, out)
	}
	if rev() <= before {
		t.Fatal("channel revision not bumped after storage change")
	}
	if code, _, _ := e.do("DELETE", "/api/v1/dvr/storages/raid", e.tok, nil); code != 409 {
		t.Fatalf("delete used storage: %d", code)
	}
	body["id"] = "canal-x"
	body["dvr"] = map[string]any{"enabled": true, "retention_hours": 24, "storage": "nada"}
	if code, _, _ := e.do("POST", "/api/v1/channels", e.tok, body); code != 422 {
		t.Fatalf("unknown storage: %d", code)
	}
}
