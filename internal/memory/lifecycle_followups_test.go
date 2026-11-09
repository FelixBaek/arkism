//go:build linux

package memory

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type costlyExtractLLM struct {
	*fakeWikiLLM
	tokens int64
}

func (l *costlyExtractLLM) Complete(ctx context.Context, system, user string, max int) (string, ProviderUsage, error) {
	text, u, e := l.fakeWikiLLM.Complete(ctx, system, user, max)
	u.TotalTokens = l.tokens
	return text, u, e
}
func TestExtractWholeJobBudgetIncludesChunksAndRestart(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "chunks", true: "restart"}[resume], func(t *testing.T) {
			f, llm := wikiFixture(t, extractAnswer(), extractAnswer(), extractAnswer(), extractAnswer(), extractAnswer(), extractAnswer())
			f.svc.Wiki = &costlyExtractLLM{fakeWikiLLM: llm, tokens: 80000}
			text := strings.Repeat(strings.Repeat("배포 전에 테스트를 실행한다. ", 200)+"\n\n", 5)
			queued, e := f.svc.ExtractFileMemories(testCtx, f.human, FileMemoriesInput{Scope: scopeA, Filename: "long.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte(text))})
			if e != nil || queued.Status != JobStatusPending {
				t.Fatal(queued, e)
			}
			if resume {
				r := f.repo.store[colJobs][recKey(scopeA, queued.JobID)]
				r.FileExtract.Tokens = fileExtractJobTokens - 1
				f.repo.store[colJobs][recKey(scopeA, r.ID)] = r
			}
			out, e := f.svc.RunFileExtractJob(testCtx, f.human, scopeA, queued.JobID)
			want := 2
			if resume {
				want = 0
			}
			if e != nil || out.Status != JobStatusFailed || len(llm.prompts) != want {
				t.Fatal("whole-job budget bypass", out.Status, len(llm.prompts), e)
			}
			_, e = f.svc.RunFileExtractJob(testCtx, f.human, scopeA, queued.JobID)
			if e != nil || len(llm.prompts) != want {
				t.Fatal("restart reset spend", len(llm.prompts), e)
			}
		})
	}
}

func TestApprovedRelationAmbiguityDoesNotGuess(t *testing.T) {
	f, repo := entityFixture(t)
	for _, e := range []NamedEntity{{ID: "person", Name: "Cedar", Type: "person"}, {ID: "organization", Name: "Cedar", Type: "organization"}, {ID: "other", Name: "Mira", Type: "person"}} {
		e.Keys = entityKeys(e)
		repo.ents[e.ID] = e
	}
	r := Record{Scope: scopeA, ID: "m", Revision: 1, ApprovedBy: "human", EntityRelations: []EntityRelationInput{{Source: "Mira", Target: "Cedar", Relation: "member_of"}}}
	edges, e := approvedEntityRelations(testCtx, repo, r)
	if !errors.Is(e, ErrConflict) || len(edges) != 0 {
		t.Fatal("ambiguous endpoint guessed", edges, e)
	}
	// A unique explicitly stated type disambiguates without merging names.
	r.Entities = []EntityInput{{Name: "Cedar", Type: "organization"}, {Name: "Mira", Type: "person"}}
	edges, e = approvedEntityRelations(testCtx, repo, r)
	if e != nil || len(edges) != 1 || edges[0].Target.ID != "organization" {
		t.Fatal("typed endpoint unresolved", edges, e)
	}
	_ = f
}

func TestApprovedSummaryScheduleRecoversWithoutModelCall(t *testing.T) {
	for _, enabled := range []string{"on", "off"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("MEMORY_AUTO_DERIVE", enabled)
			f := newFixture(t)
			repo := &approvalRecoveryRepo{entityTestRepo: newEntityTestRepo(f.repo), relations: map[string]KnowledgeRelation{}}
			f.svc.Repo = repo
			llm := &fakeWikiLLM{}
			f.svc.Wiki = llm
			f.auth.actors[f.work] = actor("worker-1", "service", true, "read", "write", "embed", "search", "context")
			claim := CaptureClaim{Subject: "person", Text: "Mira owns the release checklist", Kind: "semantic", Basis: "explicit_user", Reason: "stated fact", Entities: []EntityInput{{Name: "Mira", Type: "person"}}}
			id := oneCandidate(t, f.capture(f.human, captureInput(scopeA, "summary-recovery", claim)))
			repo.fail = errors.New("store unavailable")
			f.approveCandidate(id)
			repo.fail = nil
			delete(f.repo.store[colJobs], recKey(scopeA, autoDeriveJobID(scopeA))) // interrupted best-effort scheduling
			var job Record
			for _, r := range f.repo.rows(colJobs) {
				if r.Kind == approvedEntitiesJobKind {
					job = r
				}
			}
			if e := f.svc.runApprovedEntities(testCtx, f.work, job); e != nil {
				t.Fatal(e)
			}
			derived, e := f.repo.Get(testCtx, colJobs, scopeA, autoDeriveJobID(scopeA))
			if enabled == "off" {
				if !errors.Is(e, ErrNotFound) {
					t.Fatal("disabled scheduling", e)
				}
				return
			}
			if e != nil || derived.AutoDerive == nil || len(derived.AutoDerive.Entities) != 1 || derived.Status != JobStatusPending {
				t.Fatal("summary schedule lost", derived.AutoDerive, e)
			}
			if len(llm.prompts) != 0 {
				t.Fatal("recovery called model")
			}
			snapshot := structuredText(derived)
			if e = f.svc.runApprovedEntities(testCtx, f.work, job); e != nil {
				t.Fatal(e)
			}
			derived, _ = f.repo.Get(testCtx, colJobs, scopeA, autoDeriveJobID(scopeA))
			if structuredText(derived) != snapshot {
				t.Fatal("replay rescheduled summary")
			}
		})
	}
}

func TestPostgresStaleCascadeVisitsBeyondFirstPage(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	old := f.activeMemory("old decision")
	const total = 501
	e := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		for i := 0; i < total; i++ {
			for _, col := range []string{colTopicDigests, colSkillVersions, colObservations} {
				r := Record{ID: fmt.Sprintf("dependent-%04d", i), Scope: scopeA, Status: statusActive, Revision: 1, Text: "old derived text", ApprovedBy: "human", CreatedBy: "human", Sources: old.Sources, CreatedAt: time.Now(), UpdatedAt: time.Now()}
				switch col {
				case colTopicDigests:
					r.Kind = digestKindTopic
					r.Digest = &DigestData{Members: memRefs(old)}
				case colSkillVersions:
					r.Kind = proposedSkillKind
					r.Status = "candidate"
					r.SkillID = "skill"
					r.Relations = memRefs(old)
				case colObservations:
					r.Kind = observationKind
					r.Observation = &ObservationData{SourceMemories: memRefs(old)}
				}
				if e := tx.Insert(ctx, col, r); e != nil {
					return e
				}
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.svc.Supersede(testCtx, f.human, old.ID, SupersedeInput{Scope: scopeA, ExpectedRevision: old.Revision, Reason: "corrected", NewContent: "new decision", Sources: old.Sources})
	if e != nil {
		t.Fatal(e)
	}
	for _, col := range []string{colTopicDigests, colSkillVersions, colObservations} {
		n := 0
		for _, r := range pgCollection(t, p, col) {
			if r.StaleMark != nil && r.StaleMark.Memory.ID == old.ID {
				n++
			}
		}
		if n != total {
			t.Fatalf("%s marked %d/%d", col, n, total)
		}
	}
}

func TestPostgresExpiredLinkedRulesReleaseActiveCapacity(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	old := f.activeMemory("temporary instruction")
	old.Tags = []string{ruleTagPrefix + "always"}
	past := time.Now().Add(-time.Hour)
	old.ExpiresAt = &past
	old.Revision++
	if e := p.Replace(testCtx, colMemories, old, old.Revision-1); e != nil {
		t.Fatal(e)
	}
	e := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		for i := 0; i < directiveMaxPerScope; i++ {
			r := Record{ID: fmt.Sprintf("old-rule-%04d", i), Scope: projectScope(scopeA), Kind: directiveKind, Status: statusActive, Revision: 1, Text: old.Text, Directive: &Directive{Title: "old", AppliesTo: "both", SourceScope: &old.Scope, SourceRule: "always"}, Proposal: &Ref{Collection: colMemories, ID: old.ID, Revision: old.Revision}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
			if e := tx.Insert(ctx, colDirectives, r); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	claim := CaptureClaim{Subject: "new rule", Text: "Use the new checklist", Kind: "procedural", Basis: "explicit_user", Reason: "instruction", Rule: "always"}
	id := oneCandidate(t, f.capture(f.human, captureInput(scopeA, "new-rule", claim)))
	f.approveCandidate(id)
	rows, e := p.ListRecordsByStatus(testCtx, projectScope(scopeA), colDirectives, statusActive, directiveMaxPerScope)
	if e != nil || len(rows) != 1 {
		t.Fatal("stale rules retain active capacity", len(rows), e)
	}
	retired, e := p.Get(testCtx, colDirectives, projectScope(scopeA), "old-rule-0000")
	if e != nil || retired.Status != "inactive" || retired.Text != old.Text || retired.Revision != 2 {
		t.Fatal("retirement lost provenance", retired, e)
	}
}

func TestPostgresConversationEpochOrderSurvivesReverseCapture(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	setCaptureDensity(t, f, captureDensityConversation)
	rows := map[int]Record{}
	inputs := map[int]CreateInput{}
	for _, i := range []int{4, 2, 1, 3} {
		at := time.Now().Add(time.Duration(-10+i) * time.Second)
		in := sourceInput(CreateInput{Scope: scopeA, Kind: conversationSourceKind, Text: "repeated conversation text", ConversationOrder: &ConversationOrder{Epoch: "capture-a", Index: i, Total: 4}})
		if i == 2 {
			in.Text = "anchor copper valve"
		}
		in.SourceRef = "same-session"
		in.CollectedAt = &at
		inputs[i] = in
		rows[i] = f.create(f.human, "sources", in)
	}
	if rows[1].ID == rows[3].ID {
		t.Fatal("same-text chunks collapsed")
	}
	neighbors, e := p.ConversationNeighbors(testCtx, rows[2])
	if e != nil || len(neighbors) != 2 {
		t.Fatal("explicit neighbors absent", neighbors, e)
	}
	got := map[int]bool{}
	for _, r := range neighbors {
		got[r.ConversationOrder.Index] = true
	}
	if !got[1] || !got[3] {
		t.Fatal("capture time used as order", got)
	}
	bundle, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "copper valve", Modes: []string{"fts"}, ConversationContext: true, BudgetTokens: 3000, TokenEncoding: "cl100k_base"})
	if e != nil || len(bundle.Conversation) != 1 || bundle.Conversation[0].ID != rows[2].ID {
		t.Fatal("ordered service recall", bundle.Conversation, e)
	}
	evidence := map[string]bool{}
	for _, ref := range bundle.Conversation[0].Sources {
		evidence[ref.SourceID] = true
	}
	if !evidence[rows[1].ID] || !evidence[rows[3].ID] || evidence[rows[4].ID] {
		t.Fatal("service recall used wrong neighbors", evidence)
	}
	repeat := f.create(f.human, "sources", inputs[2])
	if repeat.ID != rows[2].ID {
		t.Fatal("recapture duplicated slot")
	}
	altered := inputs[2]
	altered.Text = "different content in same slot"
	if _, e = f.svc.Create(testCtx, f.human, "sources", altered); !errors.Is(e, ErrConflict) {
		t.Fatal("slot overwritten", e)
	}
	if e = p.Delete(testCtx, "sources", scopeA, rows[3].ID, rows[3].Revision); e != nil {
		t.Fatal(e)
	}
	neighbors, e = p.ConversationNeighbors(testCtx, rows[2])
	if e != nil || len(neighbors) != 1 || neighbors[0].ID != rows[1].ID {
		t.Fatal("deleted gap bridged", neighbors, e)
	}
	if _, e = f.svc.Create(testCtx, f.human, "sources", inputs[3]); !errors.Is(e, ErrConflict) {
		t.Fatal("deleted slot resurrected", e)
	}
	other := inputs[3]
	o := *other.ConversationOrder
	o.Epoch = "capture-b"
	other.ConversationOrder = &o
	f.create(f.human, "sources", other)
	neighbors, e = p.ConversationNeighbors(testCtx, rows[2])
	if e != nil || len(neighbors) != 1 {
		t.Fatal("epochs joined", neighbors, e)
	}
	changed := rows[1]
	order := *changed.ConversationOrder
	order.Index = 4
	changed.ConversationOrder = &order
	changed.Revision++
	if e = p.Replace(testCtx, "sources", changed, changed.Revision-1); !errors.Is(e, ErrConflict) {
		t.Fatal("source order was mutable", e)
	}
}

func TestExtractBatchRespectsWholeJobRemainingBudget(t *testing.T) {
	f, _ := wikiFixture(t)
	llm := &fakeBatchLLM{}
	f.svc.Wiki = llm
	putSettings(t, f, scopeA, SpaceConfigInput{ExtractionChunkSize: ptr(500)})
	text := strings.Repeat(strings.Repeat("alpha beta gamma ", 25)+"\n\n", 6)
	out, e := f.svc.Ingest(testCtx, f.human, IngestInput{Scope: scopeA, Async: true, Items: []IngestItem{{Content: text}}})
	if e != nil {
		t.Fatal(e)
	}
	id := out.Items[0].JobID
	r := f.repo.store[colJobs][recKey(scopeA, id)]
	r.FileExtract.Tokens = fileExtractJobTokens - 1
	f.repo.store[colJobs][recKey(scopeA, id)] = r
	result, e := f.svc.RunFileExtractJob(testCtx, f.human, scopeA, id)
	if e != nil || result.Status != JobStatusFailed || len(llm.submitted) != 0 || len(f.repo.rows(colProviderUsage)) != 0 {
		t.Fatal("batch exceeded remaining budget", result, e)
	}
}

func TestPostgresSummaryRecoveryReceiptAndAmbiguousRelations(t *testing.T) {
	t.Setenv("MEMORY_AUTO_DERIVE", "on")
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	llm := &fakeWikiLLM{}
	f.svc.Wiki = llm
	f.auth.actors[f.work] = actor("worker-1", "service", true, "read", "write", "embed", "search", "context")
	// Use an approved source-backed record whose synchronous hook never ran.
	r := graphMemory(t, f, p, "interrupted-approval")
	r.Entities = []EntityInput{{Name: "Mira", Type: "person"}}
	r.Revision++
	if e := p.Replace(testCtx, colMemories, r, r.Revision-1); e != nil {
		t.Fatal(e)
	}
	job := Record{Scope: scopeA, Target: &Ref{Collection: colMemories, ID: r.ID, Revision: r.Revision}, CreatedBy: "human"}
	if e := f.svc.runApprovedEntities(testCtx, f.work, job); e != nil {
		t.Fatal(e)
	}
	derived, e := p.Get(testCtx, colJobs, scopeA, autoDeriveJobID(scopeA))
	if e != nil || len(derived.AutoDerive.Entities) != 1 {
		t.Fatal(derived, e)
	}
	saved := structuredText(derived)
	if e = f.svc.runApprovedEntities(testCtx, f.work, job); e != nil {
		t.Fatal(e)
	}
	derived, e = p.Get(testCtx, colJobs, scopeA, autoDeriveJobID(scopeA))
	if e != nil || structuredText(derived) != saved || len(llm.prompts) != 0 {
		t.Fatal("receipt or no-model guarantee", e)
	}
	for _, ent := range []NamedEntity{{ID: "cedar-person", Name: "Cedar", Type: "person"}, {ID: "cedar-organization", Name: "Cedar", Type: "organization"}} {
		ent.Keys = entityKeys(ent)
		if e = p.PutNamedEntity(testCtx, scopeA, ent); e != nil {
			t.Fatal(e)
		}
	}
	r.EntityRelations = []EntityRelationInput{{Source: "Mira", Target: "Cedar", Relation: "member_of"}}
	edges, e := approvedEntityRelations(testCtx, p, r)
	if !errors.Is(e, ErrConflict) || len(edges) != 0 {
		t.Fatal("ambiguous DB endpoint guessed", edges, e)
	}
	r.Entities = append(r.Entities, EntityInput{Name: "Cedar", Type: "organization"})
	edges, e = approvedEntityRelations(testCtx, p, r)
	if e != nil || len(edges) != 1 || edges[0].Target.ID != "cedar-organization" {
		t.Fatal("typed DB endpoint", edges, e)
	}
}

func TestPostgresDirectiveCapacitySerializesConcurrentCreates(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	e := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		for i := 0; i < 399; i++ {
			status := statusActive
			if i >= 199 {
				status = "inactive"
			}
			r := Record{ID: fmt.Sprintf("existing-%04d", i), Scope: projectScope(scopeA), Kind: directiveKind, Status: status, Revision: 1, Text: "manual rule", Directive: &Directive{Title: "manual", AppliesTo: "both"}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
			if e := tx.Insert(ctx, colDirectives, r); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			<-start
			_, e := f.svc.CreateDirective(testCtx, f.human, DirectiveInput{Scope: scopeA, Title: fmt.Sprintf("new-%d", i), Rule: "new manual rule", AppliesTo: "both"})
			results <- e
		}(i)
	}
	close(start)
	success, conflict := 0, 0
	for i := 0; i < 2; i++ {
		e := <-results
		if e == nil {
			success++
		} else if errors.Is(e, ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("active cap raced", success, conflict)
	}
	rules, e := f.svc.directives(testCtx, scopeA, "context")
	if e != nil || len(rules) != directiveMaxPerScope {
		t.Fatal("retired history displaced live rules", len(rules), e)
	}
}

func TestSummarySchedulingDefersWithoutChangingRunningLease(t *testing.T) {
	t.Setenv("MEMORY_AUTO_DERIVE", "on")
	f := newFixture(t)
	repo := &approvalRecoveryRepo{entityTestRepo: newEntityTestRepo(f.repo), relations: map[string]KnowledgeRelation{}}
	f.svc.Repo = repo
	f.svc.Wiki = &fakeWikiLLM{}
	f.auth.actors[f.work] = actor("worker-1", "service", true, "read", "write", "embed", "search", "context")
	claim := CaptureClaim{Subject: "person", Text: "Mira owns release notes", Kind: "semantic", Basis: "explicit_user", Reason: "fact", Entities: []EntityInput{{Name: "Mira", Type: "person"}}}
	id := oneCandidate(t, f.capture(f.human, captureInput(scopeA, "running-summary", claim)))
	repo.fail = errors.New("approval hook interrupted")
	f.approveCandidate(id)
	repo.fail = nil
	running, e := f.repo.Get(testCtx, colJobs, scopeA, autoDeriveJobID(scopeA))
	if e != nil {
		t.Fatal(e)
	}
	running.Status = JobStatusRunning
	running.LeaseToken = "model-pass-lease"
	running.LeaseUntil = time.Now().Add(time.Minute)
	running.Revision++
	if e = f.repo.Replace(testCtx, colJobs, running, running.Revision-1); e != nil {
		t.Fatal(e)
	}
	var job Record
	for _, r := range f.repo.rows(colJobs) {
		if r.Kind == approvedEntitiesJobKind {
			job = r
		}
	}
	if e = f.svc.runApprovedEntities(testCtx, f.work, job); !errors.Is(e, errSummaryScheduleBusy) {
		t.Fatal("running scheduler modified", e)
	}
	after, e := f.repo.Get(testCtx, colJobs, scopeA, running.ID)
	if e != nil || structuredText(after) != structuredText(running) {
		t.Fatal("running model lease changed", e)
	}
	// The in-flight pass can still finalize against the same revision/lease.
	after.Status = JobStatusDone
	after.LeaseToken = ""
	after.Revision++
	if e = f.repo.Replace(testCtx, colJobs, after, after.Revision-1); e != nil {
		t.Fatal(e)
	}
	if e = f.svc.runApprovedEntities(testCtx, f.work, job); e != nil {
		t.Fatal("schedule did not resume", e)
	}
	after, e = f.repo.Get(testCtx, colJobs, scopeA, running.ID)
	if e != nil || after.Status != JobStatusPending || len(after.AutoDerive.Entities) != 1 {
		t.Fatal("deferred person lost", after, e)
	}
}

func TestPostgresFutureLinkedRuleKeepsCapacityReservation(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	claim := CaptureClaim{Subject: "scheduled rule", Text: "Use the scheduled checklist", Kind: "procedural", Basis: "explicit_user", Reason: "instruction", Rule: "always"}
	m := f.approveCandidate(oneCandidate(t, f.capture(f.human, captureInput(scopeA, "future-rule", claim))))
	future := time.Now().Add(time.Hour)
	m.EffectiveAt = &future
	m.Revision++
	if e := p.Replace(testCtx, colMemories, m, m.Revision-1); e != nil {
		t.Fatal(e)
	}
	_, e := f.svc.CreateDirective(testCtx, f.human, DirectiveInput{Scope: scopeA, Title: "unrelated", Rule: "Keep unrelated notes", AppliesTo: "both"})
	if e != nil {
		t.Fatal(e)
	}
	var linked Record
	for _, r := range pgCollection(t, p, colDirectives) {
		if r.Proposal != nil && r.Proposal.ID == m.ID {
			linked = r
		}
	}
	if linked.Status != statusActive {
		t.Fatal("future rule permanently retired", linked.Status)
	}
	active, e := f.svc.directives(testCtx, scopeA, "context")
	if e != nil || len(active) != 1 {
		t.Fatal("future rule applied too early", len(active), e)
	}
	past := time.Now().Add(-time.Minute)
	m.EffectiveAt = &past
	m.Revision++
	if e = p.Replace(testCtx, colMemories, m, m.Revision-1); e != nil {
		t.Fatal(e)
	}
	active, e = f.svc.directives(testCtx, scopeA, "context")
	if e != nil || len(active) != 2 {
		t.Fatal("rule did not become effective", len(active), e)
	}
}

func TestPostgresDirectiveHistoryPagesAllRows(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	if e := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		for i := 0; i < 650; i++ {
			r := Record{ID: fmt.Sprintf("history-%04d", i), Scope: projectScope(scopeA), Kind: directiveKind, Status: "inactive", Revision: 1, Text: "archived rule", Directive: &Directive{Title: "archive", AppliesTo: "both", Priority: i % 4}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
			if e := tx.Insert(ctx, colDirectives, r); e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	cursor := ""
	previousPriority := 101
	previousID := ""
	for {
		page, e := f.svc.ListDirectives(testCtx, f.human, scopeA, 37, cursor)
		if e != nil {
			t.Fatal(e)
		}
		for _, r := range page.Items {
			if seen[r.ID] || r.Priority > previousPriority || r.Priority == previousPriority && r.ID <= previousID {
				t.Fatal("unstable history page", r)
			}
			seen[r.ID] = true
			previousPriority, previousID = r.Priority, r.ID
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 650 {
		t.Fatal("history truncated", len(seen))
	}
	if _, e := f.svc.ListDirectives(testCtx, f.human, scopeB, 37, cursor); !errors.Is(e, ErrInvalid) {
		t.Fatal("cursor crossed scope", e)
	}
}

func TestPostgresSummaryBusyDoesNotSpendRecoveryAttempts(t *testing.T) {
	t.Setenv("MEMORY_AUTO_DERIVE", "on")
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	f.svc.Wiki = &fakeWikiLLM{}
	f.auth.actors[f.work] = actor("worker-1", "service", true, "read", "write", "embed", "search", "context")
	r := graphMemory(t, f, p, "busy-summary")
	r.Entities = []EntityInput{{Name: "Mira", Type: "person"}}
	r.Revision++
	if e := p.Replace(testCtx, colMemories, r, r.Revision-1); e != nil {
		t.Fatal(e)
	}
	// Provision the named node, without scheduling this new approval.
	cfg, e := f.svc.spaceSettings(testCtx, scopeA)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.applyEntityExtraction(testCtx, scopeA, "human", cfg, memoryEntityFinds(r, cfg), false); e != nil {
		t.Fatal(e)
	}
	ids, e := approvedPeopleIn(testCtx, p, r)
	if e != nil || len(ids) != 1 {
		t.Fatal(ids, e)
	}
	// The same person is already being summarized from an older evidence snapshot.
	running := Record{ID: autoDeriveJobID(scopeA), Scope: scopeA, Kind: autoDeriveJobKind, Status: JobStatusRunning, Revision: 1, LeaseToken: "summary-worker", LeaseUntil: time.Now().Add(time.Hour), AutoDerive: &AutoDeriveJob{Entities: ids}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if e = p.Insert(testCtx, colJobs, running); e != nil {
		t.Fatal(e)
	}
	running, e = p.Get(testCtx, colJobs, scopeA, running.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = p.Transaction(testCtx, func(ctx context.Context, tx Tx) error { return enqueueApprovedEntities(ctx, tx, r) }); e != nil {
		t.Fatal(e)
	}
	jobID := "approved-entities-" + ContentHash(structuredText(Ref{Collection: colMemories, ID: r.ID, Revision: r.Revision}))
	for i := 0; i < maxJobAttempts+2; i++ {
		if e = f.svc.WorkOnce(testCtx, f.work); e != nil {
			t.Fatal("normal busy failed", e)
		}
		job, e := p.Get(testCtx, colJobs, scopeA, jobID)
		if e != nil || job.Status != JobStatusPending || job.Attempts != 0 || job.LeaseToken != "" || job.RunAt == nil || !job.RunAt.After(time.Now()) {
			t.Fatal("busy spent retry", job, e)
		}
		due := time.Now().Add(-time.Second)
		job.RunAt = &due
		job.Revision++
		if e = p.Replace(testCtx, colJobs, job, job.Revision-1); e != nil {
			t.Fatal(e)
		}
	}
	after, e := p.Get(testCtx, colJobs, scopeA, running.ID)
	if e != nil || structuredText(after) != structuredText(running) {
		t.Fatal("running summary changed", e)
	}
	after.Status = JobStatusDone
	after.LeaseToken = ""
	after.AutoDerive.Entities = nil
	after.Revision++
	if e = p.Replace(testCtx, colJobs, after, after.Revision-1); e != nil {
		t.Fatal(e)
	}
	if e = f.svc.WorkOnce(testCtx, f.work); e != nil {
		t.Fatal(e)
	}
	after, e = p.Get(testCtx, colJobs, scopeA, running.ID)
	if e != nil || len(after.AutoDerive.Entities) != 1 || after.Status != JobStatusPending {
		t.Fatal("same person lost after running summary", after, e)
	}
}
