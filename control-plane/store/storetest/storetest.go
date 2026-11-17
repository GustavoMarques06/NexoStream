// Package storetest is the conformance suite every store.Store must pass.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"nexostream/control-plane/store"
	"nexostream/internal/model"
)

func channel(id string) *model.Channel {
	return &model.Channel{
		ID: id, Name: "Canal " + id, Group: "Noticias", Enabled: true, Mode: model.ModePassthrough,
		Profiles: []string{},
		Sources: []model.Source{
			{ID: "1", Priority: 1, URL: "udp://239.1.1.1:1234", Interface: "eno2"},
			{ID: "2", Priority: 2, URL: "udp://239.1.2.1:1234"},
		},
		Outputs:  model.Outputs{HLS: true, MPEGTSHTTP: true, DASH: true, Push: []string{"rtmp://cdn.local/live/key", "srt://10.0.0.9:9000"}},
		DVR:      model.DVRConfig{Enabled: true, RetentionHours: 72, Path: "/srv/dvr-a", AllQualities: true},
		Overlay:  model.OverlayConfig{Enabled: true, Image: "logo.png", Position: model.OverlayBottomLeft, Margin: 30, Width: 200, Opacity: 0.75},
		DRM:      model.DRMConfig{Enabled: true, Method: "aes-128", KeyRotationMinutes: 30},
		Auth:     model.AuthConfig{Enabled: true, BackendURL: "http://auth.local/check", AllowIPs: []string{"10.0.0.0/8"}, MaxSessions: 100},
		EPG:      model.EPGConfig{URL: "http://epg.local/guide.xml.gz", XMLTVID: "canal.br"},
		Failover: model.DefaultFailover(),
	}
}

// Run executes the suite against a fresh, empty store.
func Run(t *testing.T, s store.Store) {
	ctx := context.Background()

	t.Run("channels", func(t *testing.T) {
		c := channel("canal-001")
		if err := s.CreateChannel(ctx, c); err != nil {
			t.Fatal(err)
		}
		if c.Revision != 1 || c.CreatedAt.IsZero() {
			t.Fatalf("revision=%d created=%v", c.Revision, c.CreatedAt)
		}
		if err := s.CreateChannel(ctx, channel("canal-001")); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("duplicate: %v", err)
		}
		got, err := s.GetChannel(ctx, "canal-001")
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Sources) != 2 || got.Sources[0].Interface != "eno2" || !got.Outputs.MPEGTSHTTP || got.DVR.RetentionHours != 72 || got.DVR.Path != "/srv/dvr-a" || !got.DVR.AllQualities ||
			got.Overlay != (model.OverlayConfig{Enabled: true, Image: "logo.png", Position: model.OverlayBottomLeft, Margin: 30, Width: 200, Opacity: 0.75}) ||
			!got.Outputs.DASH || len(got.Outputs.Push) != 2 || got.Outputs.Push[1] != "srt://10.0.0.9:9000" ||
			got.DRM != (model.DRMConfig{Enabled: true, Method: "aes-128", KeyRotationMinutes: 30}) ||
			got.Failover.NoPacketsSeconds != 3 || !got.Failover.ReturnsToPrimary() {
			t.Fatalf("got %+v", got)
		}
		got.Name = "Renomeado"
		got.Mode = model.ModeTranscode
		got.Profiles = []string{"hd"}
		got.Sources = got.Sources[:1]
		if err := s.UpdateChannel(ctx, &got); err != nil {
			t.Fatal(err)
		}
		if got.Revision != 2 {
			t.Fatalf("revision=%d", got.Revision)
		}
		got2, _ := s.GetChannel(ctx, "canal-001")
		if got2.Name != "Renomeado" || len(got2.Sources) != 1 || len(got2.Profiles) != 1 || got2.Profiles[0] != "hd" {
			t.Fatalf("after update %+v", got2)
		}
		if err := s.DeleteProfile(ctx, "hd"); !errors.Is(err, store.ErrInUse) {
			t.Fatalf("delete used profile: %v", err)
		}

		for _, id := range []string{"canal-002", "canal-003", "canal-004"} {
			c := channel(id)
			if id == "canal-004" {
				c.Enabled = false
				c.Group = "Futebol"
			}
			if err := s.CreateChannel(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
		page, _ := s.ListChannels(ctx, store.ChannelFilter{Page: store.Page{Limit: 2}})
		if len(page) != 2 || page[0].ID != "canal-001" || page[1].ID != "canal-002" {
			t.Fatalf("page1 %v", ids(page))
		}
		page, _ = s.ListChannels(ctx, store.ChannelFilter{Page: store.Page{After: "canal-002", Limit: 2}})
		if len(page) != 2 || page[0].ID != "canal-003" {
			t.Fatalf("page2 %v", ids(page))
		}
		f := false
		page, _ = s.ListChannels(ctx, store.ChannelFilter{Enabled: &f})
		if len(page) != 1 || page[0].ID != "canal-004" {
			t.Fatalf("disabled %v", ids(page))
		}
		page, _ = s.ListChannels(ctx, store.ChannelFilter{Group: "Futebol"})
		if len(page) != 1 {
			t.Fatalf("group %v", ids(page))
		}
		page, _ = s.ListChannels(ctx, store.ChannelFilter{Search: "RENOMEADO"})
		if len(page) != 1 || page[0].ID != "canal-001" {
			t.Fatalf("search %v", ids(page))
		}
		total, enabled, _ := s.CountChannels(ctx)
		if total != 4 || enabled != 3 {
			t.Fatalf("count %d %d", total, enabled)
		}
		if err := s.DeleteChannel(ctx, "canal-004"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetChannel(ctx, "canal-004"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("deleted: %v", err)
		}
		if err := s.DeleteChannel(ctx, "canal-004"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("delete twice: %v", err)
		}
	})

	t.Run("profiles", func(t *testing.T) {
		ps, _ := s.ListProfiles(ctx)
		if len(ps) != 3 {
			t.Fatalf("default profiles %v", ps)
		}
		p := model.DefaultProfiles()[2]
		p.ID = "mobile"
		p.Video.Width, p.Video.Height = 640, 360
		if err := s.CreateProfile(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateProfile(ctx, p); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("dup %v", err)
		}
		p.Encoder = model.EncoderCPU
		if err := s.UpdateProfile(ctx, p); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetProfile(ctx, "mobile")
		if got.Encoder != model.EncoderCPU || got.Video.Width != 640 {
			t.Fatalf("%+v", got)
		}
		if err := s.DeleteProfile(ctx, "mobile"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetProfile(ctx, "mobile"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
	})

	t.Run("workers", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Millisecond)
		w := model.Worker{ID: "media-01", State: model.WorkerOnline, LastHeartbeatAt: now, StartedAt: now, Version: "0.1.0",
			Capabilities: model.Capabilities{Encoders: []string{"cpu"}, Interfaces: []string{"eno2"}}, Channels: 3}
		if err := s.UpsertWorker(ctx, w); err != nil {
			t.Fatal(err)
		}
		if err := s.SetWorkerDraining(ctx, "media-01", true); err != nil {
			t.Fatal(err)
		}
		_ = s.UpsertWorker(ctx, w) // heartbeat must not clear draining
		got, _ := s.GetWorker(ctx, "media-01")
		if !got.Draining || got.State != model.WorkerDraining || got.Channels != 3 || !got.Capabilities.HasInterface("eno2") {
			t.Fatalf("%+v", got)
		}
		_ = s.SetWorkerDraining(ctx, "media-01", false)
		_ = s.SetWorkerState(ctx, "media-01", model.WorkerOffline)
		got, _ = s.GetWorker(ctx, "media-01")
		if got.Draining || got.State != model.WorkerOffline {
			t.Fatalf("%+v", got)
		}
		if err := s.SetWorkerDraining(ctx, "nope", true); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		if err := s.SetAssignment(ctx, model.Assignment{ChannelID: "canal-001", WorkerID: "media-01", Since: now, Reason: "scheduled"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetAssignment(ctx, model.Assignment{ChannelID: "ghost", WorkerID: "media-01", Since: now}); err == nil {
			t.Fatal("assignment of an unknown channel must fail")
		}
		as, _ := s.ListAssignments(ctx)
		if len(as) != 1 || as[0].WorkerID != "media-01" {
			t.Fatalf("%v", as)
		}
		_ = s.DeleteAssignment(ctx, "canal-001")
		as, _ = s.ListAssignments(ctx)
		if len(as) != 0 {
			t.Fatalf("%v", as)
		}
	})

	t.Run("events", func(t *testing.T) {
		base := time.Now().UTC().Truncate(time.Second)
		for i := 0; i < 5; i++ {
			ev := model.NewEvent(model.EventChannelStateChanged, base.Add(time.Duration(i)*time.Second))
			ev.ChannelID = "canal-001"
			ev.From, ev.To, ev.Reason = model.StateRunning, model.StateDegraded, "no_packets"
			ev.Details = map[string]any{"n": float64(i)}
			if err := s.InsertEvent(ctx, ev); err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				_ = s.InsertEvent(ctx, ev) // redelivery must be ignored
			}
		}
		other := model.NewEvent(model.EventWorkerOnline, base)
		other.WorkerID = "media-01"
		_ = s.InsertEvent(ctx, other)
		evs, _ := s.ListEvents(ctx, store.EventFilter{ChannelID: "canal-001", Limit: 3})
		if len(evs) != 3 || evs[0].Details["n"] != float64(4) || evs[0].To != model.StateDegraded {
			t.Fatalf("%+v", evs)
		}
		more, _ := s.ListEvents(ctx, store.EventFilter{ChannelID: "canal-001", Before: evs[2].Seq})
		if len(more) != 2 {
			t.Fatalf("next page %d", len(more))
		}
		w, _ := s.ListEvents(ctx, store.EventFilter{Type: model.EventWorkerOnline})
		if len(w) != 1 || w[0].WorkerID != "media-01" {
			t.Fatalf("%+v", w)
		}
	})

	t.Run("dvr-storages", func(t *testing.T) {
		v := model.DVRStorage{ID: "principal", Mode: "raid1", Disks: []string{"/mnt/d1", "/mnt/d2"}}
		if err := s.CreateDVRStorage(ctx, v); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateDVRStorage(ctx, v); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("dup %v", err)
		}
		v.Mode, v.Disks = "raid0", []string{"/mnt/d1", "/mnt/d2", "/mnt/d3"}
		if err := s.UpdateDVRStorage(ctx, v); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetDVRStorage(ctx, "principal")
		if got.Mode != "raid0" || len(got.Disks) != 3 {
			t.Fatalf("%+v", got)
		}
		c := channel("canal-dvr")
		c.DVR.Storage = "principal"
		if err := s.CreateChannel(ctx, c); err != nil {
			t.Fatal(err)
		}
		gc, _ := s.GetChannel(ctx, "canal-dvr")
		if gc.DVR.Storage != "principal" {
			t.Fatalf("storage not saved: %+v", gc.DVR)
		}
		if err := s.DeleteDVRStorage(ctx, "principal"); !errors.Is(err, store.ErrInUse) {
			t.Fatalf("delete used storage: %v", err)
		}
		bad := channel("canal-bad")
		bad.DVR.Storage = "ghost"
		if err := s.CreateChannel(ctx, bad); err == nil {
			t.Fatal("unknown storage accepted")
		}
		_ = s.DeleteChannel(ctx, "canal-dvr")
		if err := s.DeleteDVRStorage(ctx, "principal"); err != nil {
			t.Fatal(err)
		}
		if l, _ := s.ListDVRStorages(ctx); len(l) != 0 {
			t.Fatalf("left %v", l)
		}
	})

	t.Run("settings", func(t *testing.T) {
		if _, err := s.GetSetting(ctx, "global"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("absent setting: %v", err)
		}
		if err := s.PutSetting(ctx, "global", []byte(`{"hls_segment_seconds":4}`)); err != nil {
			t.Fatal(err)
		}
		_ = s.PutSetting(ctx, "global", []byte(`{"hls_segment_seconds":2}`))
		_ = s.PutSetting(ctx, "server:media-01", []byte(`{"dvr_dir":"/srv"}`))
		_ = s.PutSetting(ctx, "server:media-02", []byte(`{"dvr_dir":"/mnt"}`))
		v, err := s.GetSetting(ctx, "global")
		if err != nil || !strings.Contains(string(v), "2") {
			t.Fatalf("%s %v", v, err)
		}
		all, _ := s.ListSettings(ctx, "server:")
		if len(all) != 2 {
			t.Fatalf("servers %v", all)
		}
	})

	t.Run("epg", func(t *testing.T) {
		if err := s.CreateChannel(ctx, channel("epg-1")); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetChannel(ctx, "epg-1")
		if got.Auth.BackendURL != "http://auth.local/check" || got.Auth.AllowIPs[0] != "10.0.0.0/8" || got.Auth.MaxSessions != 100 ||
			got.EPG.XMLTVID != "canal.br" {
			t.Fatalf("auth/epg not stored: %+v %+v", got.Auth, got.EPG)
		}
		base := time.Now().UTC().Truncate(time.Hour)
		prog := func(h int, title string) model.Program {
			return model.Program{Start: base.Add(time.Duration(h) * time.Hour), Stop: base.Add(time.Duration(h+1) * time.Hour), Title: title, Category: "Notícias"}
		}
		if err := s.ReplacePrograms(ctx, "epg-1", []model.Program{prog(-2, "A"), prog(-1, "B"), prog(0, "C")}); err != nil {
			t.Fatal(err)
		}
		// A newer guide starting at -1 h replaces from there on; -2 h stays (catch-up).
		if err := s.ReplacePrograms(ctx, "epg-1", []model.Program{prog(-1, "B2"), prog(0, "C2"), prog(1, "D")}); err != nil {
			t.Fatal(err)
		}
		ps, err := s.ListPrograms(ctx, "epg-1", base.Add(-3*time.Hour), base.Add(3*time.Hour))
		if err != nil || len(ps) != 4 || ps[0].Title != "A" || ps[1].Title != "B2" || ps[3].Title != "D" || ps[3].ChannelID != "epg-1" {
			t.Fatalf("programs %v %+v", err, ps)
		}
		ps, _ = s.ListPrograms(ctx, "epg-1", base.Add(30*time.Minute), base.Add(40*time.Minute))
		if len(ps) != 1 || ps[0].Title != "C2" {
			t.Fatalf("window %+v", ps)
		}
		_ = s.DeleteChannel(ctx, "epg-1")
		if ps, _ := s.ListPrograms(ctx, "epg-1", base.Add(-3*time.Hour), base.Add(3*time.Hour)); len(ps) != 0 {
			t.Fatal("programs must go with the channel")
		}
	})

	t.Run("stats", func(t *testing.T) {
		base := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
		for i := 0; i < 10; i++ {
			at := base.Add(time.Duration(i) * time.Minute)
			vals := map[string]float64{"viewers.total": float64(i), "channel:a:viewers": 2, "worker:w1:cpu": 50}
			if i%2 == 0 {
				vals["channel:b:viewers"] = 10
			}
			if err := s.InsertStats(ctx, at.Add(17*time.Second), vals); err != nil {
				t.Fatal(err)
			}
		}
		// Same minute twice: the last value wins.
		_ = s.InsertStats(ctx, base.Add(9*time.Minute), map[string]float64{"viewers.total": 9})
		got, err := s.ListStats(ctx, []string{"viewers.total"}, "worker:", base, base.Add(10*time.Minute), 5*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		v := got["viewers.total"]
		if len(got) != 2 || len(v) != 2 || v[0].Value != 2 || v[1].Value != 7 || !v[1].At.Equal(base.Add(5*time.Minute)) || len(got["worker:w1:cpu"]) != 2 {
			t.Fatalf("list %+v", got)
		}
		top, err := s.TopStats(ctx, "channel:", base, base.Add(time.Hour), 10)
		if err != nil || len(top) != 2 || top[0].Key != "channel:b:viewers" || top[0].Sum != 50 || top[0].Max != 10 || top[1].Count != 10 {
			t.Fatalf("top %v %+v", err, top)
		}
		_ = s.PruneStats(ctx, base.Add(5*time.Minute))
		got, _ = s.ListStats(ctx, []string{"viewers.total"}, "", base, base.Add(time.Hour), time.Minute)
		if len(got["viewers.total"]) != 5 {
			t.Fatalf("prune: %d points left", len(got["viewers.total"]))
		}
	})

	t.Run("play-sessions", func(t *testing.T) {
		base := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
		var ss []model.PlaySession
		for i := 0; i < 5; i++ {
			ss = append(ss, model.PlaySession{ID: fmt.Sprintf("s%d", i), ChannelID: "c1", WorkerID: "w1", Proto: "hls",
				IP: "10.0.0.1", UserID: map[bool]string{true: "u1", false: ""}[i%2 == 0], StartedAt: base,
				LastSeen: base.Add(time.Duration(i) * time.Minute), ClosedAt: base.Add(time.Duration(i) * time.Minute), Bytes: int64(i), Reason: "idle"})
		}
		ss = append(ss, model.PlaySession{ID: "x", ChannelID: "c2", WorkerID: "w1", Proto: "mpegts", IP: "10.0.0.2",
			StartedAt: base, LastSeen: base, ClosedAt: base.Add(10 * time.Minute)})
		if err := s.InsertPlaySessions(ctx, ss); err != nil {
			t.Fatal(err)
		}
		_ = s.InsertPlaySessions(ctx, ss[:1]) // again: no-op
		all, err := s.ListPlaySessions(ctx, store.SessionFilter{Limit: 100})
		if err != nil || len(all) != 6 || all[0].ID != "x" {
			t.Fatalf("all %v %+v", err, all)
		}
		c1, _ := s.ListPlaySessions(ctx, store.SessionFilter{ChannelID: "c1", Limit: 2})
		if len(c1) != 2 || c1[0].ID != "s4" || c1[1].ID != "s3" {
			t.Fatalf("page 1 %+v", c1)
		}
		cur := strconv.FormatInt(c1[1].ClosedAt.UnixNano(), 10) + "|" + c1[1].ID
		next, _ := s.ListPlaySessions(ctx, store.SessionFilter{ChannelID: "c1", Before: cur, Limit: 10})
		if len(next) != 3 || next[0].ID != "s2" {
			t.Fatalf("page 2 %+v", next)
		}
		u1, _ := s.ListPlaySessions(ctx, store.SessionFilter{UserID: "u1"})
		win, _ := s.ListPlaySessions(ctx, store.SessionFilter{From: base.Add(2 * time.Minute), To: base.Add(4 * time.Minute)})
		if len(u1) != 3 || len(win) != 2 {
			t.Fatalf("user %d window %d", len(u1), len(win))
		}
		_ = s.PrunePlaySessions(ctx, base.Add(3*time.Minute))
		left, _ := s.ListPlaySessions(ctx, store.SessionFilter{})
		if len(left) != 3 {
			t.Fatalf("prune: %d left", len(left))
		}
	})

	t.Run("leases", func(t *testing.T) {
		ok, err := s.AcquireLease(ctx, "controller", "a", time.Second)
		if err != nil || !ok {
			t.Fatalf("first acquire: %v %v", ok, err)
		}
		if ok, _ := s.AcquireLease(ctx, "controller", "b", time.Second); ok {
			t.Fatal("b took a lease held by a")
		}
		if ok, _ := s.AcquireLease(ctx, "controller", "a", 300*time.Millisecond); !ok {
			t.Fatal("a could not renew")
		}
		time.Sleep(400 * time.Millisecond)
		if ok, _ := s.AcquireLease(ctx, "controller", "b", time.Second); !ok {
			t.Fatal("b could not take an expired lease")
		}
		if ok, _ := s.AcquireLease(ctx, "controller", "a", time.Second); ok {
			t.Fatal("a took the lease back while b holds it")
		}
		_ = s.ReleaseLease(ctx, "controller", "a") // not the holder: no effect
		if ok, _ := s.AcquireLease(ctx, "controller", "a", time.Second); ok {
			t.Fatal("release by a non-holder freed the lease")
		}
		_ = s.ReleaseLease(ctx, "controller", "b")
		if ok, _ := s.AcquireLease(ctx, "controller", "a", time.Second); !ok {
			t.Fatal("lease not freed by its holder")
		}
	})

	t.Run("lockout-and-refresh-tokens", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Millisecond)
		u := model.User{ID: "sec1", Username: "seguro1", Role: model.RoleViewer, PasswordHash: "h", CreatedAt: now, UpdatedAt: now}
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetUser(ctx, "sec1")
		if got.FailedLogins != 0 || !got.LockedUntil.IsZero() {
			t.Fatalf("fresh user has security state: %+v", got)
		}
		got.FailedLogins = 3
		got.LockedUntil = now.Add(5 * time.Minute)
		if err := s.UpdateUser(ctx, got); err != nil {
			t.Fatal(err)
		}
		back, _ := s.GetUser(ctx, "sec1")
		if back.FailedLogins != 3 || !back.LockedUntil.Equal(now.Add(5*time.Minute)) {
			t.Fatalf("security state not stored: %+v", back)
		}
		back.LockedUntil, back.FailedLogins = time.Time{}, 0
		_ = s.UpdateUser(ctx, back)
		if again, _ := s.GetUser(ctx, "sec1"); !again.LockedUntil.IsZero() || again.FailedLogins != 0 {
			t.Fatalf("security state not cleared: %+v", again)
		}

		// A refresh token works once; the second use is reported as a reuse.
		rt := model.RefreshToken{ID: "rt-1", UserID: "sec1", FamilyID: "fam-1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := s.CreateRefreshToken(ctx, rt); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateRefreshToken(ctx, rt); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("duplicate refresh token id: %v", err)
		}
		if _, _, err := s.UseRefreshToken(ctx, "unknown", now); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown token: %v", err)
		}
		first, reused, err := s.UseRefreshToken(ctx, "rt-1", now)
		if err != nil || reused || first.FamilyID != "fam-1" || first.UserID != "sec1" || !first.ExpiresAt.Equal(now.Add(time.Hour)) {
			t.Fatalf("first use: %+v reused=%v err=%v", first, reused, err)
		}
		if _, reused, err := s.UseRefreshToken(ctx, "rt-1", now); err != nil || !reused {
			t.Fatalf("second use must be a reuse: reused=%v err=%v", reused, err)
		}
		// Revoking the family removes every token of it, and only it.
		_ = s.CreateRefreshToken(ctx, model.RefreshToken{ID: "rt-2", UserID: "sec1", FamilyID: "fam-1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
		_ = s.CreateRefreshToken(ctx, model.RefreshToken{ID: "rt-3", UserID: "sec1", FamilyID: "fam-2", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
		if err := s.RevokeRefreshFamily(ctx, "fam-1"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.UseRefreshToken(ctx, "rt-2", now); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("revoked family still usable: %v", err)
		}
		if _, reused, err := s.UseRefreshToken(ctx, "rt-3", now); err != nil || reused {
			t.Fatalf("other family affected: %v %v", reused, err)
		}
		// Expired tokens are pruned; all tokens of a user can be revoked.
		_ = s.CreateRefreshToken(ctx, model.RefreshToken{ID: "rt-old", UserID: "sec1", FamilyID: "f3", CreatedAt: now.Add(-48 * time.Hour), ExpiresAt: now.Add(-24 * time.Hour)})
		_ = s.CreateRefreshToken(ctx, model.RefreshToken{ID: "rt-new", UserID: "sec1", FamilyID: "f4", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
		if err := s.PruneRefreshTokens(ctx, now); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.UseRefreshToken(ctx, "rt-old", now); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expired token survived the prune: %v", err)
		}
		if err := s.RevokeUserRefreshTokens(ctx, "sec1"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.UseRefreshToken(ctx, "rt-new", now); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("user tokens survived the revoke: %v", err)
		}
	})

	t.Run("users-tokens-audit", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Millisecond)
		u := model.User{ID: "u1", Username: "operador1", Role: model.RoleOperator, PasswordHash: "h", CreatedAt: now, UpdatedAt: now}
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		dup := u
		dup.ID = "u2"
		if err := s.CreateUser(ctx, dup); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("dup username %v", err)
		}
		got, err := s.GetUserByUsername(ctx, "operador1")
		if err != nil || got.PasswordHash != "h" || got.Role != model.RoleOperator {
			t.Fatalf("%+v %v", got, err)
		}
		if !got.TokensValidAfter.IsZero() {
			t.Fatalf("tokens_valid_after should start empty, got %v", got.TokensValidAfter)
		}
		got.Disabled = true
		got.TokensValidAfter = now.Add(time.Minute)
		_ = s.UpdateUser(ctx, got)
		got, _ = s.GetUser(ctx, "u1")
		if !got.Disabled {
			t.Fatal("not disabled")
		}
		if !got.TokensValidAfter.Equal(now.Add(time.Minute)) {
			t.Fatalf("tokens_valid_after not stored: %v", got.TokensValidAfter)
		}
		if n, _ := s.CountUsers(ctx); n != 2 { // this user and "seguro1" of the previous check
			t.Fatalf("count %d", n)
		}
		tok := model.APIToken{ID: "t1", Name: "middleware", Prefix: "nxs_abcd", Hash: "sha", Scopes: []string{"channels:read"}, CreatedBy: "admin", CreatedAt: now}
		if err := s.CreateAPIToken(ctx, tok); err != nil {
			t.Fatal(err)
		}
		gt, err := s.GetAPITokenByHash(ctx, "sha")
		if err != nil || gt.ID != "t1" || len(gt.Scopes) != 1 || !gt.Active(now) {
			t.Fatalf("%+v %v", gt, err)
		}
		_ = s.TouchAPIToken(ctx, "t1", now)
		_ = s.RevokeAPIToken(ctx, "t1", now)
		gt, _ = s.GetAPITokenByHash(ctx, "sha")
		if gt.Active(now) || gt.LastUsedAt == nil {
			t.Fatalf("%+v", gt)
		}
		for i := 0; i < 3; i++ {
			if err := s.InsertAudit(ctx, model.AuditEntry{At: now, Actor: "operador1", ActorType: "user", Action: "channel.start", Target: "canal-001",
				SourceIP: "10.0.0.1", RequestID: "r", Details: map[string]any{"i": float64(i)}}); err != nil {
				t.Fatal(err)
			}
		}
		au, _ := s.ListAudit(ctx, store.AuditFilter{Actor: "operador1", Limit: 2})
		if len(au) != 2 || au[0].Details["i"] != float64(2) {
			t.Fatalf("%+v", au)
		}
		rest, _ := s.ListAudit(ctx, store.AuditFilter{Before: au[1].ID})
		if len(rest) != 1 {
			t.Fatalf("rest %d", len(rest))
		}
	})
}

func ids(cs []model.Channel) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}
