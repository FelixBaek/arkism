package skills

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func loadGolden(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	v, err := host.Loads(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return v.(map[string]any)
}

func canon(t *testing.T, v any) string {
	t.Helper()
	s, err := host.Dumps(v, host.DumpOpts{Compact: true, SortKeys: true, NoASCII: true})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// buildFixture writes the golden's files under a temp root. caseClash is true when two fixture paths differ only in
// case (tree/a/b and tree/A): a case-insensitive file system (Windows) cannot hold both, so that file is left out.
func buildFixture(t *testing.T, g map[string]any) (root string, caseClash bool) {
	t.Helper()
	root, _ = filepath.EvalSymlinks(t.TempDir())
	seen := map[string]string{}
	for _, e := range g["fixture"].([]any) {
		pair := e.([]any)
		name := pair[0].(string)
		raw, _ := base64.StdEncoding.DecodeString(pair[1].(string))
		if runtime.GOOS == "windows" && clashes(seen, name) {
			caseClash = true
			continue
		}
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, caseClash
}

// slashJSON turns the JSON-escaped Windows separators (`\\`) in a report into '/', as the golden's Unix paths have them.
func slashJSON(text string) string {
	if runtime.GOOS != "windows" {
		return text
	}
	return strings.ReplaceAll(text, `\\`, "/")
}

// clashes records name and its parent folders, and reports whether one of them differs from an earlier one only in case.
func clashes(seen map[string]string, name string) bool {
	parts := strings.Split(name, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		if prev, ok := seen[strings.ToLower(p)]; ok && prev != p {
			return true
		}
	}
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		seen[strings.ToLower(p)] = p
	}
	return false
}

func TestHubGoldens(t *testing.T) {
	g := loadGolden(t, "hub.json")
	root, caseClash := buildFixture(t, g)
	roots := []skillRoot{{"test", filepath.Join(root, "skills"), "codex"}, {"plug", filepath.Join(root, "plug"), "claude_code"}, {"test", filepath.Join(root, "missing"), "generic"}}
	rows := discover(roots)
	got := strings.ReplaceAll(slashJSON(canon(t, rows)), filepath.ToSlash(root), "ROOT")
	if want := canon(t, g["rows"]); got != want {
		t.Errorf("discover\n got %s\nwant %s", got, want)
	}
	inv, _ := dumpIndent(inventoryReport(roots), 2, false)
	if inv = strings.ReplaceAll(slashJSON(inv), filepath.ToSlash(root), "ROOT"); inv != g["inventory"] {
		t.Errorf("inventory\n got %s\nwant %s", inv, g["inventory"])
	}
	// The golden tree hash covers tree/A beside tree/a/b, which a case-insensitive file system cannot hold.
	if th, _ := treeHash(filepath.Join(root, "tree")); !caseClash && th != g["tree_hash"] {
		t.Errorf("tree_hash %s != %s", th, g["tree_hash"])
	}
	demo := filepath.Join(root, "plug/skills/demo/SKILL.md")
	body, _ := os.ReadFile(demo)
	b, err := captureBundle(demo, root, digest(body))
	if err != nil || canon(t, b) != canon(t, g["bundle"]) {
		t.Errorf("bundle %v\n got %s\nwant %s", err, canon(t, b), canon(t, g["bundle"]))
	}
	b, err = captureBundle(filepath.Join(root, "skills/c/code-review/SKILL.md"), filepath.Join(root, "skills"), "")
	if err != nil || canon(t, b) != canon(t, g["bundle_skill_only"]) {
		t.Errorf("skill-only bundle %v %s", err, canon(t, b))
	}
	item := g["materialize_item"].(map[string]any)
	folder, err := materialize(item, filepath.Join(root, "out/skill"), filepath.Join(root, "plug"), host.Obj{{Key: "source", Value: "src"}, {Key: "note", Value: "배포된 사본"}})
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := os.ReadFile(filepath.Join(folder, ".arkism.json"))
	if string(meta) != g["materialized_meta"] {
		t.Errorf("meta\n got %s\nwant %s", meta, g["materialized_meta"])
	}
	if th, _ := treeHash(folder); th != g["materialized_tree"] {
		t.Errorf("materialized tree %s", th)
	}
	if ok, err := projectionCurrent(folder); !ok || err != nil {
		t.Errorf("projection current %v %v", ok, err)
	}
	view, err := canonical(item, map[string]any{"w": json.Number("1")}, map[string]any{"tags": []any{"b", "a"}, "agents": []any{"a", "c"}, "input_format": "i"}, false)
	if s := host.MustDumps(view, host.DumpOpts{NoASCII: true}); err != nil || s != g["canonical"] {
		t.Errorf("canonical %v\n got %s\nwant %s", err, s, g["canonical"])
	}
	usageItem := withKeys(item, "trial_id", "t", "trial_status", "active", "resolution_mode", "offline_cache")
	ev, err := usageEvent("e1", usageItem, "codex", "failure", "s", "c", usageOpts{profileID: "p", errorCode: "tool_timeout"})
	if s := host.MustDumps(ev, host.DumpOpts{NoASCII: true}); err != nil || s != g["usage"] {
		t.Errorf("usage\n got %s\nwant %s", s, g["usage"])
	}
	for _, e := range g["binding"].([]any) {
		b := e.(map[string]any)
		c, err := newHubClient(b["url"].(string), b["token"].(string), b["scope"].(map[string]any), t.TempDir(), time.Second)
		if err != nil || filepath.Base(c.cache) != b["dir"] || filepath.Base(c.cachePath("code-review@1")) != b["file"] {
			t.Errorf("binding %v %s", err, c.cache)
		}
	}
	for _, e := range g["frontmatter"].([]any) {
		pair := e.([]any)
		got := map[string]any{}
		for k, v := range frontmatter(pair[0].(string)) {
			got[k] = v
		}
		if canon(t, got) != canon(t, pair[1]) {
			t.Errorf("frontmatter %q: %v want %v", pair[0], got, pair[1])
		}
	}
	for _, e := range g["secret"].([]any) {
		pair := e.([]any)
		if secretRx.MatchString(pair[0].(string)) != pair[1] {
			t.Errorf("secret %q", pair[0])
		}
	}
	for _, e := range g["quote"].([]any) {
		q := e.([]any)
		if pyQuote(q[0].(string)) != q[1] || pyQuotePlus(q[0].(string)) != q[2] {
			t.Errorf("quote %q", q[0])
		}
	}
	for _, e := range g["indent"].([]any) {
		q := e.([]any)
		n, _ := host.Int(q[2])
		if s, _ := dumpIndent(q[0], int(n), false); s != q[1] {
			t.Errorf("indent\n got %s\nwant %s", s, q[1])
		}
	}
	for _, e := range g["splitlines"].([]any) {
		q := e.([]any)
		if canon(t, orNil(splitLines(q[0].(string)))) != canon(t, q[1]) || canon(t, orNil(splitLinesKeep(q[0].(string)))) != canon(t, q[2]) {
			t.Errorf("splitlines %q", q[0])
		}
	}
}

func orNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// --- hub client (behavioural cases of the resolve / cache tests)

const hubV1 = "---\nname: code-review\ndescription: Review code changes.\n---\nReview v1.\n"

var hubV2 = strings.Replace(hubV1, "v1", "v2", 1)

func hubItem(ref string, version int, text, status string) map[string]any {
	return map[string]any{"ref": ref, "skill_id": "code-review", "name": "code-review", "description": "Review code changes.",
		"resolved_version": version, "version_id": "skill-v" + string(rune('0'+version)), "content_hash": digest([]byte(text)),
		"instructions": text, "validation_status": status,
		"requirements":  map[string]any{"authority": "requirements_only", "tools": []any{"shell"}},
		"compatibility": map[string]any{"codex": "supported", "claude_code": "supported"}}
}

type stubHub struct {
	mu     sync.Mutex
	latest int
}

func (s *stubHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	v, _ := host.Decode(r.Body)
	body := v.(map[string]any)
	s.mu.Lock()
	latest := s.latest
	s.mu.Unlock()
	out := []any{}
	for _, ref := range body["refs"].([]any) {
		var row map[string]any
		switch ref {
		case "code-review@1":
			status := "active"
			if latest == 2 {
				status = "superseded"
			}
			row = hubItem("code-review@1", 1, hubV1, status)
		case "code-review@2":
			row = hubItem("code-review@2", 2, hubV2, "active")
		case "code-review@latest-approved":
			text := hubV1
			if latest == 2 {
				text = hubV2
			}
			row = hubItem("code-review@latest-approved", latest, text, "active")
		default:
			row = map[string]any{"ref": ref, "error": "skill_not_found"}
		}
		if tid, ok := body["trial_id"]; ok {
			row["trial_id"], row["trial_status"] = tid, "active"
		}
		out = append(out, row)
	}
	w.Write([]byte(host.MustDumps(map[string]any{"authority": "data_only_untrusted", "items": out}, host.DumpOpts{})))
}

func newStub(t *testing.T) (*stubHub, string, string) {
	s := &stubHub{latest: 1}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv.URL, t.TempDir()
}

var testScope = map[string]any{"workspace_id": "w", "project_id": "p", "user_id": "u", "agent_id": "a"}

func client(t *testing.T, url, cache string) *hubClient {
	c, err := newHubClient(url, "t", testScope, cache, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func offlineClient(t *testing.T, url, cache string) *hubClient {
	c := client(t, url, cache)
	c.url = "http://127.0.0.1:1" // nothing listens: a transport error, like the hub being down
	return c
}

func TestConcurrentStoresDoNotCollide(t *testing.T) {
	_, url, cache := newStub(t)
	c := client(t, url, cache)
	item := map[string]any{"ref": "x@1", "instructions": "a", "content_hash": digest([]byte("a"))}
	var wg sync.WaitGroup
	errs := make(chan error, 800)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				if err := c.store("x@1", item); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	entry, err := c.load("x@1")
	if err != nil || canon(t, entry["item"]) != canon(t, item) {
		t.Fatalf("%v %v", entry, err)
	}
}

func TestTrialIsOnlineOnlyAndPreservesNormalCache(t *testing.T) {
	s, url, cache := newStub(t)
	c := client(t, url, cache)
	ref := "code-review@latest-approved"
	base, err := c.resolve([]string{ref}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	s.latest = 2
	trial, err := c.resolve([]string{ref}, "trial-one", "codex")
	if err != nil || canon(t, trial[0]["resolved_version"]) != "2" {
		t.Fatalf("%v %v", trial, err)
	}
	entry, _ := c.load(ref)
	if asMap(entry["item"])["version_id"] != base[0]["version_id"] {
		t.Fatal("trial overwrote the normal cache")
	}
	off := offlineClient(t, url, cache)
	if _, err := off.resolve([]string{ref}, "trial-one", "codex"); hubCode(err) != "trial_unavailable_offline" {
		t.Fatal(err)
	}
	got, err := off.resolve([]string{ref}, "", "")
	if err != nil || got[0]["version_id"] != base[0]["version_id"] {
		t.Fatal(got, err)
	}
	ev, _ := usageEvent("event", withKeys(trial[0], "trial_id", "trial-one", "trial_status", "active"), "codex", "success", "a", "b", usageOpts{})
	if p, _ := ev.Get("payload"); !strings.Contains(canon(t, p), `"trial_id":"trial-one"`) {
		t.Fatal(ev)
	}
}

func TestVersionsOfflineCacheAndProjection(t *testing.T) {
	s, url, cache := newStub(t)
	c := client(t, url, cache)
	got, err := c.resolve([]string{"code-review@1", "code-review@latest-approved", "nope@1"}, "", "")
	if err != nil || canon(t, got[0]["resolved_version"]) != "1" || canon(t, got[1]["resolved_version"]) != "1" || got[2]["error"] != "skill_not_found" {
		t.Fatal(got, err)
	}
	s.latest = 2
	got, _ = c.resolve([]string{"code-review@1", "code-review@latest-approved"}, "", "")
	if canon(t, got[1]["resolved_version"]) != "2" {
		t.Fatal(got)
	}
	off := offlineClient(t, url, cache)
	got, err = off.resolve([]string{"code-review@1", "code-review@latest-approved"}, "", "")
	if err != nil || got[0]["resolution_mode"] != "offline_cache" || got[0]["instructions"] != hubV1 ||
		got[1]["resolution_mode"] != "offline_cache_stale" || canon(t, got[1]["resolved_version"]) != "2" || got[0]["cache_fetched_at"] == nil {
		t.Fatal(got, err)
	}
	pinned := got[0]
	if _, err := off.resolve([]string{"code-review@2"}, "", ""); hubCode(err) != "skill_unavailable_offline" {
		t.Fatal(err)
	}
	// A tampered cache entry is not served.
	filepath.WalkDir(cache, func(p string, d os.DirEntry, err error) error {
		if strings.HasSuffix(p, ".json") {
			raw, _ := os.ReadFile(p)
			v, _ := host.Loads(string(raw))
			e := v.(map[string]any)
			it := e["item"].(map[string]any)
			it["instructions"] = it["instructions"].(string) + "injected"
			os.WriteFile(p, []byte(host.MustDumps(e, host.DumpOpts{})), 0o600)
		}
		return nil
	})
	if _, err := off.resolve([]string{"code-review@1"}, "", ""); hubCode(err) == "" {
		t.Fatal(err)
	}
	out := t.TempDir()
	for _, runtime := range []string{"codex", "claude_code"} {
		folder, err := project(pinned, runtime, filepath.Join(out, runtime), "")
		if err != nil {
			t.Fatal(err)
		}
		if body, _ := os.ReadFile(filepath.Join(folder, "SKILL.md")); string(body) != hubV1 {
			t.Fatal(string(body))
		}
		if ok, _ := projectionCurrent(folder); !ok {
			t.Fatal("not current")
		}
		os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte("edited"), 0o644)
		if ok, _ := projectionCurrent(folder); ok {
			t.Fatal("edit not detected")
		}
	}
	if _, err := project(withKeys(pinned, "compatibility", map[string]any{"codex": "unsupported"}), "codex", out, ""); hubCode(err) != "incompatible_skill" {
		t.Fatal(err)
	}
}

func TestHubErrorDiscardsCacheAndDecodeErrorsPropagate(t *testing.T) {
	_, url, cache := newStub(t)
	c := client(t, url, cache)
	if _, err := c.resolve([]string{"code-review@1"}, "", ""); err != nil {
		t.Fatal(err)
	}
	status := 404
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == 200 {
			w.Write([]byte("not json"))
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	c.url = srv.URL
	if _, err := c.resolve([]string{"code-review@1"}, "", ""); hubCode(err) != "hub_error" || err.Error() != "hub_error: 404" {
		t.Fatal(err)
	}
	if e, _ := c.load("code-review@1"); e != nil {
		t.Fatal("404 must discard the cached ref")
	}
	status = 200
	if _, err := c.resolve([]string{"code-review@1"}, "", ""); err == nil || hubCode(err) != "" {
		t.Fatal("a bad answer is not offline", err)
	}
}

func TestFailedSwapKeepsPreviousCopy(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "skill")
	if _, err := materialize(hubItem("code-review@1", 1, hubV1, "passed"), folder, "", nil); err != nil {
		t.Fatal(err)
	}
	renameHook = func(src, dst string) error {
		if b := filepath.Base(src); strings.HasPrefix(b, ".skill.") && !strings.Contains(b, ".replaced-") {
			return errors.New("disk full")
		}
		return os.Rename(src, dst)
	}
	defer func() { renameHook = os.Rename }()
	if _, err := materialize(hubItem("code-review@2", 2, hubV2, "passed"), folder, "", nil); err == nil {
		t.Fatal("expected failure")
	}
	if body, _ := os.ReadFile(filepath.Join(folder, "SKILL.md")); string(body) != hubV1 {
		t.Fatal(string(body))
	}
}

func TestRequirementsAreNotPermissionAndUsageCodes(t *testing.T) {
	_, url, cache := newStub(t)
	got, _ := client(t, url, cache).resolve([]string{"code-review@1"}, "", "")
	r := got[0]
	if asMap(r["requirements"])["authority"] != "requirements_only" {
		t.Fatal(r)
	}
	ev, err := usageEvent("evt-1", r, "demo-app", "validation_failed", "a", "b", usageOpts{profileID: "software-engineer"})
	if err != nil || strings.Contains(canon(t, ev), "instructions") {
		t.Fatal(ev, err)
	}
	if _, err := usageEvent("evt-2", r, "demo-app", "maybe", "a", "b", usageOpts{}); hubCode(err) != "invalid_usage_status" {
		t.Fatal(err)
	}
	if ev, err := usageEvent("evt-3", r, "codex", "failure", "a", "b", usageOpts{errorCode: "tool_timeout"}); err != nil || !strings.Contains(canon(t, ev), `"error_code":"tool_timeout"`) {
		t.Fatal(ev, err)
	}
	for _, c := range [][2]string{{"success", "tool_timeout"}, {"failure", "Timeout: /opt/arkism/x"}} {
		if _, err := usageEvent("evt-4", r, "codex", c[0], "a", "b", usageOpts{errorCode: c[1]}); hubCode(err) != "invalid_error_code" {
			t.Fatal(c, err)
		}
	}
}

func TestCanonicalModel(t *testing.T) {
	_, url, cache := newStub(t)
	got, _ := client(t, url, cache).resolve([]string{"code-review@1"}, "", "")
	view, err := canonical(got[0], testScope, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, kv := range view {
		keys = append(keys, kv.Key)
	}
	if !reflect.DeepEqual(keys, canonicalFields) {
		t.Fatal(keys)
	}
	if v, _ := view.Get("validation"); v.(host.Obj)[0].Value != "approved" {
		t.Fatal(v)
	}
	for _, c := range []struct {
		status         string
		deprecated, ev bool
		want           string
	}{{"candidate", false, false, "draft"}, {"candidate", false, true, "validated"}, {"active", true, false, "deprecated"}} {
		if got, _ := lifecycleStatus(c.status, c.deprecated, c.ev); got != c.want {
			t.Fatal(c, got)
		}
	}
}

func TestInventoryDedupAndImporter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	for _, c := range [][2]string{{"a/code-review", hubV1}, {"b/code-review", hubV1}, {"c/code-review", hubV2}, {"d/plain", "no frontmatter"}} {
		os.MkdirAll(filepath.Join(root, c[0]), 0o755)
		os.WriteFile(filepath.Join(root, c[0], "SKILL.md"), []byte(c[1]), 0o644)
	}
	os.MkdirAll(filepath.Join(root, "e"), 0o755)
	os.Symlink(filepath.Join(root, "a/code-review/SKILL.md"), filepath.Join(root, "e/SKILL.md"))
	rows := discover([]skillRoot{{"test", root, "codex"}})
	report := classify(rows)
	files, _ := report.Get("files")
	distinct, _ := report.Get("distinct_contents")
	exact, _ := report.Get("exact_duplicate_groups")
	coll, _ := report.Get("name_collisions")
	cr, _ := coll.(host.Obj).Get("code-review")
	if files != 4 || distinct != 3 || len(exact.(host.Obj)) != 1 || len(cr.([]any)) != 2 {
		t.Fatal(report)
	}
	var row, plain map[string]any
	for _, r := range rows {
		switch r["skill_id"] {
		case "a/code-review":
			row = r
		case "d/plain":
			plain = r
		}
	}
	payload, err := importPayload("codex", row, map[string]any{"name": "x"}, testScope, "r", "c1")
	if err != nil || asMap(payload["origin"])["original_runtime"] != "codex" || asMap(payload["origin"])["commit"] != "c1" {
		t.Fatal(payload, err)
	}
	if _, err := importPayload("codex", row, nil, testScope, "", ""); hubCode(err) != "needs_analysis" {
		t.Fatal(err)
	}
	if _, err := importPayload("claude_code", plain, map[string]any{"name": "x"}, testScope, "", ""); hubCode(err) == "" {
		t.Fatal(err)
	}
}
