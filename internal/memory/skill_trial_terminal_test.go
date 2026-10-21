package memory

import (
	"context"
	"testing"
	"time"
)

func TestSkillTrialTerminalDistinguishesPromotionAndExpiration(t *testing.T) {
	for _, promote := range []bool{false, true} {
		f, candidate, trial, in := skillAutomationFixture(t)
		want := "expired"
		if promote {
			if _, err := f.svc.ApplySkill(testCtx, testPrincipalToken("learner"), candidate.SkillID, in, false); err != nil {
				t.Fatal(err)
			}
			want = "promoted"
		} else {
			trial.SkillTrial.EndsAt = time.Now().Add(-time.Second)
		}
		if err := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
			state, err := skillTrialTerminal(ctx, tx, trial)
			if err != nil || state != want {
				t.Fatalf("terminal=%q error=%v want=%q", state, err, want)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
