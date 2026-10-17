package capture

import (
	"os"
	"path/filepath"
	"testing"
)

// ST-818: the hook index marks a memory whose file is gone from the working folder; it still shows it.
func TestIndexLineMarksMissingPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "here.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	gone := indexLine(map[string]any{"id": "m1", "kind": "lesson", "text": "docs/gone.md 를 먼저 본다."}, dir)
	if gone != "- [lesson, 확인 필요: 가리키는 파일이 지금 없음] docs/gone.md 를 먼저 본다. (id: m1)" {
		t.Fatal(gone)
	}
	if here := indexLine(map[string]any{"id": "m2", "kind": "lesson", "text": "./here.md 를 본다."}, dir); here != "- [lesson] ./here.md 를 본다." {
		t.Fatal(here)
	}
}
