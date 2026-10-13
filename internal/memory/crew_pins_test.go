package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func pinAgent(handle string, pins ...CrewPin) Crew {
	return Crew{Type: "agent", Name: "리뷰어 " + handle, Handle: handle, Role: "PR을 본다", Pinned: pins}
}

func memPin(id string, injection string, priority int) CrewPin {
	return CrewPin{Collection: colMemories, ID: id, Injection: injection, Priority: priority}
}

// patchRecord changes a stored row in place, as if a later write had happened.
func (f *fixture) patchRecord(col string, scope Scope, id string, change func(*Record)) {
	f.t.Helper()
	err := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, col, scope, id)
		if err != nil {
			return err
		}
		prev := r.Revision
		change(&r)
		return tx.Replace(ctx, col, r, prev)
	})
	if err != nil {
		f.t.Fatal("patch", err)
	}
}

func TestPinsSaveOnlyApprovedRecordsOfTheSameScope(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("릴리스 전에 테스트를 돌린다")
	src := f.source()
	cand := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "아직 검토 전", Sources: refs(src)}))

	a := f.crew(pinAgent("리뷰어", memPin(m.ID, "full", 90)))
	if len(a.Crew.Pinned) != 1 {
		t.Fatal("pin not kept", a.Crew)
	}
	for name, c := range map[string]Crew{
		"candidate":      pinAgent("a1", memPin(cand.ID, "full", 1)),
		"missing":        pinAgent("a2", memPin("nope", "full", 1)),
		"wrong revision": pinAgent("a3", CrewPin{Collection: colMemories, ID: m.ID, Revision: m.Revision + 5, Injection: "full"}),
		"bad injection":  pinAgent("a4", memPin(m.ID, "everything", 1)),
		"bad priority":   pinAgent("a5", memPin(m.ID, "full", 101)),
		"bad collection": pinAgent("a6", CrewPin{Collection: "sources", ID: src.ID, Injection: "full"}),
		"twice":          pinAgent("a7", memPin(m.ID, "full", 1), memPin(m.ID, "summary", 2)),
		"persona":        {Type: "persona", Name: "p", Instructions: "차분하게", Pinned: []CrewPin{memPin(m.ID, "full", 1)}},
	} {
		if _, err := f.svc.CreateCrew(testCtx, f.human, CrewInput{Scope: scopeA, Crew: c}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: accepted (%v)", name, err)
		}
	}
	// The same memory id seen from another scope does not exist there.
	if _, err := f.svc.CreateCrew(testCtx, f.human, CrewInput{Scope: scopeB, Crew: pinAgent("다른곳", memPin(m.ID, "full", 1))}); !errors.Is(err, ErrInvalid) {
		t.Fatal("pinned a record of another scope", err)
	}
	// A fixed revision that matches is fine.
	fixed := f.crew(pinAgent("고정", CrewPin{Collection: colMemories, ID: m.ID, Revision: m.Revision, Injection: "summary", Priority: 5}))
	if fixed.Crew.Pinned[0].Revision != m.Revision {
		t.Fatal("fixed revision lost")
	}

	// A pin that went stale later does not block other edits; a new bad pin does.
	f.patchRecord(colMemories, scopeA, m.ID, func(r *Record) { r.Status = "superseded"; r.Revision++ })
	c := *a.Crew
	c.Role = "PR과 이슈를 본다"
	up, err := f.svc.UpdateCrew(testCtx, f.human, a.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: a.Revision, Crew: &c})
	if err != nil {
		t.Fatal("unchanged stale pin blocked an edit", err)
	}
	c.Pinned = append(c.Pinned, memPin(cand.ID, "full", 1))
	if _, err = f.svc.UpdateCrew(testCtx, f.human, a.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: up.Revision, Crew: &c}); !errors.Is(err, ErrInvalid) {
		t.Fatal("added an unapproved pin", err)
	}
}

func TestResolvePinsOrderShapeAndStatus(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("가", 500)
	low := f.activeMemory("낮은 우선순위 기억")
	high := f.activeMemory(long)
	ref := f.activeMemory("참고만 하는 기억 본문")
	gone := f.activeMemory("나중에 대체될 기억")
	f.crew(pinAgent("리뷰어", memPin(low.ID, "full", 10), memPin(high.ID, "summary", 90), memPin(ref.ID, "reference", 50), memPin(gone.ID, "full", 40)))
	f.patchRecord(colMemories, scopeA, gone.ID, func(r *Record) { r.Status = "superseded" })

	res, err := f.svc.ResolvePins(testCtx, f.agent, scopeA, "@리뷰어", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != "agent" || res.Authority != AuthorityDataOnly || res.Notice == "" || len(res.Items) != 4 {
		t.Fatalf("resolution %+v", res)
	}
	order := []string{}
	for _, it := range res.Items {
		order = append(order, it.Pin.ID)
	}
	if strings.Join(order, ",") != strings.Join([]string{high.ID, ref.ID, gone.ID, low.ID}, ",") {
		t.Fatal("not in priority order", order)
	}
	summary, reference, stale, full := res.Items[0], res.Items[1], res.Items[2], res.Items[3]
	if summary.Status != pinOK || summary.Item.Text != "" || runes(summary.Item.Summary) != pinSummaryRunes || summary.Item.Authority != AuthorityDataOnly {
		t.Fatalf("summary %+v", summary.Item)
	}
	if reference.Item.Text != "" || reference.Item.Summary != "참고만 하는 기억 본문" || reference.Item.ID != ref.ID {
		t.Fatalf("reference %+v", reference.Item)
	}
	if stale.Status != pinNotApproved || stale.Item != nil {
		t.Fatalf("superseded memory still injected: %+v", stale)
	}
	if full.Item.Text != "낮은 우선순위 기억" || full.Item.Reasons[0] != "pinned" {
		t.Fatalf("full %+v", full.Item)
	}

	// A secret that slipped into a record is never handed out.
	f.patchRecord(colMemories, scopeA, low.ID, func(r *Record) { r.Text = "token = ghp_abcdefghijklmnopqrstuvwxyz0123" })
	res, _ = f.svc.ResolvePins(testCtx, f.agent, scopeA, "리뷰어", "")
	if last := res.Items[3]; last.Status != pinSecretBlocked || last.Item != nil {
		t.Fatalf("secret pin %+v", last)
	}
	if _, err = f.svc.ResolvePins(testCtx, f.human, scopeA, "@없는사람", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown agent", err)
	}
	if _, err = f.svc.ResolvePins(testCtx, f.human, scopeB, "@리뷰어", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("agent seen from another scope", err)
	}
}

func TestPinsResolveSkillsAndKnowledge(t *testing.T) {
	f := newFixture(t)
	_, version := f.promoteSkill("always run the focused package test before pushing")
	src := f.source()
	now := time.Now().UTC().Truncate(time.Millisecond)
	chunk := Record{ID: "kc-1", Scope: scopeA, Kind: "knowledge_chunk", Status: "active", Revision: 1, Text: "A3 paper. Bleed 3 mm.", Sources: refs(src), CreatedAt: now, UpdatedAt: now, Knowledge: &KnowledgeRecord{KnowledgeSourceID: "ks-1"}}
	ks := Record{ID: "ks-1", Scope: scopeA, Kind: "knowledge_source", Status: "active", Revision: 2, Sources: refs(src), CreatedAt: now, UpdatedAt: now, ApprovedBy: "human-1",
		Knowledge: &KnowledgeRecord{TrustLevel: "reviewed", DisplayName: "print.md", ChunkIDs: []string{"kc-1"}}}
	draft := ks
	draft.ID, draft.Status, draft.Knowledge = "ks-2", "draft", &KnowledgeRecord{TrustLevel: "user_supplied", DisplayName: "draft.md"}
	if err := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		for _, r := range []struct {
			col string
			rec Record
		}{{colKnowledgeChunks, chunk}, {colKnowledgeSources, ks}, {colKnowledgeSources, draft}} {
			if err := tx.Insert(ctx, r.col, r.rec); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateCrew(testCtx, f.human, CrewInput{Scope: scopeA, Crew: pinAgent("초안", CrewPin{Collection: colKnowledgeSources, ID: "ks-2", Injection: "full"})}); !errors.Is(err, ErrInvalid) {
		t.Fatal("pinned an unreviewed knowledge source", err)
	}
	if _, err := f.svc.CreateCrew(testCtx, f.human, CrewInput{Scope: scopeA, Crew: pinAgent("없는판", CrewPin{Collection: colSkills, ID: "skill-1", Revision: 2, Injection: "full"})}); !errors.Is(err, ErrInvalid) {
		t.Fatal("pinned a skill version that does not exist", err)
	}
	f.crew(pinAgent("인쇄", CrewPin{Collection: colSkills, ID: "skill-1", Injection: "full", Priority: 80},
		CrewPin{Collection: colKnowledgeSources, ID: "ks-1", Injection: "summary", Priority: 70}))
	res, err := f.svc.ResolvePins(testCtx, f.human, scopeA, "@인쇄", "")
	if err != nil || len(res.Items) != 2 {
		t.Fatal(res, err)
	}
	skill, know := res.Items[0], res.Items[1]
	if skill.Status != pinOK || skill.Item.Collection != colSkillVersions || skill.Item.ID != version.ID || !strings.Contains(skill.Item.Text, "focused package test") {
		t.Fatalf("skill pin %+v", skill)
	}
	if know.Status != pinOK || know.Item.Collection != colKnowledgeSources || know.Item.Summary != "print.md: A3 paper. Bleed 3 mm." {
		t.Fatalf("knowledge pin %+v", know.Item)
	}
}

func TestContextBuildPutsTheAgentsPinsFirst(t *testing.T) {
	f := newFixture(t)
	style := f.activeMemory("답은 한국어로 짧게")
	both := f.activeMemory("release checklist: run the tests")
	for i := 0; i < 4; i++ {
		f.activeMemory("release checklist step " + string(rune('a'+i)))
	}
	f.crew(pinAgent("리뷰어", memPin(style.ID, "full", 90), memPin(both.ID, "full", 50)))
	big := 64000
	in := ContextBuildInput{Scope: scopeA, Task: "release checklist", TokenEncoding: "cl100k_base", Modes: []string{"fts"}, Agent: "@리뷰어", BudgetTokens: &big}
	pack, err := f.svc.BuildContext(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Items) < 3 || pack.Items[0].ID != style.ID || pack.Items[1].ID != both.ID || pack.Items[0].Reasons[0] != "pinned" {
		t.Fatalf("pins not first: %+v", pack.Items)
	}
	seen := map[string]int{}
	for _, h := range pack.Items {
		seen[h.ID]++
	}
	if seen[both.ID] != 1 || len(pack.Items) != 2+5-1 {
		t.Fatalf("pinned memory repeated or search lost: %v (%d items)", seen, len(pack.Items))
	}
	var data ContextBundle
	if json.Unmarshal([]byte(pack.ContextData), &data) != nil || len(data.Items) != len(pack.Items) || pack.TokensUsed > big || pack.ContextID == "" {
		t.Fatal("context data does not match items")
	}

	// A small budget keeps the higher-priority pin and drops what does not fit.
	budget := pack.TokensUsed * 2 / len(pack.Items)
	in.BudgetTokens = &budget
	small, err := f.svc.BuildContext(testCtx, f.human, in)
	if err != nil || len(small.Items) == 0 || small.Items[0].ID != style.ID || small.TokensUsed > budget || len(small.Items) >= len(pack.Items) {
		t.Fatalf("budget: %d items, %d tokens, %v", len(small.Items), small.TokensUsed, err)
	}

	// Without an agent the build is what it always was.
	in.Agent, in.BudgetTokens = "", nil
	plain, _ := f.svc.BuildContext(testCtx, f.human, in)
	for _, h := range plain.Items {
		if h.ID == style.ID {
			t.Fatal("a pin leaked into a build without the agent")
		}
	}
	in.Agent = "@없음"
	if _, err = f.svc.BuildContext(testCtx, f.human, in); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown agent", err)
	}
}

func TestContextPinsHTTP(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("팀 회의는 요약부터")
	member := f.crew(crewAgent("리뷰어", "리뷰어"))
	team := crewTeam("리뷰팀", CrewMember{AgentID: member.ID, Role: "worker"})
	team.Pinned = []CrewPin{memPin(m.ID, "full", 10)}
	f.crew(team)
	req := httptest.NewRequest("GET", "/v1/context/pins?workspace_id="+scopeA.WorkspaceID+"&project_id="+scopeA.ProjectID+"&user_id="+scopeA.UserID+"&agent_id="+scopeA.AgentID+"&agent=%40%EB%A6%AC%EB%B7%B0%ED%8C%80", nil)
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	var out PinResolution
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Type != "team" || len(out.Items) != 1 || out.Items[0].Item.Text != "팀 회의는 요약부터" {
		t.Fatal(w.Code, w.Body.String())
	}
}
