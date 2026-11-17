// Package health turns ingest measurements into source health and runs the
// per-channel state machine (spec sections 8.1 to 8.5).
package health

import (
	"math"
	"time"
)

type sample struct {
	at    time.Time
	total int64
}

// Window computes the increase of a cumulative counter over a sliding window
// (bitrate over 1 s, continuity errors over 60 s).
type Window struct {
	span    time.Duration
	samples []sample
}

// NewWindow creates a window of the given span.
func NewWindow(span time.Duration) *Window { return &Window{span: span} }

// Sample records the counter value at time now.
func (w *Window) Sample(now time.Time, total int64) {
	w.samples = append(w.samples, sample{now, total})
	cut := 0
	for cut < len(w.samples)-1 && now.Sub(w.samples[cut+1].at) >= w.span {
		cut++
	}
	w.samples = w.samples[cut:]
}

// Delta returns the increase across the window and the elapsed time.
func (w *Window) Delta() (int64, time.Duration) {
	if len(w.samples) < 2 {
		return 0, 0
	}
	first, last := w.samples[0], w.samples[len(w.samples)-1]
	return last.total - first.total, last.at.Sub(first.at)
}

// Bitrate tracks the instantaneous bitrate (1 s window) and a moving
// average (exponential, time constant Tau).
type Bitrate struct {
	win     *Window
	Tau     time.Duration
	avg     float64
	lastAt  time.Time
	warmup  time.Duration
	firstAt time.Time
}

// NewBitrate returns a tracker with a 60 s average.
func NewBitrate() *Bitrate {
	return &Bitrate{win: NewWindow(time.Second), Tau: 60 * time.Second, warmup: 2 * time.Second}
}

// Sample records the cumulative byte counter.
func (b *Bitrate) Sample(now time.Time, totalBytes int64) {
	b.win.Sample(now, totalBytes)
	if b.firstAt.IsZero() && totalBytes > 0 {
		b.firstAt = now
	}
	cur := b.Current()
	if !b.firstAt.IsZero() && now.Sub(b.firstAt) >= b.warmup {
		if b.avg == 0 {
			b.avg = cur
		} else if !b.lastAt.IsZero() {
			dt := now.Sub(b.lastAt).Seconds()
			alpha := 1 - math.Exp(-dt/b.Tau.Seconds())
			b.avg += (cur - b.avg) * alpha
		}
	}
	b.lastAt = now
}

// Current returns bits per second over the last second.
func (b *Bitrate) Current() float64 {
	d, el := b.win.Delta()
	if el <= 0 {
		return 0
	}
	return float64(d) * 8 / el.Seconds()
}

// Average returns the moving average in bits per second (0 while warming up).
func (b *Bitrate) Average() float64 { return b.avg }
