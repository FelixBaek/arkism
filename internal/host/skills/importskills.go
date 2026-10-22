package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// Import the reviewed skill catalog into Arkism without external model calls.

func init() { host.Register("import-skills", importMain) }

type catalogRow struct{ skillID, path, content, contentHash string }

// catalogSkills walks root like os.walk(followlinks=False) with sorted, non-hidden, non-linked folders.
func catalogSkills(root string) ([]catalogRow, error) {
	root, err := filepath.EvalSymlinks(root)
	if err == nil {
		root, err = filepath.Abs(root)
	}
	if err != nil {
		return nil, err
	}
	var out []catalogRow
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil // os.walk ignores unreadable folders
		}
		var dirs []string
		hasSkill := false
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			st, err := os.Stat(p)
			isDirectory := err == nil && st.IsDir()
			if isDirectory {
				if !isLink(p) && !strings.HasPrefix(e.Name(), ".") {
					dirs = append(dirs, e.Name())
				}
			} else if e.Name() == "SKILL.md" {
				hasSkill = true
			}
		}
		if path := filepath.Join(dir, "SKILL.md"); hasSkill && !isLink(path) {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !utf8Valid(raw) {
				return errors.New("'utf-8' codec can't decode SKILL.md: " + path)
			}
			if len(raw) > 64000 {
				return errors.New("skill exceeds import size limit")
			}
			rel, _ := filepath.Rel(root, dir)
			out = append(out, catalogRow{filepath.ToSlash(rel), path, string(raw), digest(raw)})
		}
		sort.Strings(dirs)
		for _, d := range dirs {
			if err := walk(filepath.Join(dir, d)); err != nil {
				return err
			}
		}
		return nil
	}
	return out, walk(root)
}

func importAnalyses(path string) (map[string]host.Obj, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	result := map[string]host.Obj{}
	lines := strings.SplitAfter(string(raw), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for n, line := range lines {
		v, err := loadsOrdered(line)
		if err != nil {
			return nil, err
		}
		row, _ := v.(host.Obj)
		id := pyStr(objGet(row, "skill_id"))
		if !truthy(objGet(row, "skill_id")) || result[id] != nil {
			return nil, fmt.Errorf("invalid or duplicate skill_id at catalog line %d", n+1)
		}
		result[id] = row
	}
	return result, nil
}

// importClient never follows a redirect (a 3xx is a failure); proxies come from the environment.
var importClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func importURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return u.Scheme == "https" || u.Scheme == "http" && containsStr([]string{"127.0.0.1", "localhost", "::1"}, h)
}

func importOne(endpoint, token string, payload host.Obj) (map[string]any, error) {
	body, err := host.Dumps(payload, host.DumpOpts{})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/v1/skills/import", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := importClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode/100 != 2 {
		return nil, errors.New("import failed")
	}
	v, err := host.Loads(string(raw))
	data, ok := v.(map[string]any)
	record, hasRecord := asMap(data["record"])["id"]
	_, hasReused := data["reused"]
	if err != nil || !ok || !hasReused || !hasRecord {
		return nil, errors.New("invalid response")
	}
	return map[string]any{"reused": data["reused"], "record_id": record}, nil
}

// importMainCheck checks the option combination and returns the API token.
func importMainCheck(p *argParser, offset, limit int, hasLimit, execute bool, endpoint string) (string, bool) {
	if offset < 0 || hasLimit && (limit < 1 || limit > 500) {
		return "", p.fail("offset must be nonnegative and limit must be 1..500")
	}
	token := os.Getenv("ARKISM_API_TOKEN")
	if token == "" {
		token = os.Getenv("MEMORY_API_TOKEN")
	}
	if execute && (token == "" || !hasLimit) {
		return "", p.fail("--execute requires ARKISM_API_TOKEN and an explicit --limit")
	}
	if execute && !importURLAllowed(endpoint) {
		return "", p.fail("HTTPS or loopback HTTP required, without URL credentials")
	}
	return token, true
}

func importMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	p := newArgs("import-skills", stderr)
	root := p.str("--root", host.SkillsRoot)
	catalogPath := p.str("--catalog", filepath.Join(host.DataDir, "skill-catalog.jsonl"))
	execute := p.flag("--execute")
	limit, offset := p.int("--limit", 0), p.int("--offset", 0)
	endpoint := p.str("--url", "http://127.0.0.1:8091")
	workspace, project := p.str("--workspace", "default"), p.str("--project", "default")
	user, agent := p.str("--user", "management:owner"), p.str("--agent", "")
	if !p.parse(args, 0, 0) {
		return 2
	}
	hasLimit := p.seen["--limit"]
	token, ok := importMainCheck(p, *offset, *limit, hasLimit, *execute, *endpoint)
	if !ok {
		return 2
	}
	exit := func(msg string) int { fmt.Fprintln(stderr, msg); return 1 }
	catalog, err := importAnalyses(*catalogPath)
	if err != nil {
		return exit(err.Error())
	}
	rows, err := catalogSkills(*root)
	if err != nil {
		return exit(err.Error())
	}
	same := len(catalog) == len(rows)
	for _, r := range rows {
		same = same && catalog[r.skillID] != nil
	}
	if !same {
		return exit("Skill catalog does not exactly match canonical SKILL.md files")
	}
	start, end := min(*offset, len(rows)), len(rows)
	if hasLimit {
		end = min(start+*limit, len(rows))
	}
	scopeObj := host.Obj{{Key: "workspace_id", Value: *workspace}, {Key: "project_id", Value: *project}, {Key: "user_id", Value: *user}, {Key: "agent_id", Value: *agent}}
	for _, row := range rows[start:end] {
		analysis := catalog[row.skillID]
		if objGet(analysis, "content_hash") != row.contentHash {
			return exit("Catalog hash mismatch: " + row.skillID)
		}
		result := host.Obj{{Key: "skill_id", Value: row.skillID}, {Key: "content_hash", Value: row.contentHash}, {Key: "dry_run", Value: !*execute}}
		if *execute {
			bundle, err := captureBundle(row.path, *root, row.contentHash)
			if err != nil {
				return exit(err.Error())
			}
			data, err := importOne(*endpoint, token, host.Obj{{Key: "bundle", Value: bundle}, {Key: "scope", Value: scopeObj}, {Key: "skill_id", Value: row.skillID},
				{Key: "path", Value: row.path}, {Key: "content", Value: row.content}, {Key: "analysis", Value: analysis}, {Key: "redact_secrets", Value: true}})
			if err != nil {
				return exit("Skill import failed; response and credentials suppressed")
			}
			result = append(result, host.KV{Key: "reused", Value: data["reused"]}, host.KV{Key: "record_id", Value: data["record_id"]})
		}
		fmt.Fprintln(stdout, host.MustDumps(result, host.DumpOpts{NoASCII: true}))
	}
	return 0
}
