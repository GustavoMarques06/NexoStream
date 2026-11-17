// Command nexostream-dev runs the whole platform in one process for
// development: API + controller + one worker, with an in-memory store,
// status cache and message bus (no PostgreSQL, Redis or NATS needed).
// Data is lost on exit. Never use it in production.
//
//	go run ./cmd/nexostream-dev -panel web-panel/dist -demo
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"nexostream/control-plane/api"
	"nexostream/control-plane/controller"
	"nexostream/control-plane/epg"
	"nexostream/control-plane/statuscache"
	"nexostream/control-plane/store/memory"
	"nexostream/data-plane/agent"
	"nexostream/internal/bus"
	"nexostream/internal/logging"
	"nexostream/internal/model"
	rt "nexostream/internal/runtime"
	"nexostream/internal/version"
	"nexostream/media-engine/pipeline"
)

func main() {
	addr := flag.String("addr", ":8080", "API and panel listen address (all interfaces by default)")
	mediaAddr := flag.String("media-addr", ":9500", "worker metrics, HLS and MPEG-TS listen address")
	host := flag.String("host", "", "address clients use to reach this machine (auto-detected when empty)")
	data := flag.String("data", "var", "directory for HLS output and worker state")
	panel := flag.String("panel", "web-panel/dist", "built web panel directory (empty to disable)")
	ffmpeg := flag.String("ffmpeg", "ffmpeg", "ffmpeg binary")
	ffprobe := flag.String("ffprobe", "ffprobe", "ffprobe binary (- to disable)")
	password := flag.String("admin-password", "", "admin password (random when empty)")
	demo := flag.Bool("demo", false, "generate two test sources with ffmpeg and create a demo channel")
	level := flag.String("log-level", "info", "log level")
	flag.Parse()

	log := logging.New(*level, "dev")
	ctx, cancel := rt.SignalContext()
	defer cancel()

	st := memory.New()
	cache := statuscache.NewMemory()
	b := bus.NewMemory()
	defer b.Close()

	pw := *password
	if pw == "" {
		k := make([]byte, 9)
		_, _ = rand.Read(k)
		pw = hex.EncodeToString(k)
	}
	if _, err := api.EnsureBootstrapAdmin(ctx, st, "admin", pw); err != nil {
		rt.Fatal(log, "bootstrap", err)
	}
	jwtKey := make([]byte, 32)
	_, _ = rand.Read(jwtKey)
	// DRM secrets: random per run (dev data is in memory anyway).
	drmKey, playbackKey := make([]byte, 32), make([]byte, 32)
	_, _ = rand.Read(drmKey)
	_, _ = rand.Read(playbackKey)

	panelDir := *panel
	if panelDir != "" {
		if _, err := os.Stat(filepath.Join(panelDir, "index.html")); err != nil {
			log.Warn("web panel not built; run: cd web-panel && npm install && npm run build", "dir", panelDir)
			panelDir = ""
		}
	}
	publicHost := *host
	if publicHost == "" {
		publicHost = detectHost()
	}
	mediaBase := "http://" + net.JoinHostPort(publicHost, portOf(*mediaAddr))
	srv, err := api.New(api.Config{JWTKey: jwtKey, PanelDir: panelDir, Version: version.Version, PlaybackTokenKey: playbackKey,
		CORSOrigins: []string{"http://localhost:5173", "http://127.0.0.1:5173"}}, api.Deps{Store: st, Cache: cache, Bus: b, Log: log})
	if err != nil {
		rt.Fatal(log, "api", err)
	}
	defer srv.Close()

	ccfg := controller.DefaultConfig()
	ccfg.StartupGrace = 2 * time.Second
	ctrl := controller.New(ccfg, st, cache, b, log)
	go (&epg.Importer{Store: st, Log: log}).Run(ctx)

	nodeAgent, err := agent.New(agent.Config{
		WorkerID: "dev-worker", HLSDir: filepath.Join(*data, "hls"), StateDir: filepath.Join(*data, "worker"),
		MetricsAddr: *mediaAddr, MediaAddr: *mediaAddr,
		PublicHLSBase: mediaBase + "/hls", PublicTSBase: mediaBase + "/live", PublicDVRBase: mediaBase + "/dvr",
		DVRDir:     filepath.Join(*data, "dvr"),
		OverlayDir: filepath.Join(*data, "overlays"),
		FFmpegPath: *ffmpeg, FFprobePath: *ffprobe,
		DRMKey: drmKey, PlaybackTokenKey: playbackKey,
	}, b, &pipeline.FFmpeg{Path: *ffmpeg, Logger: log}, log)
	if err != nil {
		rt.Fatal(log, "worker", err)
	}

	go func() {
		if err := ctrl.Run(ctx); err != nil {
			log.Error("controller", "error", err)
		}
	}()
	go func() {
		if err := nodeAgent.Run(ctx); err != nil {
			log.Error("worker", "error", err)
		}
	}()
	if *demo {
		go runDemo(ctx, log, st, ctrl, *ffmpeg)
	}

	fmt.Printf("\n  NexoStream dev %s\n  Painel/API: http://%s  (usuário admin, senha %s)\n  HLS/TS/métricas: %s\n  Dados em memória: tudo se perde ao encerrar.\n\n",
		version.Version, net.JoinHostPort(publicHost, portOf(*addr)), pw, mediaBase)
	if err := rt.Serve(ctx, log, *addr, srv.Handler(), "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		rt.Fatal(log, "http", err)
	}
}

// detectHost returns the IPv4 address of the interface used to reach the
// network (no packet is sent), falling back to the first private address and
// then to loopback.
func detectHost() string {
	if c, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		defer c.Close()
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && !a.IP.IsLoopback() {
			return a.IP.String()
		}
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			return n.IP.String()
		}
	}
	return "127.0.0.1"
}

// portOf extracts the port from a listen address such as ":8080".
func portOf(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return addr
}

// runDemo generates two synthetic sources (color bars + tone) and creates a
// channel using them as primary and backup, to try failover by killing one.
func runDemo(ctx context.Context, log interface {
	Info(string, ...any)
	Error(string, ...any)
}, st *memory.Store, ctrl *controller.Controller, ffmpeg string) {
	// Different patterns and tones make the active source obvious in the player:
	// moving test pattern = primary, color bars = backup.
	srcs := []struct {
		port  int
		label string
		video string
		tone  int
	}{{5000, "PRINCIPAL", "testsrc2=size=1280x720:rate=25", 1000}, {5002, "BACKUP", "smptehdbars=size=1280x720:rate=25", 440}}
	for _, s := range srcs {
		args := []string{"-hide_banner", "-loglevel", "error", "-re",
			"-f", "lavfi", "-i", s.video,
			"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:sample_rate=48000", s.tone),
			"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-g", "50", "-b:v", "2500k",
			"-c:a", "aac", "-b:a", "128k", "-f", "mpegts", fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", s.port)}
		cmd := exec.CommandContext(ctx, ffmpeg, args...)
		if err := cmd.Start(); err != nil {
			log.Error("demo source", "error", err)
			return
		}
		go func(c *exec.Cmd, label string) {
			if err := c.Wait(); err != nil && ctx.Err() == nil {
				log.Error("demo source exited (is ffmpeg built with libx264?)", "source", label, "error", err)
			}
		}(cmd, s.label)
		log.Info("demo source started", "url", fmt.Sprintf("udp://127.0.0.1:%d", s.port), "pid", cmd.Process.Pid)
	}
	ch := &model.Channel{
		ID: "demo", Name: "Canal de demonstração", Group: "Demo", Enabled: true, Mode: model.ModeAuto,
		Sources: []model.Source{
			{ID: "1", Priority: 1, URL: "udp://127.0.0.1:5000"},
			{ID: "2", Priority: 2, URL: "udp://127.0.0.1:5002"},
		},
		Outputs:  model.Outputs{HLS: true, MPEGTSHTTP: true},
		Failover: model.DefaultFailover(),
	}
	if err := st.CreateChannel(ctx, ch); err != nil {
		log.Error("demo channel", "error", err)
		return
	}
	ctrl.Kick()
	log.Info("demo channel created", "id", ch.ID)
}
