// Package epg imports program guides in XMLTV format (ADR 0009), like the
// EPG of Flussonic: each channel points to an XMLTV URL (several channels
// usually share one file) and the programs are stored for the panel, the
// catch-up of the DVR and the XMLTV export.
package epg

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"nexostream/control-plane/store"
	"nexostream/internal/model"
	"nexostream/internal/netguard"
)

// Limits and timing.
const (
	MaxBytes     = 512 << 20 // uncompressed XMLTV
	RefreshEvery = 6 * time.Hour
	RetryAfter   = 15 * time.Minute
	KeepPast     = 7 * 24 * time.Hour
	KeepFuture   = 14 * 24 * time.Hour
	statusPrefix = "epg:"
)

// Guide is a parsed XMLTV file.
type Guide struct {
	Channels map[string]string          // XMLTV id -> display name
	Programs map[string][]model.Program // XMLTV id -> programs
}

type xProgramme struct {
	Start    string   `xml:"start,attr"`
	Stop     string   `xml:"stop,attr"`
	Channel  string   `xml:"channel,attr"`
	Title    []string `xml:"title"`
	Desc     []string `xml:"desc"`
	Category []string `xml:"category"`
}

type xChannel struct {
	ID   string   `xml:"id,attr"`
	Name []string `xml:"display-name"`
}

// ParseTime reads XMLTV times: "20261116140000 +0000" (seconds and offset
// optional; no offset = UTC).
func ParseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	digits, off, _ := strings.Cut(s, " ")
	if len(digits) < 12 {
		return time.Time{}, fmt.Errorf("bad XMLTV time %q", s)
	}
	if len(digits) == 12 {
		digits += "00"
	}
	digits = digits[:14]
	if off == "" {
		return time.Parse("20060102150405", digits)
	}
	t, err := time.Parse("20060102150405 -0700", digits+" "+strings.TrimSpace(off))
	return t.UTC(), err
}

// Parse reads an XMLTV document (plain or gzip).
func Parse(r io.Reader) (Guide, error) {
	br := bufio.NewReader(r)
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return Guide{}, fmt.Errorf("gzip: %w", err)
		}
		defer zr.Close()
		r = zr
	} else {
		r = br
	}
	g := Guide{Channels: map[string]string{}, Programs: map[string][]model.Program{}}
	dec := xml.NewDecoder(io.LimitReader(r, MaxBytes))
	dec.Strict = false
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil } // UTF-8 and Latin-1 guides
	seen := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return g, fmt.Errorf("XMLTV: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "tv":
			seen = true
		case "channel":
			var c xChannel
			if err := dec.DecodeElement(&c, &se); err == nil && c.ID != "" {
				name := c.ID
				if len(c.Name) > 0 {
					name = strings.TrimSpace(c.Name[0])
				}
				g.Channels[c.ID] = name
			}
		case "programme":
			var p xProgramme
			if err := dec.DecodeElement(&p, &se); err != nil || p.Channel == "" || len(p.Title) == 0 {
				continue
			}
			start, err1 := ParseTime(p.Start)
			stop, err2 := ParseTime(p.Stop)
			if err1 != nil || (p.Stop != "" && err2 != nil) {
				continue
			}
			prog := model.Program{Start: start, Stop: stop, Title: strings.TrimSpace(p.Title[0])}
			if len(p.Desc) > 0 {
				prog.Description = strings.TrimSpace(p.Desc[0])
			}
			if len(p.Category) > 0 {
				prog.Category = strings.TrimSpace(p.Category[0])
			}
			g.Programs[p.Channel] = append(g.Programs[p.Channel], prog)
		}
	}
	if !seen {
		return g, errors.New("not an XMLTV document (no <tv>)")
	}
	// Programs without stop end where the next one starts.
	for id, ps := range g.Programs {
		for i := range ps {
			if ps[i].Stop.IsZero() || !ps[i].Stop.After(ps[i].Start) {
				if i+1 < len(ps) && ps[i+1].Start.After(ps[i].Start) {
					ps[i].Stop = ps[i+1].Start
				} else {
					ps[i].Stop = ps[i].Start.Add(time.Hour)
				}
			}
		}
		g.Programs[id] = ps
	}
	return g, nil
}

// Fetch downloads and parses an XMLTV URL.
func Fetch(ctx context.Context, client *http.Client, url string) (Guide, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Guide{}, err
	}
	resp, err := client.Do(req) //nolint:gosec // G704: the guide URL is set by an operator with channels:write
	if err != nil {
		return Guide{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Guide{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return Parse(resp.Body)
}

// LoadStatus returns the last import of a channel guide.
func LoadStatus(ctx context.Context, st store.SettingsStore, channelID string) (model.EPGStatus, bool) {
	b, err := st.GetSetting(ctx, statusPrefix+channelID)
	if err != nil {
		return model.EPGStatus{}, false
	}
	var s model.EPGStatus
	return s, json.Unmarshal(b, &s) == nil
}

func saveStatus(ctx context.Context, st store.SettingsStore, channelID string, s model.EPGStatus) {
	b, _ := json.Marshal(s)
	_ = st.PutSetting(ctx, statusPrefix+channelID, b)
}

// Importer refreshes guides.
type Importer struct {
	Store interface {
		store.ChannelStore
		store.EPGStore
		store.SettingsStore
	}
	Client *http.Client
	Log    *slog.Logger
	Now    func() time.Time
	// Leader, when set, limits the periodic import to the active controller.
	Leader func() bool
}

func (im *Importer) now() time.Time {
	if im.Now != nil {
		return im.Now()
	}
	return time.Now()
}

func (im *Importer) client() *http.Client {
	if im.Client != nil {
		return im.Client
	}
	return netguard.Client(2 * time.Minute)
}

// Refresh imports the guides of the given channels, downloading each URL
// once, and returns the status of each channel.
func (im *Importer) Refresh(ctx context.Context, chs []model.Channel) map[string]model.EPGStatus {
	byURL := map[string][]model.Channel{}
	for _, c := range chs {
		if c.EPG.URL != "" {
			byURL[c.EPG.URL] = append(byURL[c.EPG.URL], c)
		}
	}
	out := map[string]model.EPGStatus{}
	now := im.now().UTC()
	for url, group := range byURL {
		g, err := Fetch(ctx, im.client(), url)
		for _, c := range group {
			s := model.EPGStatus{FetchedAt: now}
			if err != nil {
				s.Error = err.Error()
			} else {
				id := c.EPG.XMLTVID
				if id == "" {
					id = c.ID
				}
				var keep []model.Program
				for _, p := range g.Programs[id] {
					if p.Stop.After(now.Add(-KeepPast)) && p.Start.Before(now.Add(KeepFuture)) {
						keep = append(keep, p)
					}
				}
				switch {
				case len(keep) == 0:
					s.Error = fmt.Sprintf("o guia não tem programas para o canal %q", id)
				default:
					if err := im.Store.ReplacePrograms(ctx, c.ID, keep); err != nil {
						s.Error = "falha ao gravar: " + err.Error()
					}
					s.Programs = len(keep)
				}
			}
			if s.Error != "" && im.Log != nil {
				im.Log.Warn("epg import failed", "channel_id", c.ID, "url", url, "error", s.Error)
			}
			saveStatus(ctx, im.Store, c.ID, s)
			out[c.ID] = s
		}
	}
	return out
}

// due reports whether a channel guide should be imported now.
func (im *Importer) due(ctx context.Context, c model.Channel) bool {
	s, ok := LoadStatus(ctx, im.Store, c.ID)
	if !ok {
		return true
	}
	age := im.now().Sub(s.FetchedAt)
	return age > RefreshEvery || (s.Error != "" && age > RetryAfter)
}

// Run imports due guides every few minutes until ctx ends.
func (im *Importer) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		var due []model.Channel
		after := ""
		for im.Leader == nil || im.Leader() {
			chs, err := im.Store.ListChannels(ctx, store.ChannelFilter{Page: store.Page{After: after, Limit: 200}})
			if err != nil || len(chs) == 0 {
				break
			}
			for _, c := range chs {
				if c.EPG.URL != "" && im.due(ctx, c) {
					due = append(due, c)
				}
			}
			after = chs[len(chs)-1].ID
		}
		if len(due) > 0 {
			im.Refresh(ctx, due)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// XMLTV writes programs as an XMLTV document (export for players and
// middleware).
func XMLTV(w io.Writer, chs []model.Channel, progs map[string][]model.Program) error {
	type xTitle struct {
		Lang string `xml:"lang,attr,omitempty"`
		Text string `xml:",chardata"`
	}
	type xp struct {
		XMLName  xml.Name `xml:"programme"`
		Start    string   `xml:"start,attr"`
		Stop     string   `xml:"stop,attr"`
		Channel  string   `xml:"channel,attr"`
		Title    xTitle   `xml:"title"`
		Desc     string   `xml:"desc,omitempty"`
		Category string   `xml:"category,omitempty"`
	}
	type xc struct {
		XMLName xml.Name `xml:"channel"`
		ID      string   `xml:"id,attr"`
		Name    string   `xml:"display-name"`
	}
	const layout = "20060102150405 -0700"
	if _, err := io.WriteString(w, xml.Header+`<tv generator-info-name="NexoStream">`+"\n"); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("  ", "  ")
	for _, c := range chs {
		if err := enc.Encode(xc{ID: c.ID, Name: c.Name}); err != nil {
			return err
		}
	}
	for _, c := range chs {
		for _, p := range progs[c.ID] {
			if err := enc.Encode(xp{Start: p.Start.UTC().Format(layout), Stop: p.Stop.UTC().Format(layout), Channel: c.ID,
				Title: xTitle{Text: p.Title}, Desc: p.Description, Category: p.Category}); err != nil {
				return err
			}
		}
	}
	if err := enc.Flush(); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n</tv>\n")
	return err
}
