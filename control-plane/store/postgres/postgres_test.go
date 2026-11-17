package postgres_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	"nexostream/control-plane/store/postgres"
	"nexostream/control-plane/store/storetest"
)

// databaseURL returns NEXOSTREAM_TEST_DATABASE_URL, or starts an embedded
// PostgreSQL when NEXOSTREAM_TEST_EMBEDDED_PG=1 (downloads binaries once).
func databaseURL(t *testing.T) string {
	if u := os.Getenv("NEXOSTREAM_TEST_DATABASE_URL"); u != "" {
		return u
	}
	if os.Getenv("NEXOSTREAM_TEST_EMBEDDED_PG") != "1" {
		t.Skip("set NEXOSTREAM_TEST_DATABASE_URL or NEXOSTREAM_TEST_EMBEDDED_PG=1")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint32(l.Addr().(*net.TCPAddr).Port)
	l.Close()
	dir := t.TempDir()
	cache := filepath.Join(os.TempDir(), "nexostream-embedded-pg-cache")
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Port(port).Username("nexostream").Password("test").Database("nexostream").
		RuntimePath(filepath.Join(dir, "rt")).DataPath(filepath.Join(dir, "data")).CachePath(cache).
		Logger(nil))
	if err := pg.Start(); err != nil {
		t.Fatalf("embedded postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Stop() })
	return fmt.Sprintf("postgres://nexostream:test@127.0.0.1:%d/nexostream?sslmode=disable", port)
}

func TestConformance(t *testing.T) {
	ctx := context.Background()
	s, err := postgres.Open(ctx, databaseURL(t), "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, _ = s.Pool().Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public;")
	applied, err := postgres.Migrate(ctx, s.Pool())
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) == 0 {
		t.Fatal("no migration applied")
	}
	again, err := postgres.Migrate(ctx, s.Pool())
	if err != nil || len(again) != 0 {
		t.Fatalf("second migrate: %v %v", again, err)
	}
	storetest.Run(t, s)

	// The audit log must be append-only.
	if _, err := s.Pool().Exec(ctx, "DELETE FROM audit_log"); err == nil {
		t.Fatal("audit_log delete must be rejected")
	}
}
