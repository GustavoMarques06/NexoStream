package model

import "testing"

func TestValidOverlayImage(t *testing.T) {
	for name, ok := range map[string]bool{
		"logo.png": true, "Marca_2026-v2.PNG": true, "logo.jpg": true, "logo.jpeg": true,
		"":                 false,
		"logo":             false, // no extension
		"logo.gif":         false, // not a supported format
		"../../etc/passwd": false, // a path, not a name
		"../logo.png":      false,
		"dir/logo.png":     false,
		`dir\logo.png`:     false,
		"/abs/logo.png":    false,
		".hidden.png":      false, // must start with a letter or digit
		"logo.png.exe":     false,
		"logo .png":        false,
	} {
		if got := ValidOverlayImage(name); got != ok {
			t.Errorf("ValidOverlayImage(%q) = %v, want %v", name, got, ok)
		}
	}
}
