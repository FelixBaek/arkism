package memory

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func exerciseMemoryTags(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	f.svc.Embedder = nil
	src := f.source()
	tags := []string{"업무", "Qwen", "qwen"}
	in := candidateInput(CreateInput{Scope: scopeA, Text: "tagged original", Tags: tags, Sources: refs(src)})
	req := httptest.NewRequest("POST", "/v1/memory_candidates", strings.NewReader(structuredText(in)))
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	var candidate Record
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &candidate) != nil || !slices.Equal(candidate.Tags, tags) {
		t.Fatal("candidate tags HTTP", w.Code, w.Body.String())
	}
	memory, err := f.svc.ReviewCandidate(testCtx, f.human, candidate.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Action: "edit_approve", Content: "tagged approved"})
	if err != nil || !slices.Equal(memory.Tags, tags) {
		t.Fatal("approval lost tags", err)
	}
	saved, err := repo.Get(testCtx, "memories", scopeA, memory.ID)
	if err != nil || !slices.Equal(saved.Tags, tags) || slices.Contains(saved.Keywords, tags[0]) {
		t.Fatal("stored tag contract", saved, err)
	}
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "tagged", Modes: []string{"fts"}, BudgetTokens: 2000, TokenEncoding: "cl100k_base"})
	if err != nil || len(out.Items) != 1 || !slices.Equal(out.Items[0].Tags, tags) || !strings.Contains(out.ContextData, `"tags":["업무","Qwen","qwen"]`) {
		t.Fatal("context lost tags", out, err)
	}
	// Tags are filter metadata, not hidden additions to embedding content.
	without := saved
	without.Tags = nil
	if recordContent(saved) != recordContent(without) || HitFromRecord(colMemories, saved).Digest != HitFromRecord(colMemories, without).Digest {
		t.Fatal("tags changed embedding content")
	}
	replacement, err := f.svc.Supersede(testCtx, f.human, memory.ID, SupersedeInput{Scope: scopeA, ExpectedRevision: memory.Revision, Reason: "updated label", NewContent: "tagged next", Sources: refs(src), Tags: []string{"next"}})
	if err != nil || !slices.Equal(replacement.Old.Tags, tags) || !slices.Equal(replacement.New.Tags, []string{"next"}) {
		t.Fatal("new replacement tags", err)
	}
	in.Text = "tagged existing"
	in.Tags = []string{"existing"}
	c := f.create(f.human, "memory_candidates", in)
	existing, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	change := SupersedeInput{Scope: scopeA, ExpectedRevision: replacement.New.Revision, Reason: "use existing", ReplacementMemoryID: existing.ID, ReplacementExpectedRevision: existing.Revision, Tags: []string{"overwrite"}}
	if _, err = f.svc.Supersede(testCtx, f.human, replacement.New.ID, change); !errors.Is(err, ErrInvalid) {
		t.Fatal("existing tags overridden", err)
	}
	change.Tags = nil
	result, err := f.svc.Supersede(testCtx, f.human, replacement.New.ID, change)
	if err != nil || !slices.Equal(result.New.Tags, existing.Tags) || !slices.Equal(result.Old.Tags, []string{"next"}) {
		t.Fatal("existing replacement lost tags", err)
	}
}
func TestMemoryTags(t *testing.T) { exerciseMemoryTags(t, newFakeRepo()) }

func TestTagValidationAndInspection(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	in := candidateInput(CreateInput{Scope: scopeA, Text: "valid candidate", Sources: refs(src)})
	oversized := []string{}
	for i := 0; i < 33; i++ {
		oversized = append(oversized, strconv.Itoa(i))
	}
	for _, tags := range [][]string{{""}, {" "}, {" tag"}, {"tag "}, {"a\tb"}, {"a\nb"}, {"same", "same"}, {strings.Repeat("x", 129)}, {strings.Repeat("한", 43)}, {string([]byte{255})}, oversized} {
		in.Tags = tags
		if _, err := f.svc.Create(testCtx, f.human, "memory_candidates", in); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid tags accepted", tags, err)
		}
	}
	in.Tags = oversized[:32]
	if _, err := f.svc.Create(testCtx, f.human, "memory_candidates", in); err != nil {
		t.Fatal("32 tags", err)
	}
	in.Tags = []string{strings.Repeat("x", 128)}
	if _, err := f.svc.Create(testCtx, f.human, "memory_candidates", in); err != nil {
		t.Fatal("128-byte tag", err)
	}
	in.Tags = []string{"password=synthetic"}
	in.OnSecret = "redact"
	if _, err := f.svc.Create(testCtx, f.human, "memory_candidates", in); !errors.Is(err, ErrSecretDetected) {
		t.Fatal("secret tag redacted or stored", err)
	}
	for _, r := range f.repo.rows("audit_events") {
		if strings.Contains(structuredText(r), "password=synthetic") {
			t.Fatal("secret tag audited")
		}
	}
	in.Tags = []string{"person@example.test"}
	in.Sensitivity = "public"
	r, err := f.svc.Create(testCtx, f.human, "memory_candidates", in)
	if err != nil || r.Sensitivity != "private" || !r.Inspection.PersonalData {
		t.Fatal("tag PII not classified", err)
	}
	old := f.activeMemory("previous tagged decision")
	change := SupersedeInput{Scope: scopeA, ExpectedRevision: old.Revision, Reason: "new classification", NewContent: "new tagged decision", Sources: refs(src), Sensitivity: "public", Tags: []string{"password=synthetic"}}
	count := len(f.repo.rows(colMemories))
	if out, err := f.svc.Supersede(testCtx, f.human, old.ID, change); !errors.Is(err, ErrSecretDetected) || out.New.ID != "" || len(f.repo.rows(colMemories)) != count {
		t.Fatal("secret replacement tag persisted", err)
	}
	for _, r := range f.repo.rows("audit_events") {
		if strings.Contains(structuredText(r), "password=synthetic") {
			t.Fatal("secret replacement tag audited")
		}
	}
	change.Tags = []string{"person@example.test"}
	replaced, err := f.svc.Supersede(testCtx, f.human, old.ID, change)
	if err != nil || replaced.New.Sensitivity != "private" || !replaced.New.Inspection.PersonalData || !slices.Equal(replaced.New.Tags, change.Tags) {
		t.Fatal("replacement tag PII not classified", err)
	}

	source := sourceInput(CreateInput{Scope: scopeA, Text: "source", Tags: []string{"unsupported"}})
	if _, err := f.svc.Create(testCtx, f.human, "sources", source); !errors.Is(err, ErrInvalid) {
		t.Fatal("source silently accepted tags", err)
	}
}

func TestTagChangedAfterSearchPolicy(t *testing.T) {
	f := newFixture(t)
	r := f.activeMemory("tagged search")
	changed := false
	f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(a Authorization, d *Decision) error {
		if a.Action == "context:read" && a.ResourceID == r.ID {
			f.repo.mu.Lock()
			current := f.repo.store[colMemories][recKey(scopeA, r.ID)]
			current.Tags = []string{"changed"}
			f.repo.store[colMemories][recKey(scopeA, r.ID)] = current
			f.repo.mu.Unlock()
			changed = true
		}
		return nil
	}}
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "tagged", Modes: []string{"fts"}})
	if !changed || !errors.Is(err, ErrConflict) || len(out.Items) != 0 || out.ContextID != "" {
		t.Fatal("stale tags returned", out, err)
	}
}
