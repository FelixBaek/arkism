package crew

// arkism codegraph: index a source tree on this host and upload the symbol and
// call graph to /v1/code_graph/*. Code never leaves the host; only symbols,
// signatures, first doc lines and edges do (the server redacts secret-looking
// text). A server-side repository is read from its Git-screen bare mirror
// through a temporary detached worktree that is removed afterwards.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/codegraph"
	"github.com/FelixBaek/arkism/internal/host"
)

func init() { host.Register("codegraph", codegraphMain) }

const codegraphUsage = `usage:
  arkism codegraph index <repo-path> [--repo NAME] [--only DIR,...] [--full] [--tests] [--dry-run]
  arkism codegraph index --mirror NAME [--ref BRANCH] [--repo NAME] [--full] [--tests]
  arkism codegraph repos`

// Batch size targets; the server accepts up to 2000 files, 20000 symbols and 60000 edges.
var (
	cgBatchFiles   = 400
	cgBatchSymbols = 4000
	cgBatchEdges   = 12000
)

var cgClient = &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 120 * time.Second}

func cgCall(method, path string, body any, out any) error {
	raw, err := os.ReadFile(host.TokenFile)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, host.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(raw)))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cgClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct{ Code string }
		_ = json.Unmarshal(data, &e)
		return fmt.Errorf("%s %s: HTTP %d %s", method, path, resp.StatusCode, e.Code)
	}
	return json.Unmarshal(data, out)
}

func cgScope() map[string]any {
	s := host.Scope()
	return map[string]any{"workspace_id": s["workspace_id"], "project_id": s["project_id"]}
}

func cgScopeQuery() string {
	s := cgScope()
	return "workspace_id=" + url.QueryEscape(fmt.Sprint(s["workspace_id"])) + "&project_id=" + url.QueryEscape(fmt.Sprint(s["project_id"]))
}

type cgRepo struct {
	Repo     string `json:"repo"`
	Commit   string `json:"commit"`
	Revision int64  `json:"revision"`
	Files    int    `json:"files"`
	Symbols  int    `json:"symbols"`
	Edges    int    `json:"edges"`
}

func codegraphMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, codegraphUsage)
		return 2
	}
	switch args[0] {
	case "repos":
		var out struct{ Repos []cgRepo }
		if err := cgCall(http.MethodGet, "/v1/code_graph/repos?"+cgScopeQuery(), nil, &out); err != nil {
			fmt.Fprintln(stderr, "codegraph:", err)
			return 1
		}
		for _, r := range out.Repos {
			fmt.Fprintf(stdout, "%s\t%s\tfiles=%d symbols=%d edges=%d rev=%d\n", r.Repo, r.Commit, r.Files, r.Symbols, r.Edges, r.Revision)
		}
		return 0
	case "index":
		return codegraphIndex(args[1:], stdout, stderr)
	}
	fmt.Fprintln(stderr, codegraphUsage)
	return 2
}

// interleaved flags and one positional path: index ./repo --repo x, index --repo x ./repo
func splitArgs(args []string, valued map[string]bool) ([]string, []string) {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if valued[name] && !strings.Contains(a, "=") && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return flags, pos
}

func codegraphIndex(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("codegraph index", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "repository name on the server (default: the directory or mirror name)")
	mirrorName := fs.String("mirror", "", "index the server's bare mirror of this repository (Git screen owner)")
	ref := fs.String("ref", "", "branch of the mirror (default: its default branch)")
	full := fs.Bool("full", false, "re-read every file instead of only changed ones")
	tests := fs.Bool("tests", false, "include Go _test.go files")
	dry := fs.Bool("dry-run", false, "index and print statistics, upload nothing")
	only := fs.String("only", "", "comma-separated directory prefixes to index (others on the server stay as they are)")
	flags, pos := splitArgs(args, map[string]bool{"repo": true, "mirror": true, "ref": true, "only": true})
	if err := fs.Parse(flags); err != nil || len(pos) > 1 || (len(pos) == 0) == (*mirrorName == "") {
		fmt.Fprintln(stderr, codegraphUsage)
		return 2
	}
	root, commit, name := "", "", *repo
	if *mirrorName != "" {
		dir, sha, cleanup, err := mirrorCheckout(*mirrorName, *ref)
		if err != nil {
			fmt.Fprintln(stderr, "codegraph: mirror:", err)
			return 1
		}
		defer cleanup()
		root, commit = dir, sha
		if name == "" {
			name = *mirrorName
		}
	} else {
		abs, err := filepath.Abs(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "codegraph:", err)
			return 1
		}
		root = abs
		if out, _, err := gitRun([]string{"git", "-C", root, "rev-parse", "HEAD"}, "", 30*time.Second, false, nil); err == nil && fullSHA.MatchString(strings.TrimSpace(out)) {
			commit = strings.TrimSpace(out)
		}
		if name == "" {
			name = filepath.Base(root)
			if top, _, err := gitRun([]string{"git", "-C", root, "rev-parse", "--show-toplevel"}, "", 30*time.Second, false, nil); err == nil && strings.TrimSpace(top) != "" {
				name = filepath.Base(strings.TrimSpace(top))
			}
		}
	}
	var prefixes []string
	for _, p := range strings.Split(*only, ",") {
		if p = strings.Trim(strings.TrimSpace(p), "/"); p != "" {
			prefixes = append(prefixes, p)
		}
	}
	summary, err := runCodegraphIndex(context.Background(), cgRun{root: root, repo: name, commit: commit, full: *full, tests: *tests, dry: *dry, only: prefixes})
	if err != nil {
		fmt.Fprintln(stderr, "codegraph:", err)
		return 1
	}
	b, _ := json.MarshalIndent(summary, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return 0
}

type cgSummary struct {
	Repo      string          `json:"repo"`
	Commit    string          `json:"commit"`
	Stats     codegraph.Stats `json:"stats"`
	Batches   int             `json:"batches"`
	Redacted  int             `json:"redacted"`
	UploadMS  int64           `json:"upload_ms"`
	Revision  int64           `json:"revision"`
	Server    *cgRepo         `json:"server,omitempty"`
	Warnings  []string        `json:"warnings,omitempty"`
	DryRun    bool            `json:"dry_run,omitempty"`
	Unchanged bool            `json:"unchanged,omitempty"`
}

type cgBatch struct {
	Scope            map[string]any      `json:"scope"`
	Repo             string              `json:"repo"`
	ExpectedRevision int64               `json:"expected_revision"`
	Files            []map[string]string `json:"files"`
	Symbols          []codegraph.Symbol  `json:"symbols"`
	Edges            []codegraph.Edge    `json:"edges"`
	Remove           []string            `json:"remove"`
}

// runCodegraphIndex is the whole run: read the server's file hashes, index
// what changed, upload per-file batches with the revision each answer
// returns, then record the commit.
type cgRun struct {
	root, repo, commit string
	full, tests, dry   bool
	only               []string
}

func runCodegraphIndex(ctx context.Context, run cgRun) (cgSummary, error) {
	root, repo, commit, full, tests, dry := run.root, run.repo, run.commit, run.full, run.tests, run.dry
	sum := cgSummary{Repo: repo, Commit: commit, DryRun: dry}
	var known struct {
		Revision int64             `json:"revision"`
		Files    map[string]string `json:"files"`
	}
	if !dry {
		if err := cgCall(http.MethodGet, "/v1/code_graph/files?"+cgScopeQuery()+"&repo="+url.QueryEscape(repo), nil, &known); err != nil {
			return sum, err
		}
	}
	opt := codegraph.Options{Root: root, Tests: tests, Known: known.Files, Only: run.only}
	if full || dry || len(known.Files) == 0 {
		opt.Known = nil
	}
	res, err := codegraph.Index(ctx, opt)
	if err != nil {
		return sum, err
	}
	sum.Stats, sum.Warnings, sum.Revision = res.Stats, res.Warnings, known.Revision
	if dry {
		return sum, nil
	}
	// A full run drops what the server has but the tree no longer does.
	removed := res.Removed
	if opt.Known == nil {
		local := map[string]bool{}
		for _, f := range res.Files {
			local[f.Path] = true
		}
		for p := range known.Files {
			if !local[p] && (opt.Only == nil || inPrefixes(p, opt.Only)) {
				removed = append(removed, p)
			}
		}
		sort.Strings(removed)
	}
	started := time.Now()
	rev := known.Revision
	send := func(b *cgBatch) error {
		if len(b.Files)+len(b.Remove) == 0 {
			return nil
		}
		b.Scope, b.Repo, b.ExpectedRevision = cgScope(), repo, rev
		var out struct {
			Repo     cgRepo `json:"repo"`
			Redacted int    `json:"redacted"`
		}
		if err := cgCall(http.MethodPost, "/v1/code_graph/batch", b, &out); err != nil {
			return err
		}
		rev = out.Repo.Revision
		sum.Batches++
		sum.Redacted += out.Redacted
		return nil
	}
	newBatch := func() *cgBatch {
		return &cgBatch{Files: []map[string]string{}, Symbols: []codegraph.Symbol{}, Edges: []codegraph.Edge{}, Remove: []string{}}
	}
	b := newBatch()
	for len(removed) > 0 {
		n := min(len(removed), 1000)
		b.Remove = append(b.Remove, removed[:n]...)
		removed = removed[n:]
		if len(removed) > 0 {
			if err := send(b); err != nil {
				return sum, err
			}
			b = newBatch()
		}
	}
	for _, f := range res.Files {
		if len(b.Files) > 0 && (len(b.Files)+1 > cgBatchFiles || len(b.Symbols)+len(f.Symbols) > cgBatchSymbols || len(b.Edges)+len(f.Edges) > cgBatchEdges) {
			if err := send(b); err != nil {
				return sum, err
			}
			b = newBatch()
		}
		b.Files = append(b.Files, map[string]string{"path": f.Path, "hash": f.Hash, "lang": f.Lang})
		b.Symbols = append(b.Symbols, f.Symbols...)
		b.Edges = append(b.Edges, f.Edges...)
	}
	if err := send(b); err != nil {
		return sum, err
	}
	if rev == 0 {
		sum.Unchanged = true
		return sum, nil // nothing indexed yet and nothing to upload
	}
	stats, _ := json.Marshal(res.Stats)
	var server cgRepo
	if err := cgCall(http.MethodPost, "/v1/code_graph/finish", map[string]any{"scope": cgScope(), "repo": repo, "expected_revision": rev, "commit": commit, "stats": json.RawMessage(stats)}, &server); err != nil {
		return sum, err
	}
	sum.Unchanged = sum.Batches == 0
	sum.Server, sum.Revision, sum.UploadMS = &server, server.Revision, time.Since(started).Milliseconds()
	return sum, nil
}

func inPrefixes(p string, prefixes []string) bool {
	for _, x := range prefixes {
		if p == x || strings.HasPrefix(p, x+"/") {
			return true
		}
	}
	return false
}

// mirrorCheckout opens a detached worktree of the mirror at ref's head.
func mirrorCheckout(name, ref string) (string, string, func(), error) {
	m, err := newMirror(name, false)
	if err != nil {
		releaseGitLocks()
		return "", "", nil, err
	}
	branch := ref
	if branch == "" {
		branch = sOrDefault(m.info["default_branch"], "main")
	}
	if _, err := branchName(branch); err != nil {
		releaseGitLocks()
		return "", "", nil, err
	}
	sha := m.headOf(branch)
	if sha == "" {
		releaseGitLocks()
		return "", "", nil, errors.New("unknown branch " + branch)
	}
	work, err := os.MkdirTemp("", "arkism-codegraph-")
	if err != nil {
		releaseGitLocks()
		return "", "", nil, err
	}
	gitDir := "--git-dir=" + m.path
	if _, _, err := gitRun([]string{"git", gitDir, "worktree", "add", "--detach", "--quiet", work, sha}, "", 120*time.Second, true, nil); err != nil {
		os.RemoveAll(work)
		releaseGitLocks()
		return "", "", nil, err
	}
	cleanup := func() {
		_, _, _ = gitRun([]string{"git", gitDir, "worktree", "remove", "--force", work}, "", 60*time.Second, false, nil)
		_, _, _ = gitRun([]string{"git", gitDir, "worktree", "prune"}, "", 60*time.Second, false, nil)
		os.RemoveAll(work)
		releaseGitLocks()
	}
	return work, sha, cleanup, nil
}
