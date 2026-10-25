package skills

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

// Skill hub client: inventory, runtime importers, resolve with a validated
// local cache, runtime projection and usage evidence. Vendor formats live here, never in the core.

const skillsUsage = `Arkism skill hub client: inventory, runtime importers, resolve with a
validated local cache, runtime projection and usage evidence.

  arkism skills inventory [--root LABEL=DIR ...]
  arkism skills resolve REF [REF ...]      # needs ARKISM_URL/ARKISM_API_TOKEN
  arkism skills project REF --runtime codex --target DIR
`

func init() {
	host.Register("skills", skillsMain)
	host.ResolveSkills = func(url, token string, scope map[string]any, cacheDir string, timeout time.Duration,
		refs []string, trialID, runtime string) ([]map[string]any, error) {
		c, err := newHubClient(url, token, scope, cacheDir, timeout)
		if err != nil {
			return nil, err
		}
		return c.resolve(refs, trialID, runtime)
	}
}

func hubErr(code, detail string) error { return &host.SkillHubError{Code: code, Detail: detail} }

func hubCode(err error) string {
	var e *host.SkillHubError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// skillRoot is one inventory root: (label, root, original_runtime).
type skillRoot struct{ label, root, runtime string }

func defaultRoots() []skillRoot {
	h := host.Home
	return []skillRoot{
		{"agent-skills", host.SkillsRoot, "generic"},
		{"codex", filepath.Join(h, ".codex/skills"), "codex"},
		{"codex-plugins", filepath.Join(h, ".codex/plugins/cache"), "codex"},
		{"codex-curated", filepath.Join(h, ".codex/vendor_imports/skills"), "codex"},
		{"claude-code", filepath.Join(h, ".claude/skills"), "claude_code"},
		{"claude-plugins", filepath.Join(h, ".claude/plugins"), "claude_code"},
		{"agents", filepath.Join(h, ".agents/skills"), "generic"},
		{"hermes", filepath.Join(h, ".hermes/skills"), "hermes"},
	}
}

// repoSkillDirs are repo-local skill folders in numbered repositories under the home folder.
var repoSkillDirs = []struct{ rel, runtime string }{
	{".claude/skills", "claude_code"}, {".codex/skills", "codex"}, {".agents/skills", "generic"}, {"skills", "generic"},
}

func repoLocalRoots(base string) []skillRoot {
	entries, _ := os.ReadDir(base)
	var names []string
	for _, e := range entries {
		n := e.Name()
		r := []rune(n)
		digits := len(r) >= 3
		for i := 0; digits && i < 3; i++ {
			digits = r[i] >= '0' && r[i] <= '9'
		}
		if digits && n != filepath.Base(host.SkillsRoot) && isDir(filepath.Join(base, n)) {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	var roots []skillRoot
	for _, n := range names {
		for _, d := range repoSkillDirs {
			if p := filepath.Join(base, n, d.rel); isDir(p) {
				roots = append(roots, skillRoot{"repo:" + n + "/" + d.rel, p, d.runtime})
			}
		}
	}
	return roots
}

var (
	skipDirs       = []string{".git", "node_modules", ".trash", "build", "__pycache__"}
	frontmatterRx  = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---\r?\n`)
	secretRx       = regexp.MustCompile(`(?i)(-----BEGIN [A-Z ]*PRIVATE KEY|\b(?:sk|al)-[a-z0-9_-]{12,}|AIza[0-9A-Za-z_-]{20,}|(?:api[_ -]?key|token|password|secret)["']?\s*[=:]\s*["']?[a-z0-9_./+=-]{16,})`)
	usageStatuses  = []string{"success", "failure", "cancelled", "validation_failed"}
	errorCodeRx    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	vendorNames    = []string{"claude_code", "codex"}
	vendorRx       = map[string]*regexp.Regexp{"claude_code": regexp.MustCompile(`AskUserQuestion|TodoWrite|\bSkill tool\b|CLAUDE\.md|~/\.claude/|\bClaude Code\b`), "codex": regexp.MustCompile(`apply_patch|update_plan|~/\.codex/|\bcodex exec\b`)}
	mcpToolRx      = regexp.MustCompile(`\bmcp__[A-Za-z0-9_-]+`)
	homePathRx     = regexp.MustCompile(`(?m)(?:^|[^\w.~-])/(?:root|home/[\w.-]+)/`)
	runtimeDirs    = map[string]string{"codex": ".codex/skills", "claude_code": ".claude/skills"}
	treeSkip       = []string{"__pycache__", ".git", ".arkism.json", "evals"}
	projectNameBad = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
)

// pyStrip is str.strip() with no arguments (Unicode whitespace).
func pyStrip(s string) string { return strings.TrimFunc(s, isPySpace) }

func isPySpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', '\x1c', '\x1d', '\x1e', '\x1f', '\x85', ' ', ' ', ' ', ' ', ' ', ' ', '　':
		return true
	}
	return r >= ' ' && r <= ' '
}

// frontmatter is the minimal key: value reader (name/description only).
func frontmatter(text string) map[string]string {
	m := frontmatterRx.FindStringSubmatch(text)
	out := map[string]string{}
	if m == nil {
		return out
	}
	for _, line := range splitLines(m[1]) {
		if strings.Contains(line, ":") && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "-") {
			key, value, _ := strings.Cut(line, ":")
			out[pyStrip(key)] = strings.Trim(pyStrip(value), `"'`)
		}
	}
	return out
}

// decodeReplace is bytes.decode("utf-8", "replace").
func decodeReplace(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	var b strings.Builder
	for len(raw) > 0 {
		r, n := utf8.DecodeRune(raw)
		b.WriteRune(r)
		raw = raw[n:]
	}
	return b.String()
}

func isLink(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode()&os.ModeSymlink != 0
}

// discover lists every real (non-symlink) SKILL.md under the roots, with hash and labels.
func discover(roots []skillRoot) []map[string]any {
	var rows []map[string]any
	for _, sr := range roots {
		if !isDir(sr.root) {
			continue
		}
		var walk func(base string)
		walk = func(base string) {
			entries, err := os.ReadDir(base)
			if err != nil {
				return
			}
			var dirs []string
			hasSkill := false
			for _, e := range entries {
				p := filepath.Join(base, e.Name())
				if e.Name() == "SKILL.md" && !isDir(p) {
					hasSkill = true
				}
				if isDir(p) && !containsStr(skipDirs, e.Name()) && !isLink(p) {
					dirs = append(dirs, e.Name())
				}
			}
			slices.Sort(dirs)
			if path := filepath.Join(base, "SKILL.md"); hasSkill && !isLink(path) {
				if raw, err := os.ReadFile(path); err == nil {
					rows = append(rows, skillRow(sr, path, raw))
				}
			}
			for _, d := range dirs {
				walk(filepath.Join(base, d))
			}
		}
		walk(sr.root)
	}
	return rows
}

func skillRow(sr skillRoot, path string, raw []byte) map[string]any {
	text := decodeReplace(raw)
	meta := frontmatter(text)
	parent := filepath.Dir(path)
	rel, _ := filepath.Rel(sr.root, parent)
	name := meta["name"]
	if name == "" {
		name = filepath.Base(parent)
	}
	var vendors []any
	for _, k := range vendorNames {
		if vendorRx[k].MatchString(text) {
			vendors = append(vendors, k)
		}
	}
	tools := map[string]bool{}
	for _, t := range mcpToolRx.FindAllString(text, -1) {
		tools[t] = true
	}
	return map[string]any{
		"source": sr.label, "original_runtime": sr.runtime, "path": path, "inventory_root": sr.root,
		"skill_id": filepath.ToSlash(rel), "name": name, "description": meta["description"],
		"content_hash": digest(raw), "bytes": len(raw),
		"has_frontmatter":     meta["name"] != "" && meta["description"] != "",
		"credential_like":     secretRx.MatchString(text),
		"hardcoded_root_path": homePathRx.MatchString(text),
		"vendor_markers":      orEmpty(vendors),
		"mcp_tools":           stringsAny(sortedKeys(tools)),
		"has_scripts":         isDir(filepath.Join(parent, "scripts")),
	}
}

func orEmpty(l []any) []any {
	if l == nil {
		return []any{}
	}
	return l
}

func stringsAny(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

// classify merges exact duplicates (same hash); the same name with different content is a review item.
func classify(rows []map[string]any) host.Obj {
	byHash, byName := host.Obj{}, host.Obj{}
	for _, r := range rows {
		appendTo(&byHash, str(r["content_hash"]), r)
		appendTo(&byName, str(r["name"]), r)
	}
	exact, collisions := host.Obj{}, host.Obj{}
	for _, kv := range byHash {
		if group := kv.Value.([]map[string]any); len(group) > 1 {
			paths := []any{}
			for _, r := range group {
				paths = append(paths, r["path"])
			}
			exact = append(exact, host.KV{Key: kv.Key, Value: paths})
		}
	}
	for _, kv := range byName {
		hashes := map[string]bool{}
		for _, r := range kv.Value.([]map[string]any) {
			hashes[str(r["content_hash"])] = true
		}
		if len(hashes) > 1 {
			collisions = append(collisions, host.KV{Key: kv.Key, Value: stringsAny(sortedKeys(hashes))})
		}
	}
	return host.Obj{{Key: "files", Value: len(rows)}, {Key: "distinct_contents", Value: len(byHash)},
		{Key: "exact_duplicate_groups", Value: exact}, {Key: "name_collisions", Value: collisions}}
}

func appendTo(o *host.Obj, key string, r map[string]any) {
	v, _ := o.Get(key)
	group, _ := v.([]map[string]any)
	o.Set(key, append(group, r))
}

// importPayload is the shared SKILL.md importer; runtimes differ only in labels.
func importPayload(runtime string, row map[string]any, analysis any, scope map[string]any, repository, commit string) (map[string]any, error) {
	if runtime != "codex" && runtime != "claude_code" {
		runtime = "generic"
	}
	raw, err := os.ReadFile(str(row["path"]))
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(raw) {
		return nil, errors.New("UnicodeDecodeError: invalid utf-8")
	}
	content := string(raw)
	if digest(raw) != row["content_hash"] {
		return nil, hubErr("source_changed", str(row["path"]))
	}
	if !truthy(row["has_frontmatter"]) {
		return nil, hubErr("invalid_skill", "name/description frontmatter required")
	}
	if analysis == nil {
		// The core requires a reviewed analysis; the importer never invents one.
		return nil, hubErr("needs_analysis", str(row["skill_id"]))
	}
	origin := map[string]any{"source_type": "import", "original_format": "skill_md", "original_runtime": runtime}
	if repository != "" {
		origin["repository"] = repository
	}
	if commit != "" {
		origin["commit"] = commit
	}
	bundle, err := captureBundle(str(row["path"]), str(row["inventory_root"]), str(row["content_hash"]))
	if err == nil && row["bundle"] != nil && !sameJSON(bundle, row["bundle"]) {
		err = errors.New("skill_bundle_source_changed")
	}
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", hubErr("bundle_capture_failed", ""), err)
	}
	return map[string]any{"bundle": bundle, "scope": scope, "skill_id": row["skill_id"], "path": row["path"], "content": content,
		"analysis": analysis, "redact_secrets": true, "origin": origin,
		"compatibility": map[string]any{"codex": "supported", "claude_code": "supported"}}, nil
}

// sameJSON compares two values as their JSON text (Go ints and decoded numbers alike).
func sameJSON(a, b any) bool {
	x, err1 := host.Dumps(a, host.DumpOpts{Compact: true, SortKeys: true})
	y, err2 := host.Dumps(b, host.DumpOpts{Compact: true, SortKeys: true})
	return err1 == nil && err2 == nil && x == y
}

// ---------------------------------------------------------------- resolve + cache

// hubClient asks Arkism first and uses a validated local cache only when the hub is unreachable.
// Offline results are always labelled: exact pins offline_cache, latest-approved offline_cache_stale.
type hubClient struct {
	url, token string
	scope      map[string]any
	cache      string
	timeout    time.Duration
}

func newHubClient(url, token string, scope map[string]any, cacheDir string, timeout time.Duration) (*hubClient, error) {
	c := &hubClient{url: strings.TrimRight(url, "/"), token: token, scope: maps.Clone(scope), timeout: timeout}
	if c.scope == nil {
		c.scope = map[string]any{}
	}
	key, err := host.Dumps([]any{c.url, digest([]byte(token)), c.scope}, host.DumpOpts{SortKeys: true})
	if err != nil {
		return nil, err
	}
	c.cache = filepath.Join(cacheDir, digest([]byte(key))[:32])
	if err := os.MkdirAll(c.cache, 0o700); err != nil {
		return nil, err
	}
	return c, nil
}

// offlineError is a transport failure: the hub could not be reached or answered mid-way.
type offlineError struct{ err error }

func (e *offlineError) Error() string { return e.err.Error() }
func (e *offlineError) Unwrap() error { return e.err }

func (c *hubClient) post(path string, payload any) (map[string]any, error) {
	body, err := host.Dumps(payload, host.DumpOpts{})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+path, strings.NewReader(body))
	if err != nil {
		return nil, &offlineError{err}
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, &offlineError{err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, &host.HTTPError{Code: resp.StatusCode, Body: raw}
	}
	if err != nil {
		return nil, &offlineError{err}
	}
	v, err := host.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("TypeError: answer is not an object")
	}
	return obj, nil
}

func (c *hubClient) cachePath(ref string) string {
	return filepath.Join(c.cache, digest([]byte(ref))[:32]+".json")
}

// pyTime is time.time() as JSON: Python's float repr.
func pyTime() json.Number {
	return json.Number(strconv.FormatFloat(float64(time.Now().UnixNano())/1e9, 'f', -1, 64))
}

func (c *hubClient) store(ref string, item map[string]any) error {
	text, err := host.Dumps(host.Obj{{Key: "ref", Value: ref}, {Key: "fetched_at", Value: pyTime()}, {Key: "item", Value: item}}, host.DumpOpts{NoASCII: true})
	if err != nil {
		return err
	}
	// A temp file of its own: runs that resolve the same skill at once must not share one.
	f, err := os.CreateTemp(c.cache, "*.tmp")
	if err != nil {
		return err
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = replaceFile(f.Name(), c.cachePath(ref))
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// replaceFile renames src over dst. On Windows a rename onto a file another rename or reader has open fails with
// Access is denied (there is no atomic replace while the target is in use), so it is retried for a short while there.
func replaceFile(src, dst string) error {
	err := os.Rename(src, dst)
	for i := 0; err != nil && runtime.GOOS == "windows" && i < 100; i++ {
		time.Sleep(10 * time.Millisecond)
		err = os.Rename(src, dst)
	}
	return err
}

// load returns a cache entry, or nil when it is missing or no longer matches its hashes.
func (c *hubClient) load(ref string) (map[string]any, error) {
	raw, err := os.ReadFile(c.cachePath(ref))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v, err := host.Loads(string(raw))
	if err != nil {
		return nil, err
	}
	entry, ok := v.(map[string]any)
	item, ok2 := entry["item"].(map[string]any)
	if !ok || !ok2 {
		return nil, errors.New("KeyError: item")
	}
	instructions, ok := item["instructions"].(string)
	if !ok {
		return nil, errors.New("KeyError: instructions")
	}
	if digest([]byte(instructions)) != item["content_hash"] || entry["ref"] != ref {
		return nil, nil
	}
	if b, has := item["bundle"]; has {
		if _, err := validateBundle(b); err != nil {
			return nil, nil
		}
	}
	return entry, nil
}

func (c *hubClient) discard(ref string) {
	entry, _ := c.load(ref)
	os.Remove(c.cachePath(ref))
	if entry == nil {
		return
	}
	item := entry["item"].(map[string]any)
	for _, k := range []string{"skill_id", "resolved_version", "version_id"} {
		if !truthy(item[k]) {
			return
		}
	}
	for _, alias := range []string{pyStr(item["skill_id"]) + "@" + pyStr(item["resolved_version"]), pyStr(item["skill_id"]) + "@latest-approved"} {
		if other, _ := c.load(alias); other != nil && sameJSON(asMap(other["item"])["version_id"], item["version_id"]) {
			os.Remove(c.cachePath(alias))
		}
	}
}

func withKeys(item map[string]any, kv ...any) map[string]any {
	out := maps.Clone(item)
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func (c *hubClient) resolve(refs []string, trialID, runtime string) ([]map[string]any, error) {
	payload := map[string]any{"scope": c.scope, "refs": stringsAny(refs)}
	if trialID != "" {
		payload["trial_id"], payload["runtime"] = trialID, runtime
	}
	res, err := c.post("/v1/skills/resolve", payload)
	var offline *offlineError
	if code := host.HTTPCode(err); code != 0 {
		if trialID == "" && (code == 401 || code == 403 || code == 404) {
			for _, ref := range refs {
				c.discard(ref)
			}
		}
		return nil, hubErr("hub_error", strconv.Itoa(code))
	} else if errors.As(err, &offline) {
		if trialID != "" {
			return nil, hubErr("trial_unavailable_offline", "")
		}
		return c.resolveOffline(refs)
	} else if err != nil {
		return nil, err
	}
	items, err := resolveItems(res, trialID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if truthy(item["error"]) && trialID == "" {
			c.discard(str(item["ref"]))
		}
		if !truthy(item["error"]) {
			if err := c.resolveCheckItem(item, trialID); err != nil {
				return nil, err
			}
		}
		out = append(out, withKeys(item, "resolution_mode", "hub"))
	}
	return out, nil
}

// resolveItems is the response's items; a trial must answer with its one item.
func resolveItems(res map[string]any, trialID string) ([]map[string]any, error) {
	rawItems, ok := res["items"].([]any)
	if !ok {
		return nil, errors.New("KeyError: items")
	}
	var items []map[string]any
	for _, r := range rawItems {
		item, ok := r.(map[string]any)
		if !ok {
			return nil, errors.New("TypeError: item is not an object")
		}
		items = append(items, item)
	}
	if trialID != "" {
		status := str(items0(items)["trial_status"])
		if len(items) != 1 || items[0]["trial_id"] != trialID || !containsStr([]string{"active", "stopped", "expired", "invalidated"}, status) {
			return nil, hubErr("invalid_trial_response", "")
		}
	}
	return items, nil
}

// resolveOffline serves each ref from the cache.
func (c *hubClient) resolveOffline(refs []string) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		item, err := c.offline(ref)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

// resolveCheckItem verifies a resolved item's hashes and, outside a trial, caches it
// (also under its pinned ref when it carries a bundle).
func (c *hubClient) resolveCheckItem(item map[string]any, trialID string) error {
	instructions, ok := item["instructions"].(string)
	if !ok {
		return errors.New("KeyError: instructions")
	}
	if digest([]byte(instructions)) != item["content_hash"] {
		return hubErr("hash_mismatch", str(item["ref"]))
	}
	_, hasBundle := item["bundle"]
	if hasBundle {
		if _, err := validateBundle(item["bundle"]); err != nil {
			return hubErr("bundle_hash_mismatch", "")
		}
	}
	if trialID != "" {
		return nil
	}
	if err := c.store(str(item["ref"]), item); err != nil {
		return err
	}
	if pinned := pyStr(item["skill_id"]) + "@" + pyStr(item["resolved_version"]); hasBundle && pinned != item["ref"] {
		if err := c.store(pinned, withKeys(item, "ref", pinned)); err != nil {
			return err
		}
	}
	return nil
}

func items0(items []map[string]any) map[string]any {
	if len(items) == 0 {
		return nil
	}
	return items[0]
}

func (c *hubClient) offline(ref string) (map[string]any, error) {
	entry, err := c.load(ref)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, hubErr("skill_unavailable_offline", ref)
	}
	mode := "offline_cache"
	if strings.HasSuffix(ref, "@latest-approved") {
		mode = "offline_cache_stale"
	}
	return withKeys(entry["item"].(map[string]any), "resolution_mode", mode, "cache_fetched_at", entry["fetched_at"]), nil
}

// ---------------------------------------------------------------- canonical model

var canonicalFields = []string{"skill_id", "version", "version_id", "content_hash", "name", "description", "instructions",
	"category", "tags", "inputs", "outputs", "capabilities", "tool_requirements",
	"runtime_requirements", "dependencies", "compatibility", "provenance", "validation", "visibility"}

// lifecycleStatus maps a hub version status to the canonical lifecycle; evaluated means an
// independent passing evaluation exists for a candidate.
func lifecycleStatus(hubStatus string, deprecated, evaluated bool) (string, error) {
	switch {
	case deprecated:
		return "deprecated", nil
	case hubStatus == "candidate" && evaluated:
		return "validated", nil
	case hubStatus == "candidate":
		return "draft", nil
	case hubStatus == "approved" || hubStatus == "active":
		return "approved", nil
	case hubStatus == "superseded" || hubStatus == "rejected":
		return hubStatus, nil
	}
	return "", hubErr("unknown_status", hubStatus)
}

func getOr(m map[string]any, key string, def any) any {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

// canonical is the runtime-neutral view of one resolved skill version.
func canonical(item map[string]any, scope, analysis map[string]any, evaluated bool) (host.Obj, error) {
	if truthy(item["error"]) {
		return nil, hubErr(pyStr(item["error"]), str(item["ref"]))
	}
	req := orMap(item["requirements"])
	if analysis == nil {
		analysis = map[string]any{}
	}
	sid := str(item["skill_id"])
	category := ""
	if strings.Contains(sid, "/") {
		category, _, _ = strings.Cut(sid, "/")
	}
	tagSet := map[string]bool{}
	for _, k := range []string{"tags", "agents"} {
		for _, t := range asList(getOr(analysis, k, []any{})) {
			tagSet[pyStr(t)] = true
		}
	}
	capabilities, tools := host.Obj{}, host.Obj{}
	for _, k := range []string{"reads", "writes", "external_transfer"} {
		capabilities = append(capabilities, host.KV{Key: k, Value: getOr(req, k, false)})
	}
	for _, k := range []string{"tools", "apis", "permissions", "secret_names", "irreversible_actions"} {
		tools = append(tools, host.KV{Key: k, Value: getOr(req, k, []any{})})
	}
	status, err := lifecycleStatus(str(getOr(item, "validation_status", "")), truthy(getOr(item, "deprecated", false)), evaluated)
	if err != nil {
		return nil, err
	}
	if scope == nil {
		scope = map[string]any{}
	}
	provenance := item["provenance_summary"]
	if !truthy(provenance) {
		provenance = map[string]any{}
	}
	values := []any{item["skill_id"], item["resolved_version"], item["version_id"], item["content_hash"], item["name"], item["description"],
		item["instructions"], category, stringsAny(sortedKeys(tagSet)), getOr(analysis, "input_format", ""), getOr(analysis, "output_format", ""),
		capabilities, tools,
		host.Obj{{Key: "environments", Value: getOr(req, "environments", []any{})}, {Key: "authority", Value: getOr(req, "authority", "")}},
		getOr(req, "dependencies", []any{}), getOr(item, "compatibility", map[string]any{}), provenance,
		host.Obj{{Key: "status", Value: status}, {Key: "hub_status", Value: getOr(item, "validation_status", "")},
			{Key: "latest_approved", Value: getOr(item, "latest_approved", false)}, {Key: "resolution_mode", Value: getOr(item, "resolution_mode", "hub")}},
		host.Obj{{Key: "scope", Value: scope}}}
	out := make(host.Obj, len(canonicalFields))
	for i, k := range canonicalFields {
		out[i] = host.KV{Key: k, Value: values[i]}
	}
	return out, nil
}

// ---------------------------------------------------------------- projection

// treeHash hashes relative paths and bytes in pathlib sort order, skipping caches and the sidecar.
func treeHash(folder string) (string, error) {
	var paths []string
	err := filepath.WalkDir(folder, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != folder {
			rel, _ := filepath.Rel(folder, p)
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sortPaths(paths)
	h := sha256.New()
	for _, rel := range paths {
		p := filepath.Join(folder, filepath.FromSlash(rel))
		if !isFile(p) || slices.ContainsFunc(strings.Split(rel, "/"), func(part string) bool { return containsStr(treeSkip, part) }) {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		h.Write([]byte(rel + "\x00"))
		h.Write(raw)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// localBytes is a hash-only companion from the local source, only if it is still the pinned bytes.
func localBytes(root, relative, sha string) ([]byte, error) {
	pinned, raw, err := readLocal(root, relative)
	if err != nil || pinned != sha {
		return nil, hubErr("hash_only_file_unavailable", relative)
	}
	return raw, nil
}

// renameHook lets tests fail the swap like a full disk would.
var renameHook = os.Rename

// materialize replaces folder with exactly this version: canonical SKILL.md plus its pinned
// companions. The new folder is built beside the target and swapped in, so any failure leaves
// the previous copy untouched.
func materialize(item map[string]any, folder, localSource string, extra host.Obj) (string, error) {
	instructions, _ := item["instructions"].(string)
	if digest([]byte(instructions)) != item["content_hash"] {
		return "", hubErr("hash_mismatch", str(item["ref"]))
	}
	var bundle map[string]any
	if b, ok := item["bundle"]; ok {
		var err error
		if bundle, err = validateBundle(b); err != nil {
			return "", err
		}
	}
	parent, name := filepath.Dir(folder), filepath.Base(folder)
	if err := os.MkdirAll(parent, 0o777); err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(parent, "."+name+".")
	if err != nil {
		return "", err
	}
	old := ""
	fail := func(err error) (string, error) {
		_ = os.RemoveAll(work)
		// The previous copy must stay readable at its runtime path when the swap fails.
		if old != "" && exists(old) && !exists(folder) {
			err = errors.Join(err, os.Rename(old, folder))
		}
		return "", err
	}
	if err := os.WriteFile(filepath.Join(work, "SKILL.md"), []byte(instructions), 0o666); err != nil {
		return fail(err)
	}
	for _, f := range bundleFiles(bundle) {
		if err := materializeFile(work, localSource, f); err != nil {
			return fail(err)
		}
	}
	th, err := treeHash(work)
	if err != nil {
		return fail(err)
	}
	var bundleSHA any
	if bundle != nil {
		bundleSHA = bundle["sha256"]
	}
	meta := host.Obj{{Key: "ref", Value: pyStr(item["skill_id"]) + "@" + pyStr(item["resolved_version"])}, {Key: "version_id", Value: item["version_id"]},
		{Key: "content_hash", Value: item["content_hash"]}, {Key: "validation_status", Value: getOr(item, "validation_status", "")},
		{Key: "resolution_mode", Value: getOr(item, "resolution_mode", "hub")}, {Key: "bundle_sha256", Value: bundleSHA}, {Key: "tree_hash", Value: th}}
	for _, kv := range extra {
		meta.Set(kv.Key, kv.Value)
	}
	text, err := dumpIndent(meta, 2, false)
	if err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".arkism.json"), []byte(text+"\n"), 0o666); err != nil {
		return fail(err)
	}
	if err := os.Chmod(work, 0o755); err != nil {
		return fail(err)
	}
	if exists(folder) {
		tag := make([]byte, 4)
		rand.Read(tag)
		old = filepath.Join(parent, "."+name+".replaced-"+hex.EncodeToString(tag))
		if err := renameHook(folder, old); err != nil {
			old = ""
			return fail(err)
		}
	}
	if err := renameHook(work, folder); err != nil {
		return fail(err)
	}
	if old != "" {
		if isLink(old) {
			os.Remove(old)
		} else {
			os.RemoveAll(old)
		}
	}
	return folder, nil
}

// materializeFile writes one bundle file under work, its bytes inline or from localSource.
func materializeFile(work, localSource string, f map[string]any) error {
	rel := str(f["path"])
	if rel == "SKILL.md" {
		return hubErr("invalid_skill_bundle", rel)
	}
	var raw []byte
	if content, ok := f["content"].(string); ok {
		raw = []byte(content)
	} else if localSource != "" {
		var err error
		if raw, err = localBytes(localSource, rel, str(f["sha256"])); err != nil {
			return err
		}
	} else {
		return hubErr("hash_only_file_unavailable", rel)
	}
	target := filepath.Join(work, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
		return err
	}
	if err := os.WriteFile(target, raw, 0o666); err != nil {
		return err
	}
	// shortcut: modes are not pinned; a shebang marks an executable script.
	mode := os.FileMode(0o644)
	if bytes.HasPrefix(raw, []byte("#!")) {
		mode = 0o755
	}
	return os.Chmod(target, mode)
}

// exists is Path.exists() or is_symlink().
func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// project writes one exact version for a runtime that reads skill folders.
func project(item map[string]any, runtime, target, localSource string) (string, error) {
	if _, ok := runtimeDirs[runtime]; !ok {
		return "", hubErr("unsupported_runtime", runtime)
	}
	if truthy(item["error"]) {
		return "", hubErr(pyStr(item["error"]), str(item["ref"]))
	}
	if str(orMap(item["compatibility"])[runtime]) == "unsupported" {
		return "", hubErr("incompatible_skill", pyStr(item["skill_id"])+" on "+runtime)
	}
	folder := filepath.Join(target, "arkism-"+projectNameBad.ReplaceAllString(str(item["skill_id"]), "-"))
	return materialize(item, folder, localSource, nil)
}

// projectionCurrent is true when a projected folder is unedited since projection.
func projectionCurrent(folder string) (bool, error) {
	raw, err := os.ReadFile(filepath.Join(folder, ".arkism.json"))
	if err != nil {
		return false, err
	}
	v, err := host.Loads(string(raw))
	if err != nil {
		return false, err
	}
	meta, _ := v.(map[string]any)
	if th, ok := meta["tree_hash"]; ok {
		got, err := treeHash(folder)
		return err == nil && got == th, err
	}
	body, err := os.ReadFile(filepath.Join(folder, "SKILL.md"))
	if err != nil {
		return false, err
	}
	return digest(body) == meta["content_hash"], nil
}

// ---------------------------------------------------------------- usage

type usageOpts struct {
	profileID                               string
	retryCount, toolFailures, runtimeErrors int
	errorCode                               string
}

// usageEvent is a skill.usage trace event: IDs, hash, codes and counts only.
func usageEvent(eventID string, item map[string]any, runtime, status, startedAt, completedAt string, o usageOpts) (host.Obj, error) {
	if !containsStr(usageStatuses, status) {
		return nil, hubErr("invalid_usage_status", status)
	}
	if o.errorCode != "" && (status == "success" || !errorCodeRx.MatchString(o.errorCode)) {
		return nil, hubErr("invalid_error_code", o.errorCode)
	}
	payload := host.Obj{{Key: "skill_id", Value: item["skill_id"]}, {Key: "version", Value: item["resolved_version"]}, {Key: "version_id", Value: item["version_id"]},
		{Key: "content_hash", Value: item["content_hash"]}, {Key: "runtime", Value: runtime}, {Key: "status", Value: status},
		{Key: "started_at", Value: startedAt}, {Key: "completed_at", Value: completedAt}, {Key: "retry_count", Value: o.retryCount},
		{Key: "tool_failures", Value: o.toolFailures}, {Key: "runtime_errors", Value: o.runtimeErrors}}
	if item["trial_status"] == "active" && truthy(item["trial_id"]) {
		payload.Set("trial_id", item["trial_id"])
	}
	if o.profileID != "" {
		payload.Set("profile_id", o.profileID)
	}
	if o.errorCode != "" {
		payload.Set("error_code", o.errorCode)
	}
	if getOr(item, "resolution_mode", "hub") != "hub" {
		payload.Set("offline_cache", true)
	}
	return host.Obj{{Key: "id", Value: eventID}, {Key: "kind", Value: "skill.usage"}, {Key: "occurred_at", Value: completedAt},
		{Key: "payload", Value: payload}, {Key: "sensitivity", Value: "internal"}}, nil
}

// ---------------------------------------------------------------- cli

func cliClient() (*hubClient, error) {
	scope := map[string]any{}
	if v, err := host.Loads(host.Env("ARKISM_SKILL_SCOPE", "{}")); err != nil {
		return nil, err
	} else if m, ok := v.(map[string]any); ok {
		scope = m
	}
	return newHubClient(host.Env("ARKISM_URL", "http://127.0.0.1:8091"), host.Env("ARKISM_API_TOKEN", ""), scope,
		host.Env("ARKISM_SKILL_CACHE", filepath.Join(host.Home, ".cache/arkism-skills")), 5*time.Second)
}

func skillsMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stderr, skillsUsage)
		return 2
	}
	p := newArgs("skills "+args[0], stderr)
	var err error
	switch args[0] {
	case "inventory":
		extra := p.list("--root")
		if !p.parse(args[1:], 0, 0) {
			return 2
		}
		roots := append(defaultRoots(), repoLocalRoots(host.Home)...)
		for _, r := range *extra {
			label, dir, _ := strings.Cut(r, "=")
			roots = append(roots, skillRoot{label, dir, "generic"})
		}
		printJSON(stdout, inventoryReport(roots), 2)
		return 0
	case "resolve":
		if !p.parse(args[1:], 1, -1) {
			return 2
		}
		var c *hubClient
		var items []map[string]any
		if c, err = cliClient(); err == nil {
			if items, err = c.resolve(p.pos, "", ""); err == nil {
				printJSON(stdout, items, 2)
				return 0
			}
		}
	case "project":
		runtime, target := p.str("--runtime", ""), p.str("--target", "")
		if !p.parse(args[1:], 1, 1) || !p.need("--runtime", "--target") || !p.choice("--runtime", *runtime, sortedKeys(runtimeDirs)) {
			return 2
		}
		var c *hubClient
		var items []map[string]any
		if c, err = cliClient(); err == nil {
			if items, err = c.resolve(p.pos, "", ""); err == nil {
				var folder string
				if folder, err = project(items[0], *runtime, *target, ""); err == nil {
					fmt.Fprintln(stdout, folder)
					return 0
				}
			}
		}
	default:
		fmt.Fprint(stderr, skillsUsage)
		return 2
	}
	fmt.Fprintln(stderr, err)
	return 1
}

func inventoryReport(roots []skillRoot) host.Obj {
	rows := discover(roots)
	report := classify(rows)
	count := func(f func(map[string]any) bool) int {
		n := 0
		for _, r := range rows {
			if f(r) {
				n++
			}
		}
		return n
	}
	bySource := host.Obj{}
	for _, sr := range roots {
		if _, ok := bySource.Get(sr.label); !ok {
			bySource.Set(sr.label, count(func(r map[string]any) bool { return r["source"] == sr.label }))
		}
	}
	vendor := host.Obj{}
	for _, k := range vendorNames {
		vendor.Set(k, count(func(r map[string]any) bool { return slices.Contains(r["vendor_markers"].([]any), any(k)) }))
	}
	report = append(report,
		host.KV{Key: "by_source", Value: bySource},
		host.KV{Key: "credential_like_files", Value: count(func(r map[string]any) bool { return r["credential_like"] == true })},
		host.KV{Key: "hardcoded_root_path_files", Value: count(func(r map[string]any) bool { return r["hardcoded_root_path"] == true })},
		host.KV{Key: "missing_frontmatter", Value: count(func(r map[string]any) bool { return r["has_frontmatter"] != true })},
		host.KV{Key: "vendor_specific", Value: vendor},
		host.KV{Key: "mcp_tool_dependent_files", Value: count(func(r map[string]any) bool { return len(r["mcp_tools"].([]any)) > 0 })},
		host.KV{Key: "script_dependent_files", Value: count(func(r map[string]any) bool { return r["has_scripts"] == true })})
	return report
}
