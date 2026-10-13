package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bundleForTest(files ...SkillBundleFile) *SkillBundle {
	for i := range files {
		files[i].SHA256 = ContentHash(files[i].Content)
	}
	return &SkillBundle{Files: files, SHA256: skillBundleHash(files)}
}

func TestSkillBundleValidation(t *testing.T) {
	good := func() *SkillBundle {
		return bundleForTest(SkillBundleFile{Path: "scripts/run.py", Content: "print('한국어')\r\n"})
	}
	if !validSkillBundle(good()) {
		t.Fatal("valid UTF-8 bundle rejected")
	}
	for _, name := range []string{"", "../file", "/file", "a/../file", "a//b", "a\\b", "C:file", ".env", "a/.hidden/file", "a/x.pem", "node_modules/a", "__pycache__/a", "a\nfile"} {
		b := good()
		b.Files[0].Path = name
		b.SHA256 = skillBundleHash(b.Files)
		if validSkillBundle(b) {
			t.Fatalf("unsafe path accepted: %q", name)
		}
	}

	for _, content := range []string{string([]byte{0xff}), "a\x00b"} {
		if validSkillBundle(bundleForTest(SkillBundleFile{Path: "script.py", Content: content})) {
			t.Fatal("non-text accepted")
		}
	}
	if !validSkillBundle(bundleForTest([]SkillBundleFile{}...)) || validSkillBundle(bundleForTest()) {
		t.Fatal("empty and missing file lists confused")
	}
	for _, limit := range []struct{ count, size int }{{2049, 1}, {6, 200000}} {
		files := make([]SkillBundleFile, limit.count)
		for i := range files {
			files[i] = SkillBundleFile{Path: fmt.Sprintf("f%03d", i), Content: strings.Repeat("x", limit.size)}
		}
		if validSkillBundle(bundleForTest(files...)) {
			t.Fatal("bundle limit ignored")
		}
	}
	hashOnly := func(f SkillBundleFile) *SkillBundle {
		files := []SkillBundleFile{{Path: "a.txt", Content: "a", SHA256: ContentHash("a")}, f}
		return &SkillBundle{Files: files, SHA256: skillBundleHash(files)}
	}
	digest := strings.Repeat("ab", 32)
	if !validSkillBundle(hashOnly(SkillBundleFile{Path: "img.png", SHA256: digest, Size: 9, Omitted: "binary"})) {
		t.Fatal("hash-only companion rejected")
	}
	for _, f := range []SkillBundleFile{
		{Path: "img.png", SHA256: digest, Size: 9, Omitted: "binary", Content: "x"},
		{Path: "img.png", SHA256: digest, Omitted: "binary"},
		{Path: "img.png", SHA256: digest, Size: 9, Omitted: "other"},
		{Path: "img.png", SHA256: "AB" + digest[2:], Size: 9, Omitted: "too_large"},
		{Path: "b.txt", SHA256: ContentHash("b"), Content: "b", Size: 1},
	} {
		if validSkillBundle(hashOnly(f)) {
			t.Fatalf("invalid hash-only entry accepted: %+v", f)
		}
	}
	if validSkillBundle(bundleForTest(SkillBundleFile{Path: "z", Content: "z"}, SkillBundleFile{Path: "a", Content: "a"})) {
		t.Fatal("unsorted manifest accepted")
	}
	for _, mutate := range []func(*SkillBundle){
		func(b *SkillBundle) { b.Files[0].Content = "changed" },
		func(b *SkillBundle) { b.SHA256 = strings.Repeat("0", 64) },
		func(b *SkillBundle) {
			b.Files = append(b.Files, b.Files[0])
			b.SHA256 = skillBundleHash(b.Files)
		},
		func(b *SkillBundle) {
			b.Files[0].Content = strings.Repeat("x", 200001)
			b.Files[0].SHA256 = ContentHash(b.Files[0].Content)
			b.SHA256 = skillBundleHash(b.Files)
		},
	} {
		b := good()
		mutate(b)
		if validSkillBundle(b) {
			t.Fatal("invalid bundle accepted")
		}
	}
}

func TestSkillBundleVersionsResolve(t *testing.T) { checkSkillBundleVersions(t, newFixture(t)) }
func TestPostgresSkillBundle(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	checkSkillBundleVersions(t, f)
}
func TestPostgresSkillBundleCaptureCLI(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	inventory := t.TempDir()
	root := filepath.Join(inventory, "python-bundle")
	content := "---\nname: demo\ndescription: demo\n---\nRead scripts/run.py.\n"
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	raw := "print('한국어')\r\n"
	if err := os.WriteFile(filepath.Join(root, "scripts/run.py"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	// import-skills captures the bundle; this endpoint only records what it would send.
	var captured struct {
		Bundle SkillBundle `json:"bundle"`
	}
	capture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/skills/import" || json.NewDecoder(r.Body).Decode(&captured) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"record":{"id":"captured"},"reused":false}`)
	}))
	defer capture.Close()
	catalog := filepath.Join(inventory, "catalog.jsonl")
	if err := os.WriteFile(catalog, []byte(`{"skill_id":"python-bundle","content_hash":"`+ContentHash(content)+`"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	arkismRun(t, arkismEnv(home, capture.URL, filepath.Join(home, "no-token"), "ARKISM_API_TOKEN=capture"), "", "import-skills", "--root", inventory, "--catalog", catalog, "--execute", "--limit", "1", "--url", capture.URL)
	bundle := captured.Bundle
	in := ImportSkillInput{Scope: scopeA, SkillID: "python-bundle", Path: filepath.Join(root, "SKILL.md"), Content: content, Analysis: detailedSkillAnalysis(), Bundle: &bundle}
	if _, err := f.svc.ImportSkill(testCtx, f.human, in); err != nil {
		t.Fatal("cross-language manifest rejected", err)
	}
	// Local originals may change or disappear; the database must retain exactly the captured bytes.
	if err := os.Remove(filepath.Join(root, "scripts/run.py")); err != nil {
		t.Fatal(err)
	}
	result, err := f.svc.ResolveSkills(testCtx, f.agent, SkillResolveInput{Scope: scopeA, Refs: []string{"python-bundle@latest-approved"}})
	if err != nil || len(result.Items) != 1 || result.Items[0].Bundle == nil || result.Items[0].Bundle.Files[0].Content != raw {
		t.Fatal("captured bytes not preserved", err)
	}

	server := httptest.NewServer(Handler(f.svc))
	defer server.Close()
	tokenFile := filepath.Join(home, "agent.token")
	if err := os.WriteFile(tokenFile, []byte(f.agent), 0600); err != nil {
		t.Fatal(err)
	}
	env := arkismEnv(home, server.URL, tokenFile, "ARKISM_RUNTIME=codex", "ARKISM_SKILL_CACHE="+home)
	var loaded struct {
		Ref    string `json:"ref"`
		Bundle struct {
			Mode string `json:"mode"`
		} `json:"bundle"`
	}
	mcpResult(t, arkismMCP(t, env, mcpCall{"load_skill", map[string]any{"skill_id": "python-bundle"}})[0], &loaded)
	if loaded.Bundle.Mode != "version_pinned" {
		t.Fatalf("bundle mode: %+v", loaded)
	}
	responses := arkismMCP(t, env, mcpCall{"load_skill", map[string]any{"skill_id": "python-bundle"}}, mcpCall{"read_skill_file", map[string]any{"ref": loaded.Ref, "path": "scripts/run.py"}})
	var file struct {
		Content        string `json:"content"`
		ResolutionMode string `json:"resolution_mode"`
	}
	mcpResult(t, responses[1], &file)
	if file.Content != raw || file.ResolutionMode != "hub" {
		t.Fatalf("router HTTP/PG file: %+v", file)
	}
}

func checkSkillBundleVersions(t *testing.T, f *fixture) {
	t.Helper()
	in := ImportSkillInput{Scope: scopeA, SkillID: "bundle-review", Path: "/skills/bundle-review/SKILL.md", Content: "# Review\nRead scripts/run.py.", Analysis: detailedSkillAnalysis(), Bundle: bundleForTest(SkillBundleFile{Path: "scripts/run.py", Content: "print('first')\n"})}
	v1, e := f.svc.ImportSkill(testCtx, f.human, in)
	if e != nil {
		t.Fatal(e)
	}
	again, e := f.svc.ImportSkill(testCtx, f.human, in)
	if e != nil || !again.Reused || again.Record.ID != v1.Record.ID {
		t.Fatal("idempotency", e)
	}
	in.Bundle = bundleForTest(SkillBundleFile{Path: "scripts/run.py", Content: "print('second')\n"})
	v2, e := f.svc.ImportSkill(testCtx, f.human, in)
	if e != nil || v2.Record.ID == v1.Record.ID || v2.Record.Status != "candidate" || v2.Skill.ActiveVersion != v1.Record.ID {
		t.Fatal("bundle-only change must be a candidate", e)
	}
	got, e := f.svc.ResolveSkills(testCtx, f.agent, SkillResolveInput{Scope: scopeA, Refs: []string{"bundle-review@latest-approved", "bundle-review@2"}})
	if e != nil || len(got.Items) != 2 {
		t.Fatal(e)
	}
	for i, want := range []string{"print('first')\n", "print('second')\n"} {
		if got.Items[i].Bundle == nil || got.Items[i].Bundle.Files[0].Content != want {
			t.Fatal("wrong pinned bytes", i)
		}
	}
	// A secret-looking companion is withheld: pinned by hash, bytes neither stored nor redacted.
	secret := bundleForTest(SkillBundleFile{Path: "scripts/run.py", Content: "password=synthetic-example"})
	in.Bundle = secret
	in.RedactSecrets = true
	v3, e := f.svc.ImportSkill(testCtx, f.human, in)
	if e != nil || v3.Record.SkillBundleSHA256 != secret.SHA256 {
		t.Fatal("secret companion import", e)
	}
	got, e = f.svc.ResolveSkills(testCtx, f.agent, SkillResolveInput{Scope: scopeA, Refs: []string{"bundle-review@3"}})
	if e != nil || got.Items[0].Bundle == nil || got.Items[0].Bundle.Files[0].Omitted != "secret" || got.Items[0].Bundle.Files[0].Content != "" || got.Items[0].Bundle.SHA256 != secret.SHA256 {
		t.Fatal("secret companion stored or changed", e)
	}
	in.Bundle = bundleForTest(SkillBundleFile{Path: "password=synthetic-example", Content: "x"})
	if _, e = f.svc.ImportSkill(testCtx, f.human, in); e == nil {
		t.Fatal("secret path accepted")
	}
	// Storage corruption must not return any bundle, even when the SKILL.md hash matches.
	source, e := f.svc.Repo.Get(testCtx, "sources", scopeA, v1.Record.Sources[0].SourceID)
	if e != nil {
		t.Fatal(e)
	}
	source.SkillBundle.Files[0].Content = "tampered"
	e = f.svc.Repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		old := source.Revision
		source.Revision++
		return tx.Replace(ctx, "sources", source, old)
	})
	if e != nil {
		t.Fatal(e)
	}
	got, e = f.svc.ResolveSkills(testCtx, f.agent, SkillResolveInput{Scope: scopeA, Refs: []string{"bundle-review@latest-approved"}})
	if e != nil || got.Items[0].Error != "skill_unavailable" || got.Items[0].Bundle != nil {
		t.Fatal("corrupt bundle returned", e)
	}
}
