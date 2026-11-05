package memory

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

type panicHooks struct{}

func (panicHooks) ValidateCapture(context.Context, Decision, CaptureInput) error { panic("boom") }
func (panicHooks) BeforeJob(context.Context, Record) error                       { panic("boom") }

// A panicking validator or job hook refuses (fails closed) instead of
// crashing the request or the worker; erasure never asks the job hook.
func TestRefusingHooksContainPanics(t *testing.T) {
	f := newFixture(t)
	f.svc.Extensions = Extensions{CaptureValidator: panicHooks{}, JobHook: panicHooks{}}
	if _, err := f.svc.Capture(testCtx, f.human, captureInput(scopeA, "evt-p", explicit("앞으로 답은 짧게 한다."))); !errors.Is(err, ErrExtensionRefused) {
		t.Fatal("panicking validator", err)
	}
	if err := f.svc.Extensions.beforeJob(testCtx, Record{Kind: "embedding"}); !errors.Is(err, ErrExtensionRefused) {
		t.Fatal("panicking job hook", err)
	}
}

type requestHookFunc func(*http.Request) error

func (f requestHookFunc) BeforeRequest(r *http.Request) error { return f(r) }

func TestExtensionRequestHookFailsClosed(t *testing.T) {
	f := newFixture(t)
	seenBody := false
	f.svc.Extensions.RequestHook = requestHookFunc(func(r *http.Request) error {
		if b, _ := io.ReadAll(r.Body); len(b) > 0 || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			seenBody = true
		}
		switch r.Header.Get("X-Test") {
		case "refuse":
			return errors.New("house rule")
		case "panic":
			panic("boom")
		}
		return nil
	})
	h := handlerMode(f.svc, time.Minute, true)
	for mode, want := range map[string]int{"": http.StatusOK, "refuse": http.StatusForbidden, "panic": http.StatusForbidden} {
		req := httptest.NewRequest(http.MethodGet, "/healthz", strings.NewReader("secret body"))
		req.Header.Set("X-Test", mode)
		req.Header.Set("Authorization", "Bearer "+f.human)
		req.Header.Set("Cookie", "session=s")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%q: %d %s", mode, rec.Code, rec.Body)
		}
	}
	if seenBody {
		t.Fatal("the hook could read the request body or credentials")
	}
}
