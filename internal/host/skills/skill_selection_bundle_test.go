package skills

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSelectionBundleChangesWithoutContent(t *testing.T) {
	inline := func(path, text string) any {
		return map[string]any{"path": path, "sha256": digest([]byte(text)), "content": text}
	}
	hashOnly := func(path, text string) any {
		return map[string]any{"path": path, "sha256": digest([]byte(text)), "size": len(text), "omitted": "binary"}
	}
	bundle := func(files ...any) map[string]any {
		if files == nil {
			files = []any{}
		}
		return map[string]any{"sha256": bundleHash(files), "files": files}
	}
	body := "hidden fixture payload"
	old := bundle(inline("changed.md", body), inline("gone.md", body), inline("mode.txt", body))
	newer := bundle(inline("added.md", body), inline("changed.md", "new version"), hashOnly("mode.txt", body))
	r, err := compareSelectionBundles(old, newer, nil)
	if err != nil || r.State != "changed" || len(r.Files) != 4 || r.Counts["added"] != 1 || r.Counts["removed"] != 1 || r.Counts["content_changed"] != 1 || r.Counts["metadata_changed"] != 1 {
		t.Fatalf("diff %#v err=%v", r, err)
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), body) || strings.Contains(string(raw), "new version") {
		t.Fatal("file contents leaked")
	}
	mode := r.Files[3]
	if !mode.AvailabilityChanged || mode.Latest.Omitted != "binary" || !mode.Latest.HashOnly || mode.Latest.Size != len(body) {
		t.Fatal(mode)
	}
	metadataOnly, err := compareSelectionBundles(bundle(inline("mode.txt", body)), bundle(hashOnly("mode.txt", body)), nil)
	if err != nil || metadataOnly.State != "changed" || metadataOnly.Selected.SHA256 != metadataOnly.Latest.SHA256 {
		t.Fatal("hash-only transition with identical manifest hash missed")
	}
	partial, err := compareSelectionBundles(nil, newer, map[string]bool{"added.md": true, "unknown.md": true})
	if err != nil || partial.State != "unknown" || partial.Selected.Known || !partial.Latest.Known || len(partial.Files) != 2 || partial.Counts["added"] != 0 || partial.Files[0].State != "unknown" {
		t.Fatal(partial, err)
	}
	empty, err := compareSelectionBundles(bundle(), bundle(), map[string]bool{"not-listed": true})
	if err != nil || empty.State != "unchanged" || empty.Files[0].State != "not_listed" {
		t.Fatal(empty, err)
	}
	newer["sha256"] = strings.Repeat("0", 64)
	if _, err = compareSelectionBundles(old, newer, nil); err == nil {
		t.Fatal("accepted wrong manifest hash")
	}
	if _, err = selectionFileFilter([]string{"--file", "../secret"}); err == nil {
		t.Fatal("invalid filter accepted")
	}
	same, err := compareSelectionBundles(old, old, map[string]bool{"gone.md": true})
	if err != nil || len(same.Files) != 1 || same.Counts["unchanged"] != 3 || same.Files[0].Path != "gone.md" {
		t.Fatal(same, err)
	}
}
