package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const (
	secretPass = "correct-horse-battery-1"
	secretKey  = "abcd-secret-stream-key"
	secretTok  = "tok-secret-9f8e7d"
	secretCam  = "hunter2"
)

func secretChannel(id string) map[string]any {
	ch := channelBody(id)
	ch["sources"] = []map[string]any{
		{"priority": 1, "url": "srt://0.0.0.0:9000?mode=listener&passphrase=" + secretPass},
		{"priority": 2, "url": "rtsp://admin:" + secretCam + "@192.168.1.9/stream"},
	}
	ch["outputs"] = map[string]any{"hls": true, "push": []string{"rtmp://a.rtmp.youtube.com/live2/" + secretKey}}
	ch["auth"] = map[string]any{"enabled": true, "backend_url": "https://mw.example/check?token=" + secretTok}
	return ch
}

func leaks(body string) []string {
	var found []string
	for _, s := range []string{secretPass, secretKey, secretTok, secretCam} {
		if strings.Contains(body, s) {
			found = append(found, s)
		}
	}
	return found
}

func (e *tenv) raw(path, tok string) (int, string) {
	e.t.Helper()
	code, out, _ := e.do("GET", path, tok, nil)
	b, _ := json.Marshal(out)
	return code, string(b)
}

// Only who can edit channels sees the secrets they hold; a read-only user or
// token sees them masked, in every route that returns the configuration.
func TestSecretsAreHiddenFromReadOnlyUsers(t *testing.T) {
	e, _ := secEnv(t, nil)
	if code, out, _ := e.do("POST", "/api/v1/channels", e.tok, secretChannel("seg-1")); code != 201 {
		t.Fatalf("create: %d %v", code, out)
	}
	if code, out, _ := e.do("POST", "/api/v1/users", e.tok, map[string]any{"username": "leitor", "password": "leitor-zebra-lantern-7", "role": "viewer"}); code != 201 {
		t.Fatalf("create viewer: %d %v", code, out)
	}
	_, lo := e.loginFull("leitor", "leitor-zebra-lantern-7")
	viewer := lo["access_token"].(string)

	// The editor sees the real values: the form needs them.
	code, body := e.raw("/api/v1/channels/seg-1", e.tok)
	if code != 200 || len(leaks(body)) != 4 {
		t.Fatalf("the admin must see every secret, saw %v in %s", leaks(body), body)
	}

	for _, path := range []string{
		"/api/v1/channels/seg-1", "/api/v1/channels", "/api/v1/channels/seg-1/sources",
	} {
		code, body := e.raw(path, viewer)
		if code != 200 {
			t.Fatalf("%s as viewer: %d", path, code)
		}
		if l := leaks(body); len(l) > 0 {
			t.Errorf("%s leaks %v to a read-only user:\n%s", path, l, body)
		}
		if !strings.Contains(body, "••••") {
			t.Errorf("%s: the masked value is not visible:\n%s", path, body)
		}
	}
	// The text export is a channel read as well.
	resp, err := httpGetWith(e, "/api/v1/config", viewer)
	if err != nil {
		t.Fatal(err)
	}
	if l := leaks(resp); len(l) > 0 {
		t.Errorf("GET /config leaks %v to a read-only user:\n%s", l, resp)
	}
	adminCfg, _ := httpGetWith(e, "/api/v1/config", e.tok)
	if len(leaks(adminCfg)) == 0 {
		t.Error("the admin's export must keep the real values (it is the backup)")
	}
	// What is stored is untouched: masking happens on the way out only.
	if _, body := e.raw("/api/v1/channels/seg-1", e.tok); len(leaks(body)) != 4 {
		t.Fatal("reading as a viewer must not change what is stored")
	}
	// An integration token with read scope only is a read-only caller too.
	_, tk, _ := e.do("POST", "/api/v1/api-tokens", e.tok, map[string]any{"name": "ro", "scopes": []string{"channels:read"}})
	if _, body := e.raw("/api/v1/channels/seg-1", tk["token"].(string)); len(leaks(body)) > 0 {
		t.Errorf("a read-only integration token sees %v", leaks(body))
	}
	// Audit entries of source changes carry the masked address.
	e.do("POST", "/api/v1/channels/seg-1/sources", e.tok, map[string]any{"priority": 3, "url": "rtmp://src.example/live/" + secretKey})
	_, audit := e.raw("/api/v1/audit-log", e.tok)
	if strings.Contains(audit, secretKey) {
		t.Errorf("the audit log stored a stream key:\n%s", audit)
	}
}

// A listener opens a port to whoever can reach it; the passphrase is what
// authenticates the sender.
func TestSRTListenerNeedsAPassphrase(t *testing.T) {
	e, _ := secEnv(t, nil)
	create := func(id, url string) (int, map[string]any) {
		ch := channelBody(id)
		ch["sources"] = []map[string]any{{"priority": 1, "url": url}}
		code, out, _ := e.do("POST", "/api/v1/channels", e.tok, ch)
		return code, out
	}
	for name, url := range map[string]string{
		"no passphrase":    "srt://0.0.0.0:9000?mode=listener",
		"too short":        "srt://0.0.0.0:9000?mode=listener&passphrase=short",
		"too long":         "srt://0.0.0.0:9000?mode=listener&passphrase=" + strings.Repeat("x", 80),
		"empty passphrase": "srt://0.0.0.0:9000?mode=listener&passphrase=",
	} {
		code, out := create("srt-"+strings.ReplaceAll(name, " ", "-"), url)
		if code != 422 {
			t.Errorf("%s: %d, want 422", name, code)
			continue
		}
		b, _ := json.Marshal(out)
		if !strings.Contains(string(b), "sources[0].url") || !strings.Contains(string(b), "passphrase") {
			t.Errorf("%s: the error must name the field and the cause: %s", name, b)
		}
	}
	if code, out := create("srt-ok", "srt://0.0.0.0:9000?mode=listener&passphrase="+secretPass); code != 201 {
		t.Fatalf("a listener with a passphrase: %d %v", code, out)
	}
	// Connecting out to a sender (caller) needs nothing: the node is the one dialing.
	if code, out := create("srt-caller", "srt://cdn.example:9000?mode=caller"); code != 201 {
		t.Fatalf("a caller without a passphrase: %d %v", code, out)
	}
}

// httpGetWith returns the body of a GET (text routes such as /config are not JSON).
func httpGetWith(e *tenv, path, tok string) (string, error) {
	req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

func TestOverlayValidation(t *testing.T) {
	e, _ := secEnv(t, nil)
	create := func(id, mode string, ov map[string]any) (int, string) {
		ch := channelBody(id)
		ch["mode"] = mode
		if mode == "transcode" {
			ch["profiles"] = []string{"hd"}
		}
		ch["overlay"] = ov
		code, out, _ := e.do("POST", "/api/v1/channels", e.tok, ch)
		b, _ := json.Marshal(out)
		return code, string(b)
	}
	if code, body := create("ov-ok", "transcode", map[string]any{"enabled": true, "image": "logo.png", "position": "bottom-left", "margin": 20, "width": 160, "opacity": 0.8}); code != 201 || !strings.Contains(body, `"position":"bottom-left"`) {
		t.Fatalf("valid overlay: %d %s", code, body)
	}
	if code, body := create("ov-default", "transcode", map[string]any{"enabled": true, "image": "logo.png"}); code != 201 || !strings.Contains(body, `"position":"top-right"`) {
		t.Fatalf("the default position is top-right: %d %s", code, body)
	}
	for name, c := range map[string]struct {
		mode  string
		ov    map[string]any
		field string
	}{
		"passthrough":   {"passthrough", map[string]any{"enabled": true, "image": "logo.png"}, "overlay.enabled"},
		"path as image": {"transcode", map[string]any{"enabled": true, "image": "../../etc/passwd"}, "overlay.image"},
		"position":      {"transcode", map[string]any{"enabled": true, "image": "logo.png", "position": "center"}, "overlay.position"},
		"opacity":       {"transcode", map[string]any{"enabled": true, "image": "logo.png", "opacity": 1.5}, "overlay.opacity"},
		"width":         {"transcode", map[string]any{"enabled": true, "image": "logo.png", "width": -1}, "overlay.width"},
	} {
		code, body := create("ov-"+strings.ReplaceAll(name, " ", "-"), c.mode, c.ov)
		if code != 422 || !strings.Contains(body, c.field) {
			t.Errorf("%s: want 422 on %s, got %d %s", name, c.field, code, body)
		}
	}
	// Disabled: whatever was filled is dropped.
	if code, body := create("ov-off", "passthrough", map[string]any{"enabled": false, "image": "../x"}); code != 201 || strings.Contains(body, "../x") {
		t.Fatalf("a disabled overlay is cleared: %d %s", code, body)
	}
}

func TestRTMPListenerNeedsALongKey(t *testing.T) {
	e, _ := secEnv(t, nil)
	create := func(id, url string) int {
		ch := channelBody(id)
		ch["sources"] = []map[string]any{{"priority": 1, "url": url}}
		code, out, _ := e.do("POST", "/api/v1/channels", e.tok, ch)
		if code != 201 {
			t.Logf("%s: %d %v", id, code, out)
		}
		return code
	}
	if code := create("rtmp-short", "rtmp://0.0.0.0:1935/live/curta?mode=listener"); code != 422 {
		t.Fatalf("a short key: %d, want 422", code)
	}
	if code := create("rtmp-ok", "rtmp://0.0.0.0:1935/live/"+strings.Repeat("k", 24)+"?mode=listener"); code != 201 {
		t.Fatalf("a long key: %d, want 201", code)
	}
	if code := create("rtmp-pull", "rtmp://server.example/live/key"); code != 201 {
		t.Fatalf("pulling from a server keeps working: %d", code)
	}
	// The key never reaches a read-only user.
	e.do("POST", "/api/v1/users", e.tok, map[string]any{"username": "leitor2", "password": "leitor-zebra-lantern-7", "role": "viewer"})
	_, lo := e.loginFull("leitor2", "leitor-zebra-lantern-7")
	if _, body := e.raw("/api/v1/channels/rtmp-ok", lo["access_token"].(string)); strings.Contains(body, strings.Repeat("k", 24)) {
		t.Fatal("a read-only user sees the RTMP key")
	}
}
