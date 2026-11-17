package dvr

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"nexostream/media-engine/packaging"
)

// DVR of an ABR channel records every quality. Quality 0 (the highest) keeps
// the original layout, so recordings made before ABR support stay valid:
//
//	<root>/<channel>/<YYYY>/<MM>/<DD>/<HH>.ts        quality 0
//	<root>/<channel>/v<N>/<YYYY>/<MM>/<DD>/<HH>.ts   quality N (N >= 1)
//
// A quality is addressed by its key, "<channel>" or "<channel>/v<N>", which
// every Storage method accepts in place of the channel id. The qualities
// recorded are listed in <root>/<channel>/.variants.json for playback.

// variantsFile lists the qualities of an ABR recording.
const variantsFile = ".variants.json"

var variantDirRe = regexp.MustCompile(`^v([0-9]{1,2})$`)

// VariantKey returns the storage key of quality i of a channel.
func VariantKey(channelID string, i int) string {
	if i <= 0 {
		return channelID
	}
	return channelID + "/" + packaging.VariantDir(i)
}

// VariantKeys returns the keys of every quality recorded for the channel on
// any disk: the channel itself first, then "<channel>/v<N>" folders.
func (s Storage) VariantKeys(channelID string) []string {
	keys := []string{channelID}
	seen := map[int]bool{}
	for _, d := range s.Disks {
		entries, err := os.ReadDir(filepath.Join(d, channelID))
		if err != nil {
			continue
		}
		for _, e := range entries {
			m := variantDirRe.FindStringSubmatch(e.Name())
			if m == nil || !e.IsDir() {
				continue
			}
			if n, _ := strconv.Atoi(m[1]); n >= 1 {
				seen[n] = true
			}
		}
	}
	for n := 1; n < 100; n++ { // ascending order, deterministic
		if seen[n] {
			keys = append(keys, VariantKey(channelID, n))
		}
	}
	return keys
}

// WriteVariants stores the qualities being recorded (best effort on every
// disk). Fewer than two qualities removes the file: the channel is not ABR.
func (s Storage) WriteVariants(channelID string, vs []packaging.Variant) error {
	var errs []error
	ok := false
	for _, d := range s.Disks {
		p := filepath.Join(d, channelID, variantsFile)
		if len(vs) < 2 {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
			ok = true
			continue
		}
		b, _ := json.Marshal(vs)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { //nolint:gosec // served by the origin
			errs = append(errs, err)
			continue
		}
		if err := os.WriteFile(p, b, 0o644); err != nil { //nolint:gosec // served by the origin
			errs = append(errs, err)
			continue
		}
		ok = true
	}
	if ok {
		return nil
	}
	return errors.Join(errs...)
}

// ReadVariants returns the qualities of an ABR recording, or nil.
func (s Storage) ReadVariants(channelID string) []packaging.Variant {
	for _, d := range s.Disks {
		b, err := os.ReadFile(filepath.Join(d, channelID, variantsFile)) //nolint:gosec // channelID validated by the handler
		if err != nil {
			continue
		}
		var vs []packaging.Variant
		if json.Unmarshal(b, &vs) == nil && len(vs) >= 2 {
			return vs
		}
	}
	return nil
}

// recordedVariants returns the indexes of the qualities that have pieces in
// [from, to).
func (s Storage) recordedVariants(channelID string, n int, from, to time.Time) []int {
	var out []int
	for i := 0; i < n; i++ {
		if segs, _ := s.List(VariantKey(channelID, i), from, to); len(segs) > 0 {
			out = append(out, i)
		}
	}
	return out
}
