package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestTemporaryRuleStopsFollowingExpiredOrReplacedMemory(t *testing.T) {
	for _, status := range []string{"expired", "superseded", "expiry_changed"} {
		t.Run(status, func(t *testing.T) {
			f := newFixture(t)
			rule := CaptureClaim{Subject: "temporary rule", Text: "Always record the deployment checklist for this week", Kind: "procedural", Basis: "explicit_user", Reason: "rule", Rule: "always", Temporary: true}
			m := f.approveCandidate(oneCandidate(t, f.capture(f.human, captureInput(scopeA, "temporary-rule", rule))))
			read := func() string {
				p, e := f.svc.BuildContext(testCtx, f.human, ContextBuildInput{Scope: scopeA, Task: "deployment checklist", Modes: []string{"fts"}, TokenEncoding: "o200k_base"})
				if e != nil {
					t.Fatal(e)
				}
				return structuredText(p.Directives)
			}
			if !strings.Contains(read(), rule.Text) {
				t.Fatal("missing initial rule")
			}
			stored := f.repo.store[colMemories][recKey(scopeA, m.ID)]
			if status == "expiry_changed" {
				at := time.Now().Add(-time.Hour)
				stored.ExpiresAt = &at
			} else {
				stored.Status = status
			}
			stored.Revision++
			f.repo.store[colMemories][recKey(scopeA, m.ID)] = stored
			if strings.Contains(read(), rule.Text) {
				t.Fatal("inactive original still injects a hard rule")
			}
			reflectRules, e := f.svc.directives(testCtx, scopeA, "reflect")
			if e != nil || len(reflectRules) != 0 {
				t.Fatal("inactive original still reaches reflection", reflectRules, e)
			}
		})
	}
}

func TestCaptureDistinctEpisodeDatesSurviveApproval(t *testing.T) {
	f := newFixture(t)
	c := CaptureClaim{Subject: "routine inspection", Text: "The routine inspection was completed", Kind: "episodic", Basis: "explicit_user", Reason: "event", OccurredFrom: "2026-10-01", OccurredTo: "2026-10-01"}
	a := f.approveCandidate(oneCandidate(t, f.capture(f.human, captureInput(scopeA, "inspection-a", c))))
	c.OccurredFrom, c.OccurredTo = "2026-10-08", "2026-10-08"
	b := f.approveCandidate(oneCandidate(t, f.capture(f.human, captureInput(scopeA, "inspection-b", c))))
	if a.ID == b.ID || a.Occurred == nil || b.Occurred == nil || a.Occurred.From.Equal(b.Occurred.From) {
		t.Fatal("distinct events collapsed")
	}
	for _, event := range []Record{a, b} {
		out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "routine inspection", Modes: []string{"fts"}, TemporalWindow: &TemporalWindow{From: event.Occurred.From, To: event.Occurred.To}, Limit: 10, MaxChars: 10000})
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, h := range out.Items {
			if h.ID == event.ID && h.Occurred != nil && h.Occurred.From.Equal(event.Occurred.From) {
				found = true
			}
		}
		if !found {
			t.Fatal("dated event missing from real recall", event.ID, out)
		}
	}
	retry := f.capture(f.human, captureInput(scopeA, "inspection-b-again", c))
	if retry[0].Reason != "duplicate_claim" {
		t.Fatal("same event date no longer deduplicated", retry)
	}
}

func TestImmediateMergeGateFailureRecoversWithoutRecapture(t *testing.T) {
	f := newFixture(t)
	f.emb.semantic = true
	f.mergeWorker()
	f.embedText(f.activeMemory("Responses should always be short and clear"))
	f.emb.err = errors.New("temporary embedding failure")
	result := f.capture(f.human, captureInput(scopeA, "immediate-failure", explicit("Responses should always be short and clear please")))
	jobs := f.mergeGateJobs()
	if len(jobs) != 1 {
		t.Fatal("transient failure lost its retry", len(jobs))
	}
	f.emb.err = nil
	if e := f.svc.runDeferredMergeGate(testCtx, f.work, jobs[0]); e != nil {
		t.Fatal(e)
	}
	if _, e := f.openProposal(result[0]); e != nil {
		t.Fatal("no proposal after recovery", e)
	}
	if e := f.svc.runDeferredMergeGate(testCtx, f.work, jobs[0]); e != nil {
		t.Fatal("retry not idempotent", e)
	}
}

type approvalRecoveryRepo struct {
	*entityTestRepo
	relations map[string]KnowledgeRelation
}
type approvalRecoveryTx struct {
	Tx
	namedEntityStore
	graph *approvalRecoveryRepo
}

func (r *approvalRecoveryRepo) PutKnowledgeRelation(_ context.Context, e KnowledgeRelation) error {
	if r.fail != nil {
		return r.fail
	}
	r.relations[structuredText(e.KnowledgeRelationInput)] = e
	return nil
}
func (r *approvalRecoveryRepo) KnowledgeRelations(_ context.Context, s Scope, n GraphEntity, limit int) ([]KnowledgeRelation, error) {
	var out []KnowledgeRelation
	for _, e := range r.relations {
		if e.Scope == s && e.Source == n && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}
func (r *approvalRecoveryRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.entityTestRepo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return fn(ctx, approvalRecoveryTx{Tx: tx, namedEntityStore: tx.(namedEntityStore), graph: r})
	})
}
func (tx approvalRecoveryTx) PutKnowledgeRelation(ctx context.Context, e KnowledgeRelation) error {
	return tx.graph.PutKnowledgeRelation(ctx, e)
}
func (tx approvalRecoveryTx) KnowledgeRelations(ctx context.Context, s Scope, n GraphEntity, limit int) ([]KnowledgeRelation, error) {
	return tx.graph.KnowledgeRelations(ctx, s, n, limit)
}

func TestApprovedRelationsRecoverThroughWorker(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprint(stale), func(t *testing.T) {
			f := newFixture(t)
			repo := &approvalRecoveryRepo{entityTestRepo: newEntityTestRepo(f.repo), relations: map[string]KnowledgeRelation{}}
			f.svc.Repo = repo
			f.auth.actors[f.work] = actor("worker-1", "service", true, "read", "write", "embed", "search", "context")
			claim := CaptureClaim{Subject: "Team assignment", Text: "Mira belongs to the Cedar team", Kind: "semantic", Basis: "explicit_user", Reason: "stated fact", Entities: []EntityInput{{Name: "Mira", Type: "person"}, {Name: "Cedar", Type: "organization"}}, Relations: []EntityRelationInput{{Source: "Mira", Relation: "member_of", Target: "Cedar"}}}
			id := oneCandidate(t, f.capture(f.human, captureInput(scopeA, "relation-recovery", claim)))
			repo.fail = errors.New("entity store temporarily unavailable")
			m := f.approveCandidate(id)
			var job Record
			for key, r := range f.repo.store[colJobs] {
				if r.Kind == "approved_entities" {
					job = r
				} else {
					r.Status = JobStatusDone
					f.repo.store[colJobs][key] = r
				}
			}
			if job.ID == "" {
				t.Fatal("approval did not durably queue recovery")
			}
			if len(repo.relations) != 0 {
				t.Fatal("relations written during outage")
			}
			if e := f.svc.WorkOnce(testCtx, f.work); e == nil {
				t.Fatal("outage reported success")
			}
			if f.repo.store[colJobs][recKey(scopeA, job.ID)].Status != JobStatusRetry {
				t.Fatal("failed recovery not retried")
			}
			repo.fail = nil
			if stale {
				r := f.repo.store[colMemories][recKey(scopeA, m.ID)]
				r.Status = "superseded"
				r.Revision++
				f.repo.store[colMemories][recKey(scopeA, m.ID)] = r
			}
			if e := f.svc.WorkOnce(testCtx, f.work); e != nil {
				t.Fatal(e)
			}
			if stale {
				if len(repo.relations) != 0 {
					t.Fatal("stale approval published relations")
				}
				return
			}
			if len(repo.relations) != 1 {
				t.Fatal("recovery did not publish one edge", len(repo.relations))
			}
			for _, edge := range repo.relations {
				out, e := f.svc.ExpandKnowledgeGraph(testCtx, f.human, KnowledgeGraphInput{Scope: scopeA, Start: edge.Source})
				if e != nil || len(out.Items) != 1 {
					t.Fatal("recovered relation not searchable", out, e)
				}
			}
			if e := f.svc.WorkOnce(testCtx, f.work); !errors.Is(e, ErrNotFound) {
				t.Fatal("completed recovery was not drained", e)
			}
			if len(repo.relations) != 1 {
				t.Fatal("duplicate relation")
			}
		})
	}
}

func TestLinkedDirectiveReviewAndRuleIdentity(t *testing.T) {
	for _, change := range []string{"legacy", "rule", "quarantine", "future_revision", "source_removed"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			claim := CaptureClaim{Subject: "rule", Text: "Keep deployment notes brief", Kind: "procedural", Basis: "explicit_user", Reason: "instruction", Rule: "always"}
			m := f.approveCandidate(oneCandidate(t, f.capture(f.human, captureInput(scopeA, "linked-rule", claim))))
			rows, e := f.svc.directives(testCtx, scopeA, "")
			if e != nil || len(rows) != 1 {
				t.Fatal(rows, e)
			}
			dir := rows[0]
			switch change {
			case "legacy":
				dir.Directive.SourceScope = nil
				dir.Directive.SourceRule = ""
				f.repo.store[colDirectives][recKey(dir.Scope, dir.ID)] = dir
			case "future_revision":
				dir.Proposal.Revision = m.Revision + 1
				f.repo.store[colDirectives][recKey(dir.Scope, dir.ID)] = dir
			case "rule":
				m.Tags = []string{ruleTagPrefix + "never"}
				m.Revision++
				f.repo.store[colMemories][recKey(scopeA, m.ID)] = m
			case "source_removed":
				delete(f.repo.store["sources"], recKey(scopeA, m.Sources[0].SourceID))
			case "quarantine":
				m.PrivacyQuarantined = true
				f.repo.store[colMemories][recKey(scopeA, m.ID)] = m
			}
			list, e := f.svc.ListDirectives(testCtx, f.human, scopeA, 10, "")
			if e != nil || len(list.Items) != 1 || list.Items[0].Active || list.Items[0].SourceState == "" {
				t.Fatal("source state invisible", list, e)
			}
			get, e := f.svc.GetDirective(testCtx, f.human, scopeA, dir.ID)
			if e != nil || get.Active || get.SourceState != list.Items[0].SourceState {
				t.Fatal(get, e)
			}
			applied, e := f.svc.directives(testCtx, scopeA, "context")
			if e != nil || len(applied) != 0 {
				t.Fatal("invalid linked directive applied", applied, e)
			}
			if change == "legacy" {
				updated, e := f.svc.UpdateDirective(testCtx, f.human, dir.ID, DirectiveInput{Scope: scopeA, ExpectedRevision: dir.Revision, Title: "Reviewed rule", Rule: claim.Text, Priority: 50, AppliesTo: "both"})
				if e != nil || !updated.Active {
					t.Fatal(updated, e)
				}
				applied, e = f.svc.directives(testCtx, scopeA, "context")
				if e != nil || len(applied) != 1 {
					t.Fatal("human review did not restore independent rule", applied, e)
				}
			}
		})
	}
}

type unavailableEntityPostgres struct {
	*Postgres
	unavailable bool
}

func (p *unavailableEntityPostgres) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	if p.unavailable {
		return ErrUnavailable
	}
	return p.Postgres.Transaction(ctx, fn)
}

func TestPostgresApprovedEntityOutboxAndReplay(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	f.auth.actors[f.work] = actor("worker-1", "service", true, "read", "write", "embed", "search", "context")
	claim := CaptureClaim{Subject: "Team assignment", Text: "Mira belongs to the Cedar team", Kind: "semantic", Basis: "explicit_user", Reason: "stated fact", Entities: []EntityInput{{Name: "Mira", Type: "person"}, {Name: "Cedar", Type: "organization"}}, Relations: []EntityRelationInput{{Source: "Mira", Relation: "member_of", Target: "Cedar"}}}
	m := f.approveCandidate(oneCandidate(t, f.capture(f.human, captureInput(scopeA, "pg-relation", claim))))
	var job Record
	for _, j := range pgCollection(t, p, colJobs) {
		if j.Kind == approvedEntitiesJobKind {
			job = j
			continue
		}
		j.Status = JobStatusDone
		j.Revision++
		if e := p.Replace(testCtx, colJobs, j, j.Revision-1); e != nil {
			t.Fatal(e)
		}
	}
	if job.ID == "" || job.Target.Revision != m.Revision {
		t.Fatal("outbox not committed with approval", job)
	}
	recovery := &unavailableEntityPostgres{Postgres: p, unavailable: true}
	f.svc.Repo = recovery
	if e := f.svc.WorkOnce(testCtx, f.work); !errors.Is(e, ErrUnavailable) {
		t.Fatal("outage", e)
	}
	retry, e := p.Get(testCtx, colJobs, scopeA, job.ID)
	if e != nil || retry.Status != JobStatusRetry || retry.Attempts != 1 {
		t.Fatal("durable retry", retry, e)
	}
	past := time.Now().Add(-time.Minute)
	retry.RunAt = &past
	retry.Revision++
	if e = p.Replace(testCtx, colJobs, retry, retry.Revision-1); e != nil {
		t.Fatal(e)
	}
	recovery.unavailable = false
	// The synchronous hook already asserted the relation. A replay must retain its
	// human author rather than conflict with an assertion attributed to the worker.
	if e := f.svc.WorkOnce(testCtx, f.work); e != nil {
		t.Fatal(e)
	}
	current, e := p.Get(testCtx, colJobs, scopeA, job.ID)
	if e != nil || current.Status != JobStatusDone || current.Attempts != 2 {
		t.Fatal("claim/dispatch/finalize", current, e)
	}
	g, e := loadEntityGraph(testCtx, p, scopeA)
	if e != nil {
		t.Fatal(e)
	}
	var node GraphEntity
	for _, ent := range g.entities {
		if ent.Name == "Mira" {
			node = GraphEntity{Kind: namedEntityKind, ID: ent.ID}
		}
	}
	out, e := f.svc.ExpandKnowledgeGraph(testCtx, f.human, KnowledgeGraphInput{Scope: scopeA, Start: node})
	if e != nil || len(out.Items) != 1 {
		t.Fatal("relation recall", out, e)
	}
	if e := f.svc.runApprovedEntities(testCtx, f.work, job); e != nil {
		t.Fatal("replay", e)
	}
	edges, e := p.KnowledgeRelations(testCtx, scopeA, node, 10)
	if e != nil || len(edges) != 1 || edges[0].AssertedBy != job.CreatedBy {
		t.Fatal("duplicate or changed author", edges, e)
	}
	// Removing the source before a queued retry must never recreate its graph edge.
	src, e := p.Get(testCtx, "sources", scopeA, m.Sources[0].SourceID)
	if e != nil {
		t.Fatal(e)
	}
	if e = p.Delete(testCtx, "sources", scopeA, src.ID, src.Revision); e != nil {
		t.Fatal(e)
	}
	if e = f.svc.runApprovedEntities(testCtx, f.work, job); !errors.Is(e, ErrDenied) {
		t.Fatal("removed source accepted", e)
	}
	edges, e = p.KnowledgeRelations(testCtx, scopeA, node, 10)
	if e != nil || len(edges) != 0 {
		t.Fatal("removed evidence restored", edges, e)
	}
}

func TestCaptureDatedLegacyPointerCompatibility(t *testing.T) {
	f := newFixture(t)
	claim := CaptureClaim{Subject: "inspection", Text: "The inspection completed", Kind: "episodic", Basis: "explicit_user", Reason: "event", OccurredFrom: "2026-10-01", OccurredTo: "2026-10-01"}
	in := captureInput(scopeA, "old-event", claim)
	m := f.approveCandidate(oneCandidate(t, f.capture(f.human, in)))
	_, key, legacy := captureClaimKeys(in, claim, scopeA)
	pointer := f.repo.store[colJobs][recKey(scopeA, key)]
	if pointer.ID == "" {
		t.Fatal("missing pointer")
	}
	delete(f.repo.store[colJobs], recKey(scopeA, key))
	pointer.ID = legacy
	f.repo.store[colJobs][recKey(scopeA, legacy)] = pointer
	again := f.capture(f.human, captureInput(scopeA, "old-retry", claim))
	if again[0].CandidateID != m.ID || again[0].Reason != "duplicate_claim" {
		t.Fatal("same date legacy lost", again)
	}
	claim.OccurredFrom, claim.OccurredTo = "2026-10-08", "2026-10-08"
	next := f.capture(f.human, captureInput(scopeA, "new-event", claim))
	if next[0].CandidateID == m.ID || next[0].Status != "candidate" {
		t.Fatal("different date legacy merged", next)
	}
}

func TestPostgresReindexPreservesApprovedEntityOutbox(t *testing.T) {
	for _, status := range []string{JobStatusPending, JobStatusDone} {
		t.Run(status, func(t *testing.T) {
			p := isolatedPostgres(t)
			f := newFixture(t)
			f.svc.Repo = p
			claim := CaptureClaim{Subject: "Team assignment", Text: "Mira belongs to the Cedar team", Kind: "semantic", Basis: "explicit_user", Reason: "stated fact", Entities: []EntityInput{{Name: "Mira", Type: "person"}, {Name: "Cedar", Type: "organization"}}, Relations: []EntityRelationInput{{Source: "Mira", Relation: "member_of", Target: "Cedar"}}}
			m := f.approveCandidate(oneCandidate(t, f.capture(f.human, captureInput(scopeA, "reindex-approved", claim))))
			var job Record
			before := 0
			for _, r := range pgCollection(t, p, colJobs) {
				if r.Kind == approvedEntitiesJobKind {
					job = r
				}
				if r.Kind == "embedding" && r.Target != nil && r.Target.ID == m.ID {
					before++
				}
			}
			if job.ID == "" {
				t.Fatal("approval outbox missing")
			}
			if status == JobStatusDone {
				job.Status = status
				job.Revision++
				if e := p.Replace(testCtx, colJobs, job, job.Revision-1); e != nil {
					t.Fatal(e)
				}
			}
			queued, e := p.EnqueueReindex(testCtx, "test:new-space:1024d")
			if e != nil || queued != 1 {
				t.Fatal("reindex collided with approval outbox", queued, e)
			}
			after, count := 0, 0
			for _, r := range pgCollection(t, p, colJobs) {
				if r.Kind == approvedEntitiesJobKind {
					count++
					if structuredText(r) != structuredText(job) {
						t.Fatal("reindex altered approval job", r)
					}
				}
				if r.Kind == "embedding" && r.Target != nil && r.Target.ID == m.ID && r.Target.Revision == m.Revision {
					after++
				}
			}
			if count != 1 || after != before+1 {
				t.Fatal("reindex lost embedding or duplicated recovery", count, before, after)
			}
		})
	}
}

func TestPostgresEntityExtractionReadOnlyAndLiveBatch(t *testing.T) {
	for _, mode := range []string{"dry_run", "mixed_liveness"} {
		t.Run(mode, func(t *testing.T) {
			p := isolatedPostgres(t)
			f := newFixture(t)
			f.svc.Repo = p
			seed := func(id, name string, offset int) Record {
				r := graphMemory(t, f, p, id)
				r.Entities = []EntityInput{{Name: name, Type: "person"}}
				r.Revision++
				when := time.Now().Add(time.Duration(offset) * time.Hour)
				if offset < 0 {
					r.ExpiresAt = &when
				}
				if offset > 0 {
					r.EffectiveAt = &when
				}
				if e := p.Replace(testCtx, colMemories, r, r.Revision-1); e != nil {
					t.Fatal(e)
				}
				return r
			}
			current := seed("current-memory", "Mira", 0)
			if mode == "mixed_liveness" {
				seed("expired-memory", "ExpiredPerson", -1)
				seed("future-memory", "FuturePerson", 1)
				bare := current
				bare.ID, bare.Revision, bare.Sources = "sourceless-memory", 1, nil
				bare.Entities = []EntityInput{{Name: "UnreferencedPerson", Type: "person"}}
				if e := p.Insert(testCtx, colMemories, bare); e != nil {
					t.Fatal(e)
				}
			}
			beforeMemory, e := p.Get(testCtx, colMemories, scopeA, current.ID)
			if e != nil {
				t.Fatal(e)
			}
			beforeSource, e := p.Get(testCtx, "sources", scopeA, current.Sources[0].SourceID)
			if e != nil {
				t.Fatal(e)
			}
			out, e := f.svc.ExtractEntities(testCtx, f.human, EntityExtractInput{Scope: scopeA, DryRun: mode == "dry_run"})
			if e != nil || out.Memories != 1 || out.NewEntities != 1 {
				t.Fatal("live extraction batch", out, e)
			}
			g, e := loadEntityGraph(testCtx, p, scopeA)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "dry_run" {
				afterMemory, e := p.Get(testCtx, colMemories, scopeA, current.ID)
				if e != nil {
					t.Fatal(e)
				}
				afterSource, e := p.Get(testCtx, "sources", scopeA, current.Sources[0].SourceID)
				if e != nil {
					t.Fatal(e)
				}
				if beforeMemory.MemoryReferenceFence != afterMemory.MemoryReferenceFence || beforeSource.MemoryReferenceFence != afterSource.MemoryReferenceFence || len(g.entities) != 0 || len(g.mentions) != 0 {
					t.Fatal("dry run wrote fences or graph", beforeMemory.MemoryReferenceFence, afterMemory.MemoryReferenceFence, beforeSource.MemoryReferenceFence, afterSource.MemoryReferenceFence)
				}
			} else {
				if len(g.entities) != 1 {
					t.Fatal("non-live entity extracted", g.entities)
				}
				for _, ent := range g.entities {
					if ent.Name != "Mira" {
						t.Fatal("wrong entity extracted", ent)
					}
				}
			}
		})
	}
}

// Remove source evidence after batch selection, before its write transaction.
type entityListRacePostgres struct {
	*Postgres
	afterList func()
}

func (p *entityListRacePostgres) ListRecordsByStatus(ctx context.Context, scope Scope, col, status string, limit int) ([]Record, error) {
	rows, e := p.Postgres.ListRecordsByStatus(ctx, scope, col, status, limit)
	if e == nil && col == colMemories && p.afterList != nil {
		f := p.afterList
		p.afterList = nil
		f()
	}
	return rows, e
}
func TestPostgresEntityExtractionRejectsSourceRemovalAfterSelection(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	r := graphMemory(t, f, p, "selected-memory")
	r.Entities = []EntityInput{{Name: "Mira", Type: "person"}}
	r.Revision++
	if e := p.Replace(testCtx, colMemories, r, r.Revision-1); e != nil {
		t.Fatal(e)
	}
	f.svc.Repo = &entityListRacePostgres{Postgres: p, afterList: func() {
		source, e := p.Get(testCtx, "sources", scopeA, r.Sources[0].SourceID)
		if e != nil {
			t.Fatal(e)
		}
		if e = p.Delete(testCtx, "sources", scopeA, source.ID, source.Revision); e != nil {
			t.Fatal(e)
		}
	}}
	if _, e := f.svc.ExtractEntities(testCtx, f.human, EntityExtractInput{Scope: scopeA}); !errors.Is(e, ErrDenied) {
		t.Fatal("removed source published", e)
	}
	g, e := loadEntityGraph(testCtx, p, scopeA)
	if e != nil || len(g.entities) != 0 || len(g.mentions) != 0 {
		t.Fatal("stale extraction persisted", g, e)
	}
	after, e := p.Get(testCtx, colMemories, scopeA, r.ID)
	if e != nil || after.MemoryReferenceFence != r.MemoryReferenceFence {
		t.Fatal("failed extraction did not roll back", after.MemoryReferenceFence, e)
	}
}
