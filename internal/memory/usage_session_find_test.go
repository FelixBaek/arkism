package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUsageSessionFind(t *testing.T)         { testUsageSessionFind(t, nil) }
func TestPostgresUsageSessionFind(t *testing.T) { testUsageSessionFind(t, isolatedPostgres(t)) }

func testUsageSessionFind(t *testing.T, repo Repository) {
	f, now := archiveSearchFixture(t, repo)
	raw := jsonLine(map[string]string{"role": "user", "content": "배포 스크립트 고쳐 줘"})
	raw = append(raw, jsonLine(map[string]string{"role": "assistant", "content": strings.Repeat("앞말 ", 40) + "배포 스크립트를 고쳤어요" + strings.Repeat(" 뒷말", 60)})...)
	raw = append(raw, jsonLine(map[string]string{"role": "user", "content": "<private>배포 스크립트 비밀</private> 다른 말"})...)
	raw = append(raw, jsonLine(map[string]string{"role": "user", "content": "배포 스크립트 password: hunter2secret"})...)
	archiveID := ContentHash("find-archive")
	archiveSearchUpload(t, f, *now, archiveID, raw, "jsonl")
	id := sessionManageUsage(t, f, *now, "search-session")
	sessionManageUsage(t, f, *now, "other-session")

	// 세션 찾기: hits grouped under their session, private-only and secret messages left out.
	out, err := f.svc.SearchUsageSessions(testCtx, f.human, UsageSessionSearchInput{Scope: scopeA, Query: "배포 스크립트"})
	if err != nil || len(out.Items) != 1 || out.Authority != AuthorityDataOnly || out.Hidden != 1 || out.Secrets != 1 {
		t.Fatalf("search: %+v %v", out, err)
	}
	g := out.Items[0]
	if g.Session["id"] != id || g.Session["session_id"] != "search-session" || len(g.Hits) != 2 || g.Hits[0].Role != "user" {
		t.Fatalf("group: %+v", g)
	}
	long := g.Hits[1]
	if !strings.HasPrefix(long.Snippet, "…") || !strings.HasSuffix(long.Snippet, "…") || !strings.HasPrefix(long.Snippet[long.Match:], "배포 스크립트") || len(long.Snippet) > 400 {
		t.Fatalf("snippet: %q at %d", long.Snippet, long.Match)
	}
	if joined, _ := json.Marshal(out); strings.Contains(string(joined), "비밀") || strings.Contains(string(joined), "hunter2") {
		t.Fatalf("leak: %s", joined)
	}
	if other, err := f.svc.SearchUsageSessions(testCtx, f.human, UsageSessionSearchInput{Scope: scopeA, Query: "배포 스크립트", Agent: "claude_code"}); err != nil || len(other.Items) != 0 {
		t.Fatalf("agent filter: %+v %v", other, err)
	}
	for _, bad := range []UsageSessionSearchInput{{Scope: scopeA}, {Scope: scopeA, Query: " 배포"}, {Scope: scopeA, Query: "x", Limit: 99}, {Scope: scopeA, Query: "x", Agent: "a b"}} {
		if _, err := f.svc.SearchUsageSessions(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %+v: %v", bad, err)
		}
	}

	// The list carries the folder, filters by project and counts agents and projects.
	u := UsageSession{Agent: "claude_code", Device: "pc", SessionID: "folder-session", Project: "web", Folder: "C:\\work\\web", File: "C:\\logs\\a.jsonl",
		StartedAt: now.Add(-time.Minute), EndedAt: *now, Messages: 1, Days: map[string]map[string]UsageTokens{now.Format("2006-01-02"): {"m": {Input: 1}}}}
	if _, err := f.svc.RecordUsageSessions(testCtx, f.human, UsageSessionsInput{Scope: scopeA, Items: []UsageSession{u}}); err != nil {
		t.Fatal(err)
	}
	list, err := f.svc.usageSessionListPage(testCtx, f.human, scopeA, "all", "", "web", "", "recent", "", 50)
	items, _ := list["items"].([]map[string]any)
	facets, _ := list["facets"].(map[string]map[string]int)
	if err != nil || len(items) != 1 || items[0]["folder"] != `C:\work\web` || items[0]["file"] != `C:\logs\a.jsonl` ||
		facets["agents"]["claude_code"] != 1 || facets["agents"]["codex"] != 0 || facets["projects"]["arkism"] != 2 || facets["projects"]["web"] != 1 {
		t.Fatalf("list: %v %v %v", items, facets, err)
	}
	u.Folder = "bad\x00path"
	if _, err := f.svc.RecordUsageSessions(testCtx, f.human, UsageSessionsInput{Scope: scopeA, Items: []UsageSession{u}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("control character in folder accepted", err)
	}

	// 이어가기: the archive until a work state exists, then the newest work state too.
	h, err := f.svc.UsageSessionHandoff(testCtx, f.human, UsageSessionHandoffInput{Scope: scopeA, ID: id})
	if err != nil || h.ArchiveID != archiveID || h.Work != nil || h.Agent != "codex" || h.SessionID != "search-session" {
		t.Fatalf("handoff without work: %+v %v", h, err)
	}
	pkg := workCheckpointPackage()
	pkg.SessionID = "search-session"
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, WorkCheckpointSaveInput{Scope: scopeA, ID: "find-work", EventID: "evt-find-1", Package: pkg}); err != nil {
		t.Fatal(err)
	}
	h, err = f.svc.UsageSessionHandoff(testCtx, f.human, UsageSessionHandoffInput{Scope: scopeA, ID: id})
	if err != nil || h.Work == nil || h.Work.ID != "find-work" || h.Work.Title != pkg.Title || h.Work.Revision != 1 {
		t.Fatalf("handoff with work: %+v %v", h, err)
	}
	if _, err := f.svc.UsageSessionHandoff(testCtx, f.human, UsageSessionHandoffInput{Scope: scopeA, ID: ContentHash("missing")}); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing session", err)
	}
	if _, err := f.svc.UsageSessionHandoff(testCtx, f.human, UsageSessionHandoffInput{Scope: scopeA, ID: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad id", err)
	}
}

func TestUsageSessionFindRoutes(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	id := sessionManageUsage(t, f, *now, "route-session")
	h := Handler(f.svc)
	call := func(path string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+f.human)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, out := call("/v1/usage/sessions/search", map[string]any{"scope": scopeA, "q": "아무말"}); code != http.StatusOK || len(out["items"].([]any)) != 0 {
		t.Fatal("search route", code, out)
	}
	if code, out := call("/v1/usage/sessions/handoff", map[string]any{"scope": scopeA, "id": id}); code != http.StatusOK || out["session_id"] != "route-session" || out["work"] != nil {
		t.Fatal("handoff route", code, out)
	}
	if code, _ := call("/v1/usage/sessions/handoff", map[string]any{"scope": scopeA, "id": id, "extra": 1}); code != http.StatusBadRequest {
		t.Fatal("unknown field accepted", code)
	}
}
