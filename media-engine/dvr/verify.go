package dvr

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Problem is an integrity issue found in a recorded hour.
type Problem struct {
	File   string // hour .ts file
	Detail string
}

// VerifyHour checks a closed hour file against its index: pieces must be
// contiguous, the file must hold exactly the indexed bytes, and every piece
// must start with an MPEG-TS sync byte and be a whole number of packets.
func VerifyHour(ts string) []Problem {
	var out []Problem
	add := func(f string, a ...any) { out = append(out, Problem{File: ts, Detail: fmt.Sprintf(f, a...)}) }
	idxPath := strings.TrimSuffix(ts, ".ts") + ".idx"
	lines := readIdx(idxPath)
	fi, err := os.Stat(ts)
	if err != nil {
		add("arquivo da hora ilegível: %v", err)
		return out
	}
	if len(lines) == 0 {
		if fi.Size() > 0 {
			add("%d bytes sem índice", fi.Size())
		}
		return out
	}
	f, err := os.Open(ts) //nolint:gosec // path built from the DVR folder
	if err != nil {
		add("arquivo da hora ilegível: %v", err)
		return out
	}
	defer f.Close()
	var next int64
	var prevStart int64 = -1
	bad := 0
	b := make([]byte, 1)
	for i, l := range lines {
		if l.off != next {
			add("peça %d começa no byte %d, esperado %d (buraco ou sobreposição)", i, l.off, next)
		}
		if l.start <= prevStart {
			add("peça %d fora de ordem no tempo", i)
		}
		if l.size%188 != 0 {
			bad++
		} else if _, err := f.ReadAt(b, l.off); err != nil || b[0] != 0x47 {
			bad++
		}
		next, prevStart = l.off+l.size, l.start
	}
	if bad > 0 {
		add("%d peça(s) sem sincronismo MPEG-TS", bad)
	}
	switch {
	case fi.Size() < next:
		add("arquivo com %d bytes, índice aponta até %d (gravação truncada)", fi.Size(), next)
	case fi.Size() > next:
		add("%d bytes no fim do arquivo sem índice", fi.Size()-next)
	}
	return out
}

// Verify checks the closed hours of a channel in [from, to) on every disk.
// The hour being recorded is skipped.
func (s Storage) Verify(channelID string, from, to time.Time, now time.Time) []Problem {
	var out []Problem
	open := now.UTC().Truncate(time.Hour)
	for _, key := range s.VariantKeys(channelID) {
		for _, d := range s.Disks {
			hs, err := ListHours(d, key)
			if err != nil {
				continue
			}
			for _, h := range hs {
				if h.Start.Before(from) || !h.Start.Before(to) || !h.Start.Before(open) {
					continue
				}
				out = append(out, VerifyHour(h.Path)...)
			}
		}
	}
	return out
}
