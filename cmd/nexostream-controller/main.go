// Command nexostream-controller runs the cluster controller: it reconciles
// the desired state with the workers, schedules channels and persists
// events. Several instances may run; only the holder of the "controller"
// lease reconciles (ADR 0010).
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"nexostream/control-plane/boot"
	"nexostream/control-plane/controller"
	"nexostream/control-plane/epg"
	"nexostream/internal/config"
	"nexostream/internal/logging"
	rt "nexostream/internal/runtime"
	"nexostream/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("nexostream-controller", version.String())
		return
	}
	env := config.FromOS()
	log := logging.New(env.String("LOG_LEVEL", "info"), "controller")
	ctx, cancel := rt.SignalContext()
	defer cancel()

	st, err := boot.OpenPostgres(ctx, env, env.Bool("DB_MIGRATE", false), log)
	if err != nil {
		rt.Fatal(log, "database", err)
	}
	defer st.Close()
	cache, err := boot.OpenRedis(ctx, env)
	if err != nil {
		rt.Fatal(log, "redis", err)
	}
	defer cache.Close()
	nb, err := rt.OpenNATS(ctx, env, "nexostream-controller", log)
	if err != nil {
		rt.Fatal(log, "nats", err)
	}
	defer nb.Close()

	cfg := controller.DefaultConfig()
	cfg.Interval = env.Duration("RECONCILE_INTERVAL", cfg.Interval)
	cfg.OfflineAfter = env.Duration("WORKER_OFFLINE_AFTER", cfg.OfflineAfter)
	cfg.StartupGrace = env.Duration("STARTUP_GRACE", cfg.StartupGrace)
	cfg.MigrationsPerPass = env.Int("DRAIN_MIGRATIONS_PER_PASS", cfg.MigrationsPerPass)
	cfg.DefaultProfile = env.String("DEFAULT_PROFILE", cfg.DefaultProfile)
	cfg.Scheduler.MaxCPUPercent = env.Float("MAX_CPU_PERCENT", cfg.Scheduler.MaxCPUPercent)
	cfg.Scheduler.Costs.Passthrough = env.Float("COST_PASSTHROUGH", cfg.Scheduler.Costs.Passthrough)
	cfg.Scheduler.Costs.TranscodePerMegapixel = env.Float("COST_TRANSCODE_PER_MEGAPIXEL", cfg.Scheduler.Costs.TranscodePerMegapixel)
	cfg.Scheduler.Costs.GPUOffload = env.Float("COST_GPU_OFFLOAD", cfg.Scheduler.Costs.GPUOffload)
	if err := env.Err(); err != nil {
		rt.Fatal(log, "configuration", err)
	}
	host, _ := os.Hostname()
	cfg.Holder = env.String("CONTROLLER_ID", host) + "-" + strconv.Itoa(os.Getpid())
	cfg.LeaseTTL = env.Duration("LEADER_LEASE_TTL", 15*time.Second)
	log.Info("nexostream-controller starting", "version", version.String(), "holder", cfg.Holder)
	ctrl := controller.New(cfg, st, cache, nb, log)
	// Program guides (XMLTV) are imported by the leader (ADR 0009, 0010).
	go (&epg.Importer{Store: st, Log: log, Leader: ctrl.Leader}).Run(ctx)
	if err := ctrl.Run(ctx); err != nil {
		rt.Fatal(log, "controller", err)
	}
}
