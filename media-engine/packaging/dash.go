package packaging

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// DASHDir is the folder of the MPEG-DASH output inside the channel folder.
const DASHDir = "dash"

// ManifestName is the DASH manifest file name.
const ManifestName = "index.mpd"

// DASHTeeSlave returns the tee entry of the MPEG-DASH output (fMP4,
// templates with timeline): every video stream goes into one adaptation set
// (so an ABR ladder is a DASH ladder) and the audio into another.
func (o HLSOptions) DASHTeeSlave() string {
	seg := o.SegmentSeconds
	if seg <= 0 {
		seg = 6
	}
	size := o.ListSize
	if size <= 0 {
		size = 10
	}
	opts := []string{
		"select=" + teeOptionEscape("v,a"),
		"f=dash",
		"seg_duration=" + strconv.Itoa(seg),
		"window_size=" + strconv.Itoa(size),
		"extra_window_size=5",
		"remove_at_exit=0",
		"use_template=1",
		"use_timeline=1",
		"adaptation_sets=" + teeOptionEscape("id=0,streams=v id=1,streams=a"),
		"init_seg_name=" + teeOptionEscape("init-$RepresentationID$.m4s"),
		"media_seg_name=" + teeOptionEscape("chunk-$RepresentationID$-$Number%05d$.m4s"),
	}
	return "[" + strings.Join(opts, ":") + "]" + TeeEscape(filepath.ToSlash(filepath.Join(o.Dir, DASHDir, ManifestName)))
}

var mpdTemplateRe = regexp.MustCompile(`(media|initialization)="([^"]*)"`)

// AddTokenMPD appends ?token= to the segment templates of a DASH manifest,
// so every request of the viewer carries the token of the manifest URL.
func AddTokenMPD(mpd []byte, token string) []byte {
	if token == "" {
		return mpd
	}
	param := "token=" + url.QueryEscape(token)
	return mpdTemplateRe.ReplaceAllFunc(mpd, func(m []byte) []byte {
		sub := mpdTemplateRe.FindSubmatch(m)
		v := string(sub[2])
		if strings.Contains(v, "token=") {
			return m
		}
		sep := "?"
		if strings.Contains(v, "?") {
			sep = "&amp;"
		}
		return []byte(string(sub[1]) + `="` + v + sep + param + `"`)
	})
}
