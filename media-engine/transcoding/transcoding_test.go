package transcoding

import (
	"testing"

	"nexostream/internal/model"
)

func TestValidateProfile(t *testing.T) {
	for _, p := range model.DefaultProfiles() {
		if err := ValidateProfile(p); err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
	}
	bad := model.DefaultProfiles()[0]
	bad.Video.Width = 1921
	if ValidateProfile(bad) == nil {
		t.Fatal("odd width accepted")
	}
	bad = model.DefaultProfiles()[0]
	bad.Video.Codec = "hevc"
	if ValidateProfile(bad) == nil {
		t.Fatal("hevc output accepted in v1")
	}
}

func TestResolveEncoder(t *testing.T) {
	if e, _ := ResolveEncoder(model.EncoderAuto, []string{"cpu", "nvenc"}); e != model.EncoderNVENC {
		t.Fatalf("auto=%s", e)
	}
	if e, _ := ResolveEncoder(model.EncoderAuto, nil); e != model.EncoderCPU {
		t.Fatalf("auto fallback=%s", e)
	}
	if _, err := ResolveEncoder(model.EncoderQSV, []string{"cpu"}); err == nil {
		t.Fatal("missing qsv accepted")
	}
}
