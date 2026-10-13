//go:build unix

package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestSyncDetectsBundleOnlyChangeAndDoesNotRepeatCandidate(t *testing.T) {
	f := newBundleFixture(t)
	d := swapSync(t)
	sid := "pack/skills/demo"
	first, err := f.capture(t)
	if err != nil {
		t.Fatal(err)
	}
	active := map[string]any{"ref": sid + "@latest-approved", "resolved_version": 1, "content_hash": f.hash, "bundle": first}
	token := filepath.Join(d, "token")
	os.WriteFile(token, []byte("synthetic-local-token"), 0o600)
	os.WriteFile(syncCatalog, []byte(host.MustDumps(map[string]any{"skill_id": sid, "content_hash": f.hash, "name": "demo"}, host.DumpOpts{})+"\n"), 0o644)
	var imports []map[string]any
	syncPost = func(url, token, path string, payload any) (map[string]any, error) {
		if strings.HasSuffix(path, "resolve") {
			return map[string]any{"items": []any{active}}, nil
		}
		imports = append(imports, payload.(map[string]any))
		return map[string]any{"reused": false}, nil
	}
	syncRoots = func() []skillRoot { return []skillRoot{{"agent-skills", f.root, "generic"}} }
	run := func() host.Obj {
		t.Helper()
		var out, errOut strings.Builder
		if code := syncMain([]string{"--execute", "--token-file", token}, nil, &out, &errOut); code != 0 {
			t.Fatal(code, out.String(), errOut.String())
		}
		v, _ := loadsOrdered(out.String())
		return v.(host.Obj)
	}
	if n := objGet(run(), "up_to_date"); canon(t, n) != "1" {
		t.Fatal(n)
	}
	os.WriteFile(f.script, []byte("print('second')"), 0o644)
	if n := objGet(run(), "changed_candidates"); canon(t, n) != "1" || len(imports) != 1 {
		t.Fatal(n, len(imports))
	}
	if imports[0]["bundle"].(map[string]any)["sha256"] == first["sha256"] {
		t.Fatal("bundle change not imported")
	}
	if n := objGet(run(), "changed_candidates"); canon(t, n) != "0" || len(imports) != 1 {
		t.Fatal("candidate repeated")
	}
	// A legacy body-only state must not suppress migration to pinned companions.
	os.WriteFile(syncState, []byte(host.MustDumps(map[string]any{sid: f.hash}, host.DumpOpts{})), 0o644)
	if n := objGet(run(), "changed_candidates"); canon(t, n) != "1" {
		t.Fatal(n)
	}
}

func TestOldVersionComparisonIncludesBundle(t *testing.T) {
	f := newBundleFixture(t)
	swapSync(t)
	first, _ := f.capture(t)
	previous := map[string]any{"content_hash": f.hash, "bundle": first}
	os.WriteFile(f.script, []byte("new"), 0o644)
	syncPost = func(string, string, string, any) (map[string]any, error) {
		return map[string]any{"items": []any{previous}}, nil
	}
	now, _ := f.capture(t)
	current := map[string]any{"resolved_version": 2}
	if rowMatchesOldVersion("url", "token", map[string]any{}, "demo", f.hash, current, now) {
		t.Fatal("changed bundle matched")
	}
	if !rowMatchesOldVersion("url", "token", map[string]any{}, "demo", f.hash, current, first) {
		t.Fatal("old bundle not matched")
	}
}
