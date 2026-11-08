package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type evidenceSelectionModel struct {
	calls  int
	prompt evidenceSelectionPrompt
	answer string
	err    error
	before func()
}

func (m *evidenceSelectionModel) Generator() Generator {
	return Generator{Model: "test-evidence-selector"}
}
func (m *evidenceSelectionModel) Complete(ctx context.Context, _ string, user string, limit int) (string, ProviderUsage, error) {
	m.calls++
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > evidenceSelectionTimeout || limit != evidenceSelectionOutputTokens {
		return "", ProviderUsage{}, errors.New("missing bounded model request")
	}
	if err := json.Unmarshal([]byte(user), &m.prompt); err != nil {
		return "", ProviderUsage{}, err
	}
	if m.before != nil {
		m.before()
	}
	answer := m.answer
	if answer == "" && m.err == nil {
		answer = fmt.Sprintf(`{"memories":[%d],"conversations":[%d]}`, len(m.prompt.Memories), len(m.prompt.Conversations))
	}
	return answer, ProviderUsage{Provider: wikiProvider, Model: "test-evidence-selector", Requests: 1, InputTokens: 40, OutputTokens: 10, TotalTokens: 50}, m.err
}

func evidenceSelectionFixture(t *testing.T, count int) (*fixture, *evidenceSelectionModel, SearchInput) {
	t.Helper()
	f := newFixture(t)
	a := f.auth.actors[f.human]
	a.verbs["generate"] = true
	f.auth.actors[f.human] = a
	src := f.source()
	src = f.editRecord("sources", src.ID, func(r *Record) { r.Sensitivity = "internal" })
	now := time.Now().UTC().Add(-time.Hour)
	f.svc.RecallNow = func() time.Time { return now.Add(time.Minute) }
	for i := 0; i < count; i++ {
		r := Record{ID: fmt.Sprintf("evidence-memory-%03d", i), Scope: scopeA, Kind: "preference", Status: statusActive, Revision: 1, ApprovedBy: "reviewer", Sensitivity: "internal", Text: fmt.Sprintf("Copper pump repair memory %03d has a distinct documented condition.", i), Summary: "Retain every stated condition.", Sources: refs(src), CreatedAt: now, UpdatedAt: now}
		if err := f.repo.Insert(testCtx, colMemories, r); err != nil {
			t.Fatal(err)
		}
		conversationBatch(f, fmt.Sprintf("Avery: Copper pump repair conversation %03d has an independent inspection detail.", i), fmt.Sprintf("evidence-session-%03d", i), 0, func(r *Record) { r.ID = fmt.Sprintf("evidence-source-%03d", i); r.Sensitivity = "internal" })
	}
	setCaptureDensity(t, f, captureDensityConversation)
	provisional := false
	setRecallQuality(t, f, SpaceRecallQuality{CandidatePool: 100, Provisional: &provisional})
	m := &evidenceSelectionModel{}
	f.svc.Wiki = m
	return f, m, SearchInput{Scope: scopeA, Query: "copper pump repair", Modes: []string{"fts"}, Limit: 10, MaxChars: 6000, Include: []string{"sources"}, Explain: true}
}

func enableEvidenceSelection(t *testing.T, f *fixture) {
	t.Helper()
	cfg, err := f.svc.GetSpaceConfig(testCtx, f.human, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	q := cfg.Config.recallQuality()
	q.EvidenceSelection = true
	setRecallQuality(t, f, q)
}

func evidenceReturned(out ContextBundle) any {
	items, conversation := slices.Clone(out.Items), slices.Clone(out.Conversation)
	for i := range items {
		items[i].Explain = nil
	}
	for i := range conversation {
		conversation[i].Explain = nil
	}
	return struct {
		Items, Conversation, Provisional []Hit
		Empty                            string
	}{items, conversation, out.Provisional, out.EmptyReason}
}

func TestEvidenceSelectionServiceChoosesBeyondBothFinalLimits(t *testing.T) {
	f, m, in := evidenceSelectionFixture(t, 100)
	baseline, err := f.svc.Search(testCtx, f.human, in)
	if err != nil || len(baseline.Items) != 10 || len(baseline.Conversation) != 5 || m.calls != 0 || baseline.Explain.EvidenceSelection != "" {
		t.Fatal("baseline", err, len(baseline.Items), len(baseline.Conversation), m.calls)
	}
	enableEvidenceSelection(t, f)
	out, err := f.svc.Search(testCtx, f.human, in)
	if err != nil || out.Explain.EvidenceSelection != "applied" || m.calls != 1 {
		t.Fatal("selection", err, out.Explain, m.calls)
	}
	if len(m.prompt.Memories) != 100 || len(m.prompt.Conversations) != 100 || len(out.Items) != 1 || len(out.Conversation) != 1 {
		t.Fatal("pool lost before selection", len(m.prompt.Memories), len(m.prompt.Conversations), len(out.Items), len(out.Conversation))
	}
	if slices.Contains(ids(baseline.Items), out.Items[0].ID) || slices.Contains(ids(baseline.Conversation), out.Conversation[0].ID) {
		t.Fatal("model only saw the old output head")
	}
	if out.Items[0].Text != m.prompt.Memories[99].Text || out.Items[0].Summary != m.prompt.Memories[99].Summary || out.Conversation[0].Text != m.prompt.Conversations[99].Text || out.Items[0].Explain == nil {
		t.Fatal("model text escaped or trace lost")
	}
	if out.Conversation[0].Authority != AuthorityDataOnly || evidenceSelectionChars(out.Items) > in.MaxChars || evidenceSelectionChars(out.Conversation) > 3000 {
		t.Fatal("authority or budget changed")
	}
	if len(ledgerOf(f, evidenceSelectionOperation)) != 1 || len(f.emb.calls) != 0 {
		t.Fatal("usage or unexpected embedding")
	}
	cfg, _ := f.svc.GetSpaceConfig(testCtx, f.human, scopeA)
	q := cfg.Config.recallQuality()
	q.EvidenceSelection = false
	setRecallQuality(t, f, q)
	restored, err := f.svc.Search(testCtx, f.human, in)
	if err != nil || !reflect.DeepEqual(evidenceReturned(baseline), evidenceReturned(restored)) || m.calls != 1 {
		t.Fatal("disable did not restore baseline", err)
	}
}

func TestEvidenceSelectionStrictNumberContract(t *testing.T) {
	valid := []string{`{"memories":[2,1],"conversations":[1]}`, `{"conversations":[],"memories":[1]}`}
	for _, raw := range valid {
		if _, ok := parseEvidenceSelection(raw, 2, 2, 2); !ok {
			t.Fatal(raw)
		}
	}
	bad := []string{`null`, `{}`, `[]`, `{"memories":[1]}`, `{"memories":null,"conversations":[1]}`, `{"memories":[],"conversations":[]}`, `{"memories":[0],"conversations":[]}`, `{"memories":[3],"conversations":[]}`, `{"memories":[1,1],"conversations":[]}`, `{"memories":[],"conversations":[3]}`, `{"memories":[1],"conversations":[1,1]}`, `{"memories":[1.5],"conversations":[]}`, `{"memories":["1"],"conversations":[]}`, `{"memories":[1],"conversations":[],"text":"fabricated"}`, `{"memories":[1],"memories":[2],"conversations":[]}`, `{"memories":[1],"conversations":[]} trailing`, "```json\n{\"memories\":[1],\"conversations\":[]}\n```"}
	for _, raw := range bad {
		if _, ok := parseEvidenceSelection(raw, 2, 2, 2); ok {
			t.Fatal("accepted", raw)
		}
	}
	if _, ok := parseEvidenceSelection(`{"memories":[1,2],"conversations":[]}`, 2, 0, 1); ok {
		t.Fatal("memory limit")
	}
	if _, ok := parseEvidenceSelection(`{"memories":[],"conversations":[1,2,3,4,5,6]}`, 0, 6, 10); ok {
		t.Fatal("conversation limit")
	}
}

func TestEvidenceSelectionFailuresPreserveExactBaseline(t *testing.T) {
	for _, tc := range []struct {
		name, answer, status string
		err                  error
	}{
		{"malformed", "not json", "invalid_response", nil},
		{"duplicate", `{"memories":[1,1],"conversations":[]}`, "invalid_response", nil},
		{"unknown", `{"memories":[1],"conversations":[],"explanation":"invented"}`, "invalid_response", nil},
		{"provider", "", "failed", ErrUnavailable},
		{"timeout", "", "timeout", context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, m, in := evidenceSelectionFixture(t, 12)
			m.answer, m.err = tc.answer, tc.err
			base, err := f.svc.Search(testCtx, f.human, in)
			if err != nil {
				t.Fatal(err)
			}
			enableEvidenceSelection(t, f)
			out, err := f.svc.Search(testCtx, f.human, in)
			if err != nil || out.Explain.EvidenceSelection != tc.status || !reflect.DeepEqual(evidenceReturned(base), evidenceReturned(out)) || m.calls != 1 {
				t.Fatal("fallback changed output", err, out.Explain, m.calls)
			}
		})
	}
}

func TestEvidenceSelectionDailyBudgetPreventsCall(t *testing.T) {
	f, m, in := evidenceSelectionFixture(t, 12)
	base, err := f.svc.Search(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < evidenceSelectionDailyCalls; i++ {
		if err := f.repo.Insert(testCtx, colProviderUsage, Record{ID: fmt.Sprintf("evidence-usage-%d", i), Scope: projectScope(scopeA), Kind: providerUsageKind, CreatedAt: time.Now(), Cost: &ProviderCost{Operation: evidenceSelectionOperation}}); err != nil {
			t.Fatal(err)
		}
	}
	enableEvidenceSelection(t, f)
	out, err := f.svc.Search(testCtx, f.human, in)
	if err != nil || out.Explain.EvidenceSelection != "budget_exceeded" || m.calls != 0 || !reflect.DeepEqual(evidenceReturned(base), evidenceReturned(out)) {
		t.Fatal("budget fallback", err, out.Explain, m.calls)
	}
}

func TestEvidenceSelectionChecksEveryCandidateAndQuery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*fixture)
		status string
	}{
		{"private-memory", func(f *fixture) {
			f.editRecord(colMemories, "evidence-memory-011", func(r *Record) { r.Sensitivity = "private" })
		}, "candidate_denied"},
		{"private-source", func(f *fixture) {
			f.editRecord("sources", "evidence-source-011", func(r *Record) { r.Sensitivity = "private" })
		}, "candidate_denied"},
		{"transfer-denied", func(f *fixture) { a := f.auth.actors[f.human]; a.external = false; f.auth.actors[f.human] = a }, "denied"},
		{"generate-denied", func(f *fixture) { a := f.auth.actors[f.human]; a.verbs["generate"] = false; f.auth.actors[f.human] = a }, "denied"},
		{"candidate-transfer-denied", func(f *fixture) {
			f.svc.Auth = evidenceTransferDeny{Authorizer: f.auth}
		}, "candidate_denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, m, in := evidenceSelectionFixture(t, 12)
			tc.mutate(f)
			base, err := f.svc.Search(testCtx, f.human, in)
			if err != nil {
				t.Fatal(err)
			}
			enableEvidenceSelection(t, f)
			out, err := f.svc.Search(testCtx, f.human, in)
			if err != nil || out.Explain.EvidenceSelection != tc.status || m.calls != 0 || !reflect.DeepEqual(evidenceReturned(base), evidenceReturned(out)) {
				t.Fatal("gated output", err, out.Explain, m.calls)
			}
		})
	}
}

func TestEvidenceSelectionWholeTextAndSummaryMustFit(t *testing.T) {
	f, m, in := evidenceSelectionFixture(t, 1)
	f.editRecord(colMemories, "evidence-memory-000", func(r *Record) { r.Summary = strings.Repeat("조건", 80) })
	in.MaxChars = 100
	base, err := f.svc.Search(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	enableEvidenceSelection(t, f)
	out, err := f.svc.Search(testCtx, f.human, in)
	if err != nil || out.Explain.EvidenceSelection != "character_budget" || m.calls != 1 || !reflect.DeepEqual(evidenceReturned(base), evidenceReturned(out)) {
		t.Fatal("summary was clipped then accepted", err, out.Explain)
	}
	if m.prompt.Memories[0].Summary != strings.Repeat("조건", 80) {
		t.Fatal("input silently clipped")
	}
}

func TestEvidenceSelectionFinalFenceRejectsModelTimeMutation(t *testing.T) {
	for _, col := range []string{colMemories, "sources"} {
		t.Run(col, func(t *testing.T) {
			f, m, in := evidenceSelectionFixture(t, 1)
			enableEvidenceSelection(t, f)
			id := "evidence-memory-000"
			if col == "sources" {
				id = "evidence-source-000"
			}
			m.before = func() { f.editRecord(col, id, func(r *Record) { r.ContentErased = true }) }
			out, err := f.svc.Search(testCtx, f.human, in)
			if !errors.Is(err, ErrConflict) || len(out.Items) != 0 || len(out.Conversation) != 0 || m.calls != 1 {
				t.Fatal("stale model input returned", err, out, m.calls)
			}
		})
	}
}

func TestEvidenceSelectionUnsupportedDeliveryPreservesBaseline(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		change       func(*SearchInput)
	}{
		{"rerank-off", "rerank_disabled", func(in *SearchInput) { in.Rerank = rerankOff }},
		{"whole-items", "unsupported_delivery", func(in *SearchInput) { in.WholeItems = true }},
		{"observations", "unsupported_delivery", func(in *SearchInput) { on := true; in.PreferObservations = &on }},
		{"source-facts", "unsupported_delivery", func(in *SearchInput) { in.Include = append(in.Include, "source_facts") }},
		{"tokens", "unsupported_delivery", func(in *SearchInput) { in.BudgetTokens = 512; in.TokenEncoding = "cl100k_base" }},
		{"conversation-context", "unsupported_delivery", func(in *SearchInput) {
			in.ConversationContext = true
			in.BudgetTokens = 512
			in.TokenEncoding = "cl100k_base"
		}},
		{"external", "unsupported_context", func(in *SearchInput) { in.Destination = "external:consumer" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, m, in := evidenceSelectionFixture(t, 12)
			tc.change(&in)
			base, err := f.svc.Search(testCtx, f.human, in)
			if err != nil {
				t.Fatal("baseline", err)
			}
			enableEvidenceSelection(t, f)
			out, err := f.svc.Search(testCtx, f.human, in)
			if err != nil || out.Explain.EvidenceSelection != tc.status || m.calls != 0 || !reflect.DeepEqual(evidenceReturned(base), evidenceReturned(out)) {
				t.Fatal("unsupported delivery changed", err, out.Explain, m.calls)
			}
		})
	}
}

func TestEvidenceSelectionUnsupportedBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		change       func(*Service, *SearchInput, *string)
	}{
		{"history", "unsupported_context", func(_ *Service, in *SearchInput, _ *string) { in.IncludeInactive = true }},
		{"related", "unsupported_context", func(_ *Service, in *SearchInput, _ *string) {
			in.RelatedTo = &Ref{Collection: colMemories, ID: "anchor"}
		}},
		{"frozen", "unsupported_context", func(_ *Service, in *SearchInput, _ *string) { in.contextView = &ContextInvocationView{} }},
		{"other-collection", "unsupported_context", func(_ *Service, _ *SearchInput, col *string) { *col = colSkillVersions }},
		{"native", "unsupported_native", func(s *Service, _ *SearchInput, _ *string) {
			s.NativeSearchShadow = &NativeSearchShadowRuntime{config: NativeSearchShadowConfig{Mode: "shadow"}}
		}},
		{"cross-encoder", "unsupported_reranker", func(s *Service, _ *SearchInput, _ *string) { s.Reranker = NewLocalReranker() }},
		{"model-rerank", "unsupported_reranker", func(_ *Service, in *SearchInput, _ *string) { in.Rerank = rerankModel }},
		{"decision-prune", "unsupported_reranker", func(_ *Service, in *SearchInput, _ *string) { in.RerankOptions = &RerankOptions{Prune: true} }},
		{"decision-dedup", "unsupported_reranker", func(_ *Service, in *SearchInput, _ *string) { in.RerankOptions = &RerankOptions{Dedup: true} }},
		{"pool50", "unsupported_pool", func(_ *Service, in *SearchInput, _ *string) { in.space.RecallQuality.CandidatePool = 50 }},
		{"too-many-final", "unsupported_pool", func(_ *Service, in *SearchInput, _ *string) { in.Limit = 11 }},
		{"no-model", "model_not_configured", func(s *Service, _ *SearchInput, _ *string) { s.Wiki = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &evidenceSelectionModel{}
			s := &Service{Wiki: m}
			in := SearchInput{Query: "ordinary question", Limit: 10, Include: []string{"sources"}, space: &SpaceConfig{CaptureDensity: captureDensityConversation, RecallQuality: &SpaceRecallQuality{CandidatePool: 100, EvidenceSelection: true}}}
			col := colMemories
			tc.change(s, &in, &col)
			if got := s.evidenceSelectionStatus(in, col); got != tc.status || m.calls != 0 {
				t.Fatal(got, m.calls)
			}
		})
	}
}

func TestEvidenceSelectionPromptOverflowDoesNotCallOrClip(t *testing.T) {
	f, m, in := evidenceSelectionFixture(t, 12)
	for i := 0; i < 12; i++ {
		f.editRecord(colMemories, fmt.Sprintf("evidence-memory-%03d", i), func(r *Record) { r.Text += " " + strings.Repeat("word ", 1300) })
	}
	base, err := f.svc.Search(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	enableEvidenceSelection(t, f)
	out, err := f.svc.Search(testCtx, f.human, in)
	if err != nil || out.Explain.EvidenceSelection != "prompt_too_large" || m.calls != 0 || !reflect.DeepEqual(evidenceReturned(base), evidenceReturned(out)) {
		t.Fatal("prompt clipping or call", err, out.Explain, m.calls)
	}
}

func TestEvidenceSelectionFloorsAndNeighborFence(t *testing.T) {
	t.Run("floor", func(t *testing.T) {
		f, m, in := evidenceSelectionFixture(t, 12)
		// A reciprocal-rank lane starts at 1/61. The floor leaves only its
		// first few hits and the model must never receive lower-score memories.
		in.MinScore = 1.0 / 64
		enableEvidenceSelection(t, f)
		out, err := f.svc.Search(testCtx, f.human, in)
		if err != nil || out.Explain.EvidenceSelection != "applied" || len(m.prompt.Memories) == 0 || len(m.prompt.Memories) >= 12 {
			t.Fatal("floor not respected", err, out.Explain, len(m.prompt.Memories))
		}
		for _, h := range out.Items {
			if h.Score < in.MinScore || !slices.Contains(h.Lanes, "keyword") {
				t.Fatal("score or lane lost", h)
			}
		}
	})
	t.Run("neighbor-map", func(t *testing.T) {
		f, m, in := evidenceSelectionFixture(t, 1)
		neighbor := conversationBatch(f, "Rowan: Adjacent inspection requires the separate pressure reading.", "evidence-session-000", 1, func(r *Record) { r.Sensitivity = "internal" })
		enableEvidenceSelection(t, f)
		out, err := f.svc.Search(testCtx, f.human, in)
		if err != nil || out.Explain.EvidenceSelection != "applied" || m.calls != 1 {
			t.Fatal(err, out.Explain)
		}
		if !strings.Contains(out.Conversation[0].Text, neighbor.Text) {
			t.Fatal("adjacent context missing")
		}
		if _, ok := out.conversationRecords[Ref{Collection: "sources", ID: neighbor.ID, Revision: neighbor.Revision}]; !ok {
			t.Fatal("neighbor missing from selection fence")
		}
		f.editRecord("sources", neighbor.ID, func(r *Record) { r.ContentErased = true })
		if err := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error { return verifyConversationContext(ctx, tx, in.Scope, out) }); !errors.Is(err, ErrConflict) {
			t.Fatal("neighbor revocation not fenced", err)
		}
	})
}

// Deny only a late candidate's model transfer; ordinary internal reads still work.
type evidenceTransferDeny struct{ Authorizer }

func (a evidenceTransferDeny) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	d, err := a.Authorizer.Authorize(ctx, token, in)
	if in.Action == "context:read" && in.Context.Destination == wikiDestination && in.ResourceID == "evidence-memory-011" {
		d.ExternalTransfer = false
	}
	return d, err
}

func TestEvidenceSelectionHTTPSourceOnlyIsEmptyArray(t *testing.T) {
	f, m, in := evidenceSelectionFixture(t, 12)
	enableEvidenceSelection(t, f)
	m.answer = `{"memories":[],"conversations":[1]}`
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/v1/search", bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+f.human)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(response, request)
	var body map[string]json.RawMessage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || string(body["items"]) != "[]" {
		t.Fatal("source-only wire contract", response.Code, response.Body.String())
	}
	var out ContextBundle
	if json.Unmarshal(response.Body.Bytes(), &out) != nil || out.Explain.EvidenceSelection != "applied" || len(out.Conversation) != 1 || m.calls != 1 {
		t.Fatal("selection not applied", response.Body.String())
	}
}

func TestEvidenceSelectionSourceSummaryBudgetPreservesBaseline(t *testing.T) {
	f, m, in := evidenceSelectionFixture(t, 12)
	for i := range 12 {
		f.editRecord("sources", fmt.Sprintf("evidence-source-%03d", i), func(r *Record) { r.Summary = strings.Repeat("condition ", 70) })
	}
	base, err := f.svc.Search(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	enableEvidenceSelection(t, f)
	m.answer = `{"memories":[1],"conversations":[1]}`
	out, err := f.svc.Search(testCtx, f.human, in)
	if err != nil || out.Explain.EvidenceSelection != "character_budget" || m.calls != 1 || !reflect.DeepEqual(evidenceReturned(base), evidenceReturned(out)) {
		t.Fatal("source summary enlarged budget", err, out.Explain, m.calls)
	}
}

func TestEvidenceSelectionAuditUsesExistingBoundedLinks(t *testing.T) {
	f, _, in := evidenceSelectionFixture(t, 100)
	enableEvidenceSelection(t, f)
	out, err := f.svc.Search(testCtx, f.human, in)
	if err != nil || out.Explain.EvidenceSelection != "applied" {
		t.Fatal(err, out.Explain)
	}
	traces := llmTraces(t, f, projectScope(scopeA), evidenceSelectionOperation)
	if len(traces) != 1 || len(traces[0].Request.SourceMemoryIDs) != llmTraceMaxIDs || !strings.HasPrefix(traces[0].Request.DocumentID, "evidence-source-") || traces[0].Request.PromptSHA256 == "" {
		t.Fatal("missing bounded audit links", traces)
	}
	for _, id := range traces[0].Request.SourceMemoryIDs {
		if !strings.HasPrefix(id, "evidence-memory-") {
			t.Fatal("source ID mislabeled as memory", id)
		}
	}
}

func TestEvidenceSelectionFinalFenceRejectsSourceDependencyMutation(t *testing.T) {
	f, m, in := evidenceSelectionFixture(t, 1)
	input := sourceInput(CreateInput{Scope: scopeA, Text: "Independent parent document for the conversation."})
	input.SourceRef = "fixture/evidence-conversation-parent"
	dependency := f.create(f.human, "sources", input)
	dependency = f.editRecord("sources", dependency.ID, func(r *Record) { r.Sensitivity = "internal" })
	f.editRecord("sources", "evidence-source-000", func(r *Record) { r.Text = "Unrelated seasonal weather note." })
	conversationBatch(f, "Copper pump repair conversation with a documented parent.", "dependency-session", 0, func(r *Record) { r.Sensitivity = "internal"; r.Sources = refs(dependency) })
	enableEvidenceSelection(t, f)
	m.before = func() { f.editRecord("sources", dependency.ID, func(r *Record) { r.ContentErased = true }) }
	out, err := f.svc.Search(testCtx, f.human, in)
	if err == nil || m.calls != 1 || len(out.Items)+len(out.Conversation) != 0 {
		t.Fatal("source dependency mutation escaped final fence", err, out, m.calls)
	}
}

func TestEvidenceSelectionPrivateQueryNeverCalls(t *testing.T) {
	f, m, in := evidenceSelectionFixture(t, 12)
	in.Query += " <private>unshared condition</private>"
	base, err := f.svc.Search(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	enableEvidenceSelection(t, f)
	out, err := f.svc.Search(testCtx, f.human, in)
	if err != nil || out.Explain.EvidenceSelection != "denied" || m.calls != 0 || !reflect.DeepEqual(evidenceReturned(base), evidenceReturned(out)) {
		t.Fatal("private query not gated", err, out.Explain, m.calls)
	}
}
