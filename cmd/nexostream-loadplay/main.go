// Command nexostream-loadplay simulates HLS viewers for the audience tests
// of spec section 19.3: each viewer polls the playlist like a player and
// downloads every new segment, and the tool reports throughput, errors and
// slow segments (a segment that takes longer to download than to play means
// the viewer would stall).
//
//	nexostream-loadplay -url 'https://origin/canal-001/index.m3u8?token=…' -viewers 200 -ramp 60s -duration 10m
//
// Run it from other machines than the node under test.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type stats struct {
	active, requests, errors, bytes, slow, segments atomic.Int64
	mu                                              sync.Mutex
	ratios                                          []float64 // download time / segment duration
}

func (s *stats) ratio(r float64) {
	s.mu.Lock()
	s.ratios = append(s.ratios, r)
	s.mu.Unlock()
}

func (s *stats) takeRatios() []float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.ratios
	s.ratios = nil
	return r
}

type segment struct {
	uri string
	dur time.Duration
	seq int64
}

// parse returns the segments of a media playlist, or the first variant of a
// master playlist (variant >= 0 picks another one).
func parse(body string) (segs []segment, variant []string, target time.Duration) {
	var dur time.Duration
	var seq int64
	target = 6 * time.Second
	next := false
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(l, "#EXT-X-STREAM-INF"):
			next = true
		case strings.HasPrefix(l, "#EXT-X-TARGETDURATION:"):
			if n, err := strconv.Atoi(strings.TrimPrefix(l, "#EXT-X-TARGETDURATION:")); err == nil {
				target = time.Duration(n) * time.Second
			}
		case strings.HasPrefix(l, "#EXT-X-MEDIA-SEQUENCE:"):
			seq, _ = strconv.ParseInt(strings.TrimPrefix(l, "#EXT-X-MEDIA-SEQUENCE:"), 10, 64)
		case strings.HasPrefix(l, "#EXTINF:"):
			v, _, _ := strings.Cut(strings.TrimPrefix(l, "#EXTINF:"), ",")
			f, _ := strconv.ParseFloat(v, 64)
			dur = time.Duration(f * float64(time.Second))
		case l != "" && !strings.HasPrefix(l, "#"):
			if next {
				variant = append(variant, l)
				next = false
				continue
			}
			segs = append(segs, segment{uri: l, dur: dur, seq: seq})
			seq++
		}
	}
	return segs, variant, target
}

func resolve(base *url.URL, ref string) string {
	u, err := base.Parse(ref)
	if err != nil {
		return ref
	}
	return u.String()
}

func get(ctx context.Context, c *http.Client, u, ua string, st *stats) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// One User-Agent per viewer: without a token the server tells sessions
	// apart by IP + player, and all viewers come from this machine's IP.
	req.Header.Set("User-Agent", ua)
	st.requests.Add(1)
	resp, err := c.Do(req) //nolint:gosec // G704: the URL is the operator's test target
	if err != nil {
		st.errors.Add(1)
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		st.errors.Add(1)
		if err == nil {
			err = fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil, err
	}
	st.bytes.Add(int64(len(b)))
	return b, nil
}

// viewer plays like a live player: starts three segments from the edge,
// then downloads each new segment as the playlist grows.
func viewer(ctx context.Context, c *http.Client, start string, variant, id int, st *stats) {
	ua := fmt.Sprintf("nexostream-loadplay/1 viewer-%d", id)
	st.active.Add(1)
	defer st.active.Add(-1)
	playlist := start
	last := int64(-1)
	for ctx.Err() == nil {
		base, _ := url.Parse(playlist)
		body, err := get(ctx, c, playlist, ua, st)
		if err != nil {
			sleep(ctx, 2*time.Second)
			continue
		}
		segs, variants, target := parse(string(body))
		if len(variants) > 0 {
			i := min(max(variant, 0), len(variants)-1)
			playlist = resolve(base, variants[i])
			continue
		}
		if last < 0 && len(segs) > 3 {
			last = segs[len(segs)-4].seq
		}
		for _, s := range segs {
			if s.seq <= last {
				continue
			}
			t0 := time.Now()
			if _, err := get(ctx, c, resolve(base, s.uri), ua, st); err == nil {
				st.segments.Add(1)
				if s.dur > 0 {
					r := float64(time.Since(t0)) / float64(s.dur)
					st.ratio(r)
					if r > 1 {
						st.slow.Add(1)
					}
				}
			}
			last = s.seq
		}
		sleep(ctx, target/2)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func pct(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sort.Float64s(xs)
	return xs[min(len(xs)-1, int(p*float64(len(xs))))]
}

func main() {
	target := flag.String("url", "", "playlist URL (master or media, with ?token= if the channel needs it)")
	n := flag.Int("viewers", 10, "simultaneous viewers")
	ramp := flag.Duration("ramp", 10*time.Second, "time to start all viewers")
	dur := flag.Duration("duration", time.Minute, "test duration")
	variant := flag.Int("variant", 0, "ABR quality to play (0 = highest)")
	every := flag.Duration("report", 10*time.Second, "report interval")
	flag.Parse()
	if *target == "" || *n < 1 {
		flag.Usage()
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ctx, cancel2 := context.WithTimeout(ctx, *dur)
	defer cancel2()
	c := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: *n, MaxConnsPerHost: 0}}
	st := &stats{}
	var wg sync.WaitGroup
	go func() {
		for i := 0; i < *n && ctx.Err() == nil; i++ {
			wg.Add(1)
			go func(id int) { defer wg.Done(); viewer(ctx, c, *target, *variant, id, st) }(i + 1)
			sleep(ctx, *ramp/time.Duration(*n))
		}
	}()
	fmt.Println("tempo,espectadores,pedidos,erros,Mbps,segmentos,lentos,p50_download/dur,p95_download/dur")
	t := time.NewTicker(*every)
	defer t.Stop()
	t0 := time.Now()
	var lastBytes, lastReq, lastErr, lastSeg, lastSlow int64
	report := func() {
		b, r, e, sg, sl := st.bytes.Load(), st.requests.Load(), st.errors.Load(), st.segments.Load(), st.slow.Load()
		secs := every.Seconds()
		rs := st.takeRatios()
		fmt.Printf("%s,%d,%d,%d,%.1f,%d,%d,%.2f,%.2f\n", time.Since(t0).Round(time.Second), st.active.Load(),
			r-lastReq, e-lastErr, float64(b-lastBytes)*8/secs/1e6, sg-lastSeg, sl-lastSlow, pct(rs, 0.5), pct(rs, 0.95))
		lastBytes, lastReq, lastErr, lastSeg, lastSlow = b, r, e, sg, sl
	}
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			report()
			fmt.Fprintf(os.Stderr, "fim: %d pedidos, %d erros, %d segmentos, %d lentos (download mais demorado que o segmento)\n",
				st.requests.Load(), st.errors.Load(), st.segments.Load(), st.slow.Load())
			return
		case <-t.C:
			report()
		}
	}
}
