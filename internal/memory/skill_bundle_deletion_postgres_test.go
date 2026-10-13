package memory

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// a deleted pinned skill must not survive in the projected runtime folder or the
// router's offline cache, while the hub's pinned bytes (not the local folder) are what was deployed.
func TestPostgresSkillBundleDeletionRemovesProjectedCopies(t *testing.T) {
	repo := isolatedPostgres(t)
	f := deletionReplayFixture(t, repo)
	root := t.TempDir()
	skillDir := filepath.Join(root, "source", "demo")
	if err := os.MkdirAll(filepath.Join(skillDir, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: demo\ndescription: demo\n---\nRun scripts/run.sh.\n"
	logo := "\x89PNG\x00pinned"
	for name, data := range map[string]string{"SKILL.md": content, "scripts/run.sh": "#!/bin/sh\necho pinned\n", "scripts/logo.png": logo} {
		if err := os.WriteFile(filepath.Join(skillDir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	files := []SkillBundleFile{{Path: "scripts/logo.png", SHA256: ContentHash(logo), Size: int64(len(logo)), Omitted: "binary"}, {Path: "scripts/run.sh", Content: "#!/bin/sh\necho pinned\n", SHA256: ContentHash("#!/bin/sh\necho pinned\n")}}
	imported, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: "demo", Path: filepath.Join(skillDir, "SKILL.md"), Content: content, Analysis: detailedSkillAnalysis(), Bundle: &SkillBundle{Files: files, SHA256: skillBundleHash(files)}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler(f.svc))
	defer server.Close()
	tokenFile := filepath.Join(root, "human.token")
	if err = os.WriteFile(tokenFile, []byte(f.human), 0600); err != nil {
		t.Fatal(err)
	}
	// A second live skill keeps project-skills from treating "every skill refused" as a
	// misconfigured token, so the deleted skill's copy is actually removed.
	if _, err = f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: "keep", Path: filepath.Join(root, "source", "keep", "SKILL.md"), Content: "# Keep\nRun scripts/keep.sh.\n", Analysis: detailedSkillAnalysis(), Bundle: bundleForTest(SkillBundleFile{Path: "scripts/keep.sh", Content: "echo keep\n"})}); err != nil {
		t.Fatal(err)
	}
	runtime := filepath.Join(root, "runtime")
	projection, _ := json.Marshal(map[string]any{"targets": map[string]any{runtime: map[string]string{"demo": "demo", "keep": "keep"}}})
	if err = os.WriteFile(filepath.Join(root, "source", "projection.json"), projection, 0600); err != nil {
		t.Fatal(err)
	}
	env := func(url string) []string {
		return arkismEnv(root, url, tokenFile, "ARKISM_RUNTIME=codex", "ARKISM_SKILL_CACHE="+filepath.Join(root, "cache"), "SKILL_SOURCE_ROOT="+filepath.Join(root, "source"))
	}
	project := func() string {
		t.Helper()
		return string(arkismRun(t, env(server.URL), "", "project-skills"))
	}
	dst := filepath.Join(runtime, "demo")
	// The local folder diverges after the pin; only the hub's pinned version may be deployed.
	if err = os.WriteFile(filepath.Join(skillDir, "scripts/run.sh"), []byte("#!/bin/sh\necho local-unapproved\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if message := project(); !strings.Contains(message, "배포 demo@1") {
		t.Fatal("pinned deploy", message)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "scripts/run.sh")); err != nil || string(got) != "#!/bin/sh\necho pinned\n" {
		t.Fatal("deployed script", err, string(got))
	}
	if got, err := os.ReadFile(filepath.Join(dst, "scripts/logo.png")); err != nil || string(got) != logo {
		t.Fatal("deployed binary", err)
	}
	var loaded struct {
		Ref    string `json:"ref"`
		Bundle struct {
			HashOnly []string `json:"hash_only"`
		} `json:"bundle"`
	}
	responses := arkismMCP(t, env(server.URL), mcpCall{"load_skill", map[string]any{"skill_id": "demo"}}, mcpCall{"read_skill_file", map[string]any{"ref": "demo@1", "path": "scripts/run.sh"}})
	mcpResult(t, responses[0], &loaded)
	if loaded.Ref != "demo@1" || len(loaded.Bundle.HashOnly) != 1 || loaded.Bundle.HashOnly[0] != "scripts/logo.png" {
		t.Fatalf("loaded bundle: %+v", loaded)
	}
	var file struct {
		Content string `json:"content"`
	}
	mcpResult(t, responses[1], &file)
	if file.Content != "#!/bin/sh\necho pinned\n" {
		t.Fatal("pinned file", file.Content)
	}
	sourceID := imported.Record.Sources[0].SourceID
	source, err := f.svc.Repo.Get(testCtx, "sources", scopeA, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.Delete(testCtx, f.human, "sources", sourceID, ChangeInput{Scope: scopeA, ExpectedRevision: source.Revision}); err != nil {
		t.Fatal(err)
	}
	drainDeletionReplayFixture(t, f, repo)
	if message := project(); !strings.Contains(message, "배포 사본 삭제") {
		t.Fatal("deleted skill copy kept", message)
	} else if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("deleted skill copy remains", err)
	}
	if arkismMCP(t, env(server.URL), mcpCall{"load_skill", map[string]any{"skill_id": "demo"}})[0]["error"] == nil {
		t.Fatal("deleted skill still loads")
	}
	for _, ref := range []string{"demo", "demo@1"} {
		if message := mcpError(arkismMCP(t, env("http://127.0.0.1:9"), mcpCall{"load_skill", map[string]any{"skill_id": ref}})[0]); !strings.Contains(message, "skill_unavailable_offline") {
			t.Fatal("deleted skill served from offline cache", ref, message)
		}
	}

}
