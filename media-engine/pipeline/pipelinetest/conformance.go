package pipelinetest

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nexostream/media-engine/pipeline"
)

// Conformance describes an engine to the conformance suite. Every
// pipeline.Engine (the FFmpeg one today, a GStreamer one tomorrow) must pass
// RunConformance: the agent relies on exactly these behaviors, so passing it is
// the objective definition of "this engine can be plugged in".
type Conformance struct {
	// New returns a fresh engine for each check.
	New func(t *testing.T) pipeline.Engine
	// Spec builds the spec to run; dir is an empty temporary directory the
	// engine may write to.
	Spec func(t *testing.T, dir string) pipeline.Spec
	// Input returns MPEG-TS the engine can process. Nil skips the checks that
	// need media.
	Input func(t *testing.T) []byte
	// ProducesHLS requires the engine to write an HLS playlist and at least one
	// segment into Spec.HLS.Dir when fed Input.
	ProducesHLS bool
}

const settle = 10 * time.Second // generous: CI machines are slow

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(settle):
		t.Fatalf("timeout: %s", what)
	}
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// RunConformance runs the engine contract checks.
func RunConformance(t *testing.T, c Conformance) {
	start := func(t *testing.T, ctx context.Context) (pipeline.Process, pipeline.Spec) {
		t.Helper()
		spec := c.Spec(t, t.TempDir())
		p, err := c.New(t).Start(ctx, spec)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { p.Stop(time.Second) })
		return p, spec
	}

	t.Run("has a name", func(t *testing.T) {
		if c.New(t).Name() == "" {
			t.Fatal("Name must not be empty: it is shown in the node capabilities")
		}
	})

	t.Run("runs until asked to stop, then ends without error", func(t *testing.T) {
		p, _ := start(t, context.Background())
		if p.Input() == nil {
			t.Fatal("Input must not be nil")
		}
		time.Sleep(200 * time.Millisecond)
		if isClosed(p.Done()) {
			t.Fatalf("the process ended by itself right after Start: %v %v", p.Err(), p.LastLog())
		}
		p.Stop(5 * time.Second)
		waitClosed(t, p.Done(), "Done must close after Stop")
		if err := p.Err(); err != nil {
			t.Fatalf("Err must be nil when stopped on request (the agent would count a failure), got %v", err)
		}
	})

	t.Run("Stop can be called more than once", func(t *testing.T) {
		p, _ := start(t, context.Background())
		p.Stop(5 * time.Second)
		p.Stop(5 * time.Second)
		waitClosed(t, p.Done(), "Done")
	})

	t.Run("writing input fails once the process ended", func(t *testing.T) {
		p, _ := start(t, context.Background())
		p.Stop(5 * time.Second)
		waitClosed(t, p.Done(), "Done")
		errc := make(chan error, 1)
		go func() { _, err := p.Input().Write(make([]byte, 188)); errc <- err }()
		select {
		case err := <-errc:
			if err == nil {
				t.Fatal("Write after the end must fail, or the agent would feed a dead process forever")
			}
		case <-time.After(settle):
			t.Fatal("Write after the end blocked: the agent's receiver would hang")
		}
	})

	t.Run("canceling the context ends the process", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		p, _ := start(t, ctx)
		cancel()
		waitClosed(t, p.Done(), "Done must close when the context is canceled")
	})

	t.Run("status calls are safe while it runs and after it ends", func(t *testing.T) {
		p, _ := start(t, context.Background())
		var wg sync.WaitGroup
		stop := make(chan struct{})
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
						_ = p.Progress()
						_ = p.LastLog()
						_ = p.PID()
						_ = p.Err()
					}
				}
			}()
		}
		time.Sleep(100 * time.Millisecond)
		p.Stop(5 * time.Second)
		waitClosed(t, p.Done(), "Done")
		close(stop)
		wg.Wait()
		_ = p.Progress() // still callable after the end
		_ = p.LastLog()
	})

	if c.Input == nil {
		return
	}

	t.Run("processes media and reports progress", func(t *testing.T) {
		p, spec := start(t, context.Background())
		in := c.Input(t)
		go func() { // the agent feeds the engine from another goroutine
			for off := 0; off < len(in); off += 188 * 7 {
				end := min(off+188*7, len(in))
				if _, err := p.Input().Write(in[off:end]); err != nil {
					return
				}
			}
		}()
		deadline := time.Now().Add(2 * settle)
		for p.Progress().TotalSize == 0 && p.Progress().OutTime == 0 {
			if isClosed(p.Done()) {
				t.Fatalf("the process ended before producing output: %v %v", p.Err(), p.LastLog())
			}
			if time.Now().After(deadline) {
				t.Fatalf("no progress reported after %s: Progress must follow the media written (progress %+v, log %v)", 2*settle, p.Progress(), p.LastLog())
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !c.ProducesHLS || spec.HLS == nil {
			return
		}
		for {
			segs, _ := filepath.Glob(filepath.Join(spec.HLS.Dir, "*.ts"))
			if _, err := os.Stat(filepath.Join(spec.HLS.Dir, "index.m3u8")); err == nil && len(segs) > 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("no HLS playlist and segment in %s", spec.HLS.Dir)
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
}
