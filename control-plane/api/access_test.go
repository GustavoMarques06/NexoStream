package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nexostream/internal/model"
)

func TestChannelAuthAndEPG(t *testing.T) {
	e := newTestEnv(t)
	body := channelBody("acesso")
	body["auth"] = map[string]any{"enabled": true, "backend_url": "ftp://x", "allow_ips": []string{"10.0.0.0/8", "nao-ip"}, "max_sessions": -1}
	body["epg"] = map[string]any{"url": "guia.xml"}
	code, out, _ := e.do("POST", "/api/v1/channels", e.tok, body)
	if code != 422 {
		t.Fatalf("%d %v", code, out)
	}
	details := out["error"].(map[string]any)["details"].(map[string]any)
	for _, f := range []string{"auth.backend_url", "auth.allow_ips", "auth.max_sessions", "epg.url"} {
		if details[f] == nil {
			t.Errorf("missing %s: %v", f, details)
		}
	}

	guide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := time.Now().UTC()
		f := func(t time.Time) string { return t.Format("20060102150405 +0000") }
		_, _ = io.WriteString(w, `<tv><programme start="`+f(now.Add(-30*time.Minute))+`" stop="`+f(now.Add(30*time.Minute))+
			`" channel="x.br"><title>Agora</title></programme></tv>`)
	}))
	defer guide.Close()
	body["auth"] = map[string]any{"enabled": true, "backend_url": "https://auth.local/check", "allow_ips": []string{" 10.0.0.0/8 ", ""}, "max_sessions": 10}
	body["epg"] = map[string]any{"url": guide.URL + "/guia.xml", "xmltv_id": "x.br"}
	code, out, _ = e.do("POST", "/api/v1/channels", e.tok, body)
	if code != 201 {
		t.Fatalf("%d %v", code, out)
	}
	if ips := out["auth"].(map[string]any)["allow_ips"].([]any); len(ips) != 1 || ips[0] != "10.0.0.0/8" {
		t.Fatalf("allow_ips not cleaned: %v", ips)
	}

	code, out, _ = e.do("POST", "/api/v1/channels/acesso/epg/refresh", e.tok, nil)
	if code != 200 || out["programs"].(float64) != 1 {
		t.Fatalf("refresh %d %v", code, out)
	}
	code, out, _ = e.do("GET", "/api/v1/channels/acesso/epg", e.tok, nil)
	if ps := out["programs"].([]any); code != 200 || len(ps) != 1 || ps[0].(map[string]any)["title"] != "Agora" || out["status"] == nil {
		t.Fatalf("epg %d %v", code, out)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/epg.xml", nil)
	req.Header.Set("Authorization", "Bearer "+e.tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	xml, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(xml), `channel="acesso"`) || !strings.Contains(string(xml), "<title>Agora</title>") {
		t.Fatalf("export:\n%s", xml)
	}

	// Channels with auth can get a playback token (panel previews).
	_ = e.cache.Set(context.Background(), model.ChannelStatus{ChannelID: "acesso", State: model.StateRunning,
		Outputs: map[string]string{"hls": "https://origin/acesso/index.m3u8"}})
	if code, out, _ := e.do("POST", "/api/v1/channels/acesso/playback-token", e.tok, nil); code != 200 || out["hls_url"] == nil {
		t.Fatalf("token for auth channel: %d %v", code, out)
	}
	// Sessions of a channel not running anywhere: empty lists.
	code, out, _ = e.do("GET", "/api/v1/channels/acesso/sessions", e.tok, nil)
	if code != 200 || len(out["open"].([]any)) != 0 {
		t.Fatalf("sessions %d %v", code, out)
	}
}
