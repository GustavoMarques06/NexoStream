package agent

import (
	"strings"
	"sync"
	"time"
)

// A hardware encoder that crashes a pipeline (driver error, device lost) is
// put aside for a while: channels with encoder "auto" restart on the next
// encoder (in the end, the CPU) instead of failing in a loop.
const hwEncoderQuarantine = 30 * time.Minute

var hwEncoderNames = map[string]string{"h264_qsv": "qsv", "h264_nvenc": "nvenc"}

type encoderHealth struct {
	mu     sync.Mutex
	failed map[string]time.Time // encoder -> when it failed
}

// hwEncoderIn returns the hardware encoder named in an FFmpeg error, if any.
func hwEncoderIn(msg string) string {
	for name, enc := range hwEncoderNames {
		if strings.Contains(msg, name) {
			return enc
		}
	}
	return ""
}

func (h *encoderHealth) fail(enc string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failed == nil {
		h.failed = map[string]time.Time{}
	}
	_, already := h.failed[enc]
	h.failed[enc] = now
	return !already
}

// usable filters the encoders of the node, leaving out those in quarantine.
func (h *encoderHealth) usable(all []string, now time.Time) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(all))
	for _, e := range all {
		if t, ok := h.failed[e]; ok {
			if now.Sub(t) < hwEncoderQuarantine {
				continue
			}
			delete(h.failed, e)
		}
		out = append(out, e)
	}
	return out
}
