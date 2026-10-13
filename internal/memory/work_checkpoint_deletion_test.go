package memory

import (
	"context"
	"errors"
	"testing"
)

type workDeletionPolicy struct {
	Authorizer
	sawID       bool
	changeActor bool
}

func (p *workDeletionPolicy) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	d, err := p.Authorizer.Authorize(ctx, token, in)
	if in.Action == "delete:work_checkpoints" && in.ResourceID == "protected-work" {
		p.sawID = true
		if p.changeActor {
			d.PrincipalID = "another-human"
		} else {
			d.Allowed = false
		}
	}
	return d, err
}
func TestWorkCheckpointDeletionChecksEachTarget(t *testing.T) {
	for _, changeActor := range []bool{false, true} {
		f := newFixture(t)
		policy := &workDeletionPolicy{Authorizer: f.auth, changeActor: changeActor}
		f.svc.Auth = policy
		_, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "target-policy", TargetType: "work_checkpoint", TargetIDs: []string{"protected-work"}, Reason: "owner_request"})
		if !policy.sawID || !errors.Is(err, ErrDenied) {
			t.Fatalf("target authorization bypassed: visited=%v err=%v", policy.sawID, err)
		}
	}
}
