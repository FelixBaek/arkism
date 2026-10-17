package memory

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tiktoken-go/tokenizer"
)

func TestContextRelationshipMetadata(t *testing.T) { testContextRelationshipMetadata(t, nil) }
func TestPostgresContextRelationshipMetadata(t *testing.T) {
	testContextRelationshipMetadata(t, isolatedPostgres(t))
}
func testContextRelationshipMetadata(t *testing.T, repo Repository) {
	f := newFixture(t)
	if repo != nil {
		f.svc.Repo = repo
	}
	a := f.activeMemory("first rule")
	b := f.activeMemory("second rule")
	in := ConflictInput{Scope: scopeA, Memories: []Ref{{Collection: colMemories, ID: a.ID, Revision: a.Revision}, {Collection: colMemories, ID: b.ID, Revision: b.Revision}}, Action: "mark", Hold: "first", Reason: "human review pending"}
	marked, err := f.svc.Conflict(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	build := func() ContextPack {
		t.Helper()
		out, err := f.svc.BuildContext(testCtx, f.human, ContextBuildInput{Scope: scopeA, Task: "rule", Modes: []string{"fts"}, TokenEncoding: "o200k_base"})
		if err != nil {
			t.Fatal(err)
		}
		codec, _ := tokenizer.Get(tokenizer.O200kBase)
		n, err := codec.Count(out.ContextData)
		if err != nil || n != out.TokensUsed || n > 2000 {
			t.Fatal("metadata not budgeted", err)
		}
		var decoded ContextBundle
		if json.Unmarshal([]byte(out.ContextData), &decoded) != nil || structuredText(decoded.Items) != structuredText(out.Items) {
			t.Fatal("metadata dropped from model payload")
		}
		return out
	}
	out := build()
	if len(out.Items) != 1 || out.Items[0].ID != b.ID || !out.Items[0].ConflictPending || strings.Contains(out.ContextData, a.Text) || strings.Contains(out.ContextData, in.Reason) {
		t.Fatal("conflict annotation or held-content isolation failed")
	}
	if repo != nil {
		server := httptest.NewServer(Handler(f.svc))
		defer server.Close()
		home := t.TempDir()
		tokenFile := filepath.Join(home, "token")
		if err := os.WriteFile(tokenFile, []byte(f.human), 0600); err != nil {
			t.Fatal(err)
		}
		for _, runtime := range []string{"codex", "claude_code"} {
			env := arkismEnv(home, server.URL, tokenFile, "ARKISM_RUNTIME="+runtime, "ARKISM_ORIGIN="+runtime)
			for _, response := range arkismMCP(t, env, mcpCall{"recall", map[string]any{"ids": []string{b.ID}}}, mcpCall{"recall", map[string]any{"query": "rule"}}) {
				var value struct {
					Memories []struct {
						ConflictPending bool  `json:"conflict_pending"`
						Sources         []Ref `json:"sources"`
						Revision        int64 `json:"revision"`
					} `json:"memories"`
				}
				mcpResult(t, response, &value)
				if len(value.Memories) != 1 || !value.Memories[0].ConflictPending || len(value.Memories[0].Sources) == 0 || value.Memories[0].Revision <= 0 {
					t.Fatalf("live recall lost metadata: %+v", value)
				}
			}
			// The prompt hook renders the recalled memory as an index line; one session per runtime, since a session shows a memory once.
			hook, _ := json.Marshal(map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "s-" + runtime, "cwd": home, "prompt": "second rule"})
			var output struct {
				Hook struct {
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			raw := arkismRun(t, env, string(hook), "capture", "hook")
			if err := json.Unmarshal(raw, &output); err != nil {
				t.Fatalf("hook output: %v %s", err, raw)
			}
			if line := output.Hook.Context; !strings.Contains(line, "충돌 검토 중") || !strings.Contains(line, "(id: "+b.ID+")") || strings.Contains(line, a.Text) {
				t.Fatalf("live hook lost metadata: %s", line)
			}
		}
	}
	resolved, err := f.svc.Conflict(testCtx, f.human, ConflictInput{Scope: scopeA, Memories: marked.Relation.Relations, Action: "resolve", Reason: "reviewed", ConflictID: marked.Relation.ID, ExpectedRevision: marked.Relation.Revision, Resolution: "keep_both"})
	if err != nil {
		t.Fatal(err)
	}
	out = build()
	if len(out.Items) != 2 {
		t.Fatal("resolved memories missing")
	}
	for _, hit := range out.Items {
		if hit.ConflictPending {
			t.Fatal("resolved conflict still pending")
		}
	}
	old := resolved.Memories[0]
	replacement := resolved.Memories[1]
	superseded, err := f.svc.Supersede(testCtx, f.human, old.ID, SupersedeInput{Scope: scopeA, ExpectedRevision: old.Revision, ReplacementMemoryID: replacement.ID, ReplacementExpectedRevision: replacement.Revision, Reason: "newer decision"})
	if err != nil {
		t.Fatal(err)
	}
	out = build()
	if len(out.Items) != 1 || structuredText(out.Items[0].Supersedes) != structuredText(superseded.New.Supersedes) || len(out.Items[0].Supersedes) != 1 || strings.Contains(out.ContextData, old.Text) {
		t.Fatal("replacement evidence missing or old body leaked")
	}
	// Change only relationship metadata after packing, without a revision bump.
	// The response fence must detect it and return neither body nor receipt.
	store := f.svc.Repo
	for _, mode := range []string{"conflict", "replacement"} {
		f.svc.Repo = evaluationChangeRepo{Repository: store, change: func() {
			row, err := store.Get(testCtx, colMemories, scopeA, superseded.New.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "conflict" {
				row.ConflictID = "changed"
			} else {
				row.Supersedes = nil
			}
			if err = store.Replace(testCtx, colMemories, row, row.Revision); err != nil {
				t.Fatal(err)
			}
		}}
		result, err := f.svc.BuildContext(testCtx, f.human, ContextBuildInput{Scope: scopeA, Task: "rule", Modes: []string{"fts"}, TokenEncoding: "o200k_base"})
		if !errors.Is(err, ErrConflict) || len(result.Items) != 0 || result.ContextData != "" || result.ContextID != "" {
			t.Fatal("changed relationship escaped response fence", mode, err)
		}
	}
}
