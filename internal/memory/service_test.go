package memory

// Offline service tests: a transactional in-memory Repository, a token-keyed
// authorization fake and a 1024-dimension embedder stand in for storage, the
// policy provider and the embedding provider. No sockets are opened.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	testCtx  = context.Background()
	scopeA   = Scope{WorkspaceID: "ws-a", ProjectID: "proj-a"}
	scopeB   = Scope{WorkspaceID: "ws-a", ProjectID: "proj-b"}
	scopeFar = Scope{WorkspaceID: "elsewhere", ProjectID: "elsewhere"}
)

// scopeKey keeps all four scope fields, so a partially filled scope can never
// widen into another tenant's rows.
func scopeKey(s Scope) string {
	return strings.Join([]string{s.WorkspaceID, s.ProjectID, s.UserID, s.AgentID}, "\x1f")
}
func recKey(s Scope, id string) string { return scopeKey(s) + "\x00" + id }

// fakeTx is both the repository view and the transaction view. A transaction
// runs against a copy of the whole store, so returning an error discards every
// write the callback made: rollback is a snapshot, like one real transaction.
type fakeTx struct {
	observedAt *time.Time
	store      map[string]map[string]Record
	failAudit  bool
}

func (x fakeTx) Get(_ context.Context, col string, s Scope, id string) (Record, error) {
	rec, ok := x.store[col][recKey(s, id)]
	if !ok {
		return Record{}, ErrNotFound
	}
	return rec, nil
}

func (x fakeTx) SourceMetadata(ctx context.Context, scope Scope, ids []string, includeText bool) ([]Record, error) {
	var out []Record
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		r, e := x.Get(ctx, "sources", scope, id)
		if e == ErrNotFound {
			continue
		}
		if e != nil {
			return nil, e
		}
		meta := Record{TrustLevel: r.TrustLevel, ID: r.ID, Scope: r.Scope, Status: r.Status, ExpiresAt: r.ExpiresAt, Sensitivity: r.Sensitivity}
		if includeText {
			meta.Text = r.Text
		}
		out = append(out, meta)
	}
	return out, nil
}
func (f *fakeRepo) SourceMetadata(ctx context.Context, scope Scope, ids []string, includeText bool) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().SourceMetadata(ctx, scope, ids, includeText)
}
func (x fakeTx) memoryObservation(col string) time.Time {
	if col != colMemories || x.observedAt == nil {
		return time.Now()
	}
	if x.observedAt.IsZero() {
		*x.observedAt = time.Now()
	}
	return *x.observedAt
}

func (x fakeTx) Insert(_ context.Context, col string, rec Record) error {
	if col == "" || rec.ID == "" || checkScope(rec.Scope) != nil {
		return ErrInvalid
	}
	if col == "audit_events" && x.failAudit {
		return ErrUnavailable
	}
	if x.store[col] == nil {
		x.store[col] = map[string]Record{}
	}
	key := recKey(rec.Scope, rec.ID)
	if _, exists := x.store[col][key]; exists {
		return ErrConflict
	}
	if rec.Revision <= 0 {
		rec.Revision = 1
	}
	if err := recordMemoryState(col, nil, &rec, x.memoryObservation(col)); err != nil {
		return err
	}
	x.store[col][key] = rec
	return nil
}

func (x fakeTx) Replace(_ context.Context, col string, rec Record, revision int64) error {
	docs, key := x.store[col], recKey(rec.Scope, rec.ID)
	old, ok := docs[key]
	if !ok {
		return ErrNotFound
	}
	if col == "sources" && old.TrustLevel != rec.TrustLevel {
		return ErrConflict
	}
	if old.ErasureParentID != rec.ErasureParentID || structuredText(old.Proposal) != structuredText(rec.Proposal) || old.OriginProduct != rec.OriginProduct || old.SourceRef != rec.SourceRef || structuredText(old.CollectedAt) != structuredText(rec.CollectedAt) || old.CreatedBy != rec.CreatedBy || old.CreatedByType != rec.CreatedByType || !old.CreatedAt.Equal(rec.CreatedAt) || structuredText(old.Sources) != structuredText(rec.Sources) || structuredText(old.Generator) != structuredText(rec.Generator) {
		return ErrConflict
	}
	if old.Revision != revision {
		return ErrConflict
	}
	// Mirror Postgres.checkReplaceRevision: the record may carry the revision it
	// read, or the one it is already writing, and nothing else.
	if rec.Revision != revision && rec.Revision != revision+1 {
		return ErrInvalid
	}
	// A no-op revision fence must preserve the revision, as PostgreSQL does.
	if err := recordMemoryState(col, &old, &rec, x.memoryObservation(col)); err != nil {
		return err
	}
	docs[key] = rec
	return nil
}

func (x fakeTx) Delete(_ context.Context, col string, s Scope, id string, revision int64) error {
	docs, key := x.store[col], recKey(s, id)
	old, ok := docs[key]
	if !ok {
		return ErrNotFound
	}
	if old.Revision != revision {
		return ErrConflict
	}
	delete(docs, key)
	return nil
}

type fakeRepo struct {
	restoreManifest string
	mu              sync.Mutex
	store           map[string]map[string]Record
	failAudit       bool
	stale           []Hit // index rows that outlived the document they describe
}

func (*fakeRepo) allowLegacySourceDeleteForTest() {}

func newFakeRepo() *fakeRepo { return &fakeRepo{store: map[string]map[string]Record{}} }

func (f *fakeRepo) view() fakeTx {
	return fakeTx{store: f.store, failAudit: f.failAudit, observedAt: new(time.Time)}
}

func (f *fakeRepo) Get(ctx context.Context, col string, s Scope, id string) (Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().Get(ctx, col, s, id)
}
func (f *fakeRepo) Insert(ctx context.Context, col string, rec Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().Insert(ctx, col, rec)
}
func (f *fakeRepo) Replace(ctx context.Context, col string, rec Record, revision int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().Replace(ctx, col, rec, revision)
}
func (f *fakeRepo) Delete(ctx context.Context, col string, s Scope, id string, revision int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().Delete(ctx, col, s, id, revision)
}

func (f *fakeRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	if fn == nil {
		return ErrInvalid
	}
	f.mu.Lock()
	base, next, view := f.snapshot(), f.snapshot(), f.view()
	f.mu.Unlock()

	view.store = next
	if err := fn(ctx, view); err != nil {
		return err // rollback: next is dropped
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Commit only the rows this transaction changed, and fail like a serialization
	// error when a concurrent (or nested) commit already changed one of them.
	changed := map[string]map[string]bool{}
	for _, side := range []map[string]map[string]Record{base, next} {
		for col, docs := range side {
			for key := range docs {
				if old, ok := base[col][key]; !ok || !reflect.DeepEqual(old, next[col][key]) {
					if changed[col] == nil {
						changed[col] = map[string]bool{}
					}
					changed[col][key] = true
				}
			}
		}
	}
	for col, keys := range changed {
		for key := range keys {
			old, had := base[col][key]
			cur, has := f.store[col][key]
			if had != has || !reflect.DeepEqual(old, cur) {
				return ErrConflict
			}
		}
	}
	for col, keys := range changed {
		for key := range keys {
			if rec, ok := next[col][key]; ok {
				if f.store[col] == nil {
					f.store[col] = map[string]Record{}
				}
				f.store[col][key] = rec
			} else {
				delete(f.store[col], key)
			}
		}
	}
	return nil
}

func (f *fakeRepo) snapshot() map[string]map[string]Record {
	out := make(map[string]map[string]Record, len(f.store))
	for col, docs := range f.store {
		copied := make(map[string]Record, len(docs))
		for key, rec := range docs {
			copied[key] = rec
		}
		out[col] = copied
	}
	return out
}

// Search mirrors the three Atlas lanes: exact keyword, text and vector. Lane
// order is deterministic, so rank fusion rather than map order decides output.
func (f *fakeRepo) Search(_ context.Context, scope Scope, query string, vector []float64, space string, limit int, modes ...string) ([][]Hit, error) {
	return f.search(scope, query, vector, space, limit, "", SearchCriteria{}, modes...)
}
func (f *fakeRepo) search(scope Scope, query string, vector []float64, space string, limit int, collection string, criteria SearchCriteria, modes ...string) ([][]Hit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	tokens, lowered := exactQueryKeywords(query), strings.ToLower(query)
	var keyword, text, nearest []Hit
	for _, col := range searchableCollections {
		for _, rec := range f.rowsLocked(col) {
			if collection != "" && col != collection || !(criteria.history && historySearchable(rec) || !criteria.history && live(rec)) || !criteria.matchesRecord(rec) || !sameScope(rec.Scope, scope) {
				continue
			}
			if searchModeEnabled(modes, "exact") && matchesKeyword(rec.Keywords, tokens) {
				keyword = append(keyword, searchHit(rec, col, reasonExactKeyword))
			}
			if searchModeEnabled(modes, "fts") && strings.Contains(strings.ToLower(recordContent(rec)), lowered) {
				text = append(text, searchHit(rec, col, reasonTextSearch))
			}
			if searchModeEnabled(modes, "vector") && len(vector) > 0 && rec.Embedding != nil && rec.Embedding.Space == space && rec.Embedding.Dimension == len(vector) &&
				rec.Embedding.ContentHash == ContentHash(recordContent(rec)) {
				nearest = append(nearest, searchHit(rec, col, reasonVector))
			}
		}
	}
	if searchModeEnabled(modes, "exact") {
		keyword = append(keyword, f.stale...)
	}
	return [][]Hit{clip(keyword, limit), clip(text, limit), clip(nearest, limit)}, nil
}

func matchesKeyword(keywords, tokens []string) bool {
	for _, token := range tokens {
		for _, keyword := range keywords {
			if strings.EqualFold(keyword, token) {
				return true
			}
		}
	}
	return false
}

func clip(hits []Hit, limit int) []Hit {
	if len(hits) > limit {
		return hits[:limit]
	}
	return hits
}

func (f *fakeRepo) rowsLocked(col string) []Record {
	rows := make([]Record, 0, len(f.store[col]))
	for _, rec := range f.store[col] {
		rows = append(rows, rec)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

func (f *fakeRepo) ListRecords(_ context.Context, scope Scope, col string, limit int) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Record
	for _, r := range f.rowsLocked(col) {
		if sameScope(r.Scope, scope) && len(out) < limit {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (f *fakeRepo) ListRecordsByStatus(ctx context.Context, scope Scope, col, status string, limit int) ([]Record, error) {
	rows, _ := f.ListRecords(ctx, scope, col, 1<<30)
	out := []Record{}
	for _, r := range rows {
		if r.Status == status && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeRepo) ListCrewRunsByStatus(ctx context.Context, scope Scope, status string, limit int) ([]Record, error) {
	rows, _ := f.ListRecords(ctx, scope, colCrewRuns, 1<<30)
	out := []Record{}
	for _, r := range rows {
		if r.CrewRun != nil && r.CrewRun.Status == status && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeRepo) rows(col string) []Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rowsLocked(col)
}

// ClaimJob leases one runnable job. Retry backoff is not modelled: the fake
// only has to prove lease exclusivity and revision binding.
func (f *fakeRepo) ClaimJob(_ context.Context, now time.Time, lease time.Duration) (Record, error) {
	if lease <= 0 {
		return Record{}, ErrInvalid
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, job := range f.rowsLocked("jobs") {
		runnable := job.Status == JobStatusPending || job.Status == JobStatusRetry ||
			(job.Status == JobStatusRunning && !job.LeaseUntil.After(now))
		if (job.Kind != "embedding" && job.Kind != sourceMemoryErasureKind && job.Kind != sourceRecordErasureKind && job.Kind != sourceSkillErasureKind && job.Kind != sourceRelationErasureKind && job.Kind != sourceMemoryDependentsErasureKind && !isMemoryErasureKind(job.Kind)) || !runnable {
			continue
		}
		job.Status = JobStatusRunning
		job.LeaseUntil = now.Add(lease)
		job.Attempts++
		job.LeaseToken = fmt.Sprintf("lease-%d-%s", job.Attempts, job.ID)
		job.Revision++
		f.store["jobs"][recKey(job.Scope, job.ID)] = job
		return job, nil
	}
	return Record{}, ErrNotFound
}

func (f *fakeRepo) FinishJob(_ context.Context, rec Record, success bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := recKey(rec.Scope, rec.ID)
	job, ok := f.store["jobs"][key]
	if !ok || job.LeaseToken != rec.LeaseToken || job.Revision != rec.Revision {
		return ErrConflict
	}
	job.LeaseToken, job.LeaseUntil = "", time.Time{}
	switch {
	case success:
		job.Status = JobStatusDone
	case job.Attempts >= maxJobAttempts:
		job.Status = JobStatusFailed
	default:
		job.Status = JobStatusRetry
	}
	job.Revision++
	f.store["jobs"][key] = job
	return nil
}

type fakeActor struct {
	principal  string
	role       string
	verbs      map[string]bool
	external   bool
	wrongScope bool
}

var allVerbs = []string{"finish", "expire", "review", "analyze", "route", "read", "write", "search", "context", "transition", "delete", "apply", "rollback", "embed"}

func actor(principal, role string, external bool, verbs ...string) fakeActor {
	who := fakeActor{principal: principal, role: role, external: external, verbs: map[string]bool{}}
	for _, verb := range verbs {
		who.verbs[verb] = true
	}
	return who
}

// fakeAuth answers from the token table: the role and the verb set come from
// the token, never from the request payload.
type fakeAuth struct{ actors map[string]fakeActor }

func (a *fakeAuth) Authorize(_ context.Context, token string, req Authorization) (Decision, error) {
	who, ok := a.actors[token]
	if !ok {
		return Decision{}, ErrDenied
	}
	verb, _, _ := strings.Cut(req.Action, ":")
	scope := req.Scope
	if who.wrongScope {
		scope = scopeFar
	}
	return Decision{
		Allowed: who.verbs[verb], PrincipalID: who.principal, Role: who.role,
		Scope: scope, ExternalTransfer: who.external, ContentSafe: true,
	}, nil
}

type fakeEmbedder struct {
	space string
	dim   int
	// semantic gives each text a distinct bag-of-tokens vector so ranking order
	// is observable; the default constant vector keeps older tests unchanged.
	semantic bool
	err      error
	// onEmbed runs once per Embed call after the call is recorded, before the
	// result is produced; tests use it to race repository changes with an
	// in-flight embedding job.
	onEmbed func()
	mu      sync.Mutex
	calls   []string // "inputType:text", in call order
}

func (f *fakeEmbedder) Space() string { return f.space }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string, inputType string) ([]Embedding, error) {
	f.mu.Lock()
	for _, text := range texts {
		f.calls = append(f.calls, inputType+":"+text)
	}
	err := f.err
	f.mu.Unlock()
	if f.onEmbed != nil {
		f.onEmbed()
	}
	if err != nil {
		return nil, err
	}
	out := make([]Embedding, len(texts))
	for i, text := range texts {
		vector := make([]float64, f.dim)
		vector[0] = 0.5
		if f.semantic {
			vector[0] = 0
			for _, word := range strings.Fields(strings.ToLower(text)) {
				sum := 0
				for _, r := range word {
					sum = (sum*31 + int(r)) % f.dim
				}
				vector[sum] += 1
			}
			vector[0] += 0.001
		}
		out[i] = Embedding{
			Provider: "fake", Model: "embed", Revision: "v1", Dimension: f.dim, Space: f.space,
			EmbeddedAt: time.Now().UTC(), ContentHash: ContentHash(text), Vector: vector,
		}
	}
	return out, nil
}

type fixture struct {
	t                          *testing.T
	svc                        *Service
	repo                       *fakeRepo
	auth                       *fakeAuth
	emb                        *fakeEmbedder
	human, human2, agent, work string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		t: t, repo: newFakeRepo(), auth: &fakeAuth{actors: map[string]fakeActor{}},
		emb:   &fakeEmbedder{space: "fake:embed:v1:1024d", dim: VectorDimension},
		human: "tok-human", human2: "tok-human2", agent: "tok-agent", work: "tok-worker",
	}
	f.auth.actors[f.human] = actor("human-1", "human", true, allVerbs...)
	f.auth.actors[f.human2] = actor("human-2", "human", true, allVerbs...)
	// The agent token is permitted every action, so only the service's own
	// principal-role gate can refuse it.
	f.auth.actors[f.agent] = actor("agent-1", "agent", true, allVerbs...)
	f.auth.actors[f.work] = actor("worker-1", "service", true, "read", "embed")
	f.svc = &Service{CandidateDailyLimit: 50, Repo: f.repo, Auth: f.auth, Embedder: f.emb}
	return f
}

// One-line call wrappers, so a happy path never needs an if-block.
func (f *fixture) create(token, col string, in CreateInput) Record {
	f.t.Helper()
	rec, err := f.svc.Create(testCtx, token, col, in)
	if err != nil {
		f.t.Fatalf("create %s: %v", col, err)
	}
	return rec
}
func (f *fixture) transition(token, col, id string, in ChangeInput) Record {
	f.t.Helper()
	rec, err := f.svc.Transition(testCtx, token, col, id, in)
	if err != nil {
		f.t.Fatalf("transition %s: %v", col, err)
	}
	return rec
}
func (f *fixture) apply(token, id string, in ChangeInput, rollback bool) Record {
	f.t.Helper()
	rec, err := f.svc.ApplySkill(testCtx, token, id, in, rollback)
	if err != nil {
		f.t.Fatalf("apply skill: %v", err)
	}
	return rec
}
func (f *fixture) get(token, col string, s Scope, id string) Record {
	f.t.Helper()
	rec, err := f.svc.Get(testCtx, token, col, s, id)
	if err != nil {
		f.t.Fatalf("get %s: %v", col, err)
	}
	return rec
}
func (f *fixture) search(token string, in SearchInput) ContextBundle {
	f.t.Helper()
	bundle, err := f.svc.Search(testCtx, token, in)
	if err != nil {
		f.t.Fatalf("search: %v", err)
	}
	return bundle
}
func (f *fixture) source() Record {
	f.t.Helper()
	return f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "the original message"}))
}
func (f *fixture) activeMemory(text string, keywords ...string) Record {
	f.t.Helper()
	src := f.source()
	cand := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: text, Keywords: keywords, Sources: refs(src)}))
	approved := f.transition(f.human, "memory_candidates", cand.ID, ChangeInput{Scope: scopeA, ExpectedRevision: cand.Revision, Status: "approved"})
	return f.transition(f.human, "memories", approved.ID, ChangeInput{Scope: scopeA, ExpectedRevision: approved.Revision, Status: "active"})
}

func refs(rec Record) []SourceRef {
	return []SourceRef{sourceReference(rec, "original")}
}

func TestCandidateApprovalIsAHumanGateAndInsertsTheMemory(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	cand := f.create(f.human, "memory_candidates", candidateInput(CreateInput{
		Scope: scopeA, Text: "prefer short answers", Keywords: []string{"style"}, Sources: refs(src),
	}))
	if cand.Status != "candidate" || cand.Revision != 1 {
		t.Fatalf("candidate = %s rev %d, want candidate rev 1", cand.Status, cand.Revision)
	}
	_, err := f.svc.Transition(testCtx, f.agent, "memory_candidates", cand.ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1, Status: "approved"})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("agent approve err = %v, want ErrDenied", err)
	}
	_, err = f.svc.Transition(testCtx, f.human, "memory_candidates", cand.ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1, Status: "active"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("candidate to active err = %v, want ErrInvalid", err)
	}
	approved := f.transition(f.human, "memory_candidates", cand.ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1, Status: "approved"})
	if approved.Status != "approved" || approved.Revision != 2 || approved.ApprovedBy != "human-1" {
		t.Fatalf("approved = %+v", approved)
	}
	stored := f.get(f.human, "memories", scopeA, cand.ID)
	if stored.ID != cand.ID || stored.Status != "approved" {
		t.Fatalf("memory row = %+v, want the approved candidate", stored)
	}
	active := f.transition(f.human, "memories", cand.ID, ChangeInput{Scope: scopeA, ExpectedRevision: 2, Status: "active"})
	if active.Status != "active" || active.Revision != 3 {
		t.Fatalf("active = %+v", active)
	}
	jobs := f.embeddingJobs()
	if len(jobs) != 1 || jobs[0].Status != JobStatusPending || jobs[0].Target.ID != cand.ID {
		t.Fatalf("jobs = %+v, want one pending embedding job", jobs)
	}
	if audits := f.repo.rows("audit_events"); len(audits) != 5 {
		t.Fatalf("audit rows = %d, want two creates, two transitions and one read", len(audits))
	}
}

func TestAuditFailureRollsBackTheWholeWrite(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	f.repo.failAudit = true
	_, err := f.svc.Create(testCtx, f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "x", Sources: refs(src)}))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable from the audit write", err)
	}
	if rows := f.repo.rows("memory_candidates"); len(rows) != 0 {
		t.Fatalf("candidates = %+v, want a full rollback", rows)
	}
	if rows := f.repo.rows("audit_events"); len(rows) != 1 {
		t.Fatalf("audit rows = %d, want only the earlier source audit", len(rows))
	}
}

func TestScopeIsExactAndForeignEvidenceIsDenied(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	if _, err := f.svc.Get(testCtx, f.human, "sources", scopeB, src.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross scope read err = %v, want ErrNotFound", err)
	}
	_, err := f.svc.Create(testCtx, f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeB, Text: "x", Sources: refs(src)}))
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign source err = %v, want ErrDenied", err)
	}
	_, err = f.svc.Create(testCtx, f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "no evidence"}))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing source err = %v, want ErrInvalid", err)
	}
	f.auth.actors["tok-elsewhere"] = fakeActor{principal: "human-3", role: "human", external: true, wrongScope: true, verbs: actor("", "", true, allVerbs...).verbs}
	_, err = f.svc.Create(testCtx, "tok-elsewhere", "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "x", Sources: refs(src)}))
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign scope decision err = %v, want ErrDenied", err)
	}
}

func TestSkillApplyNeedsIndependentApprovalAndMovesPointers(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	experience := f.activeMemory("the duplicate key needed a unique index", "E11000")
	const skill = "skill-1"
	version := func(base string) Record {
		return f.seedSkillVersion(Record{Text: "run the focused test first", SkillID: skill, BaseVersion: base}, src, experience)
	}
	v1 := version("")
	// An evaluation by the version author is not independent evidence.
	_, err := f.svc.Create(testCtx, f.human, "evaluations", ratingInput(CreateInput{
		Scope: scopeA, Text: "self review", Passed: true, Sources: refs(src),
		Target: &Ref{Collection: "skill_versions", ID: v1.ID, Revision: v1.Revision},
	}))
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("self evaluation err = %v, want ErrDenied", err)
	}
	approve := func(v Record) Record {
		evaluation := f.create(f.human2, "evaluations", ratingInput(CreateInput{
			Scope: scopeA, Text: "independent check passed", Passed: true, Sources: refs(src),
			Target: &Ref{Collection: "skill_versions", ID: v.ID, Revision: v.Revision},
		}))
		return f.transition(f.human, "skill_versions", v.ID, ChangeInput{
			Scope: scopeA, ExpectedRevision: v.Revision, Status: "approved", ValidationID: evaluation.ID,
		})
	}
	v1 = approve(v1)
	if v1.Status != "approved" || v1.ApprovedBy != "human-1" || v1.ValidationID == "" {
		t.Fatalf("v1 = %+v", v1)
	}
	_, err = f.svc.ApplySkill(testCtx, f.agent, skill, ChangeInput{Scope: scopeA, ExpectedRevision: 1, VersionID: v1.ID}, false)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("agent apply err = %v, want ErrDenied", err)
	}
	applied := f.apply(f.human, skill, ChangeInput{Scope: scopeA, ExpectedRevision: 1, VersionID: v1.ID}, false)
	if applied.ActiveVersion != v1.ID || applied.Revision != 2 {
		t.Fatalf("skill = %+v, want v1 active", applied)
	}
	if got := f.get(f.human, "skill_versions", scopeA, v1.ID); got.Status != "active" {
		t.Fatalf("v1 status = %s, want active", got.Status)
	}

	v2 := version(v1.ID)
	for _, tc := range []struct {
		name       string
		expected   int64
		version    string
		rollback   bool
		wantDenied error
	}{
		{"unapproved version", 2, v2.ID, false, ErrConflict},
		{"stale skill revision", 1, v2.ID, false, ErrConflict},
		{"already active", 2, v1.ID, false, ErrConflict},
		{"unknown version", 2, "missing", false, ErrNotFound},
	} {
		_, err := f.svc.ApplySkill(testCtx, f.human, skill, ChangeInput{Scope: scopeA, ExpectedRevision: tc.expected, VersionID: tc.version}, tc.rollback)
		if !errors.Is(err, tc.wantDenied) {
			t.Fatalf("%s err = %v, want %v", tc.name, err, tc.wantDenied)
		}
	}
	v2 = approve(v2)
	f.apply(f.human, skill, ChangeInput{Scope: scopeA, ExpectedRevision: 2, VersionID: v2.ID}, false)
	if got := f.get(f.human, "skill_versions", scopeA, v1.ID); got.Status != "superseded" {
		t.Fatalf("v1 status = %s, want superseded", got.Status)
	}
	rolled := f.apply(f.human, skill, ChangeInput{Scope: scopeA, ExpectedRevision: 3, VersionID: v1.ID}, true)
	if rolled.ActiveVersion != v1.ID || rolled.Revision != 4 {
		t.Fatalf("rollback = %+v, want v1 active again", rolled)
	}
	if got := f.get(f.human, "skill_versions", scopeA, v1.ID); got.Status != "active" {
		t.Fatalf("v1 status after rollback = %s, want active", got.Status)
	}
	if got := f.get(f.human, "skill_versions", scopeA, v2.ID); got.Status != "superseded" {
		t.Fatalf("v2 status after rollback = %s, want superseded", got.Status)
	}
}

func TestSearchSendsOneQueryToBothLanesAndFusesRanks(t *testing.T) {
	f := newFixture(t)
	rec := f.activeMemory("we hit E11000 duplicate key on the unique index", "E11000")
	// The same memory is reachable from the vector lane too; fusion must not
	// hand the caller two rows or a provider score.
	stored := rec
	stored.Embedding = &Embedding{
		Provider: "fake", Model: "embed", Revision: "v1", Dimension: VectorDimension,
		Space: f.emb.space, ContentHash: ContentHash(recordContent(rec)),
	}
	if err := f.repo.Replace(testCtx, "memories", stored, stored.Revision); err != nil {
		t.Fatalf("seed embedding: %v", err)
	}
	query := "E11000 duplicate key"
	bundle := f.search(f.human, SearchInput{Purpose: "knowledge", Scope: scopeA, Query: query, Limit: 5})

	if len(f.emb.calls) != 1 || f.emb.calls[0] != "query:"+query {
		t.Fatalf("embedder calls = %v, want one query embedding of %q", f.emb.calls, query)
	}
	if bundle.Authority != AuthorityDataOnly || len(bundle.Items) != 1 || bundle.Items[0].ID != rec.ID {
		t.Fatalf("bundle = %+v, want the one memory %s", bundle, rec.ID)
	}
	if bundle.Items[0].Authority != AuthorityDataOnly {
		t.Fatalf("authority = %s, want untrusted data", bundle.Items[0].Authority)
	}
}

func TestSearchExcludesStaleIndexRowsAndDeletedSources(t *testing.T) {
	f := newFixture(t)
	rec := f.activeMemory("unique index violation", "E11000")
	f.repo.stale = []Hit{{
		ID: "ghost", Collection: "memories", Origin: "memory", Scope: scopeA, Status: "active", Revision: 1,
		Text: "ghost row", Sources: []SourceRef{{SourceID: "s", Locator: "l"}},
		UpdatedAt: time.Now().UTC(), Authority: AuthorityDataOnly, TrustLevel: "user_verified",
	}}
	bundle := f.search(f.human, SearchInput{Scope: scopeA, Query: "unique index violation"})
	if len(bundle.Items) != 1 || bundle.Items[0].ID != rec.ID {
		t.Fatalf("items = %+v, want only the live memory", bundle.Items)
	}

	src := f.repo.rows("sources")[0]
	if err := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	if after := f.search(f.human, SearchInput{Scope: scopeA, Query: "unique index violation"}); len(after.Items) != 0 {
		t.Fatalf("items after source delete = %+v, want none", after.Items)
	}
}

func TestWorkOnceEmbedsUnderLeaseAndSkipsMovedRevisions(t *testing.T) {
	f := newFixture(t)
	rec := f.activeMemory("duplicate key handling", "E11000")
	if err := f.svc.WorkOnce(testCtx, f.work); err != nil {
		t.Fatalf("WorkOnce: %v", err)
	}
	emb := f.get(f.human, "memories", scopeA, rec.ID).Embedding
	if emb == nil || emb.Dimension != VectorDimension || emb.Space != f.emb.space {
		t.Fatalf("embedding = %+v, want the configured 1024 dimension space", emb)
	}
	if emb.ContentHash != ContentHash(recordContent(rec)) {
		t.Fatalf("hash = %s, want the stored text hash", emb.ContentHash)
	}
	if len(f.emb.calls) != 1 || f.emb.calls[0] != "document:"+recordContent(rec) {
		t.Fatalf("embed calls = %v, want one document embedding", f.emb.calls)
	}
	if job := f.embeddingJobs()[0]; job.Status != JobStatusDone || job.LeaseToken != "" {
		t.Fatalf("job = %+v, want a released lease", job)
	}

	// An edit that lands after the job was queued must not be embedded.
	moved := f.activeMemory("a second memory", "E11000")
	stored := f.repo.rows("memories")
	for i := range stored {
		if stored[i].ID == moved.ID {
			stored[i].Text = "edited after the job was queued"
			updated := stored[i]
			updated.Revision++
			if err := f.repo.Replace(testCtx, "memories", updated, stored[i].Revision); err != nil {
				t.Fatalf("edit: %v", err)
			}
		}
	}
	if err := f.svc.WorkOnce(testCtx, f.work); err != nil {
		t.Fatalf("WorkOnce: %v", err)
	}
	if emb := f.get(f.human, "memories", scopeA, moved.ID).Embedding; emb != nil {
		t.Fatalf("embedding = %+v, want none for a moved target revision", emb)
	}
	if len(f.emb.calls) != 1 {
		t.Fatalf("embed calls = %v, want no second embedding", f.emb.calls)
	}
}

func TestWorkOnceRequiresExternalTransfer(t *testing.T) {
	f := newFixture(t)
	rec := f.activeMemory("local only", "E11000")
	f.auth.actors["tok-local"] = actor("worker-local", "service", false, "read", "write", "search", "context", "embed")
	if err := f.svc.WorkOnce(testCtx, "tok-local"); !errors.Is(err, ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	if emb := f.get(f.human, "memories", scopeA, rec.ID).Embedding; emb != nil {
		t.Fatalf("embedding = %+v, want none without external transfer", emb)
	}
	if len(f.emb.calls) != 0 {
		t.Fatalf("embed calls = %v, want none", f.emb.calls)
	}
	if job := f.embeddingJobs()[0]; job.Status != JobStatusRetry || job.LeaseToken != "" {
		t.Fatalf("job = %+v, want a retry with the lease released", job)
	}
}

func TestClaimJobLeaseIsExclusiveAndRevisionBound(t *testing.T) {
	f := newFixture(t)
	f.activeMemory("lease test", "E11000")
	now := time.Now().UTC()
	first, err := f.repo.ClaimJob(testCtx, now, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := f.repo.ClaimJob(testCtx, now.Add(time.Second), time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second claim err = %v, want ErrNotFound while the lease is live", err)
	}
	second, err := f.repo.ClaimJob(testCtx, now.Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatalf("reclaim after expiry: %v", err)
	}
	if second.Revision <= first.Revision || second.LeaseToken == first.LeaseToken {
		t.Fatalf("reclaimed = %+v, want a new lease over %+v", second, first)
	}
	if err := f.repo.FinishJob(testCtx, first, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale lease finish err = %v, want ErrConflict", err)
	}
	if err := f.repo.FinishJob(testCtx, second, true); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := f.repo.FinishJob(testCtx, second, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("replayed finish err = %v, want ErrConflict", err)
	}
	if _, err := f.repo.ClaimJob(testCtx, now.Add(3*time.Minute), time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claim after done err = %v, want ErrNotFound", err)
	}
}

// seedSkillVersion stores a candidate version backed by a local analysis and
// cited experience, creating its skill row on first use.
func (f *fixture) seedSkillVersion(r Record, src, experience Record) Record {
	f.t.Helper()
	now := time.Now().UTC()
	r.ID, r.Scope, r.Kind, r.Status, r.Revision = ID(), scopeA, "skill_version", "candidate", 1
	r.CreatedAt, r.UpdatedAt, r.CreatedBy, r.CreatedByType = now, now, "human-1", "human"
	r.Sources = refs(src)
	r.Relations = []Ref{
		{Collection: "memories", ID: experience.ID, Revision: experience.Revision},
		{Collection: "sources", ID: src.ID, Revision: src.Revision},
	}
	r.Analysis = &SkillAnalysis{SkillID: r.SkillID, Name: r.SkillID, Description: r.Text}
	err := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		if _, e := tx.Get(ctx, "skills", scopeA, r.SkillID); e == ErrNotFound {
			skill := Record{ID: r.SkillID, Scope: scopeA, Kind: "skill", Status: "active", Revision: 1, Sources: r.Sources, CreatedAt: now, UpdatedAt: now, CreatedBy: r.CreatedBy, CreatedByType: r.CreatedByType}
			if e = insertSourceRecord(ctx, tx, "skills", skill); e != nil {
				return e
			}
		} else if e != nil {
			return e
		}
		return insertSourceRecord(ctx, tx, "skill_versions", r)
	})
	if err != nil {
		f.t.Fatalf("seed skill version: %v", err)
	}
	return r
}

// promoteSkill drives one version from candidate to the active pointer and
// returns the cited experience id plus the version row.
func (f *fixture) promoteSkill(text string) (string, Record) {
	f.t.Helper()
	src := f.source()
	experience := f.activeMemory("the duplicate key needed a unique index", "E11000")
	version := f.seedSkillVersion(Record{Text: text, SkillID: "skill-1"}, src, experience)
	evaluation := f.create(f.human2, "evaluations", ratingInput(CreateInput{
		Scope: scopeA, Text: "independent check passed", Passed: true, Sources: refs(src),
		Target: &Ref{Collection: "skill_versions", ID: version.ID, Revision: version.Revision},
	}))
	version = f.transition(f.human, "skill_versions", version.ID, ChangeInput{
		Scope: scopeA, ExpectedRevision: version.Revision, Status: "approved", ValidationID: evaluation.ID,
	})
	f.apply(f.human, "skill-1", ChangeInput{Scope: scopeA, ExpectedRevision: 1, VersionID: version.ID}, false)
	return experience.ID, version
}

func TestActiveSkillSearchRequiresLiveLocalEvidence(t *testing.T) {
	f := newFixture(t)
	const query = "focused package test"
	experienceID, version := f.promoteSkill("always run the " + query + " before pushing")
	items := func() []Hit {
		return f.search(f.human, SearchInput{Purpose: "skills", Scope: scopeA, Query: query}).Items
	}

	hit := items()
	if len(hit) != 1 || hit[0].Collection != "skill_versions" || hit[0].ID != version.ID {
		t.Fatalf("items = %+v, want only the active version", hit)
	}
	// The cited experience is deleted: the version keeps its row, loses its hit.
	citation := f.get(f.human, "memories", scopeA, experienceID)
	if err := f.svc.Delete(testCtx, f.human, "memories", experienceID, ChangeInput{Scope: scopeA, ExpectedRevision: citation.Revision}); err != nil {
		t.Fatalf("revoke citation: %v", err)
	}
	if revoked := items(); len(revoked) != 0 {
		t.Fatalf("revoked local evidence still returned %+v", revoked)
	}
	if got := f.get(f.human, "skill_versions", scopeA, version.ID); got.Status != "active" {
		t.Fatalf("version status = %s, want the row kept but unsearchable", got.Status)
	}
}

func TestApplySkillRejectsAnExpiredVersion(t *testing.T) {
	f := newFixture(t)
	expires := time.Now().UTC().Add(time.Hour)
	src := f.source()
	experience := f.activeMemory("the duplicate key needed a unique index", "E11000")
	version := f.seedSkillVersion(Record{Text: "always run the focused package test", SkillID: "skill-1", ExpiresAt: &expires}, src, experience)
	evaluation := f.create(f.human2, "evaluations", ratingInput(CreateInput{
		Scope: scopeA, Text: "independent check passed", Passed: true, Sources: refs(src),
		Target: &Ref{Collection: "skill_versions", ID: version.ID, Revision: version.Revision},
	}))
	version = f.transition(f.human, "skill_versions", version.ID, ChangeInput{
		Scope: scopeA, ExpectedRevision: version.Revision, Status: "approved", ValidationID: evaluation.ID,
	})
	// It expires after approval, so the version itself is the only thing stale.
	stored := f.get(f.human, "skill_versions", scopeA, version.ID)
	past := time.Now().UTC().Add(-time.Minute)
	stored.ExpiresAt = &past
	if err := f.repo.Replace(testCtx, "skill_versions", stored, stored.Revision); err != nil {
		t.Fatalf("expire version: %v", err)
	}
	_, err := f.svc.ApplySkill(testCtx, f.human, "skill-1", ChangeInput{Scope: scopeA, ExpectedRevision: 1, VersionID: version.ID}, false)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expired apply err = %v, want ErrConflict", err)
	}
	if skill := f.get(f.human, "skills", scopeA, "skill-1"); skill.ActiveVersion != "" {
		t.Fatalf("skill = %+v, want the pointer unmoved", skill)
	}
	if kept := f.get(f.human, "skill_versions", scopeA, version.ID); kept.Status != "approved" {
		t.Fatalf("version status = %s, want approved", kept.Status)
	}
}

func TestWorkOnceSkipsAnAlreadyEmbeddedRevision(t *testing.T) {
	f := newFixture(t)
	rec := f.activeMemory("duplicate key handling", "E11000")
	if err := f.svc.WorkOnce(testCtx, f.work); err != nil {
		t.Fatalf("first WorkOnce: %v", err)
	}
	if len(f.emb.calls) != 1 {
		t.Fatalf("embed calls = %v, want one", f.emb.calls)
	}
	current := f.get(f.human, "memories", scopeA, rec.ID)
	if current.Embedding == nil {
		t.Fatalf("no embedding after the first run: %+v", current)
	}
	// Same content, same space: the second job must not reach the provider.
	if err := f.repo.Insert(testCtx, "jobs", Record{
		ID: ID(), Scope: scopeA, Kind: "embedding", Status: JobStatusPending, Revision: 1,
		Target: &Ref{Collection: "memories", ID: rec.ID, Revision: current.Revision},
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := f.svc.WorkOnce(testCtx, f.work); err != nil {
		t.Fatalf("second WorkOnce: %v", err)
	}
	if len(f.emb.calls) != 1 {
		t.Fatalf("embed calls = %v, want no duplicate call", f.emb.calls)
	}
	if after := f.get(f.human, "memories", scopeA, rec.ID); after.Revision != current.Revision {
		t.Fatalf("revision = %d, want no rewrite at %d", after.Revision, current.Revision)
	}
	for _, job := range f.embeddingJobs() {
		if job.Status != JobStatusDone {
			t.Fatalf("job = %+v, want every job done", job)
		}
	}
}

func TestWorkOnceParksAJobThatExceededItsAttempts(t *testing.T) {
	f := newFixture(t)
	f.activeMemory("too many attempts", "E11000")
	// Burn the attempt budget by letting the lease expire between claims.
	base := time.Now().UTC().Add(-10 * time.Minute)
	for i := 0; i <= maxJobAttempts; i++ {
		if _, err := f.repo.ClaimJob(testCtx, base.Add(time.Duration(i)*time.Minute), time.Minute); err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
	}
	if spent := f.embeddingJobs()[0]; spent.Attempts <= maxJobAttempts {
		t.Fatalf("attempts = %d, want above the %d budget", spent.Attempts, maxJobAttempts)
	}
	if err := f.svc.WorkOnce(testCtx, f.work); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable for a spent job", err)
	}
	if len(f.emb.calls) != 0 {
		t.Fatalf("embed calls = %v, want none", f.emb.calls)
	}
	if parked := f.embeddingJobs()[0]; parked.Status != JobStatusFailed {
		t.Fatalf("job = %+v, want the lease released as failed", parked)
	}
}

func TestSearchNeverLoadsLocalTextThroughASpoofedHit(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	purpose := f.activeMemory("the duplicate key needed a unique index", "E11000")
	const evaluationText = "LOCAL EVALUATION TEXT"
	evaluation := f.create(f.human2, "evaluations", ratingInput(CreateInput{
		Scope: scopeA, Text: evaluationText, Passed: true, Sources: refs(src),
		Target: &Ref{Collection: "memories", ID: purpose.ID, Revision: purpose.Revision},
	}))
	// A local-lane row naming a real source or evaluation is dropped, because
	// only memories and skill_versions may be rehydrated from this store.
	f.repo.stale = []Hit{
		{ID: evaluation.ID, Collection: "evaluations", Origin: "memory", Scope: scopeA, Status: "active", Revision: 1,
			Text: "spoofed evaluation", Sources: refs(src), UpdatedAt: time.Now().UTC(), Authority: AuthorityDataOnly},
		{ID: src.ID, Collection: "sources", Origin: "memory", Scope: scopeA, Status: "active", Revision: 1,
			Text: "spoofed source", Sources: refs(src), UpdatedAt: time.Now().UTC(), Authority: AuthorityDataOnly},
	}
	secrets := []string{src.Text, evaluationText}
	for _, item := range f.search(f.human, SearchInput{Purpose: "knowledge", Scope: scopeA, Query: "unique index"}).Items {
		if item.ID == src.ID || item.ID == evaluation.ID {
			t.Fatalf("spoofed row survived the origin check: %+v", item)
		}
		for _, secret := range secrets {
			if strings.Contains(item.Text, secret) || strings.Contains(item.Summary, secret) {
				t.Fatalf("local text %q leaked through a spoofed hit: %+v", secret, item)
			}
		}
	}
	if kept := f.get(f.human, "evaluations", scopeA, evaluation.ID); kept.Text != evaluationText {
		t.Fatalf("evaluation text = %q, want the stored row untouched", kept.Text)
	}
}

func TestReadAndSearchAuditsHashTheQueryAndFailClosed(t *testing.T) {
	f := newFixture(t)
	rec := f.activeMemory("the duplicate key needed a unique index", "E11000")
	const query = "unique index E11000 needle"
	f.search(f.human, SearchInput{Scope: scopeA, Query: query})
	f.get(f.human, "memories", scopeA, rec.ID)

	searches, reads := 0, 0
	for _, row := range f.repo.rows("audit_events") {
		if strings.Contains(row.Text, query) || strings.Contains(row.Summary, query) || strings.Contains(row.Target.ID, query) {
			t.Fatalf("plaintext query reached the audit row %+v", row)
		}
		switch row.Kind {
		case "search":
			searches++
			if row.Target.ID != ContentHash(query) {
				t.Fatalf("search audit target = %q, want the query hash", row.Target.ID)
			}
		case "read:memories":
			reads++
			if row.Target.ID != rec.ID {
				t.Fatalf("read audit target = %q, want %q", row.Target.ID, rec.ID)
			}
		}
	}
	if searches != 1 || reads != 1 {
		t.Fatalf("search audits = %d, read audits = %d, want one of each", searches, reads)
	}

	// Without its audit row, a read must return nothing at all.
	f.repo.failAudit = true
	bundle, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: query})
	if !errors.Is(err, ErrUnavailable) || len(bundle.Items) != 0 || bundle.Authority != "" {
		t.Fatalf("search = %+v err = %v, want a fail closed search", bundle, err)
	}
	record, err := f.svc.Get(testCtx, f.human, "memories", scopeA, rec.ID)
	if err == nil || record.ID != "" {
		t.Fatalf("record = %+v err = %v, want a fail closed read", record, err)
	}
}

func (f *fakeRepo) SearchCollection(_ context.Context, collection string, in SearchInput, vector []float64, space string) ([][]Hit, error) {
	return f.search(in.Scope, in.Query, vector, space, in.Limit, collection, in.SearchCriteria, in.Modes...)
}

func (f *fakeRepo) SearchReady(context.Context) error { return nil }

func (f *fakeRepo) OperationalMetrics(context.Context, Scope) (StoreMetrics, error) {
	return StoreMetrics{}, nil
}

func (f *fixture) embeddingJobs() []Record {
	var jobs []Record
	for _, r := range f.repo.rows("jobs") {
		if r.Kind == "embedding" {
			jobs = append(jobs, r)
		}
	}
	return jobs
}

// Synthetic provenance supplied explicitly by pre-existing source fixtures.
func sourceInput(in CreateInput) CreateInput {
	if in.Kind == "" {
		in.Kind = "document"
	}
	in.OriginProduct = "brain"
	in.SourceRef = "fixture/source"
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	in.CollectedAt = &at
	return in
}

func candidateInput(in CreateInput) CreateInput {
	if in.Kind == "" {
		in.Kind = "semantic"
	}
	return in
}

func (f *fakeRepo) MemoryUsage(_ context.Context, scope Scope, id string) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := []Record{}
	for _, r := range f.store["jobs"] {
		if r.Scope != scope || r.Kind != "context_usage" || r.Usage == nil {
			continue
		}
		for _, item := range r.Usage.Items {
			if item.Memory.Collection == "memories" && item.Memory.ID == id {
				rows = append(rows, r)
				break
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].ID > rows[j].ID
		}
		return rows[i].CreatedAt.After(rows[j].CreatedAt)
	})
	if len(rows) > 10 {
		rows = rows[:10]
	}
	return rows, nil
}

func (f *fakeRepo) RecentContextUsage(_ context.Context, scope Scope, limit int) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := []Record{}
	for _, r := range f.store["jobs"] {
		if r.Scope == scope && r.Kind == "context_usage" && r.Status == "done" && r.Usage != nil {
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })
	return rows[:min(limit, len(rows))], nil
}

func (f *fakeRepo) ExpiringCandidates(_ context.Context, scope Scope, before time.Time, limit int) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := []Record{}
	for _, r := range f.store["memory_candidates"] {
		if r.Scope == scope && expiresBefore(r, before) {
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].CreatedAt.Before(rows[j].CreatedAt)
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (x fakeTx) CheckRunToolTimes(_ context.Context, scope Scope, id string, finished time.Time) error {
	for _, r := range x.store["tool_calls"] {
		if r.Scope == scope && r.Target != nil && r.Target.Collection == "agent_runs" && r.Target.ID == id && (r.ToolCall == nil || r.ToolCall.FinishedAt.IsZero() || r.ToolCall.FinishedAt.After(finished)) {
			return ErrConflict
		}
	}
	return nil
}
func (x fakeTx) FenceRunCall(ctx context.Context, scope Scope, id string, revision int64) error {
	r, e := x.Get(ctx, "agent_runs", scope, id)
	if e != nil {
		return e
	}
	if r.Revision != revision {
		return ErrConflict
	}
	r.RunCallFence++
	x.store["agent_runs"][recKey(scope, id)] = r
	return nil
}
func (f *fakeRepo) CheckRunToolTimes(ctx context.Context, scope Scope, id string, finished time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().CheckRunToolTimes(ctx, scope, id, finished)
}
func (f *fakeRepo) FenceRunCall(ctx context.Context, scope Scope, id string, revision int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().FenceRunCall(ctx, scope, id, revision)
}

func (x fakeTx) EvaluationsFor(_ context.Context, scope Scope, target Ref) ([]Record, error) {
	var rows []Record
	for _, r := range x.store["evaluations"] {
		if r.Scope == scope && r.Target != nil && *r.Target == target && r.Status == "active" && live(r) {
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	if len(rows) > evaluationSummaryLimit+1 {
		rows = rows[:evaluationSummaryLimit+1]
	}
	return rows, nil
}
func (f *fakeRepo) EvaluationsFor(ctx context.Context, scope Scope, target Ref) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().EvaluationsFor(ctx, scope, target)
}
func ratingInput(in CreateInput) CreateInput {
	passed := in.Passed
	in.Evaluation = &Evaluation{EvalType: "user_rating", Passed: &passed}
	return in
}

func (x fakeTx) FenceSource(ctx context.Context, scope Scope, id string) error {
	r, e := x.Get(ctx, "sources", scope, id)
	if e != nil || !live(r) {
		return ErrConflict
	}
	r.MemoryReferenceFence++
	x.store["sources"][recKey(scope, id)] = r
	return nil
}
func (f *fakeRepo) FenceSource(ctx context.Context, scope Scope, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().FenceSource(ctx, scope, id)
}
func (x fakeTx) SourceErasureBatch(_ context.Context, scope Scope, id, kind string) ([]Ref, error) {
	out := []Ref{}
	for _, col := range sourceErasureCollections(kind) {
		rows := []Record{}
		for _, r := range x.store[col] {
			if r.Scope == scope {
				for _, src := range r.Sources {
					if src.SourceID == id {
						rows = append(rows, r)
						break
					}
				}
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		for _, r := range rows {
			if col == colSkills {
				children := []Record{}
				for _, v := range x.store[colSkillVersions] {
					if v.Scope == scope && v.SkillID == r.ID {
						children = append(children, v)
					}
				}
				sort.Slice(children, func(i, j int) bool { return children[i].ID < children[j].ID })
				if len(children) > 0 {
					for _, v := range children[:min(len(children), sourceMemoryErasureBatch-len(out))] {
						ref := Ref{Collection: colSkillVersions, ID: v.ID, Revision: v.Revision}
						exists := false
						for _, old := range out {
							if old == ref {
								exists = true
							}
						}
						if !exists {
							out = append(out, ref)
						}
					}
					return out, nil
				}
			}
			out = append(out, Ref{Collection: col, ID: r.ID, Revision: r.Revision})
			if len(out) == sourceMemoryErasureBatch {
				return out, nil
			}
		}
	}
	return out, nil
}
func (f *fakeRepo) SourceErasureBatch(ctx context.Context, scope Scope, id, kind string) ([]Ref, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().SourceErasureBatch(ctx, scope, id, kind)
}

func (x fakeTx) FenceRelationMember(ctx context.Context, scope Scope, col, id string) error {
	if !validScope(scope) || (col != "memories" && col != "memory_candidates") || !validRunLabel(id, 256) {
		return ErrInvalid
	}
	r, e := x.Get(ctx, col, scope, id)
	if e != nil {
		return ErrConflict
	}
	r.MemoryReferenceFence++
	x.store[col][recKey(scope, id)] = r
	return nil
}
func (f *fakeRepo) FenceRelationMember(ctx context.Context, scope Scope, col, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().FenceRelationMember(ctx, scope, col, id)
}

func (x fakeTx) MemoryErasureBatch(_ context.Context, scope Scope, id string, proposal bool, after, kind string) ([]Ref, error) {
	if !isMemoryErasureKind(kind) {
		return nil, ErrInvalid
	}
	if kind == memoryEvaluationErasureKind || kind == candidateEvaluationErasureKind {
		rows := []Ref{}
		for _, r := range x.store["evaluations"] {
			if r.Scope == scope && memoryEvaluationReferences(r, id, proposal, kind) {
				rows = append(rows, Ref{Collection: "evaluations", ID: r.ID, Revision: r.Revision})
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		if len(rows) > 0 {
			return rows[:min(len(rows), sourceMemoryErasureBatch)], nil
		}
	}
	if kind == memoryEvaluationErasureKind || kind == candidateEvaluationErasureKind {
		for _, col := range []string{colSkillVersions, "sources"} {
			var rows []Ref
			for _, r := range x.store[col] {
				if r.Scope == scope && (r.Kind == proposedSkillKind || r.Kind == proposedSkillSourceKind) && memoryRelationReferences(r, id, proposal, kind) {
					rows = append(rows, Ref{Collection: col, ID: r.ID, Revision: r.Revision})
				}
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
			if len(rows) > 0 {
				return rows[:min(len(rows), sourceMemoryErasureBatch)], nil
			}
		}
	}
	out := []Ref{}
	for _, r := range x.store["memory_relations"] {
		if r.Scope == scope && r.ID > after && memoryRelationReferences(r, id, proposal, kind) {
			out = append(out, Ref{Collection: "memory_relations", ID: r.ID, Revision: r.Revision})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out[:min(len(out), sourceMemoryErasureBatch)], nil
}
func (f *fakeRepo) MemoryErasureBatch(ctx context.Context, scope Scope, id string, proposal bool, after, kind string) ([]Ref, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().MemoryErasureBatch(ctx, scope, id, proposal, after, kind)
}

func (x fakeTx) ErasureChildren(_ context.Context, scope Scope, parent string) ([]Record, error) {
	out := []Record{}
	for _, r := range x.store["jobs"] {
		if r.Scope == scope && r.ErasureParentID == parent && (r.Kind == memoryEvaluationErasureKind || r.Kind == candidateEvaluationErasureKind) {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeRepo) ErasureChildren(ctx context.Context, scope Scope, parent string) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().ErasureChildren(ctx, scope, parent)
}

func (x fakeTx) DeletionRecords(_ context.Context, scope Scope) ([]Record, error) {
	rows := []Record{}
	for _, r := range x.store["jobs"] {
		if r.Scope == scope && slices.Contains(deletionLedgerKinds, r.Kind) {
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows[:min(len(rows), deletionLedgerLimit+1)], nil
}
func (f *fakeRepo) DeletionRecords(ctx context.Context, scope Scope) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().DeletionRecords(ctx, scope)
}

func (x fakeTx) BlobReferences(_ context.Context, digest string) (int64, error) {
	if !sourceHashPattern.MatchString(digest) {
		return 0, ErrInvalid
	}
	var count int64
	for _, rec := range x.store[colArtifacts] {
		if rec.Artifact != nil && rec.Artifact.Digest == digest {
			count++
		}
	}
	return count, nil
}
func (f *fakeRepo) BlobReferences(ctx context.Context, digest string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().BlobReferences(ctx, digest)
}

func (f *fakeRepo) BlobStorageIdentity(context.Context) (string, error) {
	return ContentHash(fmt.Sprintf("%p", f)), nil
}

func (f *fakeRepo) BlobGCBatch(_ context.Context, after string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for _, r := range f.store[colBlobs] {
		if r.Scope == blobScope() && r.Kind == "blob" && r.Blob != nil && r.Blob.References == 0 && (r.Status == statusActive || r.Status == JobStatusPending) && r.ID > after {
			ids = append(ids, r.ID)
		}
	}
	sort.Strings(ids)
	if len(ids) > 64 {
		ids = ids[:64]
	}
	return ids, nil
}

func (f *fakeRepo) ValidateBlobRestore(_ context.Context, files map[string]int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	counts := map[string]int64{}
	for _, r := range f.store[colArtifacts] {
		if e := countRestoreArtifact(r, files, counts); e != nil {
			return e
		}
	}
	for _, r := range f.store[colBlobs] {
		if e := checkRestoreBlob(r, files, counts); e != nil {
			return e
		}
	}
	if len(counts) != 0 {
		return ErrUnavailable
	}
	return nil
}

// Cosine distance over the fake store keeps semantic ordering testable without
// an embedding server, and still only ranks the ID allowlist it is handed.
func (x fakeTx) RankVectors(_ context.Context, s Scope, col string, ids []string, vector []float64, space string, limit int) ([]VectorRank, error) {
	if checkScope(s) != nil || col == "" || space == "" || len(vector) != VectorDimension || limit <= 0 || len(ids) == 0 {
		return nil, ErrInvalid
	}
	out := []VectorRank{}
	for _, id := range ids {
		rec, ok := x.store[col][recKey(s, id)]
		if !ok || rec.Embedding == nil || rec.Embedding.Space != space || len(rec.Embedding.Vector) != len(vector) {
			continue
		}
		dot, left, right := 0.0, 0.0, 0.0
		for i, value := range rec.Embedding.Vector {
			dot += value * vector[i]
			left += value * value
			right += vector[i] * vector[i]
		}
		distance := 1.0
		if left > 0 && right > 0 {
			distance = 1 - dot/(math.Sqrt(left)*math.Sqrt(right))
		}
		out = append(out, VectorRank{ID: id, Distance: distance})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (x fakeTx) RankKnowledgeText(_ context.Context, s Scope, ids, terms []string) ([]KnowledgeTextRank, error) {
	if checkScope(s) != nil || len(ids) > knowledgeCandidateLimit || len(terms) > knowledgeQueryMaxTerms {
		return nil, ErrInvalid
	}
	records := []Record{}
	for _, id := range ids {
		if rec, ok := x.store[colKnowledgeChunks][recKey(s, id)]; ok {
			records = append(records, rec)
		}
	}
	return knowledgeTextRankRecords(records, terms), nil
}

func (f *fakeRepo) RankKnowledgeText(ctx context.Context, s Scope, ids, terms []string) ([]KnowledgeTextRank, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().RankKnowledgeText(ctx, s, ids, terms)
}

func (f *fakeRepo) RankVectors(ctx context.Context, s Scope, col string, ids []string, vector []float64, space string, limit int) ([]VectorRank, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().RankVectors(ctx, s, col, ids, vector, space, limit)
}

func (f *fakeRepo) RequireRestoreReview(_ context.Context, digest string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.restoreManifest != "" && f.restoreManifest != digest {
		return ErrConflict
	}
	f.restoreManifest = digest
	return nil
}
func (f *fakeRepo) CheckRestorePublication(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.restoreManifest != "" {
		return ErrUnavailable
	}
	return nil
}
