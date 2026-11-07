package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tiktoken-go/tokenizer"
)

func TestContextDeliveryConversationHTTPAndBudget(t *testing.T) {
	f := newFixture(t)
	setCaptureDensity(t, f, captureDensityConversation)
	source := conversationTurn(f, "Avery: the copper valve arrived Friday.")
	var wireCases []apiContractCase
	for _, on := range []bool{false, true} {
		body := map[string]any{"scope": scopeA, "task": "copper valve", "modes": []string{"fts"}, "token_encoding": "cl100k_base", "budget_tokens": 2000, "conversation_context": on}
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/context/build", strings.NewReader(structuredText(body)))
		req.Header.Set("Authorization", "Bearer "+f.human)
		Handler(f.svc).ServeHTTP(w, req)
		var out ContextPack
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		wireCases = append(wireCases, apiContractCase{Operation: "POST /v1/context/build", Status: w.Code,
			Body: append(json.RawMessage(nil), w.Body.Bytes()...), Request: json.RawMessage(structuredText(body))})
		var data ContextBundle
		if json.Unmarshal([]byte(out.ContextData), &data) != nil {
			t.Fatal(out.ContextData)
		}
		if on && (len(data.Conversation) != 1 || data.Conversation[0].ID != source.ID || data.Conversation[0].Authority != AuthorityDataOnly) {
			t.Fatal("source did not reach consumed data", out)
		}
		if !on && len(data.Conversation) != 0 {
			t.Fatal("opt-in ignored")
		}
		codec, _ := tokenizer.Get(tokenizer.Cl100kBase)
		n, _ := codec.Count(out.ContextData)
		if n != out.TokensUsed || n > 2000 {
			t.Fatal(n, out.TokensUsed)
		}
	}
	if dest := os.Getenv("ARKISM_CONTEXT_DELIVERY_CASES"); dest != "" {
		encoded, err := json.Marshal(wireCases)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(dest, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	q := SearchInput{Scope: scopeA, Query: "copper valve", Modes: []string{"fts"}, BudgetTokens: 50, TokenEncoding: "cl100k_base", ConversationContext: true, Explain: true}
	out := f.search(f.human, q)
	if len(out.Conversation) != 0 || !strings.Contains(out.ContextData, "items") || out.Explain.Dropped["conversation_token_budget"] != 1 {
		t.Fatal("over-budget source not accounted", out)
	}
	q.BudgetTokens = 2000
	q.Destination = "external:test"
	if out := f.search(f.human, q); len(out.Conversation) != 0 {
		t.Fatal("external source disclosure")
	}
}

func TestContextDeliveryWholeFactsSurviveCharacterThenTokenBudget(t *testing.T) {
	f := newFixture(t)
	f.activeMemory("copper " + strings.Repeat("long fact ", 100))
	b := f.activeMemory("copper arrived Friday")
	c := f.activeMemory("copper valve is upstairs")
	q := SearchInput{Scope: scopeA, Query: "copper", Modes: []string{"fts"}, Limit: 3, MaxChars: 60, BudgetTokens: 3000, TokenEncoding: "cl100k_base", WholeItems: true, Explain: true}
	out := f.search(f.human, q)
	if len(out.Items) != 2 || !slices.Contains(ids(out.Items), b.ID) || !slices.Contains(ids(out.Items), c.ID) || out.Explain.Dropped["character_budget"] != 1 {
		t.Fatal("whole facts lost", ids(out.Items), out.Explain)
	}
	if !strings.Contains(out.ContextData, "Friday") || !strings.Contains(out.ContextData, "upstairs") {
		t.Fatal("final consumed data lost facts")
	}
}

type unavailableOptionalRepo struct {
	*fakeRepo
	err error
}

func (r unavailableOptionalRepo) MemoryNeighbors(context.Context, Scope, []string, int) ([]string, error) {
	return nil, r.err
}
func (r unavailableOptionalRepo) SearchMemoriesInWindow(context.Context, SearchInput, time.Time, time.Time) ([]Hit, error) {
	return nil, r.err
}
func TestContextDeliveryBestEffortPreservesBaseAndDenials(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("copper valve repair")
	f.svc.RecallGraph = true
	f.svc.Repo = unavailableOptionalRepo{f.repo, ErrUnavailable}
	q := SearchInput{Scope: scopeA, Query: "copper valve", Limit: 3, BestEffortLanes: true}
	out, err := f.svc.Search(testCtx, f.human, q)
	if err != nil || !slices.Contains(ids(out.Items), m.ID) || !slices.Contains(out.Degraded, reasonGraph) {
		t.Fatal(ids(out.Items), out.Degraded, err)
	}
	q.BestEffortLanes = false
	if _, err = f.svc.Search(testCtx, f.human, q); !errors.Is(err, ErrUnavailable) {
		t.Fatal("default failure changed", err)
	}
	q.BestEffortLanes = true
	f.svc.Repo = unavailableOptionalRepo{f.repo, ErrDenied}
	if _, err = f.svc.Search(testCtx, f.human, q); !errors.Is(err, ErrDenied) {
		t.Fatal("denial swallowed", err)
	}
	q.Query = "copper yesterday"
	f.svc.Repo = unavailableOptionalRepo{f.repo, ErrUnavailable}
	if out, err = f.svc.Search(testCtx, f.human, q); err != nil || !slices.Contains(out.Degraded, "temporal") {
		t.Fatal(out.Degraded, err)
	}
}

type directConversationRepo struct {
	*fakeRepo
	neighbors []Record
	calls     int
}

func (r *directConversationRepo) ConversationNeighbors(context.Context, Record) ([]Record, error) {
	r.calls++
	return r.neighbors, nil
}
func TestContextDeliveryDirectNeighborsBeyondScanLimit(t *testing.T) {
	f := newFixture(t)
	setCaptureDensity(t, f, captureDensityConversation)
	a := conversationBatch(f, "Rowan: copper washer required.", "long-session", 100)
	b := conversationBatch(f, "Avery: upstairs beside the valve.", "long-session", 101)
	for i := 0; i < 150; i++ {
		conversationBatch(f, "Other: unrelated batch.", "other-session", i)
	}
	repo := &directConversationRepo{fakeRepo: f.repo, neighbors: []Record{b}}
	f.svc.Repo = repo
	q := SearchInput{Scope: scopeA, Query: "copper washer", Modes: []string{"fts"}, Include: []string{"sources"}, Limit: 3}
	out := f.search(f.human, q)
	if len(out.Conversation) != 1 || out.Conversation[0].ID != a.ID || !strings.Contains(out.Conversation[0].Text, "upstairs") || repo.calls != 1 {
		t.Fatal("direct neighbor missing", out.Conversation, repo.calls)
	}
	f.editRecord("sources", b.ID, func(r *Record) { r.ContentErased = true })
	if out = f.search(f.human, q); strings.Contains(out.Conversation[0].Text, "upstairs") {
		t.Fatal("erased neighbor leaked")
	}
}

func TestContextDeliverySourceRevocationAtCommit(t *testing.T) {
	f := newFixture(t)
	setCaptureDensity(t, f, captureDensityConversation)
	source := conversationTurn(f, "Avery: copper valve is upstairs.")
	repo := &graphTestRepo{Repository: f.repo}
	repo.beforeTx = func() { f.editRecord("sources", source.ID, func(r *Record) { r.ContentErased = true }) }
	f.svc.Repo = repo
	_, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "copper valve", Modes: []string{"fts"}, BudgetTokens: 3000, TokenEncoding: "cl100k_base", ConversationContext: true})
	if !errors.Is(err, ErrConflict) {
		t.Fatal("stale packed source accepted", err)
	}
}

func TestPostgresConversationNeighborsBoundedAndScoped(t *testing.T) {
	p, scope := openPostgresListsTest(t)
	at := time.Now().UTC().Truncate(time.Millisecond)
	makeRow := func(id, session string, offset int) Record {
		return Record{ID: id, Scope: scope, Kind: conversationSourceKind, Status: statusActive, Revision: 1, Text: "turn", SourceRef: session, OriginProduct: "test", CollectedAt: &at, CreatedAt: at.Add(time.Duration(offset) * time.Second), UpdatedAt: at}
	}
	before := makeRow("before", "same", 99)
	anchor := makeRow("anchor", "same", 100)
	after := makeRow("after", "same", 101)
	insertPGRecords(t, p, "sources", before, anchor, after)
	for i := 0; i < 1200; i++ {
		r := makeRow(ID(), "other", i)
		insertPGRecords(t, p, "sources", r)
	}
	for i := 1; i <= 150; i++ {
		insertPGRecords(t, p, "sources", makeRow(ID(), "same", -i))
	}
	rows, err := p.ConversationNeighbors(testCtx, anchor)
	if err != nil || len(rows) != 2 || rows[0].ID != after.ID || rows[1].ID != before.ID {
		t.Fatal(recordIDs(rows), err)
	}
	if _, err = p.pool.Exec(testCtx, "ANALYZE arkism.memory_records"); err != nil {
		t.Fatal(err)
	}
	planRows, err := p.pool.Query(testCtx, `EXPLAIN (ANALYZE, BUFFERS) SELECT payload FROM arkism.memory_records WHERE collection='sources' AND kind='conversation' AND workspace_id=$1 AND project_id=$2 AND user_id='' AND agent_id='' AND md5(doc->>'source_ref')=md5($3) AND md5(doc->>'origin_product')=md5('test') AND doc->>'source_ref'=$3 AND doc->>'origin_product'='test' AND (doc->'collected_at'->>'$date')::timestamptz=$4 AND (`+pgDate+`>$5 OR (`+pgDate+`=$5 AND id>$6)) ORDER BY `+pgDate+` ASC,id ASC LIMIT 1`, scope.WorkspaceID, scope.ProjectID, "same", at, anchor.CreatedAt, anchor.ID)
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	for planRows.Next() {
		var line string
		if err := planRows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, line)
	}
	planRows.Close()
	if err := planRows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan, "\n"), "memory_records_conversation_neighbors") {
		t.Fatal("session lookup index not used", plan)
	}
	t.Log(strings.Join(plan, "\n"))
}

func TestContextDeliveryWholeItemsMakesLongFactTradeoffExplicit(t *testing.T) {
	f := newFixture(t)
	long := f.activeMemory("copper unique answer " + strings.Repeat("detail ", 80))
	f.activeMemory("copper related short")
	q := SearchInput{Scope: scopeA, Query: "copper", Modes: []string{"fts"}, Limit: 3, MaxChars: 50, Explain: true, WholeItems: true}
	out := f.search(f.human, q)
	if slices.Contains(ids(out.Items), long.ID) || out.Explain.Dropped["character_budget"] != 1 {
		t.Fatal("long fact tradeoff not visible", out)
	}
	q.WholeItems = false
	out = f.search(f.human, q)
	if out.Explain.Dropped["character_truncated"] == 0 {
		t.Fatal("default truncation not explained", out.Explain)
	}
}

func TestContextDeliveryFrozenPinnedAndCharacterBudget(t *testing.T) {
	f := newFixture(t)
	setCaptureDensity(t, f, captureDensityConversation)
	conversationTurn(f, "Avery: copper valve is upstairs.")
	q := SearchInput{Scope: scopeA, Query: "copper valve", Modes: []string{"fts"}, BudgetTokens: 3000, TokenEncoding: "cl100k_base", ConversationContext: true, Explain: true, MaxChars: 5}
	out := f.search(f.human, q)
	if len(out.Conversation) != 0 || out.Explain.Dropped["conversation_character_budget"] != 1 {
		t.Fatal("character budget ignored", out)
	}
	q.MaxChars = 12000
	q.contextView = &ContextInvocationView{Protocol: ContextViewProtocol, ContextScope: scopeA, Exclude: []Ref{}}
	if out = f.search(f.human, q); len(out.Conversation) != 0 || strings.Contains(out.ContextData, "upstairs") {
		t.Fatal("frozen view source bypass", out)
	}
	for _, opt := range []string{"conversation_context", "whole_items", "best_effort_lanes"} {
		body := map[string]any{"scope": scopeA, "task": "copper valve", "agent": "test-agent", "token_encoding": "cl100k_base", opt: true}
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/context/build", strings.NewReader(structuredText(body)))
		req.Header.Set("Authorization", "Bearer "+f.human)
		Handler(f.svc).ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatal("unsupported pinned option silently ignored", opt, w.Code, w.Body.String())
		}
	}
}

func TestContextDeliveryBestEffortHTTPAndCancellation(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("copper valve repair")
	f.svc.RecallGraph = true
	f.svc.Repo = unavailableOptionalRepo{f.repo, ErrUnavailable}
	body := map[string]any{"scope": scopeA, "task": "copper valve", "token_encoding": "cl100k_base", "best_effort_lanes": true}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/context/build", strings.NewReader(structuredText(body)))
	req.Header.Set("Authorization", "Bearer "+f.human)
	Handler(f.svc).ServeHTTP(w, req)
	var out ContextPack
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || !out.Partial || !slices.Contains(ids(out.Items), m.ID) || !slices.Contains(out.Degraded, reasonGraph) {
		t.Fatal(w.Code, w.Body.String())
	}
	ctx, cancel := context.WithCancel(testCtx)
	cancel()
	if _, err := f.svc.Search(ctx, f.human, SearchInput{Scope: scopeA, Query: "copper", BestEffortLanes: true}); err == nil {
		t.Fatal("cancellation swallowed")
	}
}

func TestContextDeliveryDynamicPreservesExplicitOptions(t *testing.T) {
	for _, opt := range []string{"conversation", "whole", "best_effort"} {
		t.Run(opt, func(t *testing.T) {
			runtime := nativeFSRuntime(t, "on", "V3-P1-NOTIME")
			f, q, _ := nativeFSSearchFixture(t, runtime)
			switch opt {
			case "conversation":
				q.ConversationContext = true
			case "whole":
				q.WholeItems = true
			case "best_effort":
				q.BestEffortLanes = true
			}
			baseline, err := f.svc.Search(testCtx, "native", q)
			if err != nil {
				t.Fatal(err)
			}
			f.svc.NativeSearchShadow = runtime
			out, err := f.svc.Search(testCtx, "native", q)
			if err != nil {
				t.Fatal(err)
			}
			observations := runtime.Observations()
			if nativeFSStrip(out) != nativeFSStrip(baseline) || len(observations) != 1 || observations[0].Reason != "unsupported_context" || !observations[0].BaselineReturned || observations[0].Served {
				t.Fatal("delivery policy overridden", out, observations)
			}
		})
	}
}

func TestContextDeliveryWholeItemsHTTP(t *testing.T) {
	f := newFixture(t)
	long := f.activeMemory("copper unique answer " + strings.Repeat("detail ", 2000))
	short := f.activeMemory("copper valve upstairs")
	body := map[string]any{"scope": scopeA, "task": "copper", "token_encoding": "cl100k_base", "budget_tokens": 6000, "modes": []string{"fts"}, "whole_items": true, "explain": true}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/context/build", strings.NewReader(structuredText(body)))
	req.Header.Set("Authorization", "Bearer "+f.human)
	Handler(f.svc).ServeHTTP(w, req)
	var out ContextPack
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || slices.Contains(ids(out.Items), long.ID) || !slices.Contains(ids(out.Items), short.ID) || out.Explain == nil || out.Explain.Dropped["character_budget"] != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
}

type revokingConversationRepo struct {
	*directConversationRepo
	beforeTx func()
}

func (r *revokingConversationRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	if r.beforeTx != nil {
		f := r.beforeTx
		r.beforeTx = nil
		f()
	}
	return r.fakeRepo.Transaction(ctx, fn)
}
func TestContextDeliveryNeighborRevocationAtCommit(t *testing.T) {
	f := newFixture(t)
	setCaptureDensity(t, f, captureDensityConversation)
	conversationBatch(f, "Rowan: copper washer required.", "session", 100)
	neighbor := conversationBatch(f, "Avery: upstairs beside the valve.", "session", 101)
	r := &revokingConversationRepo{directConversationRepo: &directConversationRepo{fakeRepo: f.repo, neighbors: []Record{neighbor}}}
	r.beforeTx = func() { f.editRecord("sources", neighbor.ID, func(row *Record) { row.ContentErased = true }) }
	f.svc.Repo = r
	_, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "copper washer", Modes: []string{"fts"}, ConversationContext: true, BudgetTokens: 3000, TokenEncoding: "cl100k_base"})
	if !errors.Is(err, ErrConflict) {
		t.Fatal("erased appended source escaped final fence", err)
	}
}
