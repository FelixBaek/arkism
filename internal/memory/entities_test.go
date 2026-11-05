package memory

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

type entityTestRepo struct {
	Repository
	ents  map[string]NamedEntity
	edges []EntityMention
	fail  error
}
type entityTestTx struct {
	Tx
	owner *entityTestRepo
}

func newEntityTestRepo(base Repository) *entityTestRepo {
	return &entityTestRepo{Repository: base, ents: map[string]NamedEntity{}}
}
func (r *entityTestRepo) LockNamedEntities(context.Context, Scope) error { return r.fail }
func (r *entityTestRepo) NamedEntities(_ context.Context, _ Scope, limit int) ([]NamedEntity, error) {
	if r.fail != nil {
		return nil, r.fail
	}
	out := []NamedEntity{}
	for _, id := range slices.Sorted(maps.Keys(r.ents)) {
		if len(out) < limit {
			out = append(out, r.ents[id])
		}
	}
	return out, nil
}
func (r *entityTestRepo) PutNamedEntity(_ context.Context, _ Scope, e NamedEntity) error {
	r.ents[e.ID] = e
	return nil
}
func (r *entityTestRepo) DeleteNamedEntity(_ context.Context, _ Scope, id string) error {
	delete(r.ents, id)
	r.edges = slices.DeleteFunc(r.edges, func(m EntityMention) bool { return m.EntityID == id })
	return nil
}
func (r *entityTestRepo) EntityMentions(_ context.Context, _ Scope, limit int) ([]EntityMention, error) {
	return slices.Clone(r.edges[:min(len(r.edges), limit)]), nil
}
func (r *entityTestRepo) ReplaceEntityMentions(_ context.Context, _ Scope, remove, add []EntityMention) error {
	same := func(a, b EntityMention) bool {
		return a.MemoryID == b.MemoryID && a.EntityID == b.EntityID && a.Relation == b.Relation
	}
	r.edges = slices.DeleteFunc(r.edges, func(m EntityMention) bool {
		return slices.ContainsFunc(remove, func(x EntityMention) bool { return same(m, x) })
	})
	for _, m := range add {
		if _, ok := r.ents[m.EntityID]; !ok {
			return ErrConflict // foreign key
		}
		if !slices.ContainsFunc(r.edges, func(x EntityMention) bool { return same(m, x) }) {
			r.edges = append(r.edges, m)
		}
	}
	return nil
}
func (r *entityTestRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	ents, edges := maps.Clone(r.ents), slices.Clone(r.edges)
	err := r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, entityTestTx{tx, r}) })
	if err != nil {
		r.ents, r.edges = ents, edges
	}
	return err
}
func (t entityTestTx) LockNamedEntities(ctx context.Context, s Scope) error {
	return t.owner.LockNamedEntities(ctx, s)
}
func (t entityTestTx) NamedEntities(ctx context.Context, s Scope, l int) ([]NamedEntity, error) {
	return t.owner.NamedEntities(ctx, s, l)
}
func (t entityTestTx) PutNamedEntity(ctx context.Context, s Scope, e NamedEntity) error {
	return t.owner.PutNamedEntity(ctx, s, e)
}
func (t entityTestTx) DeleteNamedEntity(ctx context.Context, s Scope, id string) error {
	return t.owner.DeleteNamedEntity(ctx, s, id)
}
func (t entityTestTx) EntityMentions(ctx context.Context, s Scope, l int) ([]EntityMention, error) {
	return t.owner.EntityMentions(ctx, s, l)
}
func (t entityTestTx) ReplaceEntityMentions(ctx context.Context, s Scope, rm, add []EntityMention) error {
	return t.owner.ReplaceEntityMentions(ctx, s, rm, add)
}

func entityFixture(t *testing.T) (*fixture, *entityTestRepo) {
	t.Helper()
	f := newFixture(t)
	repo := newEntityTestRepo(f.repo)
	f.svc.Repo = repo
	return f, repo
}

func entityMemory(t *testing.T, f *fixture, repo Repository, id, text string, at time.Time) Record {
	t.Helper()
	src := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "source for " + id}))
	r := Record{ID: id, Scope: scopeA, Kind: "decision", Text: text, Status: statusActive, ApprovedBy: "human-1", Revision: 1, Sources: refs(src), CreatedAt: at, UpdatedAt: at}
	if err := repo.Insert(testCtx, colMemories, r); err != nil {
		t.Fatal(err)
	}
	return r
}

func candidateNames(cs []entityCandidate) map[string]string {
	out := map[string]string{}
	for _, c := range cs {
		out[c.Name] = c.Type
	}
	return out
}

func TestExtractEntityCandidatesTable(t *testing.T) {
	cases := []struct {
		text string
		want map[string]string
		not  []string
	}{
		{"We moved Arkism to Postgres after @felix reviewed internal/memory/search.go.",
			map[string]string{"Arkism": "concept", "Postgres": "concept", "@felix": "person", "internal/memory/search.go": "file"}, []string{"We"}},
		{"The repo FelixBaek/inkism uses `ListRecords` and memory_records; see README.md and/or https://example.com/x.",
			map[string]string{"FelixBaek/inkism": "repo", "ListRecords": "concept", "memory_records": "concept", "README.md": "file"}, []string{"The", "and/or", "example.com"}},
		{"김민수님이 서울대학교에서 \"아크이즘 서버\"를 발표했다. 그리고 Arkism을 썼다.",
			map[string]string{"김민수": "person", "서울대학교": "organization", "아크이즘 서버": "concept", "Arkism": "concept"}, []string{"서울대학교에서", "Arkism을"}},
		{"사용자님이 e.g. don't care; commit deadbeefcafe.", map[string]string{}, []string{"사용자", "e.g", "don't", "deadbeefcafe"}},
		{"Claude Code helps. Release Notes go to Google Drive.", map[string]string{"Claude Code": "concept", "Release Notes": "concept", "Google Drive": "concept"}, nil},
	}
	for _, c := range cases {
		got := candidateNames(extractEntityCandidates(c.text))
		for name, typ := range c.want {
			if got[name] != typ {
				t.Errorf("%q: %q = %q, want %q (all %v)", c.text, name, got[name], typ, got)
			}
		}
		for _, name := range c.not {
			if _, ok := got[name]; ok {
				t.Errorf("%q: unexpected %q (all %v)", c.text, name, got)
			}
		}
	}
}

func TestEntityNormalizationAndResolve(t *testing.T) {
	if entityKey("Arkism") != entityKey(" @arkism ") || entityKey("Arkism") == entityKey("Arkis") {
		t.Fatal("key normalization")
	}
	if len(extractEntityCandidates("Arkism and later ARKISM and @arkism")) != 1 {
		t.Fatal("duplicate names inside one memory")
	}
	g := &entityGraph{entities: map[string]*NamedEntity{}, byKey: map[string][]*NamedEntity{}}
	tool := &NamedEntity{ID: "t", Name: "Postgres", Type: "tool"}
	tool.Keys = entityKeys(*tool)
	g.add(tool)
	if e, _ := g.resolve("postgres", entityTypeConcept); e != tool {
		t.Fatal("concept should resolve to the typed entity")
	}
	if e, skip := g.resolve("postgres", "person"); e != nil || skip {
		t.Fatal("a different specific type is another entity")
	}
	dead := &NamedEntity{ID: "d", Type: "person", Deleted: true, Keys: []string{entityKey("Bob")}}
	g.add(dead)
	if e, skip := g.resolve("bob", entityTypeConcept); e != nil || !skip {
		t.Fatal("tombstone must stop re-creation")
	}
}

func TestEntityExtractionCooccurrenceAndPages(t *testing.T) {
	f, repo := entityFixture(t)
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	entityMemory(t, f, repo, "m1", "Arkism stores memories in Postgres.", day)
	entityMemory(t, f, repo, "m2", "We tuned Postgres for Arkism.", day.AddDate(0, 1, 0))
	entityMemory(t, f, repo, "m3", "Postgres vacuum runs nightly; see `vacuum.sh`.", day.AddDate(0, 2, 0))
	dry, err := f.svc.ExtractEntities(testCtx, f.human, EntityExtractInput{Scope: scopeA, DryRun: true})
	if err != nil || dry.NewEntities != 3 || len(repo.ents) != 0 || len(dry.Items) != 3 {
		t.Fatal("dry run wrote or miscounted", dry, err, repo.ents)
	}
	out, err := f.svc.ExtractEntities(testCtx, f.human, EntityExtractInput{Scope: scopeA})
	if err != nil || out.NewEntities != 3 || out.AddedMentions != 6 {
		t.Fatal(out, err)
	}
	again, err := f.svc.ExtractEntities(testCtx, f.human, EntityExtractInput{Scope: scopeA})
	if err != nil || again.NewEntities != 0 || again.AddedMentions != 0 || again.RemovedMentions != 0 || len(repo.ents) != 3 {
		t.Fatal("rerun not idempotent", again, err)
	}
	list, err := f.svc.ListEntities(testCtx, f.agent, EntityListInput{Scope: scopeA, Limit: 2})
	if err != nil || list.Total != 3 || len(list.Items) != 2 || list.NextCursor != "2" || list.Items[0].Name != "Postgres" || list.Items[0].Mentions != 3 {
		t.Fatal(list, err)
	}
	pg := list.Items[0]
	if !pg.FirstSeen.Equal(day) || !pg.LastSeen.Equal(day.AddDate(0, 2, 0)) {
		t.Fatal("first/last seen", pg.FirstSeen, pg.LastSeen)
	}
	graph, err := f.svc.EntityGraphView(testCtx, f.agent, EntityGraphInput{Scope: scopeA})
	if err != nil || len(graph.Nodes) != 3 || len(graph.Edges) != 2 || graph.Edges[0].Weight != 2 {
		t.Fatal("co-occurrence", graph, err)
	}
	from := day.AddDate(0, 1, 0)
	graph, err = f.svc.EntityGraphView(testCtx, f.agent, EntityGraphInput{Scope: scopeA, From: &from})
	if err != nil || len(graph.Edges) != 2 || graph.Edges[0].Weight != 1 {
		t.Fatal("window", graph, err)
	}
	detail, err := f.svc.GetEntity(testCtx, f.human, scopeA, pg.ID)
	if err != nil || len(detail.Memories) != 3 || detail.Memories[0].ID != "m3" || detail.Memories[0].Snippet == "" ||
		len(detail.Related) != 2 || detail.Related[0].Name != "Arkism" || detail.Related[0].Weight != 2 || len(detail.Timeline) != 3 {
		t.Fatal(detail, err)
	}
}

func TestEntityMergeDeleteAndAuthorization(t *testing.T) {
	f, repo := entityFixture(t)
	now := time.Now().UTC()
	entityMemory(t, f, repo, "m1", "Deployed with `pg` today.", now)
	entityMemory(t, f, repo, "m2", "Finished the Postgres upgrade.", now)
	if _, err := f.svc.ExtractEntities(testCtx, f.agent, EntityExtractInput{Scope: scopeA}); !errors.Is(err, ErrDenied) {
		t.Fatal("agent extracted", err)
	}
	if _, err := f.svc.ExtractEntities(testCtx, f.human, EntityExtractInput{Scope: scopeA}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for id, e := range repo.ents {
		ids[e.Name] = id
	}
	pg, postgres := ids["pg"], ids["Postgres"]
	merge := EntityMergeInput{Scope: scopeA, Into: postgres}
	if _, err := f.svc.MergeEntity(testCtx, f.agent, pg, merge); !errors.Is(err, ErrDenied) {
		t.Fatal("agent merged", err)
	}
	if _, err := f.svc.PatchEntity(testCtx, f.agent, postgres, EntityPatchInput{Scope: scopeA, Type: ptr("tool")}); !errors.Is(err, ErrDenied) {
		t.Fatal("agent edited", err)
	}
	merged, err := f.svc.MergeEntity(testCtx, f.human, pg, merge)
	if err != nil || !slices.Contains(merged.Aliases, "pg") || len(repo.ents) != 1 || len(repo.edges) != 2 {
		t.Fatal(merged, err, repo.edges)
	}
	for _, m := range repo.edges {
		if m.EntityID != postgres {
			t.Fatal("mention not moved", m)
		}
	}
	// The alias now resolves to the merged entity on re-extraction.
	if out, err := f.svc.ExtractEntities(testCtx, f.human, EntityExtractInput{Scope: scopeA}); err != nil || out.NewEntities != 0 {
		t.Fatal("merged alias re-created", out, err)
	}
	renamed, err := f.svc.PatchEntity(testCtx, f.human, postgres, EntityPatchInput{Scope: scopeA, Name: ptr("PostgreSQL"), Type: ptr("tool")})
	if err != nil || renamed.Name != "PostgreSQL" || renamed.Type != "tool" || !slices.Contains(renamed.Aliases, "Postgres") {
		t.Fatal(renamed, err)
	}
	if _, err = f.svc.DeleteEntity(testCtx, f.agent, postgres, EntityScopeInput{Scope: scopeA}); !errors.Is(err, ErrDenied) {
		t.Fatal("agent deleted", err)
	}
	del, err := f.svc.DeleteEntity(testCtx, f.human, postgres, EntityScopeInput{Scope: scopeA})
	if err != nil || del.RemovedMentions != 2 || len(repo.edges) != 0 {
		t.Fatal(del, err)
	}
	if _, err = repo.Get(testCtx, colMemories, scopeA, "m1"); err != nil {
		t.Fatal("delete touched a memory", err)
	}
	if out, err := f.svc.ExtractEntities(testCtx, f.human, EntityExtractInput{Scope: scopeA}); err != nil || out.NewEntities != 0 || len(repo.edges) != 0 {
		t.Fatal("deleted entity came back", out, err)
	}
	if _, err = f.svc.GetEntity(testCtx, f.human, scopeA, postgres); !errors.Is(err, ErrNotFound) {
		t.Fatal("tombstone visible", err)
	}
	if _, err = f.svc.ListEntities(testCtx, f.agent, EntityListInput{Scope: scopeB}); err != nil {
		t.Fatal(err)
	}
}

func TestEntityModelProposalsNeedConfirmation(t *testing.T) {
	f, repo := entityFixture(t)
	entityMemory(t, f, repo, "m1", "lunch at the harbor cafe with mina", time.Now().UTC())
	if _, err := f.svc.ExtractEntities(testCtx, f.human, EntityExtractInput{Scope: scopeA, Model: true}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing model must be a clear error", err)
	}
	sent := []Record{{ID: "m1", Text: "lunch at the harbor cafe with mina"}}
	found := parseEntityModelAnswer("```json\n"+`{"items":[{"i":0,"entities":[{"name":"mina","type":"person"},{"name":"Atlantis","type":"place"},{"name":"cafe","type":"weird"}]}]}`+"\n```", sent)
	if len(found) != 1 || len(found[0].Names) != 1 || found[0].Names[0].Name != "mina" {
		t.Fatal("model output not validated against the text", found)
	}
	found[0].Memory = entityMemory(t, f, repo, "m2", "lunch at the harbor cafe with mina", time.Now().UTC())
	if _, err := f.svc.applyEntityExtraction(testCtx, scopeA, "human-1", found, false); err != nil {
		t.Fatal(err)
	}
	if len(repo.edges) != 1 || repo.edges[0].Relation != relMentionsProposed {
		t.Fatal("model finding not a proposal", repo.edges)
	}
	list, _ := f.svc.ListEntities(testCtx, f.agent, EntityListInput{Scope: scopeA})
	if list.Total != 0 {
		t.Fatal("agent sees proposals", list)
	}
	id := repo.edges[0].EntityID
	if _, err := f.svc.PatchEntity(testCtx, f.human, id, EntityPatchInput{Scope: scopeA, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if repo.edges[0].Relation != relMentions || repo.ents[id].Proposed {
		t.Fatal("confirm did not activate", repo.edges, repo.ents[id])
	}
}

func TestApprovalSucceedsWhenEntityExtractionFails(t *testing.T) {
	f, repo := entityFixture(t)
	repo.fail = errors.New("entity store down")
	src := f.source()
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "Use Postgres for Arkism", Sources: refs(src)}))
	m, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"})
	if err != nil || m.Status != statusActive {
		t.Fatal("approval failed with extraction", m, err)
	}
	repo.fail = nil
	c = f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "Deploy Arkism nightly", Sources: refs(src)}))
	if _, err = f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"}); err != nil {
		t.Fatal(err)
	}
	if len(repo.ents) != 1 || len(repo.edges) != 1 {
		t.Fatal("approval did not extract", repo.ents, repo.edges)
	}
}

// The trigger in 0010 reads these exact stored shapes; keep the SQL and the codec in step.
func TestEntitySQLMatchesStoredForms(t *testing.T) {
	doc := map[string]any{}
	b, err := storeMarshal(Record{ID: "m", Status: statusActive, ContentErased: true, PrivacyQuarantined: true})
	if err != nil || json.Unmarshal(b, &doc) != nil || doc["content_erased"] != true || doc["privacy_quarantined"] != true {
		t.Fatal("stored doc flags", string(b), err)
	}
	if b, _ = storeMarshal(Record{ID: "m"}); strings.Contains(string(b), "content_erased") || strings.Contains(string(b), "privacy_quarantined") {
		t.Fatal("false flags must be omitted, the trigger compares presence", string(b))
	}
	if b, _ = json.Marshal(NamedEntity{ID: "e", Deleted: true}); !strings.Contains(string(b), `"deleted":true`) {
		t.Fatal("tombstone metadata", string(b))
	}
	for _, want := range []string{"doc->'content_erased'", "doc->'privacy_quarantined'", "metadata->>'deleted'", "OLD.status='active'"} {
		if !strings.Contains(schemaNamedEntitiesSQL, want) {
			t.Fatal("migration no longer reads", want)
		}
	}
	if strings.Contains(schemaNamedEntitiesSQL, "::timestamptz") || strings.ContainsRune(namedEntityLockKey(scopeA), 0) {
		t.Fatal("stored dates are {$date} objects; Postgres text takes no NUL byte")
	}
}

func TestEntityModelPromptSkipsPrivateAndSecret(t *testing.T) {
	batch := []Record{{ID: "a", Text: "Arkism note"}, {ID: "b", Text: "private note", Sensitivity: "private"},
		{ID: "c", Text: "restricted note", Sensitivity: "restricted"}, {ID: "d", Text: "key AKIAIOSFODNN7EXAMPLE"}}
	user, sent := entityModelPrompt(batch)
	if len(sent) != 1 || sent[0].ID != "a" || strings.Contains(user, "private note") || strings.Contains(user, "AKIA") {
		t.Fatal(user, sent)
	}
}

func TestSupersedeMovesMentionsToReplacement(t *testing.T) {
	f, repo := entityFixture(t)
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	old := entityMemory(t, f, repo, "m1", "Arkism uses Postgres.", day)
	next := entityMemory(t, f, repo, "m2", "Arkism uses Postgres and Valkey.", day)
	if _, err := f.svc.ExtractEntities(testCtx, f.human, EntityExtractInput{Scope: scopeA}); err != nil {
		t.Fatal(err)
	}
	// The fake repo has no trigger; drop the old edges the way 0010 does on leaving 'active'.
	repo.edges = slices.DeleteFunc(repo.edges, func(m EntityMention) bool { return m.MemoryID == old.ID })
	repo.edges = slices.DeleteFunc(repo.edges, func(m EntityMention) bool { return m.MemoryID == next.ID })
	if _, err := f.svc.Supersede(testCtx, f.human, old.ID, SupersedeInput{Scope: scopeA, ExpectedRevision: old.Revision, Reason: "edit", ReplacementMemoryID: next.ID, ReplacementExpectedRevision: next.Revision}); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(repo.edges, func(m EntityMention) bool { return m.MemoryID == next.ID }) {
		t.Fatal("replacement not re-extracted", repo.edges)
	}
}
