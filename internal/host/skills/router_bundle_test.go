package skills

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// httptestServer serves answer(payload) as JSON; ok=false drops the connection (the hub going offline).
func httptestServer(t *testing.T, answer func(payload map[string]any) (any, bool)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, _ := host.Decode(r.Body)
		body, ok := answer(asMap(v))
		if !ok {
			panic(http.ErrAbortHandler)
		}
		w.Write([]byte(host.MustDumps(body, host.DumpOpts{})))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// pinnedSetup mirrors the pinned-bundle fixture: one CRLF companion file, pinned by hash.
type pinnedSetup struct {
	hub    *hubFake
	root   string
	file   map[string]any
	item   map[string]any
	bundle map[string]any
}

func newPinned(t *testing.T) *pinnedSetup {
	swapRouter(t)
	s := &pinnedSetup{hub: newHubFake(t), root: t.TempDir()}
	routerCacheDir = filepath.Join(s.root, "cache")
	routerRuntime = "codex"
	content := "print('승인판')\r\n"
	s.file = map[string]any{"path": "scripts/run.py", "content": content, "sha256": digest([]byte(content))}
	s.bundle = map[string]any{"files": []any{s.file}, "sha256": bundleHash([]any{s.file})}
	instructions := "Read scripts/run.py"
	s.item = map[string]any{"ref": "demo@latest-approved", "skill_id": "demo", "resolved_version": 1, "version_id": "version-one", "name": "demo",
		"instructions": instructions, "content_hash": digest([]byte(instructions)), "validation_status": "approved",
		"provenance_summary": map[string]any{"source_path": filepath.Join(s.root, "SKILL.md")}, "bundle": s.bundle}
	s.hub.answer = s.response
	return s
}

// response answers with a fresh copy of the current item (the test may change it between calls).
func (s *pinnedSetup) response(payload map[string]any) any {
	item := asMap(j(nil, host.MustDumps(s.item, host.DumpOpts{})))
	item["ref"] = asList(payload["refs"])[0]
	if truthy(payload["trial_id"]) {
		item["trial_id"], item["trial_status"] = payload["trial_id"], "active"
	}
	return map[string]any{"items": []any{item}}
}

func hubError(err error) bool {
	var e *host.SkillHubError
	return errors.As(err, &e)
}

func mustLoad(t *testing.T, id, trial string) host.Obj {
	t.Helper()
	out, err := loadSkill(id, trial)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func refOf(o host.Obj) string { return field(o, "ref").(string) }

func TestPinnedBytesWithoutLocalFolderAndOfflineAlias(t *testing.T) {
	s := newPinned(t)
	loaded := mustLoad(t, "demo", "")
	b := field(loaded, "bundle")
	if field(b, "mode") != "version_pinned" || canon(t, field(b, "files")) != `["scripts/run.py"]` || field(b, "content") != nil {
		t.Fatal(b)
	}
	if r, err := readSkillFile(refOf(loaded), "scripts/run.py"); err != nil || field(r, "content") != s.file["content"] {
		t.Fatal(r, err)
	}
	os.WriteFile(filepath.Join(s.root, "extra.txt"), []byte("not captured"), 0o644)
	if _, err := readSkillFile(refOf(loaded), "extra.txt"); err == nil {
		t.Fatal("uncaptured file served")
	}
	s.hub.offline = true
	if r, err := readSkillFile(refOf(loaded), "scripts/run.py"); err != nil || field(r, "resolution_mode") != "offline_cache" {
		t.Fatal(r, err)
	}
	newToken := filepath.Join(s.root, "new-token")
	os.WriteFile(newToken, []byte("another-token"), 0o600)
	base, token := routerBaseURL, routerTokenFile
	routerBaseURL = "http://127.0.0.1:1"
	if _, err := readSkillFile(refOf(loaded), "scripts/run.py"); !hubError(err) {
		t.Fatal("other hub reused the cache:", err)
	}
	routerBaseURL, routerTokenFile = base, newToken
	if _, err := readSkillFile(refOf(loaded), "scripts/run.py"); !hubError(err) {
		t.Fatal("other token reused the cache:", err)
	}
	routerTokenFile = token
}

func TestRawVersionIDUsesPinnedResolve(t *testing.T) {
	s := newPinned(t)
	version := "skill-" + strings.Repeat("a", 64)
	s.item["version_id"] = version
	routerRequest = func(path string, payload any) (map[string]any, error) {
		switch {
		case strings.HasPrefix(path, "/v1/skill_versions/"):
			return j(t, `{"skill_id":"demo","id":"`+version+`","updated_at":"","sources":[{"source_id":"legacy-source"}],"analysis":{"version":"1","name":"demo","description":"","source_path":""}}`), nil
		case strings.HasPrefix(path, "/v1/skills/demo?"):
			return j(t, `{"version_history":["skill-`+strings.Repeat("b", 64)+`","`+version+`"]}`), nil
		}
		t.Fatal("raw loads must not read sources directly: " + path)
		return nil, nil
	}
	loaded := mustLoad(t, version, "")
	if len(s.hub.payloads) != 1 || canon(t, s.hub.payloads[0]["refs"]) != `["demo@2"]` {
		t.Fatal(s.hub.payloads)
	}
	if refOf(loaded) != "demo@1" || field(field(loaded, "bundle"), "mode") != "version_pinned" || field(loaded, "content_hash") != s.item["content_hash"] {
		t.Fatal(loaded)
	}
	if r, err := readSkillFile(refOf(loaded), "scripts/run.py"); err != nil || field(r, "content") != s.file["content"] {
		t.Fatal(r, err)
	}
	s.item["version_id"] = "skill-" + strings.Repeat("c", 64)
	t.Setenv("ARKISM_SESSION_ID", "fresh-session") // no pin yet: the raw-id check itself must refuse
	if _, err := loadSkill(version, ""); err == nil || !strings.Contains(err.Error(), "skill_version_mismatch") {
		t.Fatal(err)
	}
	if _, err := loadSkill("skill-"+strings.Repeat("d", 64), ""); err == nil || !strings.Contains(err.Error(), "version_not_in_history") {
		t.Fatal(err)
	}
}

func TestHashOnlyCompanionIsListedButNeverServed(t *testing.T) {
	s := newPinned(t)
	logo := map[string]any{"path": "scripts/z.png", "sha256": strings.Repeat("ab", 32), "size": 9, "omitted": "binary"}
	files := []any{s.file, logo}
	s.item["bundle"] = map[string]any{"files": files, "sha256": bundleHash(files)}
	os.MkdirAll(filepath.Join(s.root, "scripts"), 0o755)
	os.WriteFile(filepath.Join(s.root, "scripts", "z.png"), []byte("local png"), 0o644)
	loaded := mustLoad(t, "demo", "")
	b := field(loaded, "bundle")
	if canon(t, field(b, "files")) != `["scripts/run.py"]` || canon(t, field(b, "hash_only")) != `["scripts/z.png"]` {
		t.Fatal(b)
	}
	if _, err := readSkillFile(refOf(loaded), "scripts/z.png"); err == nil || !strings.HasPrefix(err.Error(), "not_stored: binary") {
		t.Fatal(err)
	}
}

func TestOnlineUnavailabilityCannotReappearFromCache(t *testing.T) {
	s := newPinned(t)
	loaded := mustLoad(t, "demo", "")
	s.hub.answer = func(map[string]any) any { return j(t, `{"items":[{"ref":"demo@1","error":"skill_unavailable"}]}`) }
	if _, err := readSkillFile(refOf(loaded), "scripts/run.py"); err == nil {
		t.Fatal("unavailable skill served")
	}
	s.hub.offline = true
	if _, err := readSkillFile(refOf(loaded), "scripts/run.py"); !hubError(err) {
		t.Fatal(err)
	}
	if _, err := loadSkill("demo", ""); !hubError(err) {
		t.Fatal(err)
	}
}

func TestBundleCorruptionIsRejectedOnlineAndInCache(t *testing.T) {
	s := newPinned(t)
	loaded := mustLoad(t, "demo", "")
	s.file["content"] = "changed"
	if _, err := readSkillFile(refOf(loaded), "scripts/run.py"); !hubError(err) {
		t.Fatal(err)
	}
	filepath.WalkDir(routerCacheDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".json") {
			raw, _ := os.ReadFile(p)
			entry := j(t, string(raw))
			asMap(asList(asMap(asMap(entry["item"])["bundle"])["files"])[0])["content"] = "changed"
			os.WriteFile(p, []byte(host.MustDumps(entry, host.DumpOpts{})), 0o600)
		}
		return nil
	})
	s.hub.offline = true
	if _, err := readSkillFile(refOf(loaded), "scripts/run.py"); !hubError(err) {
		t.Fatal(err)
	}
}

func TestTrialFileReadsRecheckLifetimeAndNeverCache(t *testing.T) {
	s := newPinned(t)
	loaded := mustLoad(t, "demo", "trial-one")
	if r, err := readSkillFile(refOf(loaded), "scripts/run.py"); err != nil || field(r, "content") != s.file["content"] {
		t.Fatal(r, err)
	}
	if last := s.hub.payloads[len(s.hub.payloads)-1]; last["trial_id"] != "trial-one" {
		t.Fatal(last)
	}
	s.hub.answer = func(map[string]any) any {
		item := asMap(j(t, host.MustDumps(s.item, host.DumpOpts{})))
		item["trial_id"], item["trial_status"] = "trial-one", "stopped"
		return map[string]any{"items": []any{item}}
	}
	if _, err := readSkillFile(refOf(loaded), "scripts/run.py"); err == nil || !strings.Contains(err.Error(), "skill_changed_or_unavailable") {
		t.Fatal(err)
	}
	s.hub.offline = true
	if _, err := readSkillFile(refOf(loaded), "scripts/run.py"); !hubError(err) {
		t.Fatal(err)
	}
	filepath.WalkDir(routerCacheDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".json") {
			t.Fatal("trial cached:", p)
		}
		return nil
	})
}

func TestLegacyTrialNeverReadsUnversionedLocalFiles(t *testing.T) {
	s := newPinned(t)
	delete(s.item, "bundle")
	loaded := mustLoad(t, "demo", "trial-one")
	if field(field(loaded, "bundle"), "mode") != "unpinned_trial" {
		t.Fatal(loaded)
	}
	if _, err := readSkillFile(refOf(loaded), "scripts/run.py"); err == nil || !strings.Contains(err.Error(), "unpinned_trial_bundle") {
		t.Fatal(err)
	}
}
