//go:build linux

package memory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
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
		Metadata: map[string]string{"channel": "chat"}, Entities: []string{"Falcon"}}
	out, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Items: []IngestItem{item, {Content: "Second note about tabs."}}})
	if err != nil || len(out.Items) != 2 || out.Items[0].CandidatesCreated != 1 || out.Items[1].CandidatesCreated != 1 || out.Items[0].Status != JobStatusDone {
		t.Fatalf("ingest %+v %v", out, err)
	}
	c := candidateByText(f, "Project Falcon ships on Fridays.")
	if !slices.ContainsFunc(c.Entities, func(e EntityInput) bool { return e.Name == "Falcon" }) {
		t.Fatalf("entities %+v", c.Entities)
	}
	for _, tag := range []string{"release", "meta:channel=chat"} {
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
	// Batch answers are metered like direct calls: one model call trace each.
	if traces := f.repo.rows(colLLMRequests); len(traces) != len(llm.submitted) || traces[0].LLMRequest.Operation != fileExtractOperation {
		t.Fatalf("batch traces %d of %d", len(traces), len(llm.submitted))
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

func TestIngestImageBlocksAndPerItemResults(t *testing.T) {
	claim := `{"content":"금요일에는 배포하지 않는다","kind":"decision","semantic_kind":"preference","evidence_kind":"inference","evidence_quote":"금요일 배포 금지","confidence":0.8}`
	f, llm := mediaFixture(t, extractAnswer(claim))
	png := FileBlock{Type: "image", MIME: "image/png", ContentBase64: base64.StdEncoding.EncodeToString([]byte("PNGDATA"))}
	// Without the vision opt-in an image block is refused before anything is stored.
	if _, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Items: []IngestItem{{Blocks: []FileBlock{png}}}}); !errors.Is(err, errUnsupportedFormat) {
		t.Fatalf("no vision: %v", err)
	}
	for _, bad := range [][]IngestItem{
		{{Content: "x", Blocks: []FileBlock{png}}},
		{{Blocks: []FileBlock{{Type: "image", MIME: "image/gif", ContentBase64: png.ContentBase64}}}},
		{{Blocks: []FileBlock{{Type: "text", Text: "x", MIME: "image/png"}}}},
		{{Blocks: slices.Repeat([]FileBlock{png}, fileVisionPagesDefault+1)}},
	} {
		if _, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Items: bad}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	if extractRows(f) != 0 || len(ledgerOps(f)) != 0 {
		t.Fatal("stored or called a model")
	}
	v := &fakeVisionLLM{fakeWikiLLM: llm, reads: []string{"금요일 배포 금지\n<private>집 주소 서울</private>"}}
	f.svc.Wiki = v
	// The second item's image cannot be read: it fails on its own, the first stays stored.
	out, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Items: []IngestItem{
		{Blocks: []FileBlock{{Type: "text", Text: "Board notes:"}, png}}, {Blocks: []FileBlock{png}}}})
	if err != nil || len(out.Items) != 2 || out.Items[0].Status != JobStatusDone || out.Items[0].CandidatesCreated != 1 || out.Items[0].Error != "" ||
		out.Items[1].Status != JobStatusFailed || out.Items[1].Error == "" || out.Items[1].JobID != "" {
		t.Fatalf("ingest %+v %v", out, err)
	}
	if len(llm.prompts) != 1 || !strings.Contains(llm.prompts[0], "Board notes:\n\n[image 2]\n금요일 배포 금지") || strings.Contains(llm.prompts[0], "집 주소") {
		t.Fatalf("prompts %q", llm.prompts)
	}
}

func TestIngestLaterItemFailureKeepsEarlierItemWithItsOwnStatus(t *testing.T) {
	f, llm := wikiFixture(t, extractAnswer(extractTestClaim))
	src := "배포 전에 항상 테스트를 실행한다."
	// The second item's model call has no answer left: its job is stored failed and resumable.
	out, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Items: []IngestItem{{Content: src}, {Content: src + " 두 번째"}}})
	if err != nil || out.Items[0].Status != JobStatusDone || out.Items[0].CandidatesCreated != 1 || out.Items[1].Status != JobStatusFailed ||
		out.Items[1].JobID == "" || out.Items[1].Error == "" || len(llm.prompts) != 2 {
		t.Fatalf("ingest %+v %v", out, err)
	}
	// Every item failing before anything was stored fails the request.
	putSettings(t, f, scopeA, SpaceConfigInput{DefenseRules: &[]SpaceDefenseRule{{Pattern: "금지어", Action: "block"}}})
	if _, err = f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Items: []IngestItem{{Content: "금지어 포함"}}}); !errors.Is(err, ErrDefenseBlocked) {
		t.Fatalf("all failed: %v", err)
	}
}

// countingBatchLLM accepts every batch and never ends one.
type countingBatchLLM struct {
	fakeWikiLLM
	mu      sync.Mutex
	batches int
}

func (f *countingBatchLLM) Batches() bool { return true }

// Generator is slow so that two reservations overlap unless they are serialized.
func (f *countingBatchLLM) Generator() Generator {
	time.Sleep(2 * time.Millisecond)
	return f.fakeWikiLLM.Generator()
}
func (f *countingBatchLLM) SubmitBatch(context.Context, []BatchPrompt, int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches++
	return "batch-" + strconv.Itoa(f.batches), nil
}
func (f *countingBatchLLM) BatchResults(context.Context, string) (map[string]BatchAnswer, bool, error) {
	return nil, false, nil
}

func TestExtractBatchesReserveTheDailyCapAtomically(t *testing.T) {
	f, _ := wikiFixture(t)
	llm := &countingBatchLLM{}
	f.svc.Wiki = llm
	putSettings(t, f, scopeA, SpaceConfigInput{ExtractionChunkSize: ptr(500)})
	n := wikiDailyCallLimit/2 + 1 // two such batches do not fit in one day
	ids := []string{}
	for i := range 2 {
		text := strings.Repeat(strings.Repeat("alpha beta gamma ", 25)+strconv.Itoa(i)+"\n\n", n)
		out, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Async: true, Items: []IngestItem{{Content: text}}})
		if err != nil || out.Items[0].Status != JobStatusPending {
			t.Fatalf("ingest %+v %v", out, err)
		}
		ids = append(ids, out.Items[0].JobID)
	}
	views := make([]FileExtractJobView, 2)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Go(func() { views[i], _ = f.svc.RunFileExtractJob(testCtx, f.human, scopeA, id) })
	}
	wg.Wait()
	pending, budget := 0, 0
	for _, v := range views {
		switch {
		case extractBatchWaiting(v) && len(v.Job.Batch.Reserved) == n:
			pending++
		case v.Status == JobStatusFailed && v.Job.Error == "model_budget_exceeded":
			budget++
		}
	}
	if pending != 1 || budget != 1 || llm.batches != 1 || len(f.repo.rows(colProviderUsage)) != n {
		t.Fatalf("pending %d budget %d batches %d ledger %d views %+v", pending, budget, llm.batches, len(f.repo.rows(colProviderUsage)), views)
	}
	// Cancelling the waiting batch closes its reserved rows: none stays pending.
	for i, v := range views {
		if extractBatchWaiting(v) {
			if _, err := f.svc.CancelOperation(testCtx, f.human, ids[i], OperationChangeInput{Scope: scopeA}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, r := range f.repo.rows(colProviderUsage) {
		if r.Status == JobStatusPending {
			t.Fatalf("a cancelled batch left a pending ledger row %+v", r)
		}
	}
}

func TestExtractBatchResumesOnOwnerPollAndDropsWhenTurnedOff(t *testing.T) {
	f, _ := wikiFixture(t)
	llm := &fakeBatchLLM{}
	f.svc.Wiki = llm
	putSettings(t, f, scopeA, SpaceConfigInput{ExtractionChunkSize: ptr(500)})
	text := strings.Repeat(strings.Repeat("alpha beta gamma ", 25)+"\n\n", 3)
	queue := func(content string) string {
		out, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Async: true, Items: []IngestItem{{Content: content}}})
		if err != nil {
			t.Fatal(err)
		}
		if v, err := f.svc.RunFileExtractJob(testCtx, f.human, scopeA, out.Items[0].JobID); err != nil || !extractBatchWaiting(v) {
			t.Fatalf("submit %+v %v", v, err)
		}
		return out.Items[0].JobID
	}
	id := queue(text)
	// No loop follows the job (as after a restart); the owner's poll starts one.
	llm.done = true
	if _, err := f.svc.GetFileExtractJob(testCtx, f.human, scopeA, id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, err := f.svc.extractJob(testCtx, f.svc.Repo, scopeA, id)
		if err == nil && r.Status == JobStatusDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not resumed: %+v %v", r, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, r := range f.repo.rows(colProviderUsage) {
		if r.Status != JobStatusDone {
			t.Fatalf("ledger row %+v", r)
		}
	}
	// Batching turned off while a batch waits: its rows close as failed, the job goes on per chunk.
	llm.done = false
	id = queue(text + "x")
	plain := &fakeWikiLLM{answers: []string{extractAnswer(), extractAnswer(), extractAnswer()}}
	f.svc.Wiki = plain
	v, err := f.svc.RunFileExtractJob(testCtx, f.human, scopeA, id)
	failed := 0
	for _, r := range f.repo.rows(colProviderUsage) {
		if r.Status == JobStatusFailed {
			failed++
		}
	}
	if err != nil || v.Status != JobStatusDone || v.Job.Batch != nil || len(plain.prompts) != 3 || failed != 3 {
		t.Fatalf("dropped %+v %v prompts %d failed rows %d", v, err, len(plain.prompts), failed)
	}
}

func TestCaptureLabelsPassDefenseRules(t *testing.T) {
	f, _ := wikiFixture(t)
	putSettings(t, f, scopeA, SpaceConfigInput{ExtractionMode: ptr(spaceExtractChunks),
		DefenseRules: &[]SpaceDefenseRule{{Pattern: "codename", Action: "redact"}, {Pattern: "forbidden", Action: "block"}}})
	labels := []string{"codename-x"}
	out, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Items: []IngestItem{{Content: "Plain note.", Tags: labels, Entities: []string{"codename"},
		Metadata: map[string]string{"team": "codename team"}}}})
	if err != nil || out.Items[0].CandidatesCreated != 1 || labels[0] != "codename-x" {
		t.Fatalf("ingest %+v %v %v", out, err, labels)
	}
	c := candidateByText(f, "Plain note.")
	if len(c.Entities) != 1 || c.Entities[0].Name != "[redacted]" {
		t.Fatalf("entities %+v", c.Entities)
	}
	for _, tag := range []string{"[redacted]-x", "meta:team=[redacted] team"} {
		if !slices.Contains(c.Tags, tag) {
			t.Fatalf("tags %v miss %s", c.Tags, tag)
		}
	}
	out, err = f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Items: []IngestItem{{Content: "Other note.", Tags: []string{"forbidden"}}}})
	if err != nil || out.Items[0].CandidatesCreated != 0 {
		t.Fatalf("blocked label %+v %v", out, err)
	}
}

func (f *fakeRepo) WaitingExtractBatches(_ context.Context, limit int) ([]Record, error) {
	out := []Record{}
	for _, r := range f.rows(colJobs) {
		if r.Kind == fileExtractJobKind && r.Status == JobStatusRetry && r.FileExtract != nil && r.FileExtract.Batch != nil && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

func TestWorkerResumesWaitingExtractBatchAfterRestart(t *testing.T) {
	f, _ := wikiFixture(t)
	llm := &fakeBatchLLM{}
	f.svc.Wiki = llm
	putSettings(t, f, scopeA, SpaceConfigInput{ExtractionChunkSize: ptr(500)})
	out, err := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Async: true, Items: []IngestItem{{Content: strings.Repeat(strings.Repeat("alpha beta gamma ", 25)+"\n\n", 3)}}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Items[0].JobID
	if v, err := f.svc.RunFileExtractJob(testCtx, f.human, scopeA, id); err != nil || !extractBatchWaiting(v) {
		t.Fatalf("submit %+v %v", v, err)
	}
	llm.done = true
	// A worker that may not write candidates leaves the job for its owner.
	worker := f.auth.actors[f.work]
	f.auth.actors[f.work] = actor("worker-1", roleService, true, "read", "embed")
	if n, err := f.svc.ResumeExtractBatches(testCtx, f.work); err != nil || n != 0 {
		t.Fatalf("resumed without the grant: %d %v", n, err)
	}
	f.auth.actors[f.work] = worker
	if n, err := f.svc.ResumeExtractBatches(testCtx, f.work); err != nil || n != 1 {
		t.Fatalf("resumed %d %v", n, err)
	}
	r, err := f.svc.extractJob(testCtx, f.svc.Repo, scopeA, id)
	if err != nil || r.Status != JobStatusDone {
		t.Fatalf("job %s %v", r.Status, err)
	}
	for _, row := range f.repo.rows(colProviderUsage) {
		if row.Status != JobStatusDone {
			t.Fatalf("ledger row %+v", row)
		}
	}
	// The worker may not run someone's job that is not waiting on a batch.
	f.editRecord(colJobs, id, func(r *Record) { r.Status = JobStatusFailed })
	if _, err := f.svc.RunFileExtractJob(testCtx, f.work, scopeA, id); !errors.Is(err, ErrDenied) {
		t.Fatalf("the worker ran a job not waiting on a batch: %v", err)
	}
}
