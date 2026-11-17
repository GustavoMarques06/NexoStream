// Package pipelinetest provides a fake Engine that consumes input without
// running ffmpeg, for agent, recovery and integration tests.
package pipelinetest

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"nexostream/media-engine/pipeline"
)

// Engine is a fake pipeline.Engine.
type Engine struct {
	mu      sync.Mutex
	started []pipeline.Spec
	procs   []*Process
	// FailStart makes Start return an error.
	FailStart bool
}

// Name implements pipeline.Engine.
func (e *Engine) Name() string { return "fake" }

// Start implements pipeline.Engine.
func (e *Engine) Start(ctx context.Context, spec pipeline.Spec) (pipeline.Process, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.FailStart {
		return nil, errors.New("fake start failure")
	}
	p := &Process{done: make(chan struct{}), Spec: spec}
	e.started = append(e.started, spec)
	e.procs = append(e.procs, p)
	// Like a real engine, end when the context is canceled.
	go func() {
		select {
		case <-ctx.Done():
			p.Stop(0)
		case <-p.done:
		}
	}()
	return p, nil
}

// Starts returns how many processes were started.
func (e *Engine) Starts() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.started)
}

// Last returns the most recent process.
func (e *Engine) Last() *Process {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.procs) == 0 {
		return nil
	}
	return e.procs[len(e.procs)-1]
}

// Process is a fake pipeline.Process.
type Process struct {
	Spec  pipeline.Spec
	bytes atomic.Int64
	done  chan struct{}
	once  sync.Once
	mu    sync.Mutex
	err   error
}

// Input implements pipeline.Process.
func (p *Process) Input() io.Writer {
	return writerFunc(func(b []byte) (int, error) {
		select {
		case <-p.done:
			return 0, io.ErrClosedPipe
		default:
		}
		p.bytes.Add(int64(len(b)))
		return len(b), nil
	})
}

// Bytes returns how many input bytes were received.
func (p *Process) Bytes() int64 { return p.bytes.Load() }

// Done implements pipeline.Process.
func (p *Process) Done() <-chan struct{} { return p.done }

// Err implements pipeline.Process.
func (p *Process) Err() error { p.mu.Lock(); defer p.mu.Unlock(); return p.err }

// Stop implements pipeline.Process.
func (p *Process) Stop(time.Duration) { p.once.Do(func() { close(p.done) }) }

// Crash simulates an unexpected exit.
func (p *Process) Crash() {
	p.mu.Lock()
	p.err = errors.New("fake crash")
	p.mu.Unlock()
	p.once.Do(func() { close(p.done) })
}

// Progress implements pipeline.Process: media time follows input bytes.
func (p *Process) Progress() pipeline.Progress {
	return pipeline.Progress{TotalSize: p.bytes.Load(), OutTime: time.Duration(p.bytes.Load()) * time.Microsecond, UpdatedAt: time.Now(), Speed: 1}
}

// LastLog implements pipeline.Process.
func (p *Process) LastLog() []string { return nil }

// PID implements pipeline.Process.
func (p *Process) PID() int { return 0 }

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }
