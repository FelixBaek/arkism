package capture

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvedRejectsInvalidNamesAndPreservesMissingTail(t *testing.T) {
	root := t.TempDir()
	if !resolved(root) || !resolved(filepath.Join(root, "missing", "child")) {
		t.Fatal("normal or missing-tail path rejected")
	}
	if resolved(filepath.Join(root, "invalid\x00name")) {
		t.Fatal("non-existence error was treated as a missing tail")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal(err)
	}
}
