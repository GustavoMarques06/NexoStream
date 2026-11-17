package scheduler

import (
	"strings"
	"testing"

	"nexostream/internal/model"
)

func TestRolesAndLadderCost(t *testing.T) {
	online := func(id string, roles ...string) model.Worker {
		return model.Worker{ID: id, State: model.WorkerOnline,
			Capabilities: model.Capabilities{Encoders: []string{"cpu"}, Roles: roles}}
	}
	hd := model.TranscodeProfile{ID: "hd", Encoder: model.EncoderCPU, Video: model.VideoProfile{Width: 1280, Height: 720}}
	sd := model.TranscodeProfile{ID: "sd", Encoder: model.EncoderCPU, Video: model.VideoProfile{Width: 640, Height: 360}}
	profiles := map[string]model.TranscodeProfile{"hd": hd, "sd": sd}
	ws := []model.Worker{online("ingest", "passthrough"), online("enc", "transcode"), online("rec", "passthrough", "dvr"), online("all")}

	pass := model.Channel{ID: "p", Mode: model.ModeAuto}
	abr := model.Channel{ID: "a", Mode: model.ModeTranscode, Profiles: []string{"hd", "sd"}}
	dvrPass := model.Channel{ID: "d", Mode: model.ModePassthrough, DVR: model.DVRConfig{Enabled: true}}
	dvrABR := model.Channel{ID: "x", Mode: model.ModeTranscode, Profiles: []string{"hd"}, DVR: model.DVRConfig{Enabled: true}}

	ok := func(ch model.Channel) []string {
		var out []string
		for _, w := range ws {
			if eligible(ch, profiles, w) == nil {
				out = append(out, w.ID)
			}
		}
		return out
	}
	for ch, want := range map[*model.Channel]string{
		&pass: "ingest,rec,all", &abr: "enc,all", &dvrPass: "rec,all", &dvrABR: "all",
	} {
		if got := strings.Join(ok(*ch), ","); got != want {
			t.Errorf("%s: eligible %s, want %s", ch.ID, got, want)
		}
	}

	c := NewCluster(ws, nil, DefaultOptions())
	one := c.Cost(model.Channel{Mode: model.ModeTranscode, Profiles: []string{"hd"}}, profiles, ws[3])
	ladder := c.Cost(abr, profiles, ws[3])
	if ladder <= one || ladder != one+c.Cost(model.Channel{Mode: model.ModeTranscode, Profiles: []string{"sd"}}, profiles, ws[3]) {
		t.Fatalf("an ABR ladder costs every quality: %v vs %v", ladder, one)
	}
	if id, err := c.Pick(abr, profiles, ""); err != nil || (id != "enc" && id != "all") {
		t.Fatalf("pick %s %v", id, err)
	}
}
