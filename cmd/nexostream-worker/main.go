// Command nexostream-worker is the Media Node agent. Configuration:
// /etc/nexostream/worker.env (deploy/configs/worker.env.example).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"nexostream/data-plane/agent"
	"nexostream/internal/config"
	"nexostream/internal/logging"
	rt "nexostream/internal/runtime"
	"nexostream/internal/version"
	"nexostream/media-engine/drm"
	"nexostream/media-engine/pipeline"
)

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("nexostream-worker", version.String())
		return
	}
	env := config.FromOS()
	log := logging.New(env.String("LOG_LEVEL", "info"), "worker")
	host, _ := os.Hostname()
	cfg := agent.Config{
		WorkerID:             env.String("WORKER_ID", host),
		HLSDir:               env.String("HLS_DIR", "/var/lib/nexostream/hls"),
		DVRDir:               env.String("DVR_DIR", "/srv/nexostream/dvr"),
		OverlayDir:           env.String("OVERLAY_DIR", "/var/lib/nexostream/overlays"),
		StateDir:             env.String("STATE_DIR", "/var/lib/nexostream/worker"),
		MetricsAddr:          env.String("METRICS_ADDR", "127.0.0.1:9500"),
		MediaAddr:            env.String("MEDIA_ADDR", "127.0.0.1:9501"),
		PublicHLSBase:        env.String("PUBLIC_HLS_BASE", ""),
		PublicTSBase:         env.String("PUBLIC_TS_BASE", ""),
		PublicDVRBase:        env.String("PUBLIC_DVR_BASE", ""),
		IngestInterfaces:     env.List("INGEST_INTERFACES", nil),
		FFmpegPath:           env.String("FFMPEG_PATH", "ffmpeg"),
		FFprobePath:          env.String("FFPROBE_PATH", "ffprobe"),
		ReadBuffer:           env.Int("UDP_READ_BUFFER", 4<<20),
		HLSSegmentSeconds:    env.Int("HLS_SEGMENT_SECONDS", 6),
		HLSListSize:          env.Int("HLS_LIST_SIZE", 10),
		DefaultProfile:       env.String("DEFAULT_PROFILE", "hd"),
		AllowHEVCPassthrough: env.Bool("ALLOW_HEVC_PASSTHROUGH", false),
		TLSCertFile:          env.String("TLS_CERT_FILE", ""),
		TLSKeyFile:           env.String("TLS_KEY_FILE", ""),
		DRMKey:               []byte(env.Secret("DRM_KEY")),
		PlaybackTokenKey:     []byte(env.Secret("PLAYBACK_TOKEN_KEY")),
	}
	for name, v := range map[string][]byte{"NEXOSTREAM_DRM_KEY_FILE": cfg.DRMKey, "NEXOSTREAM_PLAYBACK_TOKEN_KEY_FILE": cfg.PlaybackTokenKey} {
		if len(v) > 0 && len(v) < drm.MinSecretBytes {
			rt.Fatal(log, "configuration", fmt.Errorf("%s must contain at least 32 bytes (openssl rand -hex 32)", name))
		}
	}
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		rt.Fatal(log, "configuration", fmt.Errorf("NEXOSTREAM_TLS_CERT_FILE and NEXOSTREAM_TLS_KEY_FILE must be set together"))
	}
	if cfg.TLSCertFile == "" && !strings.HasPrefix(cfg.MediaAddr, "127.0.0.1:") && !strings.HasPrefix(cfg.MediaAddr, "localhost:") {
		log.Warn("media address is reachable from the network without TLS; set NEXOSTREAM_TLS_CERT_FILE/KEY_FILE or put nginx with TLS in front of it", "addr", cfg.MediaAddr)
	}
	if err := env.Err(); err != nil {
		rt.Fatal(log, "configuration", err)
	}
	ctx, cancel := rt.SignalContext()
	defer cancel()
	nb, err := rt.OpenNATS(ctx, env, "nexostream-worker-"+cfg.WorkerID, log)
	if err != nil {
		rt.Fatal(log, "nats", err)
	}
	defer nb.Close()
	engine := &pipeline.FFmpeg{Path: cfg.FFmpegPath, Logger: log}
	nodeAgent, err := agent.New(cfg, nb, engine, log)
	if err != nil {
		rt.Fatal(log, "agent", err)
	}
	if err := nodeAgent.Run(ctx); err != nil {
		rt.Fatal(log, "agent", err)
	}
}
