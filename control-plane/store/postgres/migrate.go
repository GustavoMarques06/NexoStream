package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// advisoryLock serializes migrations when several API instances start.
const advisoryLock = 7261_2026

type migration struct {
	version int64
	name    string
	up      string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.Glob(migrationsFS, "migrations/*.up.sql")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		base := strings.TrimPrefix(e, "migrations/")
		vs, _, ok := strings.Cut(base, "_")
		if !ok {
			return nil, fmt.Errorf("bad migration name %s", base)
		}
		v, err := strconv.ParseInt(vs, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad migration version %s: %w", base, err)
		}
		b, err := migrationsFS.ReadFile(e)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: base, up: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// Migrate applies pending migrations. It uses the same schema_migrations
// table as golang-migrate (one row: version, dirty).
func Migrate(ctx context.Context, pool *pgxpool.Pool) (applied []string, err error) {
	migs, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLock); err != nil {
		return nil, fmt.Errorf("migration lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", advisoryLock) }()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version bigint NOT NULL PRIMARY KEY, dirty boolean NOT NULL)`); err != nil {
		return nil, fmt.Errorf("schema_migrations: %w", err)
	}
	var current int64 = -1
	var dirty bool
	err = conn.QueryRow(ctx, "SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&current, &dirty)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if dirty {
		return nil, fmt.Errorf("database is dirty at version %d: fix it manually, then run migrate force", current)
	}
	for _, m := range migs {
		if m.version <= current {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return applied, err
		}
		if _, err := tx.Exec(ctx, m.up); err != nil {
			_ = tx.Rollback(ctx)
			return applied, fmt.Errorf("migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM schema_migrations"); err != nil {
			_ = tx.Rollback(ctx)
			return applied, err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, dirty) VALUES ($1, false)", m.version); err != nil {
			_ = tx.Rollback(ctx)
			return applied, err
		}
		if err := tx.Commit(ctx); err != nil {
			return applied, err
		}
		applied = append(applied, m.name)
	}
	return applied, nil
}
