package memory

// The contract corpus goes through the production router, decoder, common
// authorization fence and service. Storage, notification and model providers
// are deterministic, owned fixtures; no user data or external service is used.
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/notifycfg"
)

type contractHTTP struct {
	t     *testing.T
	f     *fixture
	scope Scope
	cases []apiContractCase
	seen  map[string]bool
}

func (c *contractHTTP) call(op, id string, body any, extra url.Values) map[string]json.RawMessage {
	c.t.Helper()
	method, path, _ := strings.Cut(op, " ")
	path = strings.ReplaceAll(path, "{id}", id)
	q := url.Values{}
	if method == "GET" {
		q.Set("workspace_id", c.scope.WorkspaceID)
		q.Set("project_id", c.scope.ProjectID)
		q.Set("user_id", c.scope.UserID)
		q.Set("agent_id", c.scope.AgentID)
	}
	for k, v := range extra {
		q[k] = v
	}
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
	}
	// Every operation must reject the exact same scoped request anonymously.
	c.send(op, method, path, q, raw, "", 401)
	if method == "POST" {
		var malformed map[string]json.RawMessage
		if err := json.Unmarshal(raw, &malformed); err != nil {
			c.t.Fatal(err)
		}
		malformed["unexpected_contract_field"] = json.RawMessage("true")
		invalid, err := json.Marshal(malformed)
		if err != nil {
			c.t.Fatal(err)
		}
		c.send(op, method, path, q, invalid, c.f.human, 400)
	}

	out := c.send(op, method, path, q, raw, c.f.human, 200)
	c.seen[op] = true
	var result map[string]json.RawMessage
	if err := json.Unmarshal(out, &result); err != nil {
		c.t.Fatal(err)
	}
	return result
}

func (c *contractHTTP) send(op, method, path string, q url.Values, raw []byte, token string, want int) []byte {
	c.t.Helper()
	req := httptest.NewRequest(method, path+"?"+q.Encode(), strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	out := httptest.NewRecorder()
	Handler(c.f.svc).ServeHTTP(out, req)
	if out.Code != want {
		c.t.Fatalf("%s (%s?%s) want %d got %d: %s", op, path, q.Encode(), want, out.Code, out.Body.String())
	}
	if out.Header().Get("X-Request-ID") == "" || !strings.HasPrefix(out.Header().Get("Content-Type"), "application/json") {
		c.t.Fatalf("missing contract headers: %s", op)
	}
	record := apiContractCase{Operation: op, Status: out.Code, Body: append(json.RawMessage(nil), out.Body.Bytes()...), Query: q}
	if len(raw) > 0 {
		record.Request = append(json.RawMessage(nil), raw...)
	}
	c.cases = append(c.cases, record)
	return out.Body.Bytes()
}

func decodeContract[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (c *contractHTTP) seed(col string, r Record) {
	c.t.Helper()
	if err := c.f.repo.Insert(testCtx, col, r); err != nil {
		c.t.Fatal(err)
	}
}

func managementHTTPResponses(t *testing.T) []apiContractCase {
	t.Helper()
	c := &contractHTTP{t: t, f: newFixture(t), scope: scopeA, seen: map[string]bool{}}
	contractCodeGraph(c)
	contractReviewsAndGallery(c)
	contractDigests(c)
	contractHostAndSessions(c)
	contractWikiAndKnowledge(c)
	contractCatalogQueries(c)
	if len(c.seen) != 65 {
		t.Fatalf("management contract coverage: %d/65", len(c.seen))
	}
	t.Logf("all %d management operations exercised successfully and anonymously", len(c.seen))
	return c.cases
}

func contractCodeGraph(c *contractHTTP) {
	c.f.svc.Repo = codeGraphRepo{fakeRepo: c.f.repo, fakeCodeGraph: newFakeCodeGraph()}
	b := batches(scopeA, "fixture", indexFixture(c.t), 2000)[0]
	out := c.call("POST /v1/code_graph/batch", "", b, nil)
	state := decodeContract[CodeRepo](c.t, out["repo"])
	c.call("POST /v1/code_graph/finish", "", CodeGraphFinish{Scope: scopeA, Repo: "fixture", ExpectedRevision: state.Revision, Commit: "cbd18c7", Stats: json.RawMessage(`{"mode":"full"}`)}, nil)
	c.call("GET /v1/code_graph/repos", "", nil, nil)
	c.call("GET /v1/code_graph/files", "", nil, url.Values{"repo": {"fixture"}})
	for _, suffix := range []string{"search", "callers", "callees", "impact"} {
		c.call("POST /v1/code_graph/"+suffix, "", CodeGraphQuery{Scope: scopeA, Repo: "fixture", Query: "Summary", Symbol: "report.Summary"}, nil)
	}
	c.call("POST /v1/code_graph/changed", "", CodeChangedQuery{Scope: scopeA, Repo: "fixture", Ranges: []CodeLineRange{{Path: "shapes/shapes.go", StartLine: 20, EndLine: 20}}}, nil)
	c.call("POST /v1/code_graph/repos/delete", "", CodeGraphRepoInput{Scope: scopeA, Repo: "fixture", ExpectedRevision: state.Revision + 1}, nil)
}

func contractReviewsAndGallery(c *contractHTTP) {
	item := ReviewItem{Repo: "fixture/repository", Number: 1, Title: "Fixture", Branch: "change", Base: "main", HeadSHA: "cbd18c7", URL: "https://github.com/fixture/repository/pull/1", State: "done", Findings: []ReviewFinding{{Severity: "advisory", Path: "a.go", Line: 1, Text: "Fixture finding"}}, Steps: []ReviewStep{{Name: "quality", Status: "done"}}}
	c.call("POST /v1/code_reviews", "", CodeReviewsReport{Scope: scopeA, Repos: []ReviewRepo{{Name: item.Repo, PRs: 1}}, Items: []ReviewItem{item}}, nil)
	c.call("GET /v1/code_reviews", "", nil, nil)
	c.call("POST /v1/code_reviews/settings", "", CodeReviewSettingsInput{Scope: scopeA, Settings: ReviewSettings{Provider: "codex", OnBlocking: "notify", ReposOff: []string{}}}, nil)
	c.call("POST /v1/code_reviews/rerun", "", CodeReviewRerunInput{Scope: scopeA, Repo: item.Repo, Number: 1}, nil)
	crew := c.f.crew(crewTemplate("Fixture persona"))
	c.call("GET /v1/crew/versions/{id}", crew.ID, nil, nil)
	c.call("GET /v1/crew/agents/{id}/calls", crew.ID, nil, nil)
	for _, col := range []string{colPrompts, colDesignSystems} {
		r := Record{ID: "contract-" + col, Scope: scopeA, Status: "archived", Revision: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		if col == colPrompts {
			r.ImagePrompt = &ImagePrompt{}
		} else {
			r.DesignSystem = &DesignSystem{}
		}
		c.seed(col, r)
		c.call("GET /v1/"+col+"/archived", "", nil, nil)
		c.call("POST /v1/"+col+"/{id}/restore", r.ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1}, nil)
	}
}

func contractDigests(c *contractHTTP) {
	a := c.f.digestMemory("decision", "Fixture deployment always uses staging first")
	b := c.f.digestMemory("decision", "Fixture deployment checks staging before release")
	user := c.f.digestMemory("user", "Fixture user prefers concise answers")
	c.call("POST /v1/digests/plan", "", DigestPlanInput{Scope: scopeA, Kind: "topic"}, nil)
	out := c.call("POST /v1/digests/proposals", "", DigestProposalInput{Scope: scopeA, Kind: "topic", Title: "Fixture deployment", Body: "Staging before release", Members: memRefs(a, b)}, nil)
	id := decodeContract[string](c.t, out["id"])
	rev := decodeContract[int64](c.t, out["revision"])
	c.call("GET /v1/digests/{id}", id, nil, url.Values{"kind": {"topic"}})
	c.call("POST /v1/digests/{id}/decide", id, DigestDecisionInput{Scope: scopeA, Kind: "topic", ExpectedRevision: rev, Decision: "approve"}, nil)
	c.call("GET /v1/digests", "", nil, url.Values{"kind": {"topic"}})
	c.call("GET /v1/digests/user/context", "", nil, nil)
	c.call("POST /v1/digests/recall", "", DigestRecallInput{Scope: scopeA, Query: "staging"}, nil)
	c.call("POST /v1/digests/regenerate", "", DigestRegenerateInput{Scope: scopeA, Kind: "user"}, nil)
	c.call("POST /v1/digests/user/judge", "", DigestJudgeInput{Scope: scopeA, Judged: memRefs(user), Verdict: "minor"}, nil)
}

func contractHostAndSessions(c *contractHTTP) {
	c.call("GET /v1/skills/usage", "", nil, nil)
	c.call("GET /v1/twin/claims", "", nil, nil)
	c.call("POST /v1/context/tokens", "", TokenCountInput{Scope: scopeA, Text: "Fixture text"}, nil)
	now := time.Now().Add(-time.Hour).UTC()
	c.call("POST /v1/import/sessions", "", SessionImportInput{Scope: scopeA, DryRun: true, Generator: &Generator{Model: "fixture", PromptVersion: "v1", PipelineVersion: "v1"}, Sessions: []ImportedSession{{ID: "is_0123456789abcdef01234567", Origin: "codex", EndedAt: &now, Messages: 1, Candidates: []ImportCandidate{}}}}, nil)
	m := c.f.activeMemory("Fixture expiry record", "fixture")
	c.call("POST /v1/memories/{id}/expiry", m.ID, MemoryExpiryInput{Scope: scopeA, ExpectedRevision: m.Revision}, nil)
	c.call("POST /v1/providers/labels", "", ProviderLabel{Scope: scopeA, Provider: "openai", Kind: "account", ID: "codex", Label: "Fixture account"}, nil)
	c.call("POST /v1/providers/test", "", ProviderModelTest{Scope: scopeA, Provider: "openai", Model: "fixture-model"}, nil)
	id := sessionManageUsage(c.t, c.f, time.Now().UTC(), "contract-session")
	c.call("POST /v1/usage/sessions/transcript", "", SessionTranscriptInput{Scope: scopeA, ID: id}, nil)
	c.call("POST /v1/usage/sessions/export", "", SessionExportInput{Scope: scopeA, IDs: []string{id}, Format: "json"}, nil)
	c.call("POST /v1/usage/sessions/delete", "", SessionDeleteInput{Scope: scopeA, IDs: []string{id}}, nil)
	contractMessengers(c)
}

func contractMessengers(c *contractHTTP) {
	c.t.Setenv("ARKISM_CREW_NOTIFY", filepath.Join(c.t.TempDir(), "notify.json"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	old := notifycfg.TelegramAPI
	notifycfg.TelegramAPI = server.URL
	defer func() { notifycfg.TelegramAPI = old }()
	c.call("POST /v1/messengers", "", MessengerSave{Scope: scopeA, Channel: "telegram", BotToken: "123456:AAH3abcdefghijklmnopqrstuvwxyz", ChatID: "-100123"}, nil)
	c.call("GET /v1/messengers", "", nil, nil)
	c.call("POST /v1/messengers/test", "", MessengerRef{Scope: scopeA, Channel: "telegram"}, nil)
	c.call("POST /v1/messengers/remove", "", MessengerRef{Scope: scopeA, Channel: "telegram"}, nil)
}

// A provider fixture, never a real model call. Returning an empty plan exercises
// the successful job completion path without inventing any wiki content.
type contractWikiLLM struct{}

func (contractWikiLLM) Generator() Generator {
	return Generator{Model: "fixture", PromptVersion: "v1", PipelineVersion: "v1"}
}
func (contractWikiLLM) Complete(context.Context, string, string, int) (string, ProviderUsage, error) {
	return `{"summary":"Fixture","pages":[],"links":[]}`, ProviderUsage{Provider: wikiProvider, Model: "fixture", Requests: 1, InputTokens: 10, OutputTokens: 10, TotalTokens: 20}, nil
}

func contractWikiAndKnowledge(c *contractHTTP) {
	c.scope = Scope{WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "fixture-user", AgentID: "fixture-agent"}
	c.f.svc.SyntheticHandoff = true
	c.f.svc.Wiki = contractWikiLLM{}
	a := c.f.auth.actors[c.f.human]
	a.verbs["generate"] = true
	a.verbs["retrieve"] = true
	c.f.auth.actors[c.f.human] = a
	sc := c.scope
	now := time.Now().UTC()
	source := Record{ID: "contract-source", Scope: sc, Kind: colKnowledgeSources, Status: "active", Revision: 1, CreatedAt: now, UpdatedAt: now, Knowledge: &KnowledgeRecord{SourceVersion: "v1", DocumentID: "contract-document", DocumentIDs: []string{"contract-document"}, TrustLevel: "reviewed", LifecycleStatus: "active"}}
	doc := Record{ID: "contract-document", Scope: sc, Kind: colKnowledgeDocuments, Status: "active", Revision: 1, CreatedAt: now, UpdatedAt: now, Knowledge: &KnowledgeRecord{KnowledgeSourceID: "contract-source", ChunkIDs: []string{"contract-chunk"}}}
	evidence := c.f.source()
	evidence.Scope = sc
	c.seed("sources", evidence)
	source.Sources = refs(evidence)
	doc.Sources = refs(evidence)
	c.seed(colKnowledgeSources, source)
	c.seed(colKnowledgeDocuments, doc)
	c.seed(colKnowledgeChunks, Record{ID: "contract-chunk", Scope: sc, Status: "active", Revision: 1, Sources: refs(evidence), Text: "Fixture source content", Knowledge: &KnowledgeRecord{KnowledgeSourceID: source.ID, DocumentID: doc.ID}})
	c.call("GET /v1/knowledge/sources", "", nil, nil)
	c.call("GET /v1/knowledge/documents", "", nil, url.Values{"source": {source.ID}})
	c.call("POST /v1/knowledge/search", "", KnowledgeSearchInput{Scope: sc, ProfileID: "design_consultant", Query: "Fixture"}, nil)
	out := c.call("POST /v1/wiki/jobs", "", WikiJobInput{Scope: sc, KnowledgeSourceID: source.ID}, nil)
	jobID := decodeContract[string](c.t, out["id"])
	c.call("GET /v1/wiki/jobs", "", nil, nil)
	c.call("GET /v1/wiki/jobs/{id}", jobID, nil, nil)
	c.call("POST /v1/wiki/jobs/{id}/run", jobID, WikiScopeInput{Scope: sc}, nil)
	// Seed another queued job for the work-once handler; all state is in fakeRepo.
	job, err := c.f.repo.Get(testCtx, "jobs", sc, jobID)
	if err != nil {
		c.t.Fatal(err)
	}
	job.ID += "-queued"
	job.Status = JobStatusPending
	job.Revision = 1
	job.Wiki.Job.Result = nil
	c.seed("jobs", job)
	c.call("POST /v1/wiki/work", "", WikiScopeInput{Scope: sc}, nil)
	contractWikiPages(c, source.ID, now)
}

func contractWikiPages(c *contractHTTP, sourceID string, now time.Time) {
	sc := c.scope
	pageID := wikiPageID(sc, "fixture")
	source, err := c.f.repo.Get(testCtx, colKnowledgeSources, sc, sourceID)
	if err != nil {
		c.t.Fatal(err)
	}
	c.seed(colWikiPages, Record{ID: pageID, Scope: sc, Kind: "wiki_page", Status: "draft", Revision: 1, Sources: source.Sources, CreatedAt: now, UpdatedAt: now, Wiki: &WikiRecord{Page: &WikiPage{Slug: "fixture", Type: "concept", SourceIDs: []string{sourceID}, ReviewState: "draft", Draft: &WikiBody{Title: "Fixture", Text: "Fixture concept", Links: []string{}, At: now}}}})
	c.call("GET /v1/wiki/pages", "", nil, nil)
	c.call("GET /v1/wiki/pages/{id}", pageID, nil, nil)
	c.call("POST /v1/wiki/pages/{id}/review", pageID, WikiReviewInput{Scope: sc, ExpectedRevision: 1, Decision: "approve"}, nil)
	c.call("POST /v1/wiki/pages/{id}/lock", pageID, WikiLockInput{Scope: sc, ExpectedRevision: 2, Locked: true}, nil)
	c.call("POST /v1/wiki/pages/{id}/edit", pageID, WikiEditInput{Scope: sc, ExpectedRevision: 3, Title: "Fixture", Text: "Fixture edited concept"}, nil)
	c.call("GET /v1/wiki/graph", "", nil, nil)
	c.call("POST /v1/wiki/search", "", WikiSearchInput{Scope: sc, Query: "Fixture"}, nil)
	c.call("POST /v1/wiki/read", "", WikiSearchInput{Scope: sc, Page: "fixture"}, nil)
	c.call("POST /v1/wiki/sources/{id}/remove", sourceID, WikiSourceRemoveInput{Scope: sc, DryRun: true}, nil)
}

func contractCatalogQueries(c *contractHTTP) {
	c.call("GET /v1/usage/sessions", "", nil, url.Values{"sort": {"tokens"}, "limit": {"50"}})
	for _, path := range []string{"memories", "memory_candidates", "skills", "knowledge/sources"} {
		c.call("POST /v1/"+path+"/query", "", CatalogQueryInput{Scope: c.scope}, nil)
	}
}
