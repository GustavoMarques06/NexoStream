package dvr

import (
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Storage modes, like the disks of a Flussonic DVR.
const (
	ModeSingle = "single" // one folder
	ModeRAID0  = "raid0"  // each hour on one disk, hours spread over the disks
	ModeRAID1  = "raid1"  // every hour written to every disk (mirror)
)

// Storage is where a channel records: one or more disks (folders, usually
// mount points) on the Media Node.
type Storage struct {
	Mode  string
	Disks []string
	// State is the in-memory state of the node's DVR (open hours, failed
	// disks, exports); nil uses a process-wide default.
	State *State
}

// Single returns a one-folder storage.
func Single(root string) Storage { return Storage{Mode: ModeSingle, Disks: []string{root}} }

// Validate checks the definition.
func (s Storage) Validate() error {
	switch s.Mode {
	case ModeSingle:
		if len(s.Disks) != 1 {
			return errors.New("single mode uses exactly one disk")
		}
	case ModeRAID0, ModeRAID1:
		if len(s.Disks) < 2 {
			return fmt.Errorf("%s needs at least two disks", s.Mode)
		}
	default:
		return fmt.Errorf("unknown mode %q (single, raid0, raid1)", s.Mode)
	}
	seen := map[string]bool{}
	for _, d := range s.Disks {
		if d == "" || seen[filepath.Clean(d)] {
			return errors.New("disks must be distinct, non-empty paths")
		}
		seen[filepath.Clean(d)] = true
	}
	return nil
}

// targets returns the disk indexes a piece starting at start goes to.
func (s Storage) targets(start time.Time) []int {
	n := len(s.Disks)
	switch s.Mode {
	case ModeRAID1:
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	case ModeRAID0:
		// The whole hour goes to one disk chosen by a hash of the hour, then
		// the next disks as fallbacks when it fails.
		h := fnv.New32a()
		_, _ = h.Write([]byte(strconv.FormatInt(start.UTC().Truncate(time.Hour).Unix(), 10)))
		first := int(h.Sum32() % uint32(n)) //nolint:gosec // n is a small disk count
		out := make([]int, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, (first+i)%n)
		}
		return out
	default:
		return []int{0}
	}
}

// Record appends a live HLS segment. RAID-0 writes it on the disk of the
// hour (falling back to the next healthy disk); RAID-1 writes it on every
// healthy disk and succeeds if at least one copy was written.
func (s Storage) Record(channelID, src string, start time.Time, dur time.Duration) (Segment, error) {
	var seg Segment
	var errs []error
	written := 0
	for _, i := range s.targets(start) {
		d := s.Disks[i]
		if s.state().DiskDown(d) && s.Mode != ModeSingle {
			continue
		}
		g, err := s.state().Record(d, channelID, src, start, dur)
		if err != nil {
			s.state().markDown(d, true)
			errs = append(errs, err)
			continue
		}
		s.state().markDown(d, false)
		if written == 0 {
			seg = g
			seg.Disk = i
		}
		written++
		if s.Mode != ModeRAID1 {
			break
		}
	}
	if written == 0 {
		if len(errs) == 0 {
			errs = append(errs, errors.New("no healthy disk"))
		}
		return seg, errors.Join(errs...)
	}
	return seg, nil
}

// List merges the pieces found on every disk (mirrored copies counted once;
// each piece keeps the disk and offsets it was read from).
func (s Storage) List(channelID string, from, to time.Time) ([]Segment, error) {
	byStart := map[int64]Segment{}
	for i, d := range s.Disks {
		segs, err := List(d, channelID, from, to)
		if err != nil {
			continue
		}
		for _, g := range segs {
			if _, ok := byStart[g.Start.UnixMilli()]; !ok {
				g.Disk = i
				byStart[g.Start.UnixMilli()] = g
			}
		}
	}
	out := make([]Segment, 0, len(byStart))
	for _, g := range byStart {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// URI returns the address of a piece's hour file relative to the channel
// playlist: "2026/11/16/14.ts", or "d1/2026/11/16/14.ts" on multi-disk
// storages (mirrored hour files may differ in size after a disk failure).
func (s Storage) URI(seg Segment) string {
	if len(s.Disks) > 1 {
		return "d" + strconv.Itoa(seg.Disk) + "/" + seg.Rel
	}
	return seg.Rel
}

// Locate resolves a URI produced by URI to a file.
func (s Storage) Locate(channelID, uri string) (string, bool) {
	disk := 0
	if rest, ok := strings.CutPrefix(uri, "d"); ok && len(s.Disks) > 1 {
		n, after, found := strings.Cut(rest, "/")
		i, err := strconv.Atoi(n)
		if !found || err != nil || i < 0 || i >= len(s.Disks) {
			return "", false
		}
		disk, uri = i, after
	}
	rel := filepath.FromSlash(uri)
	if !filepath.IsLocal(rel) || !filepath.IsLocal(channelID) {
		return "", false // never leave the disk folder
	}
	p := filepath.Join(s.Disks[disk], channelID, rel)
	if st, err := os.Stat(p); err == nil && !st.IsDir() { //nolint:gosec // G703: rel and channelID checked with filepath.IsLocal
		return p, true
	}
	return "", false
}

// Cleanup applies the retention on every disk and every recorded quality of
// the channel.
func (s Storage) Cleanup(channelID string, now time.Time, retention time.Duration) ([]HourFile, error) {
	var all []HourFile
	var errs []error
	for _, key := range s.VariantKeys(channelID) {
		for _, d := range s.Disks {
			removed, err := s.state().Cleanup(d, key, now, retention)
			if err != nil {
				errs = append(errs, err)
			}
			all = append(all, removed...)
		}
	}
	return all, errors.Join(errs...)
}
