//go:build unix

package crew

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// fakeCodeGraphServer keeps file hashes and a revision like the API does and
// records every batch.
type fakeCodeGraphServer struct {
	mu       sync.Mutex
	revision int64
	files    map[string]string
	batches  []map[string]any
	finished map[string]any
}

func (f *fakeCodeGraphServer) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/code_graph/files", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Query().Get("repo") != "demo" || r.URL.Query().Get("project_id") == "" {
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"repo": "demo", "revision": f.revision, "files": f.files})
	})
	mux.HandleFunc("POST /v1/code_graph/batch", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		if int64(b["expected_revision"].(float64)) != f.revision {
			w.WriteHeader(409)
			return
		}
		f.batches = append(f.batches, b)
		for _, p := range b["remove"].([]any) {
			delete(f.files, p.(string))
		}
		for _, file := range b["files"].([]any) {
			m := file.(map[string]any)
			f.files[m["path"].(string)] = m["hash"].(string)
		}
		f.revision++
		_ = json.NewEncoder(w).Encode(map[string]any{"repo": map[string]any{"repo": "demo", "revision": f.revision}, "redacted": 0})
	})
	mux.HandleFunc("POST /v1/code_graph/finish", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewDecoder(r.Body).Decode(&f.finished)
		f.revision++
		_ = json.NewEncoder(w).Encode(map[string]any{"repo": "demo", "revision": f.revision, "commit": f.finished["commit"], "files": len(f.files)})
	})
	return mux
}

func TestCodegraphIndexMirrorUploadsIncrementally(t *testing.T) {
	t.Setenv("PATH", "/usr/local/bin:/usr/bin:/bin")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	upstream, work := filepath.Join(root, "remote", "ORG", "demo.git"), filepath.Join(root, "work")
	sh(t, "", "git", "init", "--bare", "-q", "-b", "main", upstream)
	sh(t, "", "git", "clone", "-q", upstream, work)
	g := func(args ...string) string {
		return sh(t, work, append([]string{"git", "-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	}
	write := func(name, text string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(work, name)), 0o755)
		_ = os.WriteFile(filepath.Join(work, name), []byte(text), 0o644)
	}
	write("ui/a.js", "import { b } from './b.js';\nexport function a() { return b(); }\n")
	write("ui/b.js", "export function b() { return 1; }\n")
	g("add", ".")
	g("commit", "-qm", "first")
	g("push", "-q", "origin", "main")
	patch(t, &gitState, filepath.Join(root, "state"))
	patch(t, &gitRemote, filepath.Join(root, "remote"))
	patch(t, &gitOrg, "ORG")
	patch(t, &reposFn, func() ([]any, error) {
		return []any{map[string]any{"name": "demo", "default_branch": "main"}}, nil
	})
	server := &fakeCodeGraphServer{files: map[string]string{}}
	api := httptest.NewServer(server.handler(t))
	defer api.Close()
	tokenFile := filepath.Join(root, "token")
	_ = os.WriteFile(tokenFile, []byte("tok\n"), 0o600)
	patch(t, &host.BaseURL, api.URL)
	patch(t, &host.TokenFile, tokenFile)

	var out, errOut strings.Builder
	if code := codegraphMain([]string{"index", "--mirror", "demo"}, nil, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var sum cgSummary
	if err := json.Unmarshal([]byte(out.String()), &sum); err != nil {
		t.Fatal(err, out.String())
	}
	head := g("rev-parse", "HEAD")
	if sum.Repo != "demo" || sum.Commit != head || sum.Stats.Mode != "full" || sum.Batches != 1 || len(server.files) != 2 || server.finished["commit"] != head {
		t.Fatalf("first run %+v %+v", sum, server.finished)
	}
	if entries, _ := filepath.Glob(filepath.Join(os.TempDir(), "arkism-codegraph-*")); len(entries) > 0 {
		for _, e := range entries {
			if strings.Contains(sh(t, "", "git", "--git-dir="+filepath.Join(root, "state", "demo.git"), "worktree", "list"), e) {
				t.Fatalf("worktree left behind: %s", e)
			}
		}
	}
	// one file changes, one is deleted: one batch with the changed file and the removal
	write("ui/a.js", "export function a() { return 2; }\n")
	g("rm", "-q", "ui/b.js")
	g("commit", "-qam", "second")
	g("push", "-q", "origin", "main")
	_ = os.Remove(filepath.Join(root, "state", "demo.git", "arkism-fetched")) // fetch now, not in 60 s
	out.Reset()
	if code := codegraphMain([]string{"index", "--mirror", "demo"}, nil, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	last := server.batches[len(server.batches)-1]
	files := last["files"].([]any)
	if len(server.batches) != 2 || len(files) != 1 || files[0].(map[string]any)["path"] != "ui/a.js" || len(last["remove"].([]any)) != 1 || len(server.files) != 1 {
		t.Fatalf("incremental batch %+v files %v", last, server.files)
	}
	// nothing changed: no batch, finish still records the commit
	out.Reset()
	if code := codegraphMain([]string{"index", "--mirror", "demo"}, nil, &out, &errOut); code != 0 || len(server.batches) != 2 {
		t.Fatalf("unchanged run %d %d", code, len(server.batches))
	}
	if codegraphMain([]string{"index"}, nil, &out, &errOut) != 2 {
		t.Fatal("index without a path or mirror")
	}
}
