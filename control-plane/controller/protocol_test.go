package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"nexostream/control-plane/statuscache"
	"nexostream/control-plane/store/memory"
	"nexostream/internal/bus"
	"nexostream/internal/protocol"
)

// A worker on another contract revision is reported once per revision, so a
// mixed-version cluster is visible without flooding the log.
func TestContractRevisionMismatchIsReportedOnce(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	b := bus.NewMemory()
	defer b.Close()
	c := New(DefaultConfig(), memory.New(), statuscache.NewMemory(), b, log)
	ctx := context.Background()
	send := func(worker string, proto int) {
		d, _ := json.Marshal(protocol.Heartbeat{WorkerID: worker, Version: "x", Protocol: proto})
		c.onHeartbeat(ctx, d)
	}

	send("w-ok", protocol.Version)
	if buf.Len() != 0 {
		t.Fatalf("matching worker must not be reported: %s", buf.String())
	}
	send("w-old", 0) // an older worker that does not send the field
	send("w-old", 0)
	send("w-old", 0)
	if n := strings.Count(buf.String(), "another contract revision"); n != 1 {
		t.Fatalf("reported %d times, want once:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "w-old") {
		t.Fatalf("log must name the worker:\n%s", buf.String())
	}
	send("w-new", protocol.Version+1)
	if n := strings.Count(buf.String(), "another contract revision"); n != 2 {
		t.Fatalf("a different worker must be reported too (%d):\n%s", n, buf.String())
	}
}
