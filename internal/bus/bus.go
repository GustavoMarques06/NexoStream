// Package bus abstracts the messaging layer. Production uses NATS with
// JetStream for events that must not be lost; development and tests can use
// the in-memory implementation.
package bus

import (
	"context"
	"errors"
	"strings"
)

// ErrNoResponders is returned by Request when nobody handles the subject.
var ErrNoResponders = errors.New("bus: no responders")

// Handler receives a message.
type Handler func(subject string, data []byte)

// RequestHandler answers a request. The returned bytes are the reply.
type RequestHandler func(subject string, data []byte) []byte

// DurableHandler processes a persisted message. Returning an error asks for
// redelivery.
type DurableHandler func(subject string, data []byte) error

// Subscription can be canceled.
type Subscription interface {
	Unsubscribe() error
}

// Bus is the messaging contract used by every component.
type Bus interface {
	// Publish sends a fire-and-forget message (heartbeats, status).
	Publish(subject string, data []byte) error
	// Subscribe receives messages matching a subject pattern (* and >).
	Subscribe(subject string, h Handler) (Subscription, error)
	// Request sends a request and waits for one reply.
	Request(ctx context.Context, subject string, data []byte) ([]byte, error)
	// Handle answers requests on a subject.
	Handle(subject string, h RequestHandler) (Subscription, error)
	// PublishDurable stores the message before acknowledging (JetStream).
	// msgID deduplicates retries.
	PublishDurable(ctx context.Context, subject, msgID string, data []byte) error
	// ConsumeDurable processes persisted messages with a named consumer that
	// survives restarts.
	ConsumeDurable(ctx context.Context, name string, subjects []string, h DurableHandler) (Subscription, error)
	// Close releases the connection.
	Close() error
}

// Match reports whether subject matches a NATS-style pattern.
func Match(pattern, subject string) bool {
	p := strings.Split(pattern, ".")
	s := strings.Split(subject, ".")
	for i, tok := range p {
		if tok == ">" {
			return len(s) > i
		}
		if i >= len(s) {
			return false
		}
		if tok != "*" && tok != s[i] {
			return false
		}
	}
	return len(p) == len(s)
}
