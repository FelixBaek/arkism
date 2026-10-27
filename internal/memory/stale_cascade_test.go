package memory

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Supersede reaches only what was written from the old memory: digests that
// list it, lesson→skill proposals that cite it and supports relations naming it.
func TestSupersedeMarksDerivedRecordsStale(t *testing.T) {
	f := newFixture(t)
	old := f.digestMemory("decision", "캐시는 redis")
	other := f.digestMemory("decision", "캐시 서버 redis")
	hit := f.approveDigest("topic", f.proposeTopic("캐시", "캐시는 redis 를 쓴다.", memRefs(old, other)))
	miss := f.approveDigest("topic", f.proposeTopic("배포", "staging 먼저.", memRefs(other)))
	src := f.source()
	rel := func(id, kind, target string) Record {
		return Record{ID: id, Scope: scopeA, Kind: kind, Status: "active", Revision: 1, Sources: refs(src), Text: kind, Relations: []Ref{{Collection: colMemories, ID: target, Revision: 1}, {Collection: colMemories, ID: other.ID, Revision: 1}}}
	}
	for _, r := range []Record{rel("sup-hit", "supports", old.ID), rel("sup-miss", "supports", other.ID), rel("human-link", "related", old.ID)} {
		if e := f.repo.Insert(testCtx, "memory_relations", r); e != nil {
			t.Fatal(e)
		}
	}
	for _, r := range []Record{{ID: "skill-hit", Kind: proposedSkillKind, Relations: []Ref{{Collection: "memory_candidates", ID: old.ID, Revision: 1}}}, {ID: "skill-miss", Kind: proposedSkillKind, Relations: []Ref{{Collection: "memory_candidates", ID: other.ID, Revision: 1}}}} {
		r.Scope, r.Status, r.Revision, r.SkillID, r.Sources = scopeA, "candidate", 1, "sk", refs(src)
		if e := f.repo.Insert(testCtx, colSkillVersions, r); e != nil {
			t.Fatal(e)
		}
	}
	in := SupersedeInput{Scope: scopeA, ExpectedRevision: old.Revision, Reason: "cache moved", NewContent: "캐시는 valkey", Sources: refs(src)}
	f.repo.failAudit = true
	if _, e := f.svc.Supersede(testCtx, f.human, old.ID, in); e == nil {
		t.Fatal("audit failed open")
	}
	f.repo.failAudit = false
	if r := f.repo.store[colTopicDigests][recKey(scopeA, hit.ID)]; r.StaleMark != nil || r.Status != digestActive {
		t.Fatal("cascade escaped rollback", r.Status)
	}
	out, e := f.svc.Supersede(testCtx, f.human, old.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	marked := func(col, id string) *StaleMark { return f.repo.store[col][recKey(scopeA, id)].StaleMark }
	d := f.repo.store[colTopicDigests][recKey(scopeA, hit.ID)]
	if d.Status != digestStale || d.Text != "" || d.StaleMark == nil || d.StaleMark.Memory.ID != old.ID || d.StaleMark.ReplacedBy.ID != out.New.ID || d.Revision != hit.Revision+1 {
		t.Fatal("digest", d.Status, d.StaleMark)
	}
	if m := f.repo.store[colTopicDigests][recKey(scopeA, miss.ID)]; m.Status != digestActive || m.StaleMark != nil {
		t.Fatal("unrelated digest touched")
	}
	if marked("memory_relations", "sup-hit") == nil || marked("memory_relations", "sup-miss") != nil || marked("memory_relations", "human-link") != nil {
		t.Fatal("supports relations")
	}
	if r := f.repo.store["memory_relations"][recKey(scopeA, "sup-hit")]; r.Status != "active" {
		t.Fatal("relation left the graph")
	}
	if marked(colSkillVersions, "skill-hit") == nil || marked(colSkillVersions, "skill-miss") != nil {
		t.Fatal("skill proposals")
	}
	v, e := f.svc.GetDigest(testCtx, f.human, scopeA, "topic", hit.ID)
	if e != nil || v.StaleMark == nil || v.Status != digestStale {
		t.Fatal("view", v.StaleMark, e)
	}
	if n := len(f.repo.rows(colTopicDigests)) + len(f.repo.rows("memory_relations")) + len(f.repo.rows(colSkillVersions)); n != 2+4+2 {
		t.Fatal("something was deleted", n)
	}
	if state, _ := digestStateRecord(testCtx, f.repo, colUserDigests, scopeA); state.Digest.Pending != "" {
		t.Fatal("user digest requested without a user digest", state.Digest.Pending)
	}
}

// A superseded user memory retires the user summary and asks for a new one, so a person gets a candidate.
func TestSupersedeRequestsNewUserDigest(t *testing.T) {
	f := newFixture(t)
	me := f.digestMemory("user", "아침형 인간")
	if _, e := f.svc.JudgeUserDigest(testCtx, f.agent, DigestJudgeInput{Scope: scopeA, Judged: memRefs(me), Verdict: "new_rule"}); e != nil {
		t.Fatal(e)
	}
	v, e := f.svc.ProposeDigest(testCtx, f.agent, DigestProposalInput{Scope: scopeA, Kind: digestKindUser, Body: "아침에 일한다.", Members: memRefs(me)})
	if e != nil {
		t.Fatal(e)
	}
	f.approveDigest("user", v)
	if _, e := f.svc.Supersede(testCtx, f.human, me.ID, SupersedeInput{Scope: scopeA, ExpectedRevision: me.Revision, Reason: "changed", NewContent: "저녁형 인간", Sources: refs(f.source())}); e != nil {
		t.Fatal(e)
	}
	r := f.repo.store[colUserDigests][recKey(scopeA, v.ID)]
	state, _ := digestStateRecord(testCtx, f.repo, colUserDigests, scopeA)
	if r.Status != digestStale || r.StaleMark == nil || state.Digest.Pending != "requested" {
		t.Fatal("user digest", r.Status, state.Digest.Pending)
	}
}

// A digest records when the scope's newest approved memory last changed; an
// approval after that makes it stale from one listing query.
func TestDigestWatermarkGoesStaleAfterLaterApproval(t *testing.T) {
	f := newFixture(t)
	a := f.digestMemory("decision", "배포는 staging 먼저")
	v := f.approveDigest("topic", f.proposeTopic("배포", "staging 먼저.", memRefs(a)))
	got, e := f.svc.GetDigest(testCtx, f.human, scopeA, "topic", v.ID)
	if e != nil || got.Watermark == nil || !got.Watermark.Equal(a.UpdatedAt) || got.Stale {
		t.Fatal("fresh watermark", got.Watermark, got.Stale, e)
	}
	time.Sleep(2 * time.Millisecond)
	f.digestMemory("decision", "배포 뒤에는 smoke test")
	page, e := f.svc.ListDigests(testCtx, f.human, scopeA, "topic", "")
	if e != nil || len(page.Items) != 1 || !page.Items[0].Stale || !page.Items[0].Fresh {
		t.Fatal("stale after approval", page.Items, e)
	}
}

// A confirm counts on the lesson and changes nothing else; only a person or an agent may send one.
func TestConfirmMemoryCountsProofWithoutEditing(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("retry on 429")
	cand := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "waiting lesson", Sources: refs(f.source())}))
	for i := 1; i <= 2; i++ {
		got, e := f.svc.ConfirmMemory(testCtx, f.agent, colMemories, m.ID, MemoryConfirmInput{Scope: scopeA})
		if e != nil || got.ProofCount != i || got.LastConfirmedAt == nil || got.Text != m.Text || got.Revision != m.Revision || !got.UpdatedAt.Equal(m.UpdatedAt) {
			t.Fatal("confirm", i, got.ProofCount, got.Revision, e)
		}
	}
	if got, e := f.svc.ConfirmMemory(testCtx, f.agent, "memory_candidates", cand.ID, MemoryConfirmInput{Scope: scopeA}); e != nil || got.ProofCount != 1 || got.Status != "candidate" {
		t.Fatal("candidate confirm", got.ProofCount, got.Status, e)
	}
	if _, e := f.svc.ConfirmMemory(testCtx, f.work, colMemories, m.ID, MemoryConfirmInput{Scope: scopeA}); !errors.Is(e, ErrDenied) {
		t.Fatal("service confirmed", e)
	}
	if _, e := f.svc.ConfirmMemory(testCtx, f.human, "sources", m.ID, MemoryConfirmInput{Scope: scopeA}); !errors.Is(e, ErrInvalid) {
		t.Fatal("collection", e)
	}
	hits := f.search(f.human, SearchInput{Scope: scopeA, Query: "429"})
	if len(hits.Items) != 1 || hits.Items[0].ProofCount != 2 || hits.Items[0].LastConfirmedAt == nil {
		t.Fatal("hit fields", hits.Items)
	}
	body, _ := json.Marshal(MemoryConfirmInput{Scope: scopeA})
	req := httptest.NewRequest("POST", "/v1/memories/"+m.ID+"/confirm", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+f.agent)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"proof_count":3`) {
		t.Fatal("HTTP", w.Code, w.Body.String())
	}
}

// Deleting a memory also takes it out of the context_usage receipts that handed it out.
func TestMemoryErasureScrubsUsageReceipts(t *testing.T) {
	f := newFixture(t)
	gone, kept := f.activeMemory("to delete"), f.activeMemory("to keep")
	use := func(id string, ids ...string) Record {
		u := &ContextUsage{Items: []MemoryUse{}}
		for i, m := range ids {
			u.Items = append(u.Items, MemoryUse{Memory: Ref{Collection: colMemories, ID: m, Revision: 1}, Rank: i + 1})
		}
		return Record{ID: id, Scope: scopeA, Kind: "context_usage", Status: "done", Revision: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(), CreatedBy: "agent-1", Usage: u}
	}
	for _, r := range []Record{use("u-both", gone.ID, kept.ID), use("u-gone", gone.ID), use("u-kept", kept.ID)} {
		if e := f.repo.Insert(testCtx, "jobs", r); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.svc.Delete(testCtx, f.human, colMemories, gone.ID, ChangeInput{Scope: scopeA, ExpectedRevision: gone.Revision}); e != nil {
		t.Fatal(e)
	}
	token := erasureWorker(f)
	for key, r := range f.repo.store["jobs"] { // only the erasure saga is under test
		if r.Status == JobStatusPending && r.Kind != memoryEvaluationErasureKind {
			delete(f.repo.store["jobs"], key)
		}
	}
	for i := 0; i < 6; i++ {
		job, e := f.repo.ClaimJob(testCtx, time.Now(), time.Minute)
		if errors.Is(e, ErrNotFound) {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if e = f.svc.eraseSourceMemories(testCtx, token, job); e != nil {
			t.Fatal(i, e)
		}
	}
	if rows, _ := f.repo.MemoryUsage(testCtx, scopeA, gone.ID); len(rows) != 0 {
		t.Fatal("receipts still name the deleted memory", len(rows))
	}
	if _, e := f.repo.Get(testCtx, "jobs", scopeA, "u-gone"); !errors.Is(e, ErrNotFound) {
		t.Fatal("empty receipt kept", e)
	}
	both, e := f.repo.Get(testCtx, "jobs", scopeA, "u-both")
	if e != nil || len(both.Usage.Items) != 1 || both.Usage.Items[0].Memory.ID != kept.ID || both.Revision != 2 {
		t.Fatal("shared receipt", both.Usage, e)
	}
	if rows, _ := f.repo.MemoryUsage(testCtx, scopeA, kept.ID); len(rows) != 2 {
		t.Fatal("kept memory lost receipts", len(rows))
	}
	job, e := f.repo.Get(testCtx, "jobs", scopeA, deletionJobID(colMemories, scopeA, gone.ID))
	if e != nil || job.Status != JobStatusDone || job.SourceMemoryErasure.DeletedRecords["context_usage"] != 2 {
		t.Fatal("job", job.Status, job.SourceMemoryErasure, e)
	}
}
