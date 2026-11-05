package memory

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// Walk tests for the extraction categories (people and relations, work, rules, events):
// each one captures a claim with the fake extractor's labels, checks the candidate it
// classifies and stores, approves it as a person, recalls it, and then updates or forgets it.

// extractionRepo is the fake store with named entities and a knowledge graph.
type extractionRepo struct {
	*entityTestRepo
	edges []KnowledgeRelation
}

func (r *extractionRepo) PutKnowledgeRelation(_ context.Context, e KnowledgeRelation) error {
	r.edges = append(r.edges, e)
	return nil
}
func (r *extractionRepo) KnowledgeRelations(_ context.Context, s Scope, n GraphEntity, limit int) ([]KnowledgeRelation, error) {
	out := []KnowledgeRelation{}
	for _, e := range r.edges {
		if e.Scope == s && e.Source == n && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fixture) approveCandidate(id string) Record {
	f.t.Helper()
	c := f.get(f.human, "memory_candidates", scopeA, id)
	m, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"})
	if err != nil {
		f.t.Fatal("approve:", err)
	}
	return m
}

// approveReplacing approves a correction held for conflict review, replacing the old memory.
func (f *fixture) approveReplacing(id string, old Record) Record {
	f.t.Helper()
	c := f.get(f.human, "memory_candidates", scopeA, id)
	old = f.get(f.human, "memories", scopeA, old.ID)
	rel, err := f.repo.Get(testCtx, "memory_relations", scopeA, c.ConflictID)
	if err != nil {
		f.t.Fatal("conflict relation:", err)
	}
	m, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve", OperationID: "replace-" + id,
		Conflict: &CandidateReviewConflict{ID: rel.ID, ExpectedRevision: rel.Revision, Memory: Ref{colMemories, old.ID, old.Revision}, Resolution: "replace_old", Reason: "person confirmed the correction"}})
	if err != nil {
		f.t.Fatal("approve replacing:", err)
	}
	return m
}

func (f *fixture) rejectCandidate(id string) {
	f.t.Helper()
	c := f.get(f.human, "memory_candidates", scopeA, id)
	if _, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "reject", Reason: "not lasting"}); err != nil {
		f.t.Fatal("reject:", err)
	}
}

func (f *fixture) recalls(query, id string) bool {
	f.t.Helper()
	for _, h := range f.search(f.human, SearchInput{Scope: scopeA, Query: query, Modes: []string{"fts"}}).Items {
		if h.ID == id {
			return true
		}
	}
	return false
}

func oneCandidate(t *testing.T, items []CaptureItem) string {
	t.Helper()
	if len(items) != 1 || items[0].Status != "candidate" || items[0].CandidateID == "" {
		t.Fatalf("want one pending candidate, got %+v", items)
	}
	return items[0].CandidateID
}

// 사람·관계: a fact about another person carries the person and stated relations; approval
// writes the relations as graph edges with the memory as evidence; a correction replaces it.
func TestExtractionPersonFactsAndRelationsWalk(t *testing.T) {
	f := newFixture(t)
	repo := &extractionRepo{entityTestRepo: newEntityTestRepo(f.repo)}
	f.svc.Repo = repo
	claim := CaptureClaim{Subject: "결제팀", Text: "김민수는 결제팀 팀장이고 박지영 본부장에게 보고한다", Kind: "semantic", Basis: "explicit_user", Reason: "사람에 대한 사실",
		Entities:  []EntityInput{{Name: "김민수", Type: "person"}, {Name: "결제팀", Type: "organization"}, {Name: "박지영", Type: "person"}},
		Relations: []EntityRelationInput{{Source: "김민수", Relation: "member_of", Target: "결제팀"}, {Source: "김민수", Relation: "reports_to", Target: "박지영"}}}

	// Classify: a relation to a name the claim does not state, or a bad label, is refused whole.
	for _, bad := range [][]EntityRelationInput{{{Source: "김민수", Relation: "reports_to", Target: "이서연"}}, {{Source: "김민수", Relation: "Reports To", Target: "박지영"}}} {
		c := claim
		c.Relations = bad
		if got := f.capture(f.human, captureInput(scopeA, "evt-bad-"+bad[0].Target+bad[0].Relation, c)); got[0].Status != "rejected" {
			t.Fatal("malformed relation stored", got)
		}
	}
	id := oneCandidate(t, f.capture(f.human, captureInput(scopeA, "evt-person", claim)))
	if len(repo.edges) != 0 {
		t.Fatal("a candidate wrote graph edges before review")
	}
	m := f.approveCandidate(id)
	if len(m.EntityRelations) != 2 || len(repo.edges) != 2 {
		t.Fatalf("relations after approval: memory %v edges %v", m.EntityRelations, repo.edges)
	}
	for _, e := range repo.edges {
		if e.Evidence.ID != m.ID || e.Source.Kind != namedEntityKind || e.AssertedBy != "human-1" {
			t.Fatalf("edge %+v", e)
		}
	}
	if !f.recalls("결제팀", m.ID) {
		t.Fatal("approved person fact not recalled")
	}
	// Update: a correction supersedes the old fact once a person approves it.
	fix := claim
	fix.Text, fix.Supersedes = "김민수는 이제 정산팀 팀장이다", &Ref{Collection: "memories", ID: m.ID, Revision: m.Revision}
	fix.Entities, fix.Relations = []EntityInput{{Name: "김민수", Type: "person"}, {Name: "정산팀", Type: "organization"}}, []EntityRelationInput{{Source: "김민수", Relation: "member_of", Target: "정산팀"}}
	f.approveReplacing(oneCandidate(t, f.capture(f.human, captureInput(scopeA, "evt-person-fix", fix))), m)
	if got := f.get(f.human, "memories", scopeA, m.ID); got.Status == statusActive {
		t.Fatal("corrected person fact still active")
	}
	if len(repo.edges) != 3 {
		t.Fatal("the correction's relation was not added", repo.edges)
	}
}

// 일: a decision carries its status; a saved checkpoint proposes where the work stands only when
// the space opts in, once per event; a person confirms and later drops the decision.
func TestExtractionWorkStateAndDecisionStatusWalk(t *testing.T) {
	f := newFixture(t)
	decision := CaptureClaim{Subject: "인증", Text: "로그인 세션 저장소는 Redis 대신 Postgres를 쓰기로 검토 중이다", Kind: "decision", Basis: "explicit_user", Reason: "결정", DecisionStatus: "proposed"}
	bad := decision
	bad.Kind = "user"
	if got := f.capture(f.human, captureInput(scopeA, "evt-status-bad", bad)); got[0].Status != "rejected" {
		t.Fatal("a status on a non-decision was stored", got)
	}
	cand := f.get(f.human, "memory_candidates", scopeA, oneCandidate(t, f.capture(f.human, captureInput(scopeA, "evt-decision", decision))))
	if !slices.Contains(cand.Tags, "decision_status:proposed") {
		t.Fatal("status tag missing", cand.Tags)
	}
	m := f.approveCandidate(cand.ID)

	save := WorkCheckpointSaveInput{Scope: scopeA, ID: "auth-move", EventID: "e1", Package: WorkCheckpointPackage{Title: "세션 저장소 이전", Goal: "세션을 Postgres로", Repository: "acme/web",
		Status: "in_progress", Origin: "crew_handoff", Decisions: []string{"세션 만료는 14일로 한다"}, Completed: []string{"스키마 추가"}, NextSteps: []string{"이전 스크립트 검증"}}}
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, save); err != nil {
		t.Fatal(err)
	}
	if n := len(workStateRows(f)); n != 0 {
		t.Fatal("work state proposed without the opt-in", n)
	}
	on := true
	putSettings(t, f, scopeA, SpaceConfigInput{ExpectedRevision: -1, WorkStateCandidates: &on})
	save.EventID, save.ExpectedRevision = "e2", 1
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, save); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, save); err != nil { // exact replay
		t.Fatal(err)
	}
	rows := workStateRows(f)
	if len(rows) != 2 {
		t.Fatalf("want the decision and the state line once, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Status != "candidate" || r.Kind != "decision" || !slices.Contains(r.Tags, "repo:acme/web") {
			t.Fatalf("work state row %+v", r)
		}
	}
	if !slices.ContainsFunc(rows, func(r Record) bool {
		return r.Text == "세션 만료는 14일로 한다" && slices.Contains(r.Tags, "decision_status:proposed")
	}) || !slices.ContainsFunc(rows, func(r Record) bool { return strings.Contains(r.Text, "next 이전 스크립트 검증") }) {
		t.Fatal("work state candidates", rows)
	}
	if !f.recalls("Postgres", m.ID) {
		t.Fatal("decision not recalled")
	}
	// Update: confirmed, then dropped, each replacing the last only on approval.
	prev := m
	for _, st := range []string{"confirmed", "dropped"} {
		c := decision
		c.DecisionStatus, c.Text, c.Supersedes = st, "로그인 세션 저장소를 Postgres로 옮기는 안은 "+st, &Ref{Collection: "memories", ID: prev.ID, Revision: prev.Revision}
		next := f.approveReplacing(oneCandidate(t, f.capture(f.human, captureInput(scopeA, "evt-decision-"+st, c))), prev)
		if f.get(f.human, "memories", scopeA, prev.ID).Status == statusActive || !slices.Contains(next.Tags, "decision_status:"+st) {
			t.Fatal("status update", st)
		}
		prev = next
	}
}

func workStateRows(f *fixture) []Record {
	var out []Record
	for _, r := range f.repo.store["memory_candidates"] {
		if slices.Contains(r.Keywords, "work_state") {
			out = append(out, r)
		}
	}
	return out
}

// 지시·규칙: an always/never rule becomes an active directive only when a person approves it,
// in the same step; context build packs it first; a rejected rule makes nothing.
func TestExtractionRuleBecomesDirectiveWalk(t *testing.T) {
	f := newFixture(t)
	rule := CaptureClaim{Subject: "운영 배포", Text: "운영 배포 중에는 데이터베이스 마이그레이션을 직접 실행하지 않는다", Kind: "procedural", Basis: "explicit_user", Reason: "규칙", Rule: "never"}
	bad := rule
	bad.Rule = "sometimes"
	if got := f.capture(f.human, captureInput(scopeA, "evt-rule-bad", bad)); got[0].Status != "rejected" {
		t.Fatal("unknown rule value stored", got)
	}
	id := oneCandidate(t, f.capture(f.agent, captureInput(scopeA, "evt-rule", rule)))
	if list, _ := f.svc.ListDirectives(testCtx, f.human, scopeA, 0, ""); len(list.Items) != 0 {
		t.Fatal("a candidate made a directive")
	}
	m := f.approveCandidate(id)
	list, err := f.svc.ListDirectives(testCtx, f.human, scopeA, 0, "")
	if err != nil || len(list.Items) != 1 || !list.Items[0].Active || list.Items[0].Rule != rule.Text || list.Items[0].AppliesTo != "both" || list.Items[0].Title != "운영 배포" {
		t.Fatalf("directive after approval: %+v %v", list, err)
	}
	pack, err := f.svc.BuildContext(testCtx, f.human, ContextBuildInput{Scope: scopeA, Task: "운영 배포 순서", Modes: []string{"fts"}, TokenEncoding: "o200k_base"})
	if err != nil || pack.Directives == nil || !strings.Contains(structuredText(pack.Directives), "마이그레이션") {
		t.Fatalf("context build lost the directive: %+v %v", pack.Directives, err)
	}
	if !f.recalls("마이그레이션", m.ID) {
		t.Fatal("rule memory not recalled")
	}
	// Forget: a rejected rule candidate makes no directive.
	other := rule
	other.Text, other.Rule = "배포 전에는 항상 변경 기록을 남긴다", "always"
	f.rejectCandidate(oneCandidate(t, f.capture(f.human, captureInput(scopeA, "evt-rule-2", other))))
	if list, _ = f.svc.ListDirectives(testCtx, f.human, scopeA, 0, ""); len(list.Items) != 1 {
		t.Fatal("a rejected rule made a directive", list.Items)
	}
}

// 사건: an episode keeps its date and people; a temporary request expires on its own, the
// approved memory with it; the opt-in session lane shows past messages beside memories.
func TestExtractionEpisodesTemporaryAndSessionLaneWalk(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	episode := CaptureClaim{Subject: "장애 회고", Text: "2026-10-28 결제 장애 때 김민수와 함께 롤백했다", Kind: "episodic", Basis: "explicit_user", Reason: "사건",
		OccurredFrom: "2026-10-28", OccurredTo: "2026-10-28", Entities: []EntityInput{{Name: "김민수", Type: "person"}}}
	ep := f.approveCandidate(oneCandidate(t, f.capture(f.human, captureInput(scopeA, "evt-episode", episode))))
	if ep.Occurred == nil || ep.Occurred.From.Format("2006-01-02") != "2026-10-28" || ep.Kind != "episodic" {
		t.Fatalf("episode date %+v", ep.Occurred)
	}
	temp := CaptureClaim{Subject: "PR 제목", Text: "이번만 PR 제목을 영어로 쓴다", Kind: "user", Basis: "explicit_user", Reason: "임시 요청"}
	cand := f.get(f.human, "memory_candidates", scopeA, oneCandidate(t, f.capture(f.human, captureInput(scopeA, "evt-temp", temp))))
	week := time.Now().Add(temporaryCandidateTTL)
	if cand.ExpiresAt == nil || cand.ExpiresAt.Sub(week).Abs() > time.Minute || !slices.Contains(cand.Tags, temporaryTag) {
		t.Fatalf("temporary candidate expiry %v tags %v", cand.ExpiresAt, cand.Tags)
	}
	if m := f.approveCandidate(cand.ID); m.ExpiresAt == nil || !m.ExpiresAt.Equal(*cand.ExpiresAt) {
		t.Fatal("approved temporary memory lost its end date")
	}
	lasting := temp
	lasting.Text = "PR 제목은 영어로 쓴다"
	if c := f.get(f.human, "memory_candidates", scopeA, oneCandidate(t, f.capture(f.human, captureInput(scopeA, "evt-lasting", lasting)))); c.ExpiresAt != nil {
		t.Fatal("a lasting request got an expiry")
	}

	archiveSearchUpload(t, f, *now, ContentHash("episode-session"), jsonLine(map[string]any{"role": "user", "content": "결제 장애 롤백은 끝났고 원인은 캐시 설정이었다"}), "jsonl")
	query := SearchInput{Scope: scopeA, Query: "결제 장애", Modes: []string{"fts"}}
	if got := f.search(f.human, query); len(got.Sessions) != 0 || !slices.ContainsFunc(got.Items, func(h Hit) bool { return h.ID == ep.ID }) {
		t.Fatal("session lane ran without the opt-in, or the episode was not recalled", got.Sessions)
	}
	on := true
	putSettings(t, f, scopeA, SpaceConfigInput{ExpectedRevision: -1, RecallSessionArchives: &on})
	got := f.search(f.human, query)
	if len(got.Sessions) != 1 || !strings.Contains(got.Sessions[0].Text, "캐시 설정") || slices.ContainsFunc(got.Items, func(h Hit) bool { return h.ID == "" }) {
		t.Fatalf("session lane: %+v", got.Sessions)
	}
	// Archive messages pass no transfer check, so a search for an outside destination gets no lane.
	external := query
	external.Destination = "external:anthropic"
	if got := f.search(f.human, external); len(got.Sessions) != 0 {
		t.Fatalf("session lane sent outside: %+v", got.Sessions)
	}
	// A token without archive access gets the memories and no lane.
	f.auth.actors[f.work] = actor("worker-1", "service", true, "search")
	if got, err := f.svc.Search(testCtx, f.work, query); err != nil || len(got.Sessions) != 0 || slices.Contains(got.Degraded, "sessions") {
		t.Fatalf("lane without archive access: %+v %v", got.Sessions, err)
	}
	// Forget: the person forgets the episode; it is not recalled again.
	p, err := f.svc.ProposeMaintenance(testCtx, f.agent, MaintenanceInput{Scope: scopeA, Proposal: MaintenanceProposal{Type: invalidationType, Memory: &Ref{Collection: "memories", ID: ep.ID, Revision: ep.Revision}, Reason: "outdated"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.DecideMaintenance(testCtx, f.human, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: p.Revision, Decision: "forget", ReasonCode: "stale"}); err != nil {
		t.Fatal(err)
	}
	if f.recalls("결제 장애", ep.ID) {
		t.Fatal("forgotten episode recalled")
	}
}

// Near-duplicate lessons: a reworded lesson close to two approved lessons (cosine 0.857, under
// the 0.92 bar for other kinds) opens one merge proposal naming both, for one human decision.
func TestCaptureGroupsNearDuplicateLessons(t *testing.T) {
	f := newFixture(t)
	f.emb.semantic = true
	lesson := func(text string) Record {
		src := f.source()
		c := f.create(f.human, "memory_candidates", CreateInput{Scope: scopeA, Kind: "lesson", Text: text, Sources: refs(src)})
		a := f.transition(f.human, "memory_candidates", c.ID, ChangeInput{Scope: scopeA, ExpectedRevision: c.Revision, Status: "approved"})
		m := f.transition(f.human, "memories", a.ID, ChangeInput{Scope: scopeA, ExpectedRevision: a.Revision, Status: "active"})
		f.embedText(m)
		return m
	}
	a := lesson("배포 전 마이그레이션 상태를 반드시 먼저 확인한다")
	b := lesson("배포 전 마이그레이션 상태를 반드시 먼저 점검한다")
	claim := CaptureClaim{Subject: "배포", Text: "배포 전 마이그레이션 상태를 반드시 먼저 살핀다", Kind: "lesson", Basis: "explicit_user", Reason: "실수"}
	got := f.capture(f.human, captureInput(scopeA, "evt-lesson", claim))
	if got[0].Reason != "merge_proposed" {
		t.Fatal("near-duplicate lesson not grouped", got)
	}
	p, err := f.openProposal(got[0])
	ids := []string{}
	for _, r := range p.Merge.Proposal.Targets {
		ids = append(ids, r.ID)
	}
	if err != nil || len(ids) != 2 || !slices.Contains(ids, a.ID) || !slices.Contains(ids, b.ID) {
		t.Fatalf("proposal targets %v %v", ids, err)
	}
	// The same closeness for a decision stays a plain candidate.
	claim.Kind, claim.Text = "decision", "배포 전 마이그레이션 상태를 반드시 먼저 들여다본다"
	if got = f.capture(f.human, captureInput(scopeA, "evt-decision-near", claim)); got[0].Reason != "review_required" {
		t.Fatal("non-lesson used the lesson bar", got)
	}
}

// A procedure whose files are gone can be proposed for forgetting with reason path_missing.
func TestInvalidationPathMissingReason(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("scripts/release.sh로 배포한다")
	ref := Ref{Collection: "memories", ID: m.ID, Revision: m.Revision}
	p, err := f.svc.ProposeMaintenance(testCtx, f.agent, MaintenanceInput{Scope: scopeA, Proposal: MaintenanceProposal{Type: invalidationType, Memory: &ref, Reason: "path_missing"}})
	if err != nil || p.Status != maintenanceOpen {
		t.Fatal(p, err)
	}
	if _, err = f.svc.ProposeMaintenance(testCtx, f.agent, MaintenanceInput{Scope: scopeA, Proposal: MaintenanceProposal{Type: invalidationType, Memory: &ref, Reason: "gone"}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown reason accepted", err)
	}
}

func TestSpaceSettingsExtractionOptIns(t *testing.T) {
	f := newFixture(t)
	view, err := f.svc.EffectiveSpaceConfig(testCtx, f.human, scopeA)
	if err != nil || view.Config.MemoryLanguage != memoryLanguageAuto || *view.Config.WorkStateCandidates || *view.Config.RecallSessionArchives ||
		view.Config.CaptureDensity != "balanced" || view.Sources["capture_density"] != spaceSourceServer {
		t.Fatalf("defaults %+v %v", view.Config, err)
	}
	// a space that chose durable before the default changed keeps it
	durable := "durable"
	putSettings(t, f, scopeA, SpaceConfigInput{ExpectedRevision: -1, CaptureDensity: &durable})
	if view, _ = f.svc.EffectiveSpaceConfig(testCtx, f.human, scopeA); view.Config.CaptureDensity != "durable" || view.Sources["capture_density"] != spaceSourceSpace {
		t.Fatalf("durable %+v", view)
	}
	for _, bad := range []string{"Korean!", "k", "ko_KR"} {
		if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: -1, MemoryLanguage: &bad}, false); !errors.Is(err, ErrInvalid) {
			t.Fatal("bad language accepted", bad, err)
		}
	}
	en, on := "en", true
	putSettings(t, f, scopeA, SpaceConfigInput{ExpectedRevision: -1, MemoryLanguage: &en, WorkStateCandidates: &on})
	if view, _ = f.svc.EffectiveSpaceConfig(testCtx, f.human, scopeA); view.Config.MemoryLanguage != "en" || !*view.Config.WorkStateCandidates || view.Sources["memory_language"] != spaceSourceSpace {
		t.Fatalf("set %+v", view)
	}
	var reset SpaceConfigInput
	if err := reset.UnmarshalJSON([]byte(`{"expected_revision":-1,"work_state_candidates":null}`)); err != nil {
		t.Fatal(err)
	}
	putSettings(t, f, scopeA, reset)
	if view, _ = f.svc.EffectiveSpaceConfig(testCtx, f.human, scopeA); *view.Config.WorkStateCandidates || view.Config.MemoryLanguage != "en" {
		t.Fatal("null did not hand the setting back")
	}
}

// Live-style inputs: realistic Korean and English claims, as an extractor would label them, all
// end as candidates; only approval acts on their labels.
func TestExtractionScenarioKoreanAndEnglish(t *testing.T) {
	f := newFixture(t)
	repo := &extractionRepo{entityTestRepo: newEntityTestRepo(f.repo)}
	f.svc.Repo = repo
	claims := []CaptureClaim{
		{Subject: "code review", Text: "Never merge a pull request before every CI check passes", Kind: "procedural", Basis: "explicit_user", Reason: "rule", Rule: "never"},
		{Subject: "platform team", Text: "Dana leads the platform team and reports to Priya", Kind: "semantic", Basis: "explicit_user", Reason: "person",
			Entities:  []EntityInput{{Name: "Dana", Type: "person"}, {Name: "platform team", Type: "organization"}, {Name: "Priya", Type: "person"}},
			Relations: []EntityRelationInput{{Source: "Dana", Relation: "member_of", Target: "platform team"}, {Source: "Dana", Relation: "reports_to", Target: "Priya"}}},
		{Subject: "release", Text: "For now, release notes are written only in English", Kind: "user", Basis: "explicit_user", Reason: "temporary"},
		{Subject: "검색", Text: "검색 순위는 BM25와 벡터 점수를 합치는 방식으로 확정했다", Kind: "decision", Basis: "explicit_user", Reason: "결정", DecisionStatus: "confirmed"},
		{Subject: "온보딩", Text: "11월 3일 신규 입사자 온보딩에서 배포 권한 요청 절차를 설명했다", Kind: "episodic", Basis: "explicit_user", Reason: "사건", OccurredFrom: "2026-11-03"},
	}
	items := f.capture(f.human, captureInput(scopeA, "evt-scenario", claims...))
	for i, it := range items {
		if it.Status != "candidate" {
			t.Fatalf("claim %d: %+v", i, it)
		}
	}
	cand := func(i int) Record { return f.get(f.human, "memory_candidates", scopeA, items[i].CandidateID) }
	if !slices.Contains(cand(0).Tags, "rule:never") || cand(2).ExpiresAt == nil || !slices.Contains(cand(3).Tags, "decision_status:confirmed") || cand(4).Occurred == nil {
		t.Fatal("labels lost on candidates")
	}
	if list, _ := f.svc.ListDirectives(testCtx, f.human, scopeA, 0, ""); len(list.Items) != 0 || len(repo.edges) != 0 {
		t.Fatal("labels acted before review")
	}
	for i := range items {
		f.approveCandidate(items[i].CandidateID)
	}
	if list, _ := f.svc.ListDirectives(testCtx, f.human, scopeA, 0, ""); len(list.Items) != 1 || len(repo.edges) != 2 {
		t.Fatalf("after approval: directives %d edges %d", len(list.Items), len(repo.edges))
	}
}

// "This time only" words expire a request (user, procedural) but not a lesson, decision or
// event that only describes one time, nor a fact saying something is temporarily so.
func TestTemporaryWordsOnlyExpireRequests(t *testing.T) {
	cases := []struct {
		kind, text string
		want       bool
	}{
		{"user", "이번만 PR 제목을 영어로 쓴다", true},
		{"procedural", "For now, deploy from the main branch only", true},
		{"lesson", "이번만 예외로 테스트를 건너뛰었다가 장애가 났으니 테스트는 건너뛰지 않는다", false},
		{"episodic", "11월 3일에는 이번 한 번만 수동으로 배포했다", false},
		{"decision", "검색은 당분간 벡터 점수만 쓰기로 했다", false},
		{"procedural", "When the database is temporarily unavailable, retry with backoff", false},
		{"user", "I like the plan for today's standup notes", false},
	}
	for _, c := range cases {
		if got := claimTemporary(CaptureClaim{Kind: c.kind, Text: c.text}); got != c.want {
			t.Errorf("%s %q: temporary %v, want %v", c.kind, c.text, got, c.want)
		}
	}
	if !claimTemporary(CaptureClaim{Kind: "decision", Text: "x", Temporary: true}) {
		t.Error("the extractor's own flag was ignored")
	}
}

// File extraction writes claim content in the space's memory_language; auto keeps the source's.
func TestFileExtractPromptFollowsMemoryLanguage(t *testing.T) {
	auto, _ := extractPrompt("본문", &FileExtractJob{Language: memoryLanguageAuto})
	unset, _ := extractPrompt("본문", &FileExtractJob{})
	en, _ := extractPrompt("본문", &FileExtractJob{Language: "en"})
	if !strings.Contains(auto, "Korean stays Korean") || auto != unset {
		t.Fatal("auto language rule:", auto)
	}
	if !strings.Contains(en, "language with tag en") || strings.Contains(en, "Korean stays Korean") || !strings.Contains(en, "evidence_quote stays an exact excerpt") {
		t.Fatal("set language rule:", en)
	}
}

// A secret an agent writes into a checkpoint decision never becomes a work-state candidate:
// the checkpoint save refuses it before anything is proposed.
func TestWorkStateCandidatesDropSecrets(t *testing.T) {
	f := newFixture(t)
	on := true
	putSettings(t, f, scopeA, SpaceConfigInput{ExpectedRevision: -1, WorkStateCandidates: &on})
	secret := "ghp_" + strings.Repeat("a1B2c3D4e5", 4)
	save := WorkCheckpointSaveInput{Scope: scopeA, ID: "deploy-key", EventID: "e1", Package: WorkCheckpointPackage{Title: "배포 v2.4.1", Goal: "운영 배포", Repository: "acme/web", Status: "in_progress",
		Origin: "crew_handoff", Decisions: []string{"배포 토큰은 " + secret + " 로 바꿨다", "운영은 v2.4.1로 배포했다"}}}
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, save); !errors.Is(err, ErrSecretDetected) || len(workStateRows(f)) != 0 {
		t.Fatalf("secret checkpoint: %v, rows %d", err, len(workStateRows(f)))
	}
	save.Package.Decisions = save.Package.Decisions[1:]
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, save); err != nil {
		t.Fatal(err)
	}
	if rows := workStateRows(f); !slices.ContainsFunc(rows, func(r Record) bool { return r.Text == "운영은 v2.4.1로 배포했다" }) {
		t.Fatal("clean checkpoint proposed nothing", rows)
	}
}
