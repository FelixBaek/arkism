package main

import (
	"testing"

	"github.com/FelixBaek/arkism/internal/memory"
)

// The skill judge and the model judge can be chosen apart (설정 › 역할별 모델).
func TestConfigureDaemonRoutingSplitsJudges(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "k")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "a")
	t.Setenv("CLOUDFLARE_AI_TOKEN", "t")
	for _, tc := range []struct{ skill, model, wantSkill, wantModel string }{
		{"", "", "fallback", "clef"},
		{"jev", "fixed", "jev", ""},
		{"fixed", "jev", "", "jev"},
		{"clef", "clef", "fallback", "clef"},
	} {
		t.Setenv("ARKISM_SKILL_JUDGE", tc.skill)
		t.Setenv("ARKISM_MODEL_JUDGE", tc.model)
		t.Setenv("ARKISM_SKILL_CANDIDATES", "5")
		svc := &memory.Service{}
		configureDaemonRouting(svc)
		kind := func(v any) string {
			switch j := v.(type) {
			case nil:
				return ""
			case *memory.FallbackJudge:
				return "fallback"
			case *memory.JevJudge:
				if j.Endpoint != "" {
					return "clef"
				}
				return "jev"
			}
			return "other"
		}
		if got := kind(svc.Router); got != tc.wantSkill {
			t.Errorf("%q/%q: skill judge %s, want %s", tc.skill, tc.model, got, tc.wantSkill)
		}
		if got := kind(svc.ModelPicker); got != tc.wantModel {
			t.Errorf("%q/%q: model judge %s, want %s", tc.skill, tc.model, got, tc.wantModel)
		}
		if svc.SkillCandidates != 5 {
			t.Errorf("candidates %d", svc.SkillCandidates)
		}
	}
}
