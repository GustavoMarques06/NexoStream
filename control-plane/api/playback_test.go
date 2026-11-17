package api

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"nexostream/internal/model"
	"nexostream/media-engine/drm"
)

func TestChannelDRM(t *testing.T) {
	e := newTestEnv(t)

	// MPEG-TS over HTTP is never encrypted: refused together with DRM.
	body := channelBody("protegido")
	body["drm"] = map[string]any{"enabled": true}
	code, out, _ := e.do("POST", "/api/v1/channels", e.tok, body)
	if code != 422 {
		t.Fatalf("%d %v", code, out)
	}
	details := out["error"].(map[string]any)["details"].(map[string]any)
	if _, ok := details["outputs.mpegts_http"]; !ok {
		t.Fatalf("mpegts_http with drm: %v", details)
	}

	body["outputs"] = map[string]any{"hls": true}
	body["drm"] = map[string]any{"enabled": true, "key_rotation_minutes": 5000}
	code, out, _ = e.do("POST", "/api/v1/channels", e.tok, body)
	if code != 422 || out["error"].(map[string]any)["details"].(map[string]any)["drm.key_rotation_minutes"] == nil {
		t.Fatalf("rotation 5000: %d %v", code, out)
	}
	body["drm"] = map[string]any{"enabled": true, "method": "widevine"}
	if code, _, _ := e.do("POST", "/api/v1/channels", e.tok, body); code != 422 {
		t.Fatalf("widevine accepted: %d", code)
	}

	// Defaults: aes-128, keys rotate every hour.
	body["drm"] = map[string]any{"enabled": true}
	if code, out, _ := e.do("POST", "/api/v1/channels", e.tok, body); code != 201 {
		t.Fatalf("%d %v", code, out)
	}
	_, out, _ = e.do("GET", "/api/v1/channels/protegido", e.tok, nil)
	d := out["drm"].(map[string]any)
	if d["enabled"] != true || d["method"] != "aes-128" || d["key_rotation_minutes"].(float64) != 60 {
		t.Fatalf("drm %v", d)
	}

	// Playback token with the URLs reported by the worker.
	_ = e.cache.Set(context.Background(), model.ChannelStatus{ChannelID: "protegido", State: model.StateRunning,
		Outputs: map[string]string{"hls": "https://origin/protegido/index.m3u8", "dvr": "https://origin/dvr/protegido/"}})
	code, out, _ = e.do("POST", "/api/v1/channels/protegido/playback-token", e.tok, map[string]any{"ttl_seconds": 600})
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	tok := out["token"].(string)
	if err := drm.VerifyToken(testPlaybackKey, "protegido", tok, time.Now()); err != nil {
		t.Fatalf("token: %v", err)
	}
	if err := drm.VerifyToken(testPlaybackKey, "protegido", tok, time.Now().Add(11*time.Minute)); err == nil {
		t.Fatal("ttl not applied")
	}
	u, _ := url.Parse(out["hls_url"].(string))
	if u.Path != "/protegido/index.m3u8" || u.Query().Get("token") != tok {
		t.Fatalf("hls_url %v", out["hls_url"])
	}
	if !strings.HasPrefix(out["dvr_url"].(string), "https://origin/dvr/protegido/index.m3u8?token=") {
		t.Fatalf("dvr_url %v", out["dvr_url"])
	}
	if code, _, _ := e.do("POST", "/api/v1/channels/protegido/playback-token", e.tok, map[string]any{"ttl_seconds": 5}); code != 422 {
		t.Fatalf("ttl 5 s: %d", code)
	}

	// Channels without DRM need no token.
	plain := channelBody("aberto")
	if code, _, _ := e.do("POST", "/api/v1/channels", e.tok, plain); code != 201 {
		t.Fatal("create plain")
	}
	if code, out, _ := e.do("POST", "/api/v1/channels/aberto/playback-token", e.tok, nil); code != 409 || errCode(out) != "channel_not_protected" {
		t.Fatalf("%d %v", code, out)
	}

	// Turning DRM off clears the settings.
	code, out, _ = e.do("PATCH", "/api/v1/channels/protegido", e.tok, map[string]any{"drm": map[string]any{"enabled": false, "key_rotation_minutes": 30}})
	if code != 200 || out["drm"].(map[string]any)["key_rotation_minutes"] != nil {
		t.Fatalf("%d %v", code, out)
	}
}
