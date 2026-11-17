// Package boot opens the infrastructure the Control Plane binaries need
// (PostgreSQL and Redis). It lives under control-plane/ on purpose: the Media
// Node binary must not link database code (ADR 0001), which it would if
// these functions sat in the helpers shared by every binary.
package boot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"nexostream/control-plane/sharedstate"
	"nexostream/control-plane/statuscache"
	"nexostream/control-plane/store/postgres"
	"nexostream/internal/config"
)

// OpenPostgres connects and optionally migrates.
func OpenPostgres(ctx context.Context, env *config.Env, migrate bool, log *slog.Logger) (*postgres.Store, error) {
	url := env.String("DATABASE_URL", "")
	if url == "" {
		return nil, errors.New("NEXOSTREAM_DATABASE_URL is required")
	}
	st, err := postgres.Open(ctx, url, env.Secret("DATABASE_PASSWORD"))
	if err != nil {
		return nil, err
	}
	if migrate {
		applied, err := postgres.Migrate(ctx, st.Pool())
		if err != nil {
			st.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
		if len(applied) > 0 {
			log.Info("database migrated", "applied", applied)
		}
	}
	return st, nil
}

// OpenSharedState connects the state shared by the API instances (login rate
// limit, idempotent replies) to the same Redis as the status cache. While
// Redis is unreachable it falls back to this instance's memory.
func OpenSharedState(env *config.Env, log *slog.Logger) (sharedstate.State, func(), error) {
	r, err := sharedstate.NewRedis(env.String("REDIS_URL", "redis://127.0.0.1:6379/0"), env.Secret("REDIS_PASSWORD"))
	if err != nil {
		return nil, nil, err
	}
	return sharedstate.WithFallback(r, log), func() { _ = r.Close() }, nil
}

// OpenRedis connects the status cache.
func OpenRedis(ctx context.Context, env *config.Env) (*statuscache.Redis, error) {
	c, err := statuscache.NewRedis(env.String("REDIS_URL", "redis://127.0.0.1:6379/0"), env.Secret("REDIS_PASSWORD"))
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Ping(pctx); err != nil {
		return nil, fmt.Errorf("redis: %w", err)
	}
	return c, nil
}
