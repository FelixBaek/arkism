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

func TestSessionManage(t *testing.T)         { testSessionManage(t, nil) }
func TestPostgresSessionManage(t *testing.T) { testSessionManage(t, isolatedPostgres(t)) }

func sessionManageUsage(t *testing.T, f *fixture, now time.Time, sid string) string {
	t.Helper()
	cost := 0.5
	u := UsageSession{Agent: "codex", Device: "test", SessionID: sid, Title: "세션 " + sid, Project: "arkism", StartedAt: now.Add(-time.Hour), EndedAt: now,
		Messages: 4, Days: map[string]map[string]UsageTokens{now.Format("2006-01-02"): {"gpt-5": {Input: 10, Output: 20, Requests: 2, CostUSD: &cost}}},
		Tools: map[string]int{"shell": 3}}
	if _, err := f.svc.RecordUsageSessions(testCtx, f.human, UsageSessionsInput{Scope: scopeA, Items: []UsageSession{u}}); err != nil {
		t.Fatal(err)
	}
	return usageSessionID(u)
}

func testSessionManage(t *testing.T, repo Repository) {
	f, now := archiveSearchFixture(t, repo)
	raw := jsonLine(map[string]string{"role": "user", "content": "첫 질문 <private>내 비밀 계획</private> 끝", "timestamp": "2026-10-13T01:12:05.250+09:00"})
	raw = append(raw, jsonLine(map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": "답변입니다"}, {"type": "tool_use", "text": "rm -rf hidden"}}})...)
	raw = append(raw, jsonLine(map[string]string{"role": "tool", "content": "tool output hidden"})...)
	raw = append(raw, jsonLine(map[string]string{"role": "user", "content": "password: hunter2secret"})...)
	raw = append(raw, jsonLine(map[string]string{"role": "assistant", "content": "긴 답 " + strings.Repeat("가", 6000) + " 끝"})...)
	archiveID := ContentHash("manage-archive")
	archiveSearchUpload(t, f, *now, archiveID, raw, "jsonl")
	withArchive := sessionManageUsage(t, f, *now, "search-session")
	without := sessionManageUsage(t, f, *now, "no-archive-session")

	// 대화 보기: pages, user/assistant text only, private masked, secret hidden.
	var got []SessionTranscriptMessage
	in := SessionTranscriptInput{Scope: scopeA, ID: withArchive, Limit: 1}
	hidden := 0
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("transcript paging did not end")
		}
		out, err := f.svc.SessionTranscript(testCtx, f.human, in)
		if err != nil {
			t.Fatal(err)
		}
		if out.State != "active" || out.ArchiveID != archiveID || len(out.Archives) != 1 || out.Authority != AuthorityDataOnly {
			t.Fatalf("wrong transcript head: %+v", out)
		}
		got = append(got, out.Items...)
		hidden += out.Hidden.Secrets
		if out.Next == "" {
			break
		}
		in.ArchiveID, in.After = out.ArchiveID, out.Next
	}
	joined, _ := json.Marshal(got)
	if len(got) != 3 || got[0].Role != "user" || got[1].Role != "assistant" || got[1].Text != "답변입니다" || hidden != 1 {
		t.Fatalf("wrong dialogue: %s hidden=%d", joined, hidden)
	}
	if !strings.Contains(got[0].Text, "[비공개]") || strings.Contains(string(joined), "비밀 계획") || strings.Contains(string(joined), "hunter2") ||
		strings.Contains(string(joined), "hidden") || !strings.HasSuffix(got[2].Text, " 끝") || got[2].Truncated {
		t.Fatalf("leak or lost text: %s", joined)
	}
	if got[0].At == nil || !got[0].At.Equal(time.Date(2026, 10, 12, 16, 12, 5, 250e6, time.UTC)) || got[1].At != nil {
		t.Fatalf("message time: %s", joined)
	}
	none, err := f.svc.SessionTranscript(testCtx, f.human, SessionTranscriptInput{Scope: scopeA, ID: without})
	if err != nil || none.State != "none" || len(none.Items) != 0 {
		t.Fatalf("no-archive session: %+v %v", none, err)
	}
	for _, bad := range []SessionTranscriptInput{{Scope: scopeA, ID: "x"}, {Scope: scopeA, ID: withArchive, After: "abc"}, {Scope: scopeA, ID: withArchive, Limit: 99}} {
		if _, err := f.svc.SessionTranscript(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %+v: %v", bad, err)
		}
	}

	// 대화 저장: one call says which sessions still keep their dialogue and until when; no text.
	st, err := f.svc.SessionArchiveStatuses(testCtx, f.human, SessionArchiveStatusInput{Scope: scopeA, IDs: []string{withArchive, without, ContentHash("missing")}})
	if err != nil || len(st.Items) != 2 || len(st.Missing) != 1 || st.Items[0].State != "active" || len(st.Items[0].Archives) != 1 ||
		st.Items[0].Archives[0].ExpiresAt == nil || st.Items[1].State != "none" || st.Authority != AuthorityDataOnly {
		t.Fatalf("archive statuses: %+v %v", st, err)
	}
	for _, bad := range []SessionArchiveStatusInput{{Scope: scopeA}, {Scope: scopeA, IDs: []string{"x"}}, {Scope: scopeA, IDs: []string{withArchive, withArchive}}} {
		if _, err := f.svc.SessionArchiveStatuses(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %+v: %v", bad, err)
		}
	}
	if _, err := f.svc.SessionArchiveStatuses(testCtx, "", SessionArchiveStatusInput{Scope: scopeA, IDs: []string{withArchive}}); err == nil {
		t.Fatal("anonymous read archive statuses")
	}
	if got := archiveSummaryState([]SessionTranscriptArchive{{State: "expired"}, {State: "active", Supported: false}}); got != "unsupported" {
		t.Fatal(got)
	}
	if got := archiveSummaryState([]SessionTranscriptArchive{{State: "deleted"}}); got != "expired" {
		t.Fatal(got)
	}

	// 내보내기: markdown and JSON carry the safe dialogue only.
	md, err := f.svc.ExportSessions(testCtx, f.human, SessionExportInput{Scope: scopeA, IDs: []string{withArchive, without}, Format: "markdown", Transcript: true})
	if err != nil || md.Sessions != 2 || md.Messages != 3 || !strings.HasSuffix(md.Filename, ".md") || !strings.Contains(md.Content, "### Assistant\n\n답변입니다") ||
		!strings.Contains(md.Content, "No archived transcript") || strings.Contains(md.Content, "hunter2") || strings.Contains(md.Content, "비밀 계획") {
		t.Fatalf("markdown export: %v\n%s", err, md.Content)
	}
	js, err := f.svc.ExportSessions(testCtx, f.human, SessionExportInput{Scope: scopeA, IDs: []string{withArchive}, Format: "json"})
	var doc struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err != nil || json.Unmarshal([]byte(js.Content), &doc) != nil || len(doc.Sessions) != 1 || doc.Sessions[0]["transcript"] != nil || doc.Sessions[0]["session_id"] != "search-session" {
		t.Fatalf("json export: %v %s", err, js.Content)
	}

	// A memory-style citation resolves before deletion.
	refs, err := f.svc.SearchSessionArchiveMessages(testCtx, f.human, ArchiveSearchInput{Scope: scopeA, ArchiveID: archiveID, Limit: 1})
	if err != nil || len(refs.Items) != 1 {
		t.Fatal("ref", err)
	}
	ref := refs.Items[0].Ref

	// 지우기: agents cannot, humans can; archives become erased tombstones.
	if _, err := f.svc.DeleteSessions(testCtx, f.agent, SessionDeleteInput{Scope: scopeA, IDs: []string{withArchive}}); !errors.Is(err, ErrDenied) {
		t.Fatal("agent deleted sessions", err)
	}
	del, err := f.svc.DeleteSessions(testCtx, f.human, SessionDeleteInput{Scope: scopeA, IDs: []string{withArchive, without, ContentHash("missing")}})
	if err != nil || len(del.Deleted) != 2 || len(del.Missing) != 1 || del.Archives != 1 {
		t.Fatalf("delete: %+v %v", del, err)
	}
	if _, err := f.svc.Repo.Get(testCtx, colUsageSessions, scopeA, withArchive); !errors.Is(err, ErrNotFound) {
		t.Fatal("usage row kept", err)
	}
	header, err := f.svc.Repo.Get(testCtx, colSessionArchives, scopeA, archiveID)
	if err != nil || !header.ContentErased || header.Text != "" || len(header.SessionArchive.Chunks) != 0 {
		t.Fatalf("header not erased: %+v %v", header, err)
	}
	chunks, _ := f.svc.Repo.ListRecords(testCtx, scopeA, colSessionArchiveChunks, 1000)
	for _, c := range chunks {
		if c.SessionArchive != nil && c.SessionArchive.ParentID == archiveID {
			t.Fatal("chunk ciphertext kept")
		}
	}
	if _, err := f.svc.ReadSessionArchiveMessage(testCtx, f.human, ArchiveMessageInput{Scope: scopeA, Ref: ref}); err == nil {
		t.Fatal("citation still resolves after deletion")
	}
	// The collector sending the same session or a grown file again does not bring it back.
	sessionManageUsage(t, f, *now, "search-session")
	if _, err := f.svc.Repo.Get(testCtx, colUsageSessions, scopeA, withArchive); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted usage row revived", err)
	}
	*now = now.Add(time.Minute)
	archiveSearchUpload(t, f, *now, archiveID, append(raw, jsonLine(map[string]string{"role": "user", "content": "later"})...), "jsonl")
	header, err = f.svc.Repo.Get(testCtx, colSessionArchives, scopeA, archiveID)
	if err != nil || !header.ContentErased || sessionArchiveStateOf(header, *now) != sessionArchiveStateExpired {
		t.Fatal("deleted archive revived", err)
	}
	if _, err := f.svc.SessionTranscript(testCtx, f.human, SessionTranscriptInput{Scope: scopeA, ID: withArchive}); !errors.Is(err, ErrNotFound) {
		t.Fatal("transcript of deleted session", err)
	}
	// Deleting again is idempotent.
	again, err := f.svc.DeleteSessions(testCtx, f.human, SessionDeleteInput{Scope: scopeA, IDs: []string{withArchive}})
	if err != nil || len(again.Missing) != 1 {
		t.Fatal("repeat delete", again, err)
	}
}

func TestSessionManageRoutes(t *testing.T) {
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
	if code, out := call("/v1/usage/sessions/transcript", map[string]any{"scope": scopeA, "id": id}); code != http.StatusOK || out["state"] != "none" {
		t.Fatal("transcript route", code, out)
	}
	if code, out := call("/v1/usage/sessions/export", map[string]any{"scope": scopeA, "ids": []string{id}, "format": "markdown"}); code != http.StatusOK || !strings.Contains(out["content"].(string), "route-session") {
		t.Fatal("export route", code, out)
	}
	if code, _ := call("/v1/usage/sessions/export", map[string]any{"scope": scopeA, "ids": []string{id}, "format": "pdf"}); code != http.StatusBadRequest {
		t.Fatal("export accepted pdf", code)
	}
	if code, out := call("/v1/usage/sessions/delete", map[string]any{"scope": scopeA, "ids": []string{id}}); code != http.StatusOK || len(out["deleted"].([]any)) != 1 {
		t.Fatal("delete route", code, out)
	}
}
