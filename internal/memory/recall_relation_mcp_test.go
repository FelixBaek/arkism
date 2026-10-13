package memory

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostgresRecallRelationMCP(t *testing.T) {
	p := isolatedPostgres(t)
	f, now := archiveSearchFixture(t, p)
	old := f.activeMemory("decision rule")
	newer := f.activeMemory("newer choice")
	newer.Tags = []string{"session:search-session"}
	if err := p.Replace(testCtx, colMemories, newer, newer.Revision); err != nil {
		t.Fatal(err)
	}
	independent := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "unrelated source"}))
	candidate := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "decision rule unrelated", Sources: refs(independent)}))
	approved := f.transition(f.human, "memory_candidates", candidate.ID, ChangeInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Status: "approved"})
	f.transition(f.human, "memories", approved.ID, ChangeInput{Scope: scopeA, ExpectedRevision: approved.Revision, Status: "active"})
	result, err := f.svc.Supersede(testCtx, f.human, old.ID, SupersedeInput{Scope: scopeA, ExpectedRevision: old.Revision, ReplacementMemoryID: newer.ID, ReplacementExpectedRevision: newer.Revision, Reason: "new evidence"})
	if err != nil {
		t.Fatal(err)
	}
	source, err := p.Get(testCtx, "sources", scopeA, newer.Sources[0].SourceID)
	if err != nil {
		t.Fatal(err)
	}
	evaluation := f.create(f.human2, "evaluations", CreateInput{Scope: scopeA, Text: "supporting evaluation", Sources: refs(source), Target: &Ref{Collection: colMemories, ID: result.New.ID, Revision: result.New.Revision}, Evaluation: &Evaluation{EvalType: "user_rating"}})
	id := ContentHash("relation-no-use")
	header := archiveSearchUpload(t, f, *now, id, jsonLine(map[string]string{"role": "user", "content": "original"}), "jsonl")
	f.auth.actors["denied"] = actor("denied-agent", "agent", true, allVerbs...)
	originalAuth := f.svc.Auth
	f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, in Authorization) (Decision, error) {
		if token == "denied" && in.ResourceID == result.Old.ID {
			return Decision{}, ErrDenied
		}
		return originalAuth.Authorize(ctx, token, in)
	})
	server := httptest.NewServer(Handler(f.svc))
	defer server.Close()
	home := t.TempDir()
	tokenFile := filepath.Join(home, "token")
	writeToken := func(token string) {
		t.Helper()
		if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeToken(f.human)
	anchor := Ref{Collection: colMemories, ID: result.Old.ID, Revision: result.Old.Revision}
	sourceRef := Ref{Collection: "sources", ID: source.ID, Revision: source.Revision}
	evaluationRef := Ref{Collection: "evaluations", ID: evaluation.ID, Revision: evaluation.Revision}
	for _, runtime := range []string{"codex", "claude_code", "zcode", "gemini", "grok"} {
		env := arkismEnv(home, server.URL, tokenFile, "ARKISM_CLIENT=", "ARKISM_RUNTIME="+runtime)
		call := func(args map[string]any) map[string]json.RawMessage {
			t.Helper()
			return arkismMCP(t, env, mcpCall{"recall", args})[0]
		}
		for _, ref := range []Ref{anchor, sourceRef, evaluationRef} {
			var value struct {
				Memories []struct {
					ID         string `json:"id"`
					Supersedes []any  `json:"supersedes"`
				} `json:"memories"`
				RelatedTo    Ref    `json:"related_to"`
				Authority    string `json:"authority"`
				RelationNote string `json:"relation_note"`
			}
			mcpResult(t, call(map[string]any{"related_to": ref, "query": "decision rule"}), &value)
			if len(value.Memories) != 1 || value.Memories[0].ID != result.New.ID || value.RelatedTo != ref || value.Authority != "data_only_untrusted" || value.RelationNote == "" || len(value.Memories[0].Supersedes) == 0 {
				t.Fatalf("%s relation recall: %+v", runtime, value)
			}
		}
		stale := anchor
		stale.Revision--
		if message := mcpError(call(map[string]any{"related_to": stale})); !strings.Contains(message, "HTTP 409") {
			t.Fatal("stale anchor", runtime, message)
		}
		foreign := anchor
		foreign.ID = "missing-or-foreign"
		if call(map[string]any{"related_to": foreign})["error"] == nil {
			t.Fatal("foreign anchor accepted", runtime)
		}
		if call(map[string]any{"related_to": anchor, "ids": []string{result.New.ID}})["error"] == nil {
			t.Fatal("related_to combined with ids", runtime)
		}
		writeToken("denied")
		if message := mcpError(call(map[string]any{"related_to": anchor})); !strings.Contains(message, "HTTP 403") {
			t.Fatal("denied anchor", runtime, message)
		}
		writeToken(f.human)
	}
	got, err := p.Get(testCtx, colSessionArchives, scopeA, id)
	if err != nil || got.Revision != header.Revision || !got.ExpiresAt.Equal(*header.ExpiresAt) {
		t.Fatal("relation recall renewed original", err)
	}
	var uses int
	if err = p.q().QueryRow(testCtx, `SELECT count(*) FROM memory_records WHERE collection=$1 AND kind=$2`, colJobs, kindSessionArchiveUsage).Scan(&uses); err != nil || uses != 0 {
		t.Fatal("relation recall acknowledged actual use", err)
	}
}
