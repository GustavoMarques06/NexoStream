package api

import (
	"testing"

	"nexostream/internal/model"
)

func TestBranding(t *testing.T) {
	e := newTestEnv(t)
	// Public: the login page reads it before anyone signs in.
	code, out, _ := e.do("GET", "/api/v1/branding", "", nil)
	if code != 200 || out["name"] != "NexoStream" || out["tagline"] != "Media Server" {
		t.Fatalf("%d %v", code, out)
	}
	st := model.DefaultSettings()
	st.SystemName, st.BrandColor, st.BrandLogo = "TV Operadora", "#FF0000", "data:text/html;base64,PHNjcmlwdD4="
	code, out, _ = e.do("PUT", "/api/v1/settings", e.tok, st)
	details, _ := out["error"].(map[string]any)["details"].(map[string]any)
	if code != 422 || details["brand_logo"] == nil {
		t.Fatalf("html as logo: %d %v", code, out)
	}
	st.BrandColor = "vermelho"
	st.BrandLogo = "data:image/png;base64,iVBORw0KGgo="
	if code, out, _ = e.do("PUT", "/api/v1/settings", e.tok, st); code != 422 {
		t.Fatalf("bad color: %d %v", code, out)
	}
	st.BrandColor, st.BrandLoginMessage = "#FF0000", "Suporte: 0800 000 0000"
	if code, out, _ = e.do("PUT", "/api/v1/settings", e.tok, st); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	_, out, _ = e.do("GET", "/api/v1/branding", "", nil)
	if out["name"] != "TV Operadora" || out["color"] != "#ff0000" || out["logo"] != st.BrandLogo || out["login_message"] != st.BrandLoginMessage {
		t.Fatalf("branding %v", out)
	}
	// The text config has no logo: applying it keeps the panel's logo.
	code, out, _ = e.do("POST", "/api/v1/config/apply", e.tok, map[string]any{"text": "settings { name \"TV Nova\"; brand_color 00aa00; }"})
	if code != 200 {
		t.Fatalf("apply %d %v", code, out)
	}
	_, out, _ = e.do("GET", "/api/v1/branding", "", nil)
	if out["name"] != "TV Nova" || out["color"] != "#00aa00" || out["logo"] != st.BrandLogo {
		t.Fatalf("after text apply %v", out)
	}
}
