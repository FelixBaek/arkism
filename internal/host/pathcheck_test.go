package host

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestMissingPaths(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "pkg")
	for _, d := range []string{filepath.Join(root, ".git"), filepath.Join(root, "internal", "a"), sub} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(root, "internal", "a", "x.go"), filepath.Join(sub, "local.txt")} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	abs := filepath.Join(root, "internal", "a", "x.go")
	text := "internal/a/x.go:12 은 있고 internal/a/y.go 는 없다. ./local.txt 와 ../pkg/local.txt 는 cwd 기준. " +
		abs + " 그대로, /nope-st818/z.md 없음. 비율 1/2.5 와 https://example.com/p/q.html 은 경로가 아니다. internal/a/y.go 두 번."
	got := MissingPaths(sub, text, "original")
	if want := []string{"internal/a/y.go", "/nope-st818/z.md"}; !slices.Equal(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
	if got := MissingPaths(sub, "경로 없음"); got != nil {
		t.Fatal(got)
	}
	many := ""
	for i := range 12 {
		many += " gone/f" + string(rune('a'+i)) + ".go"
	}
	if got := MissingPaths(sub, many); len(got) != maxCheckedPaths {
		t.Fatal(len(got))
	}
}
