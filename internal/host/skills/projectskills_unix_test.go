//go:build unix

package skills

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

const projBody = "---\nname: demo\ndescription: demo\n---\nRun scripts/run.sh.\n"

type projFixture struct {
	root, source, src, dst string
	item                   map[string]any
}

func newProjFixture(t *testing.T) *projFixture {
	t.Helper()
	root := t.TempDir()
	f := &projFixture{root: root, source: filepath.Join(root, "src"), dst: filepath.Join(root, "runtime", "demo")}
	f.src = filepath.Join(f.source, "demo")
	write(t, filepath.Join(f.src, "SKILL.md"), projBody)
	write(t, filepath.Join(f.src, "scripts/run.sh"), "#!/bin/sh\necho pinned\n")
	write(t, filepath.Join(f.src, "scripts/logo.png"), "\x89PNG\x00pinned")
	f.pin(t, 1)
	src, res := projectSource, reviewResolve
	projectSource = f.source
	reviewResolve = func(string) (map[string]any, error) { return maps.Clone(f.item), nil }
	t.Cleanup(func() { projectSource, reviewResolve = src, res })
	return f
}

func (f *projFixture) pin(t *testing.T, version int) {
	t.Helper()
	raw, _ := os.ReadFile(filepath.Join(f.src, "SKILL.md"))
	bundle, err := captureBundle(filepath.Join(f.src, "SKILL.md"), f.source, digest(raw))
	if err != nil {
		t.Fatal(err)
	}
	f.item = map[string]any{"skill_id": "demo", "resolved_version": version, "version_id": fmt.Sprintf("skill-%064x", version),
		"content_hash": digest([]byte(projBody)), "instructions": projBody, "validation_status": "approved", "bundle": bundle}
}

func (f *projFixture) run(t *testing.T, check bool) (string, error) {
	t.Helper()
	return projectOne(filepath.Dir(f.dst), "demo", "demo", check, nil, true)
}

func (f *projFixture) mustRun(t *testing.T, check bool) string {
	t.Helper()
	msg, err := f.run(t, check)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func readText(t *testing.T, p string) string {
	t.Helper()
	raw, _ := os.ReadFile(p)
	return string(raw)
}

func TestDeploysServerPinNotLocalFolder(t *testing.T) {
	f := newProjFixture(t)
	if msg := f.mustRun(t, false); !strings.Contains(msg, "배포 demo@1") {
		t.Fatal(msg)
	}
	// Local edits after the pin never reach the runtime folder.
	write(t, filepath.Join(f.src, "scripts/run.sh"), "#!/bin/sh\necho local-unapproved\n")
	write(t, filepath.Join(f.src, "SKILL.md"), projBody+"local edit\n")
	write(t, filepath.Join(f.src, "new.md"), "not approved")
	if msg := f.mustRun(t, false); msg != "" {
		t.Fatal(msg)
	}
	if readText(t, filepath.Join(f.dst, "SKILL.md")) != projBody || readText(t, filepath.Join(f.dst, "scripts/run.sh")) != "#!/bin/sh\necho pinned\n" {
		t.Fatal("local edit deployed")
	}
	if st, _ := os.Stat(filepath.Join(f.dst, "scripts/run.sh")); st.Mode()&0o100 == 0 {
		t.Fatal("exec bit lost")
	}
	if readText(t, filepath.Join(f.dst, "scripts/logo.png")) != "\x89PNG\x00pinned" || exists(filepath.Join(f.dst, "new.md")) {
		t.Fatal("hash-only or unpinned file wrong")
	}
	meta, _ := readSidecar(f.dst)
	if meta["ref"] != "demo@1" || meta["bundle_sha256"] != asMap(f.item["bundle"])["sha256"] {
		t.Fatal(meta)
	}
}

func TestHashOnlyMismatchKeepsPreviousCopy(t *testing.T) {
	f := newProjFixture(t)
	f.mustRun(t, false)
	before, _ := treeHash(f.dst)
	f.item["resolved_version"], f.item["version_id"] = 2, "skill-"+strings.Repeat("2", 64)
	write(t, filepath.Join(f.src, "scripts/logo.png"), "\x89PNG\x00changed")
	if _, err := f.run(t, false); !hubError(err) {
		t.Fatal(err)
	}
	after, _ := treeHash(f.dst)
	meta, _ := readSidecar(f.dst)
	entries, _ := os.ReadDir(filepath.Dir(f.dst))
	if after != before || meta["ref"] != "demo@1" || len(entries) != 1 {
		t.Fatal(after, meta, entries)
	}
}

func TestUserEditsAndForeignFoldersArePreserved(t *testing.T) {
	f := newProjFixture(t)
	f.mustRun(t, false)
	write(t, filepath.Join(f.dst, "scripts/run.sh"), "#!/bin/sh\necho mine\n")
	f.item["resolved_version"], f.item["version_id"] = 2, "skill-"+strings.Repeat("2", 64)
	if msg := f.mustRun(t, false); !strings.Contains(msg, "직접 수정됨") || readText(t, filepath.Join(f.dst, "scripts/run.sh")) != "#!/bin/sh\necho mine\n" {
		t.Fatal(msg)
	}
	other := filepath.Join(f.root, "runtime2", "demo")
	write(t, filepath.Join(other, "SKILL.md"), "user owned")
	if msg, err := projectOne(filepath.Dir(other), "demo", "demo", false, nil, true); err != nil || !strings.Contains(msg, "관리 표시") {
		t.Fatal(msg, err)
	}
	if readText(t, filepath.Join(other, "SKILL.md")) != "user owned" {
		t.Fatal("foreign folder overwritten")
	}
}

func TestUnpinnedOrPluginVersionsAreNotDeployed(t *testing.T) {
	f := newProjFixture(t)
	delete(f.item, "bundle")
	if msg := f.mustRun(t, false); !strings.Contains(msg, "고정된 보조 파일이 없어") || exists(f.dst) {
		t.Fatal(msg)
	}
	f.pin(t, 1)
	os.Mkdir(filepath.Join(f.source, ".claude-plugin"), 0o755)
	if msg := f.mustRun(t, false); !strings.Contains(msg, "플러그인 스킬") || exists(f.dst) {
		t.Fatal(msg)
	}
}

func TestRefusedSkillRemovesOnlyUneditedCopy(t *testing.T) {
	f := newProjFixture(t)
	f.mustRun(t, false)
	ok := reviewResolve
	refuse := func(code, detail string) {
		reviewResolve = func(string) (map[string]any, error) { return nil, hubErr(code, detail) }
	}
	for _, code := range []string{"skill_unavailable", "skill_not_found"} {
		refuse(code, "demo")
		if msg := f.mustRun(t, true); !strings.Contains(msg, "삭제 예정") || !exists(f.dst) {
			t.Fatal(msg)
		}
		if msg := f.mustRun(t, false); !strings.Contains(msg, "배포 사본 삭제") || exists(f.dst) {
			t.Fatal(msg)
		}
		reviewResolve = ok
		f.mustRun(t, false)
	}
	write(t, filepath.Join(f.dst, "SKILL.md"), "mine")
	refuse("skill_not_found", "")
	if msg := f.mustRun(t, false); !strings.Contains(msg, "지우지 않음") || readText(t, filepath.Join(f.dst, "SKILL.md")) != "mine" {
		t.Fatal(msg)
	}
	refuse("hub_error", "401")
	if _, err := f.run(t, false); !hubError(err) {
		t.Fatal(err)
	}
}

func TestAllRefusedRunRemovesNothing(t *testing.T) {
	f := newProjFixture(t)
	f.mustRun(t, false)
	write(t, filepath.Join(f.source, "projection.json"), host.MustDumps(map[string]any{"targets": map[string]any{filepath.Dir(f.dst): map[string]any{"demo": "demo"}}}, host.DumpOpts{}))
	reviewResolve = func(string) (map[string]any, error) { return nil, hubErr("skill_not_found", "") }
	var out, errOut strings.Builder
	if code := projectMain(nil, nil, &out, &errOut); code != 0 || !exists(f.dst) || !strings.Contains(out.String(), "설정 오류") {
		t.Fatal(code, out.String(), errOut.String())
	}
}

func TestProjectCheckDoesNotWrite(t *testing.T) {
	f := newProjFixture(t)
	if msg := f.mustRun(t, true); !strings.Contains(msg, "배포 예정") || exists(f.dst) {
		t.Fatal(msg)
	}
}
