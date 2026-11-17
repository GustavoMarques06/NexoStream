// Package runtime holds helpers shared by the binaries: signal handling,
// the NATS connection and HTTP serving. Database and cache connections live
// in control-plane/boot, so Media Node binaries do not link them.
package runtime

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nexostream/internal/bus"
	"nexostream/internal/config"
	"nexostream/internal/protocol"
)

// SignalContext is canceled on SIGINT/SIGTERM.
func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// Fatal logs and exits.
func Fatal(log *slog.Logger, msg string, err error) {
	log.Error(msg, "error", err)
	os.Exit(1)
}

// OpenNATS connects to NATS with the events stream.
func OpenNATS(ctx context.Context, env *config.Env, name string, log *slog.Logger) (*bus.NATS, error) {
	url := env.String("NATS_URL", "nats://127.0.0.1:4222")
	return bus.NewNATS(ctx, bus.NATSConfig{
		URL: url, Name: name, CredsFile: env.String("NATS_CREDS", ""),
		StreamName: protocol.EventsStream, StreamSubjects: protocol.EventSubjects,
		EventsMaxAge: env.Duration("EVENTS_RETENTION", 7*24*time.Hour), Logger: log,
	})
}

// Serve runs an HTTP server until ctx is canceled. TLS is used when both
// files are given.
func Serve(ctx context.Context, log *slog.Logger, addr string, h http.Handler, certFile, keyFile string) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second,
		MaxHeaderBytes: 64 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	errc := make(chan error, 1)
	go func() {
		var err error
		if certFile != "" && keyFile != "" {
			log.Info("https listening", "addr", addr)
			err = srv.ListenAndServeTLS(certFile, keyFile)
		} else {
			log.Info("http listening", "addr", addr)
			err = srv.ListenAndServe()
		}
		if !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	case err := <-errc:
		return err
	}
}
