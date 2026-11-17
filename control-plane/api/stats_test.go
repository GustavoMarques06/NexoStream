package api

import (
	"context"
	"testing"
	"time"
)

func TestStatsAPI(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Minute)
	for i := 0; i < 30; i++ {
		_ = e.st.InsertStats(ctx, now.Add(-time.Duration(i)*time.Minute), map[string]float64{
			"viewers.total": 5, "channel:x:viewers": 4, "channel:y:viewers": 1, "worker:w1:cpu": 30,
		})
	}
	code, out, _ := e.do("GET", "/api/v1/stats?keys=viewers.total&prefix=worker:", e.tok, nil)
	series := out["series"].(map[string]any)
	if code != 200 || len(series) != 2 || len(series["viewers.total"].([]any)) == 0 {
		t.Fatalf("%d %v", code, out)
	}
	if code, _, _ := e.do("GET", "/api/v1/stats", e.tok, nil); code != 400 {
		t.Fatalf("no keys: %d", code)
	}
	if code, _, _ := e.do("GET", "/api/v1/stats?keys=a&from=2026-01-01T00:00:00Z&to=2026-03-01T00:00:00Z", e.tok, nil); code != 400 {
		t.Fatalf("range over 31 days: %d", code)
	}
	code, out, _ = e.do("GET", "/api/v1/stats/top-channels", e.tok, nil)
	items := out["items"].([]any)
	first := items[0].(map[string]any)
	if code != 200 || len(items) != 2 || first["channel_id"] != "x" || first["viewer_minutes"].(float64) != 120 || first["peak_viewers"].(float64) != 4 {
		t.Fatalf("top %d %v", code, out)
	}
}
