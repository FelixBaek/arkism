package memory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host/desktopcontrol"
)

type requestWorkRepo struct {
	Repository
	entered     chan struct{}
	release     chan struct{}
	finished    chan struct{}
	ready       atomic.Int32
	publication atomic.Int32
}

func (r *requestWorkRepo) SearchReady(context.Context) error {
	r.ready.Add(1)
	close(r.entered)
	defer close(r.finished)
	<-r.release
	return ErrUnavailable
}
func (r *requestWorkRepo) RequireRestoreReview(context.Context, string) error { return nil }
func (r *requestWorkRepo) CheckRestorePublication(context.Context) error {
	r.publication.Add(1)
	return nil
}

func TestDesktopControlHTTPTimeoutRetainsActualMuxWork(t *testing.T) {
	repo := &requestWorkRepo{entered: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(repo.release) }) })
	gate := &desktopcontrol.Gate{}
	h := WithRequestWork(handler(&Service{Repo: repo}, 20*time.Millisecond), gate.Middleware)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(response, httptest.NewRequest("GET", "/readyz", nil)) }()
	<-repo.entered
	<-done
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected timeout response, got %d", response.Code)
	}
	select {
	case <-repo.finished:
		t.Fatal("fixture work ended with response")
	default:
	}
	if idle, err := gate.Pause(1); err != nil || idle {
		t.Fatal("timeout response claimed idle before actual mux returned")
	}
	// Pause did not cancel the operation; it reopens without altering its context/work.
	release.Do(func() { close(repo.release) })
	<-repo.finished
	for deadline := time.Now().Add(time.Second); ; {
		idle, err := gate.Pause(2)
		if err != nil {
			t.Fatal(err)
		}
		if idle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("returned mux retained work forever")
		}
		time.Sleep(time.Millisecond)
	}
}

type unusedWorkAnalyzer struct{ calls atomic.Int32 }

func (a *unusedWorkAnalyzer) Generator() Generator { a.calls.Add(1); return Generator{} }
func (a *unusedWorkAnalyzer) Analyze(context.Context, string) (SkillAnalysis, error) {
	a.calls.Add(1)
	return SkillAnalysis{}, ErrUnavailable
}

func TestDesktopControlHTTPPauseWinnerHasNoPublicationRepoOrModelEffects(t *testing.T) {
	repo := &requestWorkRepo{}
	model := &unusedWorkAnalyzer{}
	gate := &desktopcontrol.Gate{}
	if idle, err := gate.Pause(1); err != nil || !idle {
		t.Fatal("pause failed")
	}
	h := WithRequestWork(Handler(&Service{Repo: repo, Analyzer: model}), gate.Middleware)
	for _, input := range []struct{ method, path, body string }{
		{"GET", "/readyz", ""},
		{"POST", "/v1/skills/analyze", `{"scope":{"workspace_id":"default","project_id":"default","user_id":"owner"},"skill_id":"fixture","path":"SKILL.md","content":"inert skill"}`},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(input.method, input.path, strings.NewReader(input.body)))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatal("paused request reached API")
		}
	}
	if repo.ready.Load() != 0 || repo.publication.Load() != 0 || model.calls.Load() != 0 {
		t.Fatal("paused API performed work")
	}
	if gate.Resume(1) != nil {
		t.Fatal("resume failed")
	}
}

func TestDesktopControlHTTPNilHookPreservesOrdinaryAndRecoveryHandlers(t *testing.T) {
	for _, h := range []http.Handler{Handler(&Service{}), RecoveryHandler(&Service{})} {
		w := httptest.NewRecorder()
		WithRequestWork(h, nil).ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
		if w.Code != http.StatusOK {
			t.Fatal("nil hook changed normal behavior")
		}
	}
}
