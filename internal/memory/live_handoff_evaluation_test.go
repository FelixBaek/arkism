package memory

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Explicit opt-in only: invokes two real provider CLIs using existing login
// files. Data, database, repository, and approvals are synthetic test fixtures.
// This is a functional smoke test, not a productivity or cost comparison.
func TestLiveCrossAgentRepositoryHandoff(t *testing.T) {
	if os.Getenv("ARKISM_LIVE_HANDOFF") != "1" {
		t.Skip("real model handoff requires explicit opt-in and isolated database")
	}
	authHome := os.Getenv("ARKISM_LIVE_AUTH_HOME")
	claudeBin, codexBin := os.Getenv("ARKISM_LIVE_CLAUDE_BIN"), os.Getenv("ARKISM_LIVE_CODEX_BIN")
	if !filepath.IsAbs(authHome) || !filepath.IsAbs(claudeBin) || !filepath.IsAbs(codexBin) {
		t.Fatal("explicit absolute login home and CLI paths required")
	}
	f := newFixture(t)
	f.auth.actors[f.agent] = actor("agent-1", roleAgent, true, "read", "write", "transfer")
	f.svc.Repo = isolatedPostgres(t)
	server := httptest.NewServer(Handler(f.svc))
	defer server.Close()
	home, repo := t.TempDir(), t.TempDir()
	for _, relative := range []string{".codex/auth.json", ".claude/.credentials.json"} {
		raw, err := os.ReadFile(filepath.Join(authHome, relative))
		if err != nil {
			t.Fatal("required login file unavailable")
		}
		target := filepath.Join(home, relative)
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(target, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := exec.Command("git", "init", "-q", repo).Run(); err != nil {
		t.Fatal(err)
	}
	fixtureFiles := map[string]string{
		"normalizer.py":      "def normalize(text):\n    return text.strip().lower()\n",
		"test_normalizer.py": "import unittest\nfrom normalizer import normalize\nclass Check(unittest.TestCase):\n    def test_case(self):\n        self.assertEqual(normalize('  MiXeD  '), 'MiXeD')\n    def test_empty(self):\n        self.assertEqual(normalize('  '), '')\n",
	}
	for name, content := range fixtureFiles {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	src := f.create(f.agent, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "Synthetic repository policy: trim surrounding whitespace and preserve letter case."}))
	pkg := workCheckpointPackage()
	pkg.Repository, pkg.SessionID, pkg.Goal = repo, "live-parent", "Repair the normalizer without changing tests"
	pkg.Files, pkg.Checks = nil, nil
	pkg.SourceRefs = []Ref{{Collection: "sources", ID: src.ID, Revision: src.Revision}}
	if _, err := workCheckpointSave(f, f.agent, "live-parent", "initial", 0, pkg); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: "live-review-policy", Path: "/synthetic/SKILL.md", Content: "# Evidence review\nRead the repository's tests. Preserve letter case. Never change tests to match a broken implementation. Cite the source ID in your final summary.", Analysis: detailedSkillAnalysis()})
	if err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(home, "agent.token")
	if err = os.WriteFile(token, []byte(f.agent), 0600); err != nil {
		t.Fatal(err)
	}
	env := arkismEnv(home, server.URL, token, "ARKISM_CLAUDE_BIN="+claudeBin, "ARKISM_CODEX_BIN="+codexBin)
	for _, step := range []struct{ id, parent, sender, recipient, model, access, task string }{
		{"live-review", "live-parent", "codex", "claude", "claude-opus-5-5", "read", "Read normalizer.py and test_normalizer.py. Explain the bug and the minimal fix. Do not edit files. Cite the exact source ID " + src.ID + "."},
		{"live-fix", "live-review", "claude", "codex", "gpt-6-astra", "write", "Fix normalizer.py according to the parent review and the source policy. Do not modify tests. Run python3 -m unittest discover. Cite the exact source ID " + src.ID + "."},
	} {
		input := map[string]any{"id": step.id, "parent_work_id": step.parent, "sender": step.sender, "recipient": step.recipient, "session_id": step.id, "model": step.model, "access": step.access, "task": step.task, "cwd": repo, "timeout_s": 180, "skills": []string{"live-review-policy"}}
		raw, _ := json.Marshal(input)
		out, err := arkismExec(t, env, string(raw), "crew", "handoff")
		if err != nil {
			t.Fatalf("%s transport failed: %v %s", step.id, err, out)
		}
		var result map[string]any
		if json.Unmarshal(out, &result) != nil || result["ok"] != true {
			t.Fatalf("%s did not finish: %s", step.id, out)
		}
		view, err := f.svc.GetWorkCheckpoint(testCtx, f.agent, WorkCheckpointGetInput{Scope: scopeA, ID: step.id})
		hasOriginalSource := false
		for _, ref := range view.Package.SourceRefs {
			if ref.Collection == "sources" && ref.ID == src.ID && ref.Revision == src.Revision {
				hasOriginalSource = true
			}
		}
		if err != nil || !hasOriginalSource || !strings.Contains(strings.Join(view.Package.Completed, "\n"), src.ID) {
			t.Fatalf("%s missing source citation or stored provenance: %v", step.id, err)
		}
		t.Logf("%s: actual model=%s completed, original source ID preserved and cited", step.id, step.model)
	}
	unchangedTests, err := os.ReadFile(filepath.Join(repo, "test_normalizer.py"))
	if err != nil || string(unchangedTests) != fixtureFiles["test_normalizer.py"] {
		t.Fatal("repository test fixture was modified")
	}
	check := exec.Command("python3", "-m", "unittest", "discover")
	check.Dir = repo
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("independent repository tests failed: %v %s", err, out)
	}
	t.Log("independent repository checks: 2 passed; provider charges and human review time not measured")
}
