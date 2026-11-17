// Command nexostream-api serves the management REST API and, optionally,
// the web panel. Configuration: /etc/nexostream/api.env (deploy/configs).
package main

import (
	"flag"
	"fmt"
	"strings"

	"nexostream/control-plane/api"
	"nexostream/control-plane/boot"
	"nexostream/internal/config"
	"nexostream/internal/logging"
	rt "nexostream/internal/runtime"
	"nexostream/internal/version"
	"nexostream/media-engine/drm"
)

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	migrateOnly := flag.Bool("migrate", false, "apply database migrations and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("nexostream-api", version.String())
		return
	}
	env := config.FromOS()
	log := logging.New(env.String("LOG_LEVEL", "info"), "api")
	ctx, cancel := rt.SignalContext()
	defer cancel()

	st, err := boot.OpenPostgres(ctx, env, env.Bool("DB_MIGRATE", true) || *migrateOnly, log)
	if err != nil {
		rt.Fatal(log, "database", err)
	}
	defer st.Close()
	if *migrateOnly {
		log.Info("migrations applied")
		return
	}

	jwtKey := env.Secret("JWT_KEY")
	if len(jwtKey) < 32 {
		rt.Fatal(log, "configuration", fmt.Errorf("NEXOSTREAM_JWT_KEY_FILE must contain at least 32 bytes (openssl rand -hex 32)"))
	}
	created, err := api.EnsureBootstrapAdmin(ctx, st, env.String("BOOTSTRAP_ADMIN_USER", "admin"), env.Secret("BOOTSTRAP_ADMIN_PASSWORD"))
	if err != nil {
		log.Warn("bootstrap admin", "error", err)
	} else if created {
		log.Info("bootstrap admin created; remove the bootstrap password file now")
	}

	cache, err := boot.OpenRedis(ctx, env)
	if err != nil {
		rt.Fatal(log, "redis", err)
	}
	defer cache.Close()
	shared, closeShared, err := boot.OpenSharedState(env, log)
	if err != nil {
		rt.Fatal(log, "shared state", err)
	}
	defer closeShared()
	nb, err := rt.OpenNATS(ctx, env, "nexostream-api", log)
	if err != nil {
		rt.Fatal(log, "nats", err)
	}
	defer nb.Close()

	playbackKey := []byte(env.Secret("PLAYBACK_TOKEN_KEY"))
	if len(playbackKey) > 0 && len(playbackKey) < drm.MinSecretBytes {
		rt.Fatal(log, "configuration", fmt.Errorf("NEXOSTREAM_PLAYBACK_TOKEN_KEY_FILE must contain at least 32 bytes (openssl rand -hex 32)"))
	}
	certFile, keyFile := env.String("TLS_CERT_FILE", ""), env.String("TLS_KEY_FILE", "")
	addr := env.String("HTTP_ADDR", "127.0.0.1:8080")
	if certFile == "" && !strings.HasPrefix(addr, "127.0.0.1:") && !strings.HasPrefix(addr, "localhost:") {
		log.Warn("API listening without TLS on a non-loopback address; put nginx with TLS in front of it", "addr", addr)
	}
	srv, err := api.New(api.Config{
		JWTKey:           []byte(jwtKey),
		AccessTTL:        env.Duration("ACCESS_TOKEN_TTL", 0),
		RefreshTTL:       env.Duration("REFRESH_TOKEN_TTL", 0),
		DefaultProfile:   env.String("DEFAULT_PROFILE", "hd"),
		PanelDir:         env.String("PANEL_DIR", ""),
		CORSOrigins:      env.List("CORS_ORIGINS", nil),
		TrustProxy:       env.Bool("TRUST_PROXY", true),
		TrustedProxies:   env.List("TRUSTED_PROXIES", nil),
		LockoutThreshold: env.Int("LOCKOUT_THRESHOLD", 0), // 0 = default (5); negative turns the lockout off
		LockoutBase:      env.Duration("LOCKOUT_BASE", 0),
		LockoutMax:       env.Duration("LOCKOUT_MAX", 0),
		PlaybackTokenKey: playbackKey,
		Version:          version.Version,
	}, api.Deps{Store: st, Cache: cache, Bus: nb, Log: log, Shared: shared})
	if err != nil {
		rt.Fatal(log, "api", err)
	}
	defer srv.Close()
	if err := env.Err(); err != nil {
		rt.Fatal(log, "configuration", err)
	}
	log.Info("nexostream-api starting", "version", version.String())
	if err := rt.Serve(ctx, log, addr, srv.Handler(), certFile, keyFile); err != nil {
		rt.Fatal(log, "http", err)
	}
}
