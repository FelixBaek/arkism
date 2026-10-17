package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func taskScriptSaver(t *testing.T, folder string) *instSaver {
	t.Helper()
	backup := filepath.Join(folder, "backups", "task-test")
	if err := os.MkdirAll(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	return &instSaver{backup: backup, manifest: host.Obj{}}
}

func TestInstallerTaskPayloadIncludesEveryDependencyAndBacksUpReplacement(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "profile with [brackets]", ".config", "arkism")
	saver := taskScriptSaver(t, folder)
	if err := instWriteTaskScripts(saver, folder); err != nil {
		t.Fatal(err)
	}
	for _, name := range instSupport {
		raw, err := instTaskPayload.ReadFile("install_tasks/" + name)
		if err != nil || len(raw) == 0 {
			t.Fatal("missing embedded dependency", name, err)
		}
		if got := lfText(t, filepath.Join(folder, name)); got != string(raw) {
			t.Fatal("installed script differs from embedded payload", name)
		}
	}
	if len(instSupport) != 3 || len(saver.manifest) != 3 {
		t.Fatal("incomplete task helper set", instSupport, saver.manifest)
	}
	path := filepath.Join(folder, "install_arkism_work_task.ps1")
	if err := os.WriteFile(path, []byte("previous registration helper"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := taskScriptSaver(t, filepath.Join(folder, "second-install"))
	if err := instWriteTaskScripts(second, folder); err != nil {
		t.Fatal(err)
	}
	backup, ok := second.manifest.Get(path)
	if !ok || lfText(t, backup.(string)) != "previous registration helper" {
		t.Fatal("replacement was not backed up", second.manifest)
	}
}

func TestInstallerTaskPayloadReportsWriteFailures(t *testing.T) {
	root := t.TempDir()
	saver := taskScriptSaver(t, root)
	blocked := filepath.Join(root, "not-a-folder")
	if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instWriteTaskScripts(saver, blocked); err == nil {
		t.Fatal("missing task helpers were silently accepted")
	}
}

func TestUninstallRemovesKnownTaskHelpersButPreservesUnrelatedFiles(t *testing.T) {
	root := instInstallFixture(t)
	folder := filepath.Join(root, ".config", "arkism")
	if err := instWriteTaskScripts(taskScriptSaver(t, folder), folder); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(folder, "my-script.ps1")
	if err := os.WriteFile(unrelated, []byte("keep this"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := instRemoveTasks
	instRemoveTasks = func() []string { return append([]string(nil), instTaskNames...) }
	t.Cleanup(func() { instRemoveTasks = previous })
	result, err := instUninstall(root, filepath.Join(root, "roaming"), filepath.Join(root, "local"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range instSupport {
		if exists(filepath.Join(folder, name)) {
			t.Fatal("registration helper left after uninstall", name)
		}
	}
	if lfText(t, unrelated) != "keep this" {
		t.Fatal("unrelated user script changed")
	}
	backup, _ := result.Get("backup")
	manifest := instLoad(t, filepath.Join(backup.(string), "manifest.json"))
	for _, name := range instSupport {
		saved, ok := manifest[filepath.Join(folder, name)].(string)
		if !ok || !strings.Contains(lfText(t, saved), "Arkism") {
			t.Fatal("removed helper was not backed up", name)
		}
	}
}
