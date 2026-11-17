package epg

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nexostream/control-plane/store/memory"
	"nexostream/internal/model"
)

const sample = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE tv SYSTEM "xmltv.dtd">
<tv>
  <channel id="globo.br"><display-name>Globo</display-name></channel>
  <programme start="20261116120000 -0300" stop="20261116130000 -0300" channel="globo.br">
    <title lang="pt">Jornal Hoje</title><desc>Notícias &amp; mais</desc><category>Notícias</category>
  </programme>
  <programme start="20261116130000 -0300" channel="globo.br"><title>Sessão da Tarde</title></programme>
  <programme start="20261116150000 -0300" stop="20261116160000 -0300" channel="globo.br"><title>Novela</title></programme>
  <programme start="20261116120000 +0000" stop="20261116130000 +0000" channel="outro"><title>X</title></programme>
</tv>`

func TestParse(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(sample))
	_ = zw.Close()
	for name, data := range map[string][]byte{"plain": []byte(sample), "gzip": gz.Bytes()} {
		g, err := Parse(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ps := g.Programs["globo.br"]
		if g.Channels["globo.br"] != "Globo" || len(ps) != 3 || len(g.Programs["outro"]) != 1 {
			t.Fatalf("%s: %+v", name, g)
		}
		if !ps[0].Start.Equal(time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)) || ps[0].Description != "Notícias & mais" || ps[0].Category != "Notícias" {
			t.Fatalf("%s: %+v", name, ps[0])
		}
		if !ps[1].Stop.Equal(ps[2].Start) {
			t.Fatalf("%s: program without stop must end at the next start: %+v", name, ps[1])
		}
	}
	if _, err := Parse(strings.NewReader("<html></html>")); err == nil {
		t.Fatal("not XMLTV accepted")
	}
	if tm, err := ParseTime("202611161400"); err != nil || tm.Hour() != 14 {
		t.Fatal(tm, err)
	}
}

func TestRefreshAndExport(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(sample))
	}))
	defer srv.Close()
	st := memory.New()
	ctx := context.Background()
	a := model.Channel{ID: "globo", Name: "Globo", EPG: model.EPGConfig{URL: srv.URL, XMLTVID: "globo.br"}}
	b := model.Channel{ID: "outro", Name: "Outro", EPG: model.EPGConfig{URL: srv.URL}}
	c := model.Channel{ID: "sem", Name: "Sem guia", EPG: model.EPGConfig{URL: srv.URL}}
	now := time.Date(2026, 11, 16, 16, 0, 0, 0, time.UTC)
	im := &Importer{Store: st, Now: func() time.Time { return now }}
	res := im.Refresh(ctx, []model.Channel{a, b, c})
	if calls != 1 {
		t.Fatalf("one download per URL, got %d", calls)
	}
	if res["globo"].Programs != 3 || res["outro"].Programs != 1 || res["sem"].Error == "" {
		t.Fatalf("%+v", res)
	}
	if s, ok := LoadStatus(ctx, st, "globo"); !ok || s.Programs != 3 {
		t.Fatalf("status %+v", s)
	}
	if im.due(ctx, a) {
		t.Fatal("just imported")
	}
	now = now.Add(RefreshEvery + time.Minute)
	if !im.due(ctx, a) {
		t.Fatal("must refresh after 6 h")
	}
	ps, _ := st.ListPrograms(ctx, "globo", now.Add(-24*time.Hour), now)
	var out bytes.Buffer
	if err := XMLTV(&out, []model.Channel{a}, map[string][]model.Program{"globo": ps}); err != nil {
		t.Fatal(err)
	}
	g, err := Parse(&out)
	if err != nil || len(g.Programs["globo"]) != 3 || g.Programs["globo"][0].Title != "Jornal Hoje" {
		t.Fatalf("export round trip: %v %+v", err, g)
	}
}
