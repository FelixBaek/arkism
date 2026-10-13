package memory

import (
	"errors"
	"testing"
)

func TestCodeReviewsReportSettingsAndRerun(t *testing.T) {
	f := newFixture(t)
	empty, err := f.svc.CodeReviews(testCtx, f.human, scopeA)
	if err != nil || empty.CodeReviews.Settings.Provider != "codex" {
		t.Fatal("empty", empty, err)
	}
	item := ReviewItem{Repo: "FelixBaek/arkism", Number: 36, Title: "desktop 0.5.0", Branch: "feat/x", Base: "main", HeadSHA: "40e24fa", URL: "https://github.com/FelixBaek/arkism/pull/36",
		State: "done", Findings: []ReviewFinding{{Severity: "advisory", Path: "desktop/ui/pages.js", Line: 31, Text: "규칙이 경로 대신 ID를 봐요"}}}
	r, err := f.svc.ReportCodeReviews(testCtx, f.human, CodeReviewsReport{Scope: scopeA, Repos: []ReviewRepo{{Name: "FelixBaek/arkism", PRs: 36}}, Items: []ReviewItem{item}})
	if err != nil || len(r.CodeReviews.Items) != 1 {
		t.Fatal("report", err)
	}
	bad := item
	bad.URL = "https://evil.example/pull/1"
	if _, err = f.svc.ReportCodeReviews(testCtx, f.human, CodeReviewsReport{Scope: scopeA, Items: []ReviewItem{bad}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("a non-GitHub link accepted", err)
	}
	if _, err = f.svc.SetCodeReviewSettings(testCtx, f.agent, CodeReviewSettingsInput{Scope: scopeA, Settings: ReviewSettings{Provider: "claude", OnBlocking: "block"}}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent changed the review settings", err)
	}
	if r, err = f.svc.SetCodeReviewSettings(testCtx, f.human, CodeReviewSettingsInput{Scope: scopeA, Settings: ReviewSettings{Provider: "claude", OnBlocking: "block", ReposOff: []string{"demo-org/demo-repo"}}}); err != nil || r.CodeReviews.Settings.OnBlocking != "block" || len(r.CodeReviews.Items) != 1 {
		t.Fatal("settings", err)
	}
	r, err = f.svc.RerunCodeReview(testCtx, f.human, CodeReviewRerunInput{Scope: scopeA, Repo: "FelixBaek/arkism", Number: 36})
	if err != nil || len(r.CodeReviews.Reruns) != 1 || r.CodeReviews.Reruns[0].Status != "pending" {
		t.Fatal("rerun", err)
	}
	id := r.CodeReviews.Reruns[0].ID
	r, _ = f.svc.ReportCodeReviews(testCtx, f.human, CodeReviewsReport{Scope: scopeA, Items: []ReviewItem{item}, Results: []AgentToolResult{{ID: id, Status: "done"}}})
	if r.CodeReviews.Reruns[0].Status != "done" || r.CodeReviews.Settings.Provider != "claude" {
		t.Fatal("rerun settled, settings kept", r.CodeReviews)
	}
}
