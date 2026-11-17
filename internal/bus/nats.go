package bus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// NATSConfig configures the NATS connection.
type NATSConfig struct {
	URL       string
	Name      string // client name shown in NATS monitoring
	CredsFile string // optional .creds file (per-node credentials)
	// EventsMaxAge is how long JetStream keeps events (default 7 days).
	EventsMaxAge time.Duration
	// StreamName and StreamSubjects define the durable stream.
	StreamName     string
	StreamSubjects []string
	Logger         *slog.Logger
}

// NATS implements Bus on top of NATS core and JetStream.
type NATS struct {
	nc  *nats.Conn
	js  jetstream.JetStream
	cfg NATSConfig
	log *slog.Logger
}

// NewNATS connects to NATS and makes sure the events stream exists. It keeps
// reconnecting forever: agents must survive Control Plane outages.
func NewNATS(ctx context.Context, cfg NATSConfig) (*NATS, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.EventsMaxAge == 0 {
		cfg.EventsMaxAge = 7 * 24 * time.Hour
	}
	lg := cfg.Logger.With("component", "bus")
	opts := []nats.Option{
		nats.Name(cfg.Name),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
		nats.RetryOnFailedConnect(true),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				lg.Warn("nats disconnected", "error", err)
			}
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			lg.Info("nats reconnected", "url", c.ConnectedUrl())
		}),
	}
	if cfg.CredsFile != "" {
		opts = append(opts, nats.UserCredentials(cfg.CredsFile))
	}
	nc, err := nats.Connect(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("connect nats %s: %w", cfg.URL, err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	n := &NATS{nc: nc, js: js, cfg: cfg, log: lg}
	if cfg.StreamName != "" {
		if err := n.ensureStream(ctx); err != nil {
			lg.Warn("events stream not ready yet; will retry on first use", "error", err)
		}
	}
	return n, nil
}

func (n *NATS) ensureStream(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := n.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       n.cfg.StreamName,
		Subjects:   n.cfg.StreamSubjects,
		Storage:    jetstream.FileStorage,
		Retention:  jetstream.LimitsPolicy,
		MaxAge:     n.cfg.EventsMaxAge,
		Duplicates: 2 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("create stream %s: %w", n.cfg.StreamName, err)
	}
	return nil
}

// Conn exposes the underlying connection (health checks).
func (n *NATS) Conn() *nats.Conn { return n.nc }

// Publish implements Bus.
func (n *NATS) Publish(subject string, data []byte) error {
	return n.nc.Publish(subject, data)
}

// Subscribe implements Bus.
func (n *NATS) Subscribe(subject string, h Handler) (Subscription, error) {
	return n.nc.Subscribe(subject, func(m *nats.Msg) { h(m.Subject, m.Data) })
}

// Request implements Bus.
func (n *NATS) Request(ctx context.Context, subject string, data []byte) ([]byte, error) {
	m, err := n.nc.RequestWithContext(ctx, subject, data)
	if err != nil {
		if errors.Is(err, nats.ErrNoResponders) {
			return nil, ErrNoResponders
		}
		return nil, err
	}
	return m.Data, nil
}

// Handle implements Bus.
func (n *NATS) Handle(subject string, h RequestHandler) (Subscription, error) {
	return n.nc.Subscribe(subject, func(m *nats.Msg) {
		if err := m.Respond(h(m.Subject, m.Data)); err != nil {
			n.log.Warn("respond failed", "subject", m.Subject, "error", err)
		}
	})
}

// PublishDurable implements Bus.
func (n *NATS) PublishDurable(ctx context.Context, subject, msgID string, data []byte) error {
	opts := []jetstream.PublishOpt{}
	if msgID != "" {
		opts = append(opts, jetstream.WithMsgID(msgID))
	}
	_, err := n.js.Publish(ctx, subject, data, opts...)
	if errors.Is(err, jetstream.ErrNoStreamResponse) || errors.Is(err, nats.ErrNoResponders) {
		if serr := n.ensureStream(ctx); serr == nil {
			_, err = n.js.Publish(ctx, subject, data, opts...)
		}
	}
	if err != nil {
		return fmt.Errorf("jetstream publish %s: %w", subject, err)
	}
	return nil
}

// ConsumeDurable implements Bus.
func (n *NATS) ConsumeDurable(ctx context.Context, name string, subjects []string, h DurableHandler) (Subscription, error) {
	if err := n.ensureStream(ctx); err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cons, err := n.js.CreateOrUpdateConsumer(cctx, n.cfg.StreamName, jetstream.ConsumerConfig{
		Durable:        name,
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: subjects,
		DeliverPolicy:  jetstream.DeliverAllPolicy,
		MaxDeliver:     20,
		AckWait:        30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("create consumer %s: %w", name, err)
	}
	cc, err := cons.Consume(func(m jetstream.Msg) {
		if err := h(m.Subject(), m.Data()); err != nil {
			n.log.Warn("durable handler failed; will redeliver", "subject", m.Subject(), "error", err)
			_ = m.NakWithDelay(2 * time.Second)
			return
		}
		_ = m.Ack()
	})
	if err != nil {
		return nil, fmt.Errorf("consume %s: %w", name, err)
	}
	return consumeSub{cc}, nil
}

type consumeSub struct{ cc jetstream.ConsumeContext }

func (c consumeSub) Unsubscribe() error { c.cc.Stop(); return nil }

// Close implements Bus.
func (n *NATS) Close() error {
	if err := n.nc.Drain(); err != nil {
		n.nc.Close()
	}
	return nil
}
