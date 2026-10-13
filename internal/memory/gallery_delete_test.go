package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"
)

func galleryCall(t *testing.T, f *fixture, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func scopeParams(s Scope) string {
	return url.Values{"workspace_id": {s.WorkspaceID}, "project_id": {s.ProjectID}, "user_id": {s.UserID}, "agent_id": {s.AgentID}}.Encode()
}

func countAudit(f *fixture, kind, id string) int {
	n := 0
	for _, row := range f.repo.rows("audit_events") {
		if row.Kind == kind && row.Target != nil && row.Target.ID == id {
			n++
		}
	}
	return n
}

func TestGalleryPromptDeleteArchiveRestore(t *testing.T) {
	f := newFixture(t)
	p := ImagePrompt{Title: "흑백 인물", Kind: "image", Engine: "gemini", Prompt: "line art of {인물}", Images: []string{"data:image/png;base64,iVBORw0KGgo="}}
	keep, err := f.svc.CreatePrompt(testCtx, f.human, PromptInput{Scope: scopeA, Prompt: p})
	if err != nil {
		t.Fatal(err)
	}
	gone, _ := f.svc.CreatePrompt(testCtx, f.human, PromptInput{Scope: scopeA, Prompt: p})

	// An agent cannot delete; a stale revision cannot delete.
	if err := f.svc.Delete(testCtx, f.agent, colPrompts, gone.ID, ChangeInput{Scope: scopeA, ExpectedRevision: gone.Revision}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent deleted a prompt", err)
	}
	if err := f.svc.Delete(testCtx, f.human, colPrompts, gone.ID, ChangeInput{Scope: scopeA, ExpectedRevision: gone.Revision + 1}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale delete accepted", err)
	}
	code, _ := galleryCall(t, f, "DELETE", "/v1/prompts/"+gone.ID, f.human, ChangeInput{Scope: scopeA, ExpectedRevision: gone.Revision})
	if code != 200 {
		t.Fatal("delete over HTTP", code)
	}
	if _, err := f.svc.Get(testCtx, f.human, colPrompts, scopeA, gone.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted prompt still readable", err)
	}
	if countAudit(f, "delete:prompts", gone.ID) != 1 {
		t.Fatal("delete not audited")
	}

	// Archive, see it in the 보관함, restore it, archive again, delete it from there.
	arch, err := f.svc.UpdatePrompt(testCtx, f.human, keep.ID, PromptUpdate{Scope: scopeA, ExpectedRevision: keep.Revision, Archive: true})
	if err != nil {
		t.Fatal(err)
	}
	code, out := galleryCall(t, f, "GET", "/v1/prompts/archived?"+scopeParams(scopeA), f.human, nil)
	if items, _ := out["items"].([]any); code != 200 || len(items) != 1 {
		t.Fatal("archived list", code, out)
	}
	if _, err := f.svc.RestoreGallery(testCtx, f.agent, colPrompts, keep.ID, ChangeInput{Scope: scopeA, ExpectedRevision: arch.Revision}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent restored a prompt", err)
	}
	code, out = galleryCall(t, f, "POST", "/v1/prompts/"+keep.ID+"/restore", f.human, ChangeInput{Scope: scopeA, ExpectedRevision: arch.Revision})
	if code != 200 || out["status"] != statusActive {
		t.Fatal("restore", code, out)
	}
	if page, _ := f.svc.ListPrompts(testCtx, f.human, scopeA); len(page.Items) != 1 {
		t.Fatal("restored prompt not listed")
	}
	if _, err := f.svc.RestoreGallery(testCtx, f.human, colPrompts, keep.ID, ChangeInput{Scope: scopeA, ExpectedRevision: arch.Revision + 1}); !errors.Is(err, ErrConflict) {
		t.Fatal("restored an active prompt", err)
	}
	if countAudit(f, "restore:prompts", keep.ID) != 1 {
		t.Fatal("restore not audited")
	}
	live, _ := f.repo.Get(testCtx, colPrompts, scopeA, keep.ID)
	arch, _ = f.svc.UpdatePrompt(testCtx, f.human, keep.ID, PromptUpdate{Scope: scopeA, ExpectedRevision: live.Revision, Archive: true})
	if err := f.svc.Delete(testCtx, f.human, colPrompts, keep.ID, ChangeInput{Scope: scopeA, ExpectedRevision: arch.Revision}); err != nil {
		t.Fatal("archived prompt not deletable", err)
	}
	if rows, _ := f.svc.ListArchivedGallery(testCtx, f.human, colPrompts, scopeA); len(rows) != 0 {
		t.Fatal("deleted prompt still archived", rows)
	}
	// Other collections do not answer on these routes.
	if _, err := f.svc.ListArchivedGallery(testCtx, f.human, "memories", scopeA); !errors.Is(err, ErrInvalid) {
		t.Fatal("archived list for another collection", err)
	}
}

func TestGalleryDesignSystemDeleteArchiveRestore(t *testing.T) {
	f := newFixture(t)
	d := DesignSystem{Title: "Demo 메일", Product: "Demo", Colors: []DesignColor{{Name: "accent", Value: "#e4e1db"}}}
	var ids []Record
	for range 3 {
		r, err := f.svc.CreateDesignSystem(testCtx, f.human, DesignSystemInput{Scope: scopeA, System: d})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r)
	}
	if err := f.svc.Delete(testCtx, f.agent, colDesignSystems, ids[0].ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent deleted a design system", err)
	}
	arch, _ := f.svc.UpdateDesignSystem(testCtx, f.human, ids[2].ID, DesignSystemUpdate{Scope: scopeA, ExpectedRevision: 1, Archive: true})
	code, out := galleryCall(t, f, "GET", "/v1/design_systems/archived?"+scopeParams(scopeA), f.human, nil)
	if items, _ := out["items"].([]any); code != 200 || len(items) != 1 {
		t.Fatal("archived list", code, out)
	}
	code, _ = galleryCall(t, f, "POST", "/v1/design_systems/"+ids[2].ID+"/restore", f.human, ChangeInput{Scope: scopeA, ExpectedRevision: arch.Revision})
	if code != 200 {
		t.Fatal("restore", code)
	}
	// 모두 선택 → 삭제: every one goes, each audited.
	for _, r := range ids {
		cur, _ := f.repo.Get(testCtx, colDesignSystems, scopeA, r.ID)
		if code, _ := galleryCall(t, f, "DELETE", "/v1/design_systems/"+r.ID, f.human, ChangeInput{Scope: scopeA, ExpectedRevision: cur.Revision}); code != 200 {
			t.Fatal("delete", r.ID, code)
		}
		if countAudit(f, "delete:design_systems", r.ID) != 1 {
			t.Fatal("delete not audited", r.ID)
		}
	}
	if page, _ := f.svc.ListDesignSystems(testCtx, f.human, scopeA); len(page.Items) != 0 {
		t.Fatal("design systems left", page.Items)
	}
}
