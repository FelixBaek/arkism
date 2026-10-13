package memory

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMemoryTimeBoundariesAndUnknowns(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	r := Record{Text: "old", Status: "active", ApprovedBy: "human", Revision: 1}
	if _, reason := memoryAt(r, at); reason != "history_not_recorded" {
		t.Fatal(reason)
	}
	recordMemoryState(colMemories, nil, &r, at)
	if st, reason := memoryAt(r, at.Add(-time.Millisecond)); st != nil || reason != "" {
		t.Fatal(st, reason)
	}
	if st, reason := memoryAt(r, at); st == nil || reason != "" {
		t.Fatal(st, reason)
	}
	old := r
	r.Status = "expired"
	r.Revision++
	recordMemoryState(colMemories, &old, &r, at.Add(time.Second))
	if st, _ := memoryAt(r, at); st == nil {
		t.Fatal("lost earlier state")
	}
	if st, _ := memoryAt(r, at.Add(time.Second)); st != nil {
		t.Fatal("expiry boundary")
	}
	r.Text = "changed"
	if _, reason := memoryAt(r, at); reason != "content_unavailable" {
		t.Fatal(reason)
	}
	r = old
	expiry := at
	r.ExpiresAt = &expiry
	recordMemoryState(colMemories, nil, &r, at)
	if st, _ := memoryAt(r, at); st != nil {
		t.Fatal("expiry inclusive")
	}
	r.ExpiresAt = nil
	future := at.Add(time.Second)
	r.EffectiveAt = &future
	recordMemoryState(colMemories, nil, &r, at)
	if st, _ := memoryAt(r, at); st != nil {
		t.Fatal("future validity")
	}
	if st, _ := memoryAt(r, future); st == nil {
		t.Fatal("inclusive effective time")
	}
	legacy := old
	legacy.MemoryTimeline = nil
	r = legacy
	recordMemoryState(colMemories, &legacy, &r, at)
	if _, reason := memoryAt(r, at.Add(-time.Millisecond)); reason != "history_not_recorded" {
		t.Fatal("invented legacy history")
	}
}

func TestMemoryTimeQuarantineAndClockReversal(t *testing.T) {
	at := time.Now().Add(-time.Hour)
	r := Record{Status: "active", ApprovedBy: "human", Revision: 1, Text: "kept"}
	recordMemoryState(colMemories, nil, &r, at)
	old := r
	r.PrivacyQuarantined = true
	r.Revision++
	recordMemoryState(colMemories, &old, &r, at.Add(time.Second))
	old = r
	r.PrivacyQuarantined = false
	r.Revision++
	recordMemoryState(colMemories, &old, &r, at.Add(2*time.Second))
	if state, _ := memoryAt(r, at.Add(time.Second)); state != nil {
		t.Fatal("quarantine omitted")
	}
	if state, _ := memoryAt(r, at.Add(2*time.Second)); state == nil {
		t.Fatal("released state missing")
	}
	old = r
	r.Status = "rejected"
	if e := recordMemoryState(colMemories, &old, &r, at); !errors.Is(e, ErrUnavailable) {
		t.Fatal("backward observation", e)
	}
}

func exerciseMemoryTime(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	src := f.source()
	for _, id := range []string{"time-a", "time-b"} {
		r := Record{ID: id, Scope: scopeA, Kind: "decision", Text: "시간 결정", Status: "active", ApprovedBy: "human", Revision: 1, Sources: refs(src)}
		if e := repo.Insert(testCtx, colMemories, r); e != nil {
			t.Fatal(e)
		}
	}
	r, e := repo.Get(testCtx, colMemories, scopeA, "time-a")
	if e != nil || len(r.MemoryTimeline) != 1 {
		t.Fatal(r, e)
	}
	// Updating retrieval metadata must preserve the lifecycle history, including attempted forgery.
	r.MemoryTimeline = nil
	r.Trust = .7
	if e = repo.Replace(testCtx, colMemories, r, r.Revision); e != nil {
		t.Fatal(e)
	}
	r, _ = repo.Get(testCtx, colMemories, scopeA, r.ID)
	if len(r.MemoryTimeline) != 1 {
		t.Fatal("lost journal")
	}
	at := time.Now().UTC()
	time.Sleep(2 * time.Millisecond)
	if _, e = f.svc.Transition(testCtx, f.human, colMemories, r.ID, ChangeInput{Scope: scopeA, ExpectedRevision: r.Revision, Status: "rejected"}); e != nil {
		t.Fatal(e)
	}
	in := MemoryAtInput{Scope: scopeA, AsOf: at, Query: "시간", Limit: 1}
	first, e := f.svc.MemoriesAt(testCtx, f.human, in)
	if e != nil || len(first.Items) != 1 || first.Items[0].ID != "time-a" || first.NextCursor == "" || first.Items[0].HistoricalRevision != 1 {
		t.Fatal(first, e)
	}
	in.Cursor = first.NextCursor
	second, e := f.svc.MemoriesAt(testCtx, f.human, in)
	if e != nil || len(second.Items) != 1 || second.Items[0].ID != "time-b" || second.NextCursor != "" {
		t.Fatal(second, e)
	}
	in.AsOf = in.AsOf.Add(-time.Second)
	if _, e = f.svc.MemoriesAt(testCtx, f.human, in); !errors.Is(e, ErrInvalid) {
		t.Fatal("unbound cursor", e)
	}
	in.Cursor = ""
	in.AsOf = time.Now()
	now, e := f.svc.MemoriesAt(testCtx, f.human, in)
	if e != nil || len(now.Items) != 1 || now.Items[0].ID != "time-b" {
		t.Fatal(now, e)
	}
	// A failed transaction cannot publish a state observation.
	before, _ := repo.Get(testCtx, colMemories, scopeA, "time-b")
	e = repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		r := before
		r.Status = "rejected"
		r.Revision++
		if e := tx.Replace(ctx, colMemories, r, before.Revision); e != nil {
			return e
		}
		return ErrInvalid
	})
	if !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	after, _ := repo.Get(testCtx, colMemories, scopeA, "time-b")
	if structuredText(after) != structuredText(before) {
		t.Fatal("rollback lost")
	}
	// Current erasure blocks a historical view even though the requested state was active.
	after.ContentErased = true
	after.Text = ""
	after.Revision++
	if e = repo.Replace(testCtx, colMemories, after, before.Revision); e != nil {
		t.Fatal(e)
	}
	in.AsOf = at
	in.Limit = 10
	erased, e := f.svc.MemoriesAt(testCtx, f.human, in)
	if e != nil || len(erased.Items) != 1 || erased.Items[0].ID != "time-a" {
		t.Fatal(erased, e)
	}
	in.Scope = scopeB
	if page, e := f.svc.MemoriesAt(testCtx, f.agent, in); e != nil || len(page.Items)+len(page.Unknown) != 0 {
		t.Fatal("cross scope", page, e)
	}
}
func TestMemoryTimeRoundtrip(t *testing.T)         { exerciseMemoryTime(t, newFakeRepo()) }
func TestPostgresMemoryTimeRoundtrip(t *testing.T) { exerciseMemoryTime(t, isolatedPostgres(t)) }
func TestMemoryTimeHTTPValidation(t *testing.T) {
	f := newFixture(t)
	for _, body := range []string{`{"as_of":"2026-01-01T00:00:00"}`, `{"as_of":"9999-01-01T00:00:00Z"}`, `{"as_of":"2026-01-01T00:00:00Z","extra":true}`} {
		req := httptest.NewRequest("POST", "/v1/memories/at", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestMemoryTimeCurrentPolicyAndReadFence(t *testing.T) {
	for _, target := range []string{"deny", "actor", "body", "source", "erased", "audit"} {
		t.Run(target, func(t *testing.T) {
			f := newFixture(t)
			r := f.activeMemory("needle")
			auth := &memoryListAuth{Authorizer: f.auth}
			switch target {
			case "deny":
				auth.deny = map[string]bool{r.ID: true}
			case "actor":
				auth.change = "principal"
			default:
				auth.mutate = func() {
					var e error
					switch target {
					case "body":
						r.Text = "changed"
						e = f.repo.Replace(testCtx, colMemories, r, r.Revision)
					case "erased":
						r.ContentErased = true
						e = f.repo.Replace(testCtx, colMemories, r, r.Revision)
					case "source":
						src, _ := f.repo.Get(testCtx, "sources", r.Scope, r.Sources[0].SourceID)
						e = f.repo.Delete(testCtx, "sources", r.Scope, src.ID, src.Revision)
					case "audit":
						f.repo.failAudit = true
					}
					if e != nil {
						t.Fatal(e)
					}
				}
			}
			f.svc.Auth = auth
			out, e := f.svc.MemoriesAt(testCtx, f.human, MemoryAtInput{Scope: scopeA, AsOf: time.Now(), Query: "needle"})
			if target == "deny" {
				if e != nil || len(out.Items)+len(out.Unknown) != 0 {
					t.Fatal(out, e)
				}
			} else if e == nil || len(out.Items)+len(out.Unknown) != 0 {
				t.Fatal("stale historical result", out, e)
			}
		})
	}
}

func exerciseMemoryTimeReplacement(t *testing.T, repo Repository) {
	f := newFixture(t)
	f.svc.Repo = repo
	src := f.source()
	old := f.activeMemory("이전 결정")
	at := time.Now()
	time.Sleep(2 * time.Millisecond)
	replaced, e := f.svc.Supersede(testCtx, f.human, old.ID, SupersedeInput{Scope: scopeA, ExpectedRevision: old.Revision, Reason: "새 결정", NewContent: "다음 결정", Sources: refs(src)})
	if e != nil {
		t.Fatal(e)
	}
	oldStored, _ := repo.Get(testCtx, colMemories, scopeA, old.ID)
	next, _ := repo.Get(testCtx, colMemories, scopeA, replaced.New.ID)
	boundary := oldStored.MemoryTimeline[len(oldStored.MemoryTimeline)-1].ObservedAt
	if !boundary.Equal(next.MemoryTimeline[0].ObservedAt) {
		t.Fatal("replacement was not one boundary")
	}
	for _, tc := range []struct {
		at time.Time
		id string
	}{{at, old.ID}, {boundary, next.ID}} {
		page, e := f.svc.MemoriesAt(testCtx, f.human, MemoryAtInput{Scope: scopeA, AsOf: tc.at})
		if e != nil || len(page.Items) != 1 || page.Items[0].ID != tc.id {
			t.Fatal(page, e)
		}
	}
}
func TestMemoryTimeReplacement(t *testing.T) { exerciseMemoryTimeReplacement(t, newFakeRepo()) }
func TestPostgresMemoryTimeReplacement(t *testing.T) {
	exerciseMemoryTimeReplacement(t, isolatedPostgres(t))
}
