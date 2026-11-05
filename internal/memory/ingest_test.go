package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

const extractTestClaim = `{"content":"배포 전에 항상 테스트를 실행한다","kind":"procedural","semantic_kind":"lesson","evidence_kind":"user_statement","occurred_from":"","occurred_to":"","evidence_quote":"배포 전에 항상 테스트를 실행한다.","confidence":0.8}`

// candidateByText is the newest candidate with the text.
func candidateByText(f *fixture, text string) Record {
	out := Record{}
	for _, c := range f.repo.rows("memory_candidates") {
		if c.Text == text && !c.CreatedAt.Before(out.CreatedAt) {
			out = c
		}
	}
	return out
}

func TestIngestChunksModeLabelsAndDedupe(t *testing.T) {
	f, _ := wikiFixture(t)
	f.svc.Wiki = nil // chunks mode stores the text itself and needs no model
	putSettings(t, f, scopeA, SpaceConfigInput{ExtractionMode: ptr(spaceExtractChunks)})
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	item := IngestItem{Content: "Project Falcon ships on Fridays.", Context: "release channel", Timestamp: &at, Tags: []string{"release"},
		Metadata: map[string]string{"channel": "slack"}, Entities: []string{"Falcon"}}
	out, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Items: []IngestItem{item, {Content: "Second note about tabs."}}})
	if err != nil || len(out.Items) != 2 || out.Items[0].CandidatesCreated != 1 || out.Items[1].CandidatesCreated != 1 || out.Items[0].Status != JobStatusDone {
		t.Fatalf("ingest %+v %v", out, err)
	}
	c := candidateByText(f, "Project Falcon ships on Fridays.")
	if !slices.ContainsFunc(c.Entities, func(e EntityInput) bool { return e.Name == "Falcon" }) {
		t.Fatalf("entities %+v", c.Entities)
	}
	for _, tag := range []string{"release", "meta:channel=slack"} {
		if !slices.Contains(c.Tags, tag) {
			t.Fatalf("tags %v miss %s", c.Tags, tag)
		}
	}
	if c.Generator == nil || c.Generator.Model != "none" || c.MemoryContext == nil || c.MemoryContext.ObservedAt == nil || !c.MemoryContext.ObservedAt.Equal(at) {
		t.Fatalf("candidate %+v", c)
	}
	// The same text again names the same document: nothing changed, nothing new.
	again, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Items: []IngestItem{item}})
	if err != nil || again.Items[0].CandidatesCreated != 0 || again.Items[0].Delta.ChunksUnchanged != 1 || len(f.repo.rows("memory_candidates")) != 2 {
		t.Fatalf("again %+v %v", again, err)
	}
}

func TestIngestRejectsBadItemsAndStoresNothing(t *testing.T) {
	f, llm := wikiFixture(t)
	future := time.Now().Add(time.Hour)
	bad := []IngestItem{
		{Content: " "},
		{Content: "x", Tags: []string{"event:forged"}},
		{Content: "x", Metadata: map[string]string{"Bad Key": "v"}},
		{Content: "x", Timestamp: &future},
		{Content: "x", UpdateMode: "merge"},
		{Content: "x", Strategy: "missing"},
		{Content: "x", Entities: []string{"api_key=sk-abcdefghijklmnop1234"}},
	}
	for _, it := range bad {
		if _, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Items: []IngestItem{{Content: "fine"}, it}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", it, err)
		}
	}
	if _, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Items: make([]IngestItem, ingestMaxItems+1)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("too many: %v", err)
	}
	if extractRows(f) != 0 || len(llm.prompts) != 0 {
		t.Fatalf("stored %d rows, %d prompts", extractRows(f), len(llm.prompts))
	}
}

func TestIngestDocumentAppendAndAsync(t *testing.T) {
	f, llm := wikiFixture(t, extractAnswer(), extractAnswer(), extractAnswer())
	first, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Items: []IngestItem{{Content: "Monday: kickoff.", DocumentID: "standup"}}})
	if err != nil || len(llm.prompts) != 1 {
		t.Fatalf("first %+v %v", first, err)
	}
	next, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Items: []IngestItem{{Content: "Tuesday: review.", DocumentID: "standup", UpdateMode: "append"}}})
	d := next.Items[0].Delta
	if err != nil || len(llm.prompts) != 2 || next.Items[0].DocumentID != first.Items[0].DocumentID || d.ChunksUnchanged != 1 || d.ChunksChanged != 1 || d.ChunksRemoved != 0 ||
		!strings.Contains(llm.prompts[1], "Tuesday") || strings.Contains(llm.prompts[1], "Monday") {
		t.Fatalf("append %+v %v prompts %v", next, err, llm.prompts)
	}
	queued, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Async: true, Items: []IngestItem{{Content: "Wednesday: ship."}}})
	if err != nil || queued.Items[0].Status != JobStatusPending || len(llm.prompts) != 2 {
		t.Fatalf("async %+v %v", queued, err)
	}
	ran, err := f.svc.RunFileExtractJob(testCtx, f.human, scopeA, queued.Items[0].JobID)
	if err != nil || ran.Status != JobStatusDone || len(llm.prompts) != 3 {
		t.Fatalf("run %+v %v", ran, err)
	}
}

func TestExtractPreviewPromptOnlyStrategiesAndOverrides(t *testing.T) {
	f, llm := wikiFixture(t)
	putSettings(t, f, scopeA, SpaceConfigInput{ExtractionMission: ptr("Remember design rules for the app team."), DefaultTimezone: ptr("Asia/Seoul"),
		ExtractionStrategies: &map[string]ExtractStrategy{"brief": {Mode: spaceExtractCustom, Instructions: "Only rules about color."}}})
	at := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	out, err := f.svc.ExtractPreview(testCtx, f.human, ExtractPreviewInput{Scope: scopeA, PromptOnly: true, Strategy: "brief", Context: "design channel", Timestamp: &at})
	p := out.Prompt
	if err != nil || p == nil || len(llm.prompts) != 0 || !strings.Contains(p.System, "Remember design rules") || !strings.Contains(p.System, "Only rules about color.") ||
		!strings.Contains(p.User, "Reference time: 2026-10-26T10:00:00+09:00 (Asia/Seoul)") || !strings.Contains(p.User, "design channel") ||
		p.Strategy != "brief" || !slices.Equal(p.Strategies, []string{"brief"}) || p.Settings.Mode != spaceExtractCustom || p.Timezone != "Asia/Seoul" {
		t.Fatalf("prompt %+v %v", out, err)
	}
	f.svc.Wiki = nil // a prompt preview needs no model, and neither does a chunks-mode dry run
	if _, err = f.svc.ExtractPreview(testCtx, f.human, ExtractPreviewInput{Scope: scopeA, PromptOnly: true}); err != nil {
		t.Fatal(err)
	}
	dry, err := f.svc.ExtractPreview(testCtx, f.human, ExtractPreviewInput{Scope: scopeA, Text: extractKoreanSource, Overrides: &ExtractStrategy{Mode: spaceExtractChunks}})
	if err != nil || len(dry.Claims) != 1 || !strings.Contains(dry.Claims[0].Content, "주황 강조선") || extractRows(f) != 0 {
		t.Fatalf("chunks dry run %+v %v", dry, err)
	}
	if _, err = f.svc.ExtractPreview(testCtx, f.human, ExtractPreviewInput{Scope: scopeA, Text: extractKoreanSource}); !errors.Is(err, ErrModelNotConfigured) {
		t.Fatalf("no model: %v", err)
	}
}

func TestExtractUsesItemTimestampInSpaceZone(t *testing.T) {
	f, llm := wikiFixture(t, extractAnswer(strings.Replace(extractRuleClaim, `"kind":"user"`, `"kind":"episodic"`, 1)))
	putSettings(t, f, scopeA, SpaceConfigInput{DefaultTimezone: ptr("Asia/Seoul")})
	at := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	if _, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Items: []IngestItem{{Content: extractKoreanSource, Timestamp: &at}}}); err != nil {
		t.Fatal(err)
	}
	seoul, _ := time.LoadLocation("Asia/Seoul")
	c := candidateByText(f, "선택 상태에 주황 강조선을 쓰지 않는다")
	if !strings.Contains(llm.prompts[0], "Reference time: 2026-10-26T10:00:00+09:00 (Asia/Seoul)") || c.Occurred == nil || !c.Occurred.From.Equal(time.Date(2026, 10, 25, 0, 0, 0, 0, seoul)) || c.Kind != "episodic" {
		t.Fatalf("candidate %+v", c)
	}
	res, _ := parseExtractedClaims(extractAnswer(extractRuleClaim, strings.Replace(extractRuleClaim, `"kind":"user"`, `"kind":"episodic"`, 1)), extractKoreanSource, "chunk:1", at)
	if res.Claims[0].FactType != "world" || res.Claims[1].FactType != "experience" {
		t.Fatalf("fact types %+v", res.Claims)
	}
}

func TestExtractCausalCandidatesAreOptIn(t *testing.T) {
	answer := `{"claims":[` + extractRuleClaim + `,` + extractTestClaim + `],"causes":[{"cause":1,"effect":0,"reason":"Testing first led to the rule."},{"cause":0,"effect":7,"reason":"bad"}]}`
	f, llm := wikiFixture(t, answer, answer)
	if _, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Items: []IngestItem{{Content: extractKoreanSource, DocumentID: "a"}}}); err != nil {
		t.Fatal(err)
	}
	if len(f.repo.rows(colMemoryRelations)) != 0 || strings.Contains(llm.prompts[0], `"causes"`) {
		t.Fatal("causes stored or asked for while the setting is off")
	}
	on := true
	putSettings(t, f, scopeA, SpaceConfigInput{ExtractCausal: &on})
	out, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Items: []IngestItem{{Content: extractKoreanSource + "\n\n추가", DocumentID: "b"}}})
	rows := f.repo.rows(colMemoryRelations)
	cause, effect := candidateByText(f, "배포 전에 항상 테스트를 실행한다"), candidateByText(f, "선택 상태에 주황 강조선을 쓰지 않는다")
	if err != nil || !strings.Contains(llm.prompts[1], `"causes"`) || len(rows) != 1 || rows[0].Status != "candidate" || rows[0].Relations[0].ID != cause.ID || rows[0].Relations[1].ID != effect.ID ||
		!slices.ContainsFunc(out.Items[0].Skipped, func(s ExtractSkip) bool { return s.Reason == "malformed_cause" }) {
		t.Fatalf("causal rows %+v out %+v %v", rows, out, err)
	}
	// The link waits for both claims: approving them makes it reviewable like any proposal.
	for _, c := range []Record{cause, effect} {
		if _, err = f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"}); err != nil {
			t.Fatal(err)
		}
	}
	link, err := f.svc.ReviewCausalLink(testCtx, f.human, rows[0].ID, CausalReviewInput{Scope: scopeA, ExpectedRevision: 1, Action: "approve"})
	if err != nil || link.Status != statusActive {
		t.Fatalf("approve link %+v %v", link, err)
	}
}

type fakeBatchLLM struct {
	fakeWikiLLM
	submitted []BatchPrompt
	done      bool
}

func (f *fakeBatchLLM) Batches() bool { return true }
func (f *fakeBatchLLM) SubmitBatch(_ context.Context, p []BatchPrompt, _ int) (string, error) {
	f.submitted = p
	return "batch-1", nil
}
func (f *fakeBatchLLM) BatchResults(_ context.Context, id string) (map[string]BatchAnswer, bool, error) {
	if id != "batch-1" || !f.done {
		return nil, false, nil
	}
	out := map[string]BatchAnswer{}
	for _, p := range f.submitted {
		out[p.ID] = BatchAnswer{Text: extractAnswer(), Usage: ProviderUsage{Provider: wikiProvider, Model: "fake-wiki", Requests: 1, InputTokens: 10, OutputTokens: 5, TotalTokens: 15}}
	}
	return out, true, nil
}

func TestExtractJobUsesProviderBatch(t *testing.T) {
	f, _ := wikiFixture(t)
	llm := &fakeBatchLLM{}
	f.svc.Wiki = llm
	putSettings(t, f, scopeA, SpaceConfigInput{ExtractionChunkSize: ptr(500)})
	text := strings.Repeat(strings.Repeat("alpha beta gamma ", 25)+"\n\n", 6)
	out, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Async: true, Items: []IngestItem{{Content: text}}})
	if err != nil || out.Items[0].Status != JobStatusPending {
		t.Fatalf("ingest %+v %v", out, err)
	}
	id := out.Items[0].JobID
	for range 2 { // submitted, then still running at the provider
		view, err := f.svc.RunFileExtractJob(testCtx, f.human, scopeA, id)
		if err != nil || view.Status != JobStatusRetry || view.Job.Error != extractBatchPending || view.Job.Batch == nil || len(llm.submitted) < 3 || len(llm.prompts) != 0 {
			t.Fatalf("pending %+v %v", view, err)
		}
	}
	llm.done = true
	view, err := f.svc.RunFileExtractJob(testCtx, f.human, scopeA, id)
	if err != nil || view.Status != JobStatusDone || view.Job.Calls != len(llm.submitted) || len(f.repo.rows(colProviderUsage)) != len(llm.submitted) || len(llm.prompts) != 0 {
		t.Fatalf("done %+v %v ledger %d", view, err, len(f.repo.rows(colProviderUsage)))
	}
}

func TestChatWikiLLMBatchProtocol(t *testing.T) {
	var srv *httptest.Server
	results := ""
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/messages/batches":
			var body struct {
				Requests []struct {
					CustomID string `json:"custom_id"`
				} `json:"requests"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Requests) != 2 || r.Header.Get("X-Api-Key") != "k" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"id":"b1"}`))
		case r.URL.Path == "/v1/messages/batches/b1":
			_, _ = w.Write([]byte(`{"processing_status":"ended","results_url":"` + results + `"}`))
		case r.URL.Path == "/results/b1":
			_, _ = w.Write([]byte(`{"custom_id":"chunk-0","result":{"type":"succeeded","message":{"content":[{"type":"text","text":"{\"claims\":[]}"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}}}` + "\n" +
				`{"custom_id":"chunk-1","result":{"type":"errored"}}` + "\n"))
		}
	}))
	defer srv.Close()
	c := &ChatWikiLLM{Config: LLMConfig{Endpoint: srv.URL + "/v1/messages", APIKey: "k", Model: "m", Protocol: llmProtocolMessages}, Batch: true}
	id, err := c.SubmitBatch(testCtx, []BatchPrompt{{ID: "chunk-0", System: "s", User: "u"}, {ID: "chunk-1", System: "s", User: "u"}}, 100)
	if err != nil || id != "b1" {
		t.Fatalf("submit %q %v", id, err)
	}
	results = srv.URL + "/results/b1"
	got, done, err := c.BatchResults(testCtx, id)
	if err != nil || !done || got["chunk-0"].Text != `{"claims":[]}` || got["chunk-0"].Usage.TotalTokens != 5 || !got["chunk-1"].Failed {
		t.Fatalf("results %+v %v %v", got, done, err)
	}
	results = "https://elsewhere.example/results/b1" // the key never goes to another host
	if _, _, err = c.BatchResults(testCtx, id); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("foreign results url: %v", err)
	}
	if (&ChatWikiLLM{Config: LLMConfig{Protocol: ""}, Batch: true}).Batches() || (&ChatWikiLLM{Config: c.Config}).Batches() {
		t.Fatal("batches on without opt-in or protocol")
	}
}

func TestExtractionSettingsValidation(t *testing.T) {
	f := newFixture(t)
	for _, in := range []SpaceConfigInput{
		{DefaultTimezone: ptr("Local")},
		{DefaultTimezone: ptr("Mars/Base")},
		{ExtractionChunkSize: ptr(100)},
		{ExtractionStrategies: &map[string]ExtractStrategy{"Bad Name": {}}},
		{ExtractionStrategies: &map[string]ExtractStrategy{"x": {Mode: "poetry"}}},
		{ExtractionDefaultStrategy: ptr("Bad Name")}, // a valid name may live in a template layer; an unknown one fails at extraction
		{ExtractionMission: ptr("api_key=sk-abcdefghijklmnop1234")},
	} {
		if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, in, false); err == nil {
			t.Fatalf("accepted %+v", in)
		}
	}
}
