package agent

import (
	"sync"
	"time"

	"nexostream/internal/model"
	"nexostream/internal/protocol"
)

// verified remembers the hour files already checked (each closed hour is
// verified once, after it closes).
var verified = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

// verifyDVR checks the closed hours of the last day of a channel and emits a
// dvr.integrity_error event per problem found.
func (a *Agent) verifyDVR(s protocol.ChannelSpec) {
	ch := s.Channel
	now := time.Now()
	problems := a.dvrStorage(s).Verify(ch.ID, now.Add(-24*time.Hour), now, now)
	verified.Lock()
	defer verified.Unlock()
	if len(verified.m) > 100000 {
		verified.m = map[string]bool{}
	}
	for _, p := range problems {
		key := p.File + "|" + p.Detail
		if verified.m[key] {
			continue
		}
		verified.m[key] = true
		a.log.Error("dvr integrity", "channel_id", ch.ID, "file", p.File, "problem", p.Detail)
		a.metrics.dvrIntegrity.WithLabelValues(ch.ID).Inc()
		ev := model.NewEvent(model.EventDVRIntegrityError, now)
		ev.ChannelID, ev.WorkerID = ch.ID, a.cfg.WorkerID
		ev.Reason = p.Detail
		ev.Details = map[string]any{"file": p.File}
		a.emit(ev)
	}
}
