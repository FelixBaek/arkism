package capture

import (
	"path/filepath"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// Repair helpers even when this already-installed binary is reconnected. No
// sidecar files ship next to the Go test executable; nothing is registered.
func TestWindowsInstallerIncludesTaskScripts(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "installed profile")
	if err := instFiles(taskScriptSaver(t, folder), folder, host.Executable()); err != nil {
		t.Fatal(err)
	}
	for _, name := range instSupport {
		if !isFile(filepath.Join(folder, name)) {
			t.Fatal("Windows installer omitted embedded task helper", name)
		}
	}
}
