package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnv(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "jwt.key")
	_ = os.WriteFile(secret, []byte("  s3cr3t\n"), 0o600)
	e := FromMap(map[string]string{
		"NEXOSTREAM_WORKER_ID":         "media-01",
		"NEXOSTREAM_MAX_CPU_PERCENT":   "75.5",
		"NEXOSTREAM_INGEST_INTERFACES": "eno2, eno3,",
		"NEXOSTREAM_INTERVAL":          "2s",
		"NEXOSTREAM_JWT_KEY_FILE":      secret,
		"NEXOSTREAM_BAD_INT":           "x",
	})
	if e.String("WORKER_ID", "") != "media-01" || e.Float("MAX_CPU_PERCENT", 0) != 75.5 ||
		len(e.List("INGEST_INTERFACES", nil)) != 2 || e.Duration("INTERVAL", 0) != 2*time.Second ||
		e.Secret("JWT_KEY") != "s3cr3t" || e.Int("MISSING", 7) != 7 {
		t.Fatal("unexpected values")
	}
	if e.Err() != nil {
		t.Fatal(e.Err())
	}
	_ = e.Int("BAD_INT", 0)
	if e.Err() == nil {
		t.Fatal("expected an error")
	}
}
