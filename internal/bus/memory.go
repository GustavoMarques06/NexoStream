package bus

import (
	"context"
	"errors"
	"sync"
)

// Memory is an in-process Bus for the all-in-one development mode and tests.
// Delivery is asynchronous and ordered per subscription.
type Memory struct {
	mu       sync.RWMutex
	subs     map[int]*memSub
	handlers map[int]*memHandler
	durables map[string]*memDurable
	backlog  []memMsg // durable messages published before any consumer exists
	next     int
	closed   bool
}

type memMsg struct {
	subject string
	data    []byte
}

type memSub struct {
	pattern string
	ch      chan memMsg
	done    chan struct{}
}

type memHandler struct {
	pattern string
	h       RequestHandler
}

type memDurable struct {
	subjects []string
	ch       chan memMsg
	done     chan struct{}
}

const memQueue = 4096
const memBacklog = 10000

// NewMemory returns an empty in-memory bus.
func NewMemory() *Memory {
	return &Memory{
		subs:     map[int]*memSub{},
		handlers: map[int]*memHandler{},
		durables: map[string]*memDurable{},
	}
}

func clone(b []byte) []byte { return append([]byte(nil), b...) }

// Publish implements Bus.
func (m *Memory) Publish(subject string, data []byte) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return errors.New("bus: closed")
	}
	for _, s := range m.subs {
		if Match(s.pattern, subject) {
			select {
			case s.ch <- memMsg{subject, clone(data)}:
			default: // slow subscriber: drop, like NATS core
			}
		}
	}
	return nil
}

// Subscribe implements Bus.
func (m *Memory) Subscribe(subject string, h Handler) (Subscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.next
	m.next++
	s := &memSub{pattern: subject, ch: make(chan memMsg, memQueue), done: make(chan struct{})}
	m.subs[id] = s
	go func() {
		for {
			select {
			case msg := <-s.ch:
				h(msg.subject, msg.data)
			case <-s.done:
				return
			}
		}
	}()
	return unsub(func() {
		m.mu.Lock()
		if _, ok := m.subs[id]; ok {
			delete(m.subs, id)
			close(s.done)
		}
		m.mu.Unlock()
	}), nil
}

// Request implements Bus.
func (m *Memory) Request(ctx context.Context, subject string, data []byte) ([]byte, error) {
	m.mu.RLock()
	var h RequestHandler
	for _, hd := range m.handlers {
		if Match(hd.pattern, subject) {
			h = hd.h
			break
		}
	}
	m.mu.RUnlock()
	if h == nil {
		return nil, ErrNoResponders
	}
	res := make(chan []byte, 1)
	go func() { res <- h(subject, clone(data)) }()
	select {
	case r := <-res:
		return r, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Handle implements Bus.
func (m *Memory) Handle(subject string, h RequestHandler) (Subscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.next
	m.next++
	m.handlers[id] = &memHandler{pattern: subject, h: h}
	return unsub(func() {
		m.mu.Lock()
		delete(m.handlers, id)
		m.mu.Unlock()
	}), nil
}

// PublishDurable implements Bus. Messages are queued for durable consumers;
// with no consumer yet they wait in a bounded backlog.
func (m *Memory) PublishDurable(_ context.Context, subject, _ string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("bus: closed")
	}
	delivered := false
	for _, d := range m.durables {
		for _, p := range d.subjects {
			if Match(p, subject) {
				d.ch <- memMsg{subject, clone(data)}
				delivered = true
				break
			}
		}
	}
	if !delivered && len(m.backlog) < memBacklog {
		m.backlog = append(m.backlog, memMsg{subject, clone(data)})
	}
	// Durable subjects are also visible to core subscribers (like JetStream).
	for _, s := range m.subs {
		if Match(s.pattern, subject) {
			select {
			case s.ch <- memMsg{subject, clone(data)}:
			default:
			}
		}
	}
	return nil
}

// ConsumeDurable implements Bus. Failed messages are retried a few times.
func (m *Memory) ConsumeDurable(ctx context.Context, name string, subjects []string, h DurableHandler) (Subscription, error) {
	m.mu.Lock()
	if _, ok := m.durables[name]; ok {
		m.mu.Unlock()
		return nil, errors.New("bus: durable consumer already exists: " + name)
	}
	d := &memDurable{subjects: subjects, ch: make(chan memMsg, memBacklog+memQueue), done: make(chan struct{})}
	m.durables[name] = d
	rest := m.backlog[:0]
	for _, msg := range m.backlog {
		matched := false
		for _, p := range subjects {
			if Match(p, msg.subject) {
				matched = true
				break
			}
		}
		if matched {
			d.ch <- msg
		} else {
			rest = append(rest, msg)
		}
	}
	m.backlog = rest
	m.mu.Unlock()

	go func() {
		for {
			select {
			case msg := <-d.ch:
				for attempt := 0; attempt < 5; attempt++ {
					if h(msg.subject, msg.data) == nil {
						break
					}
				}
			case <-d.done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return unsub(func() {
		m.mu.Lock()
		if _, ok := m.durables[name]; ok {
			delete(m.durables, name)
			close(d.done)
		}
		m.mu.Unlock()
	}), nil
}

// Close implements Bus.
func (m *Memory) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	for id, s := range m.subs {
		close(s.done)
		delete(m.subs, id)
	}
	for name, d := range m.durables {
		close(d.done)
		delete(m.durables, name)
	}
	return nil
}

type unsub func()

func (u unsub) Unsubscribe() error { u(); return nil }
