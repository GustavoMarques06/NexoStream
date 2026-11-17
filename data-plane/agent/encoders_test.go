package agent

import (
	"strings"
	"testing"
	"time"
)

func TestEncoderQuarantine(t *testing.T) {
	var h encoderHealth
	now := time.Now()
	all := []string{"qsv", "cpu"}
	msg := "exit status 0xfffffffb: [vost#0:0/h264_qsv @ 0x1] Terminating thread with return code -5 (I/O error)"
	enc := hwEncoderIn(msg)
	if enc != "qsv" || hwEncoderIn("exit status 1: [libx264] error") != "" {
		t.Fatalf("detected %q", enc)
	}
	if !h.fail(enc, now) || h.fail(enc, now) {
		t.Fatal("only the first failure is news")
	}
	if got := strings.Join(h.usable(all, now.Add(time.Minute)), ","); got != "cpu" {
		t.Fatalf("usable during quarantine: %s", got)
	}
	if got := strings.Join(h.usable(all, now.Add(hwEncoderQuarantine+time.Second)), ","); got != "qsv,cpu" {
		t.Fatalf("usable after quarantine: %s", got)
	}
}
