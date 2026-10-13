package memory

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func importInput(dry bool, sessions ...ImportedSession) SessionImportInput {
	return SessionImportInput{Scope: scopeA, DryRun: dry, Sessions: sessions,
		Generator: &Generator{Model: "test-extractor", PromptVersion: "import-v1", PipelineVersion: "import-sessions-v1"}}
}

func importSession(id string, candidates ...ImportCandidate) ImportedSession {
	start, end := time.Now().UTC().Add(-48*time.Hour), time.Now().UTC().Add(-47*time.Hour)
	return ImportedSession{ID: id, Origin: "claude_code", StartedAt: &start, EndedAt: &end, Messages: 12, Candidates: candidates}
}

const sessA = "is_0123456789abcdef01234567"

func lessonCandidate(text string) ImportCandidate {
	return ImportCandidate{Kind: "lesson", Subject: "deploy", Text: text, Message: 3, Quote: "그거 운영에 바로 올리면 안 돼", Reason: "user objected"}
}

func candidateRows(f *fixture) []Record {
	var out []Record
	for _, r := range f.repo.store["memory_candidates"] {
		out = append(out, r)
	}
	return out
}

func TestImportSessionsDryRunWritesNothing(t *testing.T) {
	f := newFixture(t)
	in := importInput(true, importSession(sessA, lessonCandidate("배포 전에는 스테이징에서 먼저 확인한다 (운영에 바로 올려 사고)."),
		ImportCandidate{Kind: "skill", Subject: "release", Text: "릴리스할 때는 태그, 빌드, 피드 순서로 한다.", Message: 5, Quote: "태그 먼저 찍고"},
		ImportCandidate{Kind: "decision", Text: "키는 sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA 로 한다", Message: 1, Quote: "키는"},
		ImportCandidate{Kind: "decision", Text: "인용 없는 후보", Message: 2, Quote: "  "}))
	out, err := f.svc.ImportSessions(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	items := out.Sessions[0].Items
	if out.Sessions[0].Status != "would_import" || items[0].Status != "candidate" || items[1].Status != "candidate" ||
		items[2].Reason != "secret_detected" || items[3].Reason != "invalid_claim" {
		t.Fatalf("dry run verdicts: %+v", out)
	}
	if out.Counts["candidate"] != 2 || out.Counts["secret_detected"] != 1 {
		t.Fatal("counts", out.Counts)
	}
	if len(candidateRows(f)) != 0 || len(f.repo.store["sources"]) != 0 || len(f.repo.store["jobs"]) != 0 {
		t.Fatal("dry run stored records")
	}
}

func TestImportSessionsRunStoresCandidatesOnceNeverActive(t *testing.T) {
	f := newFixture(t)
	in := importInput(false, importSession(sessA, lessonCandidate("배포 전에는 스테이징에서 먼저 확인한다 (운영에 바로 올려 사고)."),
		ImportCandidate{Kind: "skill", Subject: "release notes", Text: "릴리스할 때는 태그, 빌드, 피드 순서로 한다.", Message: 5, Quote: "태그 먼저 찍고"},
		ImportCandidate{Kind: "decision", Text: "앞으로 기본 브랜치는 main으로 둔다.", Message: 7, Quote: "main으로"}))
	out, err := f.svc.ImportSessions(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Sessions[0].Status != "imported" || out.Counts["candidate"] != 3 {
		t.Fatalf("run: %+v", out)
	}
	rows := candidateRows(f)
	if len(rows) != 3 || len(activeMemories(f, scopeA)) != 0 {
		t.Fatal("expected 3 candidates and no active memory, even for a human token", len(rows))
	}
	kinds := map[string]bool{}
	for _, r := range rows {
		kinds[r.Kind] = true
		if r.Status != "candidate" || !strings.Contains(r.Summary, "#") || !hasTag(r.Tags, "session:"+sessA) || !hasTag(r.Tags, "event:backfill") {
			t.Fatalf("candidate lacks review status or citation: %+v", r)
		}
	}
	if !kinds["lesson"] || !kinds["procedural"] || !kinds["decision"] {
		t.Fatal("kinds", kinds)
	}
	// Original time is kept on the source.
	for _, src := range f.repo.store["sources"] {
		if src.CollectedAt == nil || time.Since(*src.CollectedAt) < 46*time.Hour {
			t.Fatal("source lost the session's original time", src.CollectedAt)
		}
	}
	// A re-run, even worded differently, stores nothing new.
	in.Sessions[0].Candidates[0].Text = "스테이징 확인 후 배포한다."
	again, err := f.svc.ImportSessions(testCtx, f.human, in)
	if err != nil || again.Sessions[0].Status != "already_imported" || len(candidateRows(f)) != 3 {
		t.Fatalf("re-run duplicated: %+v %v", again, err)
	}
	dry, _ := f.svc.ImportSessions(testCtx, f.human, importInput(true, in.Sessions[0]))
	if dry.Sessions[0].Status != "already_imported" {
		t.Fatal("dry run does not see the earlier import", dry.Sessions[0])
	}
	// The same claim from another session merges on the claim key.
	other := importSession("is_ffffffffffffffffffffffff", ImportCandidate{Kind: "decision", Text: "앞으로 기본 브랜치는 main으로 둔다.", Message: 1, Quote: "main"})
	preview, _ := f.svc.ImportSessions(testCtx, f.human, importInput(true, other))
	if preview.Sessions[0].Items[0].Status != "merged" {
		t.Fatal("dry run misses the duplicate claim", preview.Sessions[0].Items)
	}
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func TestImportSessionsValidates(t *testing.T) {
	f := newFixture(t)
	future := time.Now().Add(time.Hour)
	bad := []SessionImportInput{
		importInput(false),
		importInput(false, importSession("claude-session-uuid")),
		importInput(false, importSession(sessA), importSession(sessA)),
		func() SessionImportInput {
			s := importSession(sessA)
			s.EndedAt = &future
			return importInput(false, s)
		}(),
		func() SessionImportInput { s := importSession(sessA); s.EndedAt = nil; return importInput(false, s) }(),
		func() SessionImportInput {
			s := importSession(sessA)
			for i := 0; i <= importMaxCandidates; i++ {
				s.Candidates = append(s.Candidates, lessonCandidate("x"))
			}
			return importInput(false, s)
		}(),
	}
	for i, in := range bad {
		if _, err := f.svc.ImportSessions(testCtx, f.human, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d: want invalid, got %v", i, err)
		}
	}
	if _, err := f.svc.ImportSessions(testCtx, "tok-unknown", importInput(true, importSession(sessA))); err == nil {
		t.Fatal("unknown token allowed")
	}
}

func TestImportSessionsHTTPRoute(t *testing.T) {
	f := newFixture(t)
	body, _ := json.Marshal(importInput(true, importSession(sessA, lessonCandidate("배포 전에는 스테이징에서 먼저 확인한다."))))
	req := httptest.NewRequest("POST", "/v1/import/sessions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+f.human)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"would_import"`) {
		t.Fatalf("route: %d %s", w.Code, w.Body.String())
	}
}
