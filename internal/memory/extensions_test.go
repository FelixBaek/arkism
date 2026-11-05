package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type testExtension struct {
	refuseCapture, refuseJob bool
	approved                 []string
	jobs                     []string
}

func (x *testExtension) ValidateCapture(_ context.Context, _ Decision, in CaptureInput) error {
	if x.refuseCapture && strings.Contains(in.Claims[0].Text, "forbidden") {
		return errors.New("house rule")
	}
	return nil
}

func (x *testExtension) BeforeJob(_ context.Context, job Record) error {
	x.jobs = append(x.jobs, job.Kind)
	if x.refuseJob {
		return errors.New("not now")
	}
	return nil
}

func (x *testExtension) AfterApprove(_ context.Context, r Record) {
	x.approved = append(x.approved, r.ID)
}

func TestExtensionHooks(t *testing.T) {
	f := newFixture(t)
	x := &testExtension{refuseCapture: true, refuseJob: true}
	f.svc.Extensions = Extensions{CaptureValidator: x, JobHook: x, ApproveHook: x}

	if _, err := f.svc.Capture(testCtx, f.human, captureInput(scopeA, "evt-x", explicit("앞으로 forbidden 단어를 쓴다."))); !errors.Is(err, ErrInvalid) || !errors.Is(err, ErrExtensionRefused) {
		t.Fatal("validator did not refuse", err)
	}
	if items := f.capture(f.human, captureInput(scopeA, "evt-y", explicit("앞으로 답은 짧게 한다."))); items[0].Status != "candidate" {
		t.Fatal("allowed capture must stay a candidate", items)
	}

	mem := f.activeMemory("prefer short answers")
	if len(x.approved) != 1 || x.approved[0] != mem.ID {
		t.Fatal("approve hook", x.approved, mem.ID)
	}
	calls := len(f.emb.calls)
	if err := f.svc.WorkOnce(testCtx, f.work); !errors.Is(err, ErrExtensionRefused) || len(f.emb.calls) != calls {
		t.Fatal("job hook did not hold the job", err)
	}
	x.refuseJob = false
	if err := f.svc.WorkOnce(testCtx, f.work); err != nil {
		t.Fatal("job after hook allowed", err)
	}
	if len(x.jobs) < 2 || x.jobs[0] != "embedding" {
		t.Fatal("job kinds seen", x.jobs)
	}
}

type panicApprove struct{}

func (panicApprove) AfterApprove(context.Context, Record) { panic("boom") }

func TestApproveHookPanicDoesNotUndoApproval(t *testing.T) {
	f := newFixture(t)
	f.svc.Extensions.ApproveHook = panicApprove{}
	if mem := f.activeMemory("keep it"); mem.Status != "active" {
		t.Fatal("approval lost", mem.Status)
	}
}
