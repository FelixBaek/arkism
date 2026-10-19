package capture

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	sqlite "github.com/ncruces/go-sqlite3"

	"github.com/FelixBaek/arkism/internal/host"
)

// archiveHome points the collector's home, PC inbox and state file at temp folders.
func archiveHome(t *testing.T) (home, pc string) {
	t.Helper()
	archiveTestSettings(t)
	h, p, s, sl := host.Home, archivePC, archiveState, archiveSleep
	projects, codex := capProjects, capCodexSessions
	t.Cleanup(func() { capProjects, capCodexSessions = projects, codex })
	t.Cleanup(func() { host.Home, archivePC, archiveState, archiveSleep = h, p, s, sl })
	host.Home, archivePC = t.TempDir(), t.TempDir()
	capProjects = filepath.Join(host.Home, ".claude/projects")
	capCodexSessions = filepath.Join(host.Home, ".codex/sessions")
	archiveState = filepath.Join(t.TempDir(), "state", "state.json")
	archiveSleep = func(time.Duration) {}
	t.Setenv("ARKISM_PC_LOCAL", "")
	return host.Home, archivePC
}

func put(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func zcodeExportFile(t *testing.T, dir, sid string, updatedMs int64) string {
	t.Helper()
	header := host.MustDumps(map[string]any{"session": map[string]any{"id": sid, "time_updated": updatedMs}}, host.DumpOpts{}) + "\n"
	return put(t, filepath.Join(dir, sha256Hex([]byte(sid))+".jsonl"), header+`{"table": "message", "record": {"data": "{}"}}`+"\n")
}

func TestArchiveCollectFindsEveryAgentFolderAndSkipsWhatItMust(t *testing.T) {
	archiveTestSettings(t)
	home, pc := archiveHome(t)
	const uuid = "0f8fad5b-d9cb-469f-a165-70867728950e"
	put(t, filepath.Join(home, ".claude/projects/p/a.jsonl"), `{"sessionId": "claude-sid"}`+"\n"+`{"message": {"role": "user", "content": "안녕"}}`+"\n")
	put(t, filepath.Join(home, ".claude/projects/p/rollout-"+uuid+".jsonl"), "not json\n")
	put(t, filepath.Join(home, ".codex/sessions/2026/10/15/r.jsonl"), `{"type": "session_meta", "payload": {"id": "codex-sid"}}`+"\n")
	put(t, filepath.Join(home, ".grok/sessions/grok-sid/summary.json"), `{"a": 1}`+"\n")
	put(t, filepath.Join(home, ".kiro/sessions/kiro-sid/messages.jsonl"), `{"a": 1}`+"\n")
	put(t, filepath.Join(home, ".qwen/projects/q/private.jsonl"), `{"message": {"role": "user", "content": "<private>비밀</private>"}}`+"\n")
	os.Symlink(filepath.Join(home, ".claude/projects/p/a.jsonl"), filepath.Join(home, ".claude/projects/p/link.jsonl"))
	conv := filepath.Join(home, ".gemini/antigravity-cli/conversations/conv.db")
	os.MkdirAll(filepath.Dir(conv), 0o700)
	db, err := sqlite.Open(conv)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE t (x); INSERT INTO t VALUES (1);`)
	db.Close()
	put(t, filepath.Join(pc, "claude-projects/p/b.jsonl"), `{"sessionId": "pc-sid"}`+"\n")
	zcodeExportFile(t, filepath.Join(pc, "zcode-sessions"), "zsid", int64(timeNow()*1000))
	put(t, filepath.Join(pc, "zcode-sessions", "wrong-name.jsonl"), `{"session": {"id": "x", "time_updated": 1}}`+"\n")

	client := newFakeArchive(t)
	state := host.Obj{}
	c := newCollector(client, &state, true)
	c.collect()
	var got []string
	for _, cm := range client.commits {
		got = append(got, str(cm["device"])+"/"+str(cm["origin"])+"/"+str(cm["session_id"])+"/"+str(cm["format"]))
	}
	sort.Strings(got)
	want := []string{"pc/claude_code/pc-sid/jsonl", "pc/zcode/zsid/reconstructed_zcode",
		"server/antigravity-cli/conv/sqlite_snapshot", "server/claude_code/" + uuid + "/jsonl", "server/claude_code/claude-sid/jsonl",
		"server/codex/codex-sid/jsonl", "server/grok/grok-sid/jsonl", "server/kiro/kiro-sid/jsonl"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	n := c.counts.n
	if n["symlink_skipped"] != 1 || n["private_skipped"] != 1 || n["failed"] != 1 || n["committed"] != len(want) {
		t.Fatal(n)
	}
	for _, cm := range client.commits {
		if cm["session_id"] == "claude-sid" && cm["text"] != "안녕" {
			t.Fatal(cm)
		}
	}
	// a second run finds every file unchanged
	again := newCollector(client, &state, true)
	again.collect()
	if again.counts.n["unchanged"] != len(want) || len(client.commits) != len(want) {
		t.Fatal(again.counts.n)
	}
}

func TestArchiveOnThePCTheAgentFoldersAreCountedOnce(t *testing.T) {
	archiveTestSettings(t)
	archiveHome(t)
	t.Setenv("ARKISM_PC_LOCAL", "1")
	var labels []string
	for _, r := range archiveRoots() {
		if r.origin == "claude_code" || r.origin == "codex" {
			labels = append(labels, r.device+":"+strings.TrimPrefix(r.root, host.Home))
		}
	}
	if !reflect.DeepEqual(labels, []string{"pc:/.claude/projects", "pc:/.codex/sessions"}) {
		t.Fatal(labels)
	}
}

func TestArchiveServerZcodeDatabaseIsPlannedOrSnapshotted(t *testing.T) {
	archiveTestSettings(t)
	home, _ := archiveHome(t)
	raw, err := os.ReadFile(filepath.Join("testdata", "zcode_fixture.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	path := put(t, filepath.Join(home, ".zcode/cli/db/db.sqlite"), string(raw))
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Exec("UPDATE session SET time_updated = " + str(int64(timeNow()*1000)))
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	plan := newCollector(nil, nil, false)
	plan.collect()
	if plan.counts.n["planned"] == 0 || plan.counts.n["failed"] != 0 {
		t.Fatal(plan.counts.n)
	}
	client := newFakeArchive(t)
	run := newCollector(client, nil, true)
	run.collect()
	// the fixture's messages are old: the snapshot is made but its producer time keeps it out
	if run.counts.n["failed"] != 0 || run.counts.n["older_than_7d"]+run.counts.n["committed"] == 0 {
		t.Fatal(run.counts.n)
	}
}

func TestArchiveZcodeExportHeaderMustMatchItsName(t *testing.T) {
	archiveTestSettings(t)
	dir := t.TempDir()
	good := zcodeExportFile(t, dir, "s1", 1760000000123)
	sid, observed, err := zcodeExportMetadata(good)
	if err != nil || sid != "s1" || observed != 1760000000.123 {
		t.Fatal(sid, observed, err)
	}
	for name, body := range map[string]string{
		sha256Hex([]byte("other")) + ".jsonl": `{"session": {"id": "s1", "time_updated": 1}}` + "\n",
		"noid.jsonl":                          `{"session": {"time_updated": 1}}` + "\n",
		"notjson.jsonl":                       "{\n",
		"nonewline.jsonl":                     `{"session": {"id": "s1", "time_updated": 1}}`,
	} {
		_, _, err := zcodeExportMetadata(put(t, filepath.Join(dir, name), body))
		if err == nil || !strings.HasPrefix(err.Error(), "zcode_export_") {
			t.Fatal(name, err)
		}
	}
	if _, _, err := zcodeExportMetadata(filepath.Join(dir, "gone.jsonl")); err == nil || err.Error() != "zcode_export_path_invalid" {
		t.Fatal(err)
	}

	var copied string
	err = pcZcodeSnapshot(dir, "s1", func(target string, obs float64, before []any) error {
		b, _ := os.ReadFile(target)
		copied = string(b)
		if obs != observed || len(before) != 3 || target == good {
			t.Fatal(obs, before, target)
		}
		return nil
	})
	src, _ := os.ReadFile(good)
	if err != nil || copied != string(src) {
		t.Fatal(err, copied)
	}
	if err := pcZcodeSnapshot(dir, "none", nil); err == nil || err.Error() != "zcode_export_missing" {
		t.Fatal(err)
	}
	// a file named for s2 whose header says s1
	put(t, filepath.Join(dir, sha256Hex([]byte("s2"))+".jsonl"), string(src))
	if err := pcZcodeSnapshot(dir, "s2", nil); err == nil || err.Error() != "zcode_export_identity_mismatch" {
		t.Fatal(err)
	}
}

func TestArchiveSessionIDFallsBackToTheFileName(t *testing.T) {
	archiveTestSettings(t)
	dir := t.TempDir()
	for _, c := range []struct{ name, origin, body, want string }{
		{"x.jsonl", "codex", `{"type": "session_meta", "payload": {}}` + "\n" + `{"type": "session_meta", "payload": {"id": "late"}}` + "\n", "late"},
		{"plain-name.jsonl", "claude_code", `{"sessionId": ""}` + "\n", "plain-name"},
		{"big.jsonl", "claude_code", strings.Repeat("a", 2*archiveChunk+5) + "\n" + `{"sessionId": "after"}` + "\n", "big"},
	} {
		got, err := archiveSessionID(put(t, filepath.Join(dir, c.name), c.body), c.origin)
		if err != nil || got != c.want {
			t.Fatal(c.name, got, err)
		}
	}
	if _, err := archiveSessionID(filepath.Join(dir, "gone.jsonl"), "codex"); err == nil {
		t.Fatal("missing file")
	}
}

func TestArchiveCommitRejectedForASecretIsRetriedWithoutText(t *testing.T) {
	archiveTestSettings(t)
	path := put(t, filepath.Join(t.TempDir(), "s.jsonl"), `{"message": {"role": "user", "content": "토큰 sk-xxx"}}`+"\n")
	var commits []map[string]any
	poster := posterFunc(func(endpoint string, payload map[string]any) (map[string]any, error) {
		if endpoint == "chunks" {
			return map[string]any{"id": payload["sha256"]}, nil
		}
		commits = append(commits, map[string]any{"text": payload["text"]})
		if len(commits) == 1 {
			return nil, &APIError{Status: 422, Code: "secret_detected"}
		}
		return map[string]any{"state": "active"}, nil
	})
	state := host.Obj{}
	if err := newCollector(poster, &state, true).upload(path, "server", "claude_code", "sid", "s", -1, "jsonl", nil); err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 || commits[0]["text"] != "토큰 sk-xxx" || commits[1]["text"] != "" || len(state) != 1 {
		t.Fatal(commits, state)
	}
	// any other failure is counted, with its status
	poster = posterFunc(func(string, map[string]any) (map[string]any, error) {
		return nil, &APIError{Status: 413, Code: "too_large"}
	})
	c := newCollector(poster, nil, true)
	c.attempt(func() error { return c.upload(path, "server", "claude_code", "sid2", "s", -1, "jsonl", nil) })
	if c.counts.n["failed"] != 1 || c.counts.n["http_413"] != 1 {
		t.Fatal(c.counts.n)
	}
	// an empty file and a chunk answer without an id
	empty := put(t, filepath.Join(t.TempDir(), "e.jsonl"), "")
	c = newCollector(poster, nil, true)
	if err := c.upload(empty, "server", "codex", "e", "e", -1, "jsonl", nil); err != nil || c.counts.n["empty"] != 1 {
		t.Fatal(err, c.counts.n)
	}
	poster = posterFunc(func(string, map[string]any) (map[string]any, error) { return map[string]any{}, nil })
	if err := newCollector(poster, nil, true).upload(path, "server", "claude_code", "sid3", "s", -1, "jsonl", nil); pyType(err) != "KeyError" {
		t.Fatal(err)
	}
	if err := newCollector(poster, nil, true).upload(path, "server", "claude_code", "sid4", "s", timeNow()+3600, "jsonl", nil); err == nil || err.Error() != "future_time" {
		t.Fatal(err)
	}
}

type posterFunc func(string, map[string]any) (map[string]any, error)

func (f posterFunc) post(endpoint string, payload map[string]any) (map[string]any, error) {
	return f(endpoint, payload)
}

// archiveAPI serves the session-archive API on loopback; reply decides each answer.
func archiveAPI(t *testing.T, reply func(n int, path string, body map[string]any) (int, string)) {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		if body["scope"] == nil {
			t.Error("request without scope")
		}
		n++
		code, text := reply(n, r.URL.Path, body)
		w.WriteHeader(code)
		w.Write([]byte(text))
	}))
	t.Cleanup(srv.Close)
	token := put(t, filepath.Join(t.TempDir(), "token"), "tok\n")
	u, tf := host.BaseURL, host.TokenFile
	t.Cleanup(func() { host.BaseURL, host.TokenFile = u, tf })
	host.BaseURL, host.TokenFile = srv.URL, token
}

func TestArchiveClientRetriesBusyAnswersAndNamesTheFailure(t *testing.T) {
	archiveTestSettings(t)
	archiveHome(t)
	var paths []string
	archiveAPI(t, func(n int, path string, _ map[string]any) (int, string) {
		paths = append(paths, path)
		switch {
		case n == 1:
			return 409, `{"error": {"code": "busy"}}`
		case n == 2:
			return 200, `{"id": "c1"}`
		case n <= 5:
			return 503, `{"code": "overloaded"}`
		case n == 6:
			return 400, `{"error": "bad_chunk"}`
		case n == 7:
			return 400, `not json`
		}
		return 200, `[1]`
	})
	got, err := archiveClient{}.post("chunks", map[string]any{"x": 1})
	if err != nil || got["id"] != "c1" || !reflect.DeepEqual(paths, []string{"/v1/session-archives/chunks", "/v1/session-archives/chunks"}) {
		t.Fatal(got, err, paths)
	}
	for _, want := range []string{`(503, "overloaded")`, `(400, "bad_chunk")`, `(400, "http_error")`} {
		if _, err := (archiveClient{}).post("commit", nil); err == nil || err.Error() != want {
			t.Fatal(want, err)
		}
	}
	if _, err := (archiveClient{}).post("commit", nil); pyType(err) != "ValueError" { // an answer that is not an object
		t.Fatal(err)
	}
	host.BaseURL = "http://127.0.0.1:1" // nothing listens: unavailable after three tries
	if _, err := (archiveClient{}).post("commit", nil); err == nil || err.Error() != `(0, "unavailable")` {
		t.Fatal(err)
	}
}

func TestArchiveMainParsesArgumentsLikeArgparse(t *testing.T) {
	archiveTestSettings(t)
	archiveHome(t)
	for _, c := range []struct {
		args []string
		code int
		err  string
	}{
		{[]string{"-h"}, 0, ""},
		{nil, 2, "the following arguments are required: command"},
		{[]string{"upload"}, 2, "invalid choice: 'upload'"},
		{[]string{"collect", "--bogus"}, 2, "unrecognized arguments: --bogus"},
		{[]string{"migrate", "--database"}, 2, "argument --database: expected one argument"},
		{[]string{"migrate"}, 2, "--database is required"},
		{[]string{"migrate", "--database=" + filepath.Join(t.TempDir(), "none.db")}, 1, ""},
		{[]string{"--execute", "expire"}, 1, "FileNotFoundError:"},
	} {
		host.TokenFile = filepath.Join(t.TempDir(), "no-token")
		var out, errOut bytes.Buffer
		code := sessionArchiveMain(c.args, nil, &out, &errOut)
		if code != c.code || !strings.Contains(errOut.String(), c.err) {
			t.Fatal(c.args, code, errOut.String())
		}
	}
}

func TestArchiveMainPlansCollectsAndExpires(t *testing.T) {
	archiveTestSettings(t)
	home, _ := archiveHome(t)
	put(t, filepath.Join(home, ".claude/projects/p/a.jsonl"), `{"sessionId": "s"}`+"\n")
	var out, errOut bytes.Buffer
	if code := sessionArchiveMain([]string{"collect"}, nil, &out, &errOut); code != 0 || strings.TrimSpace(out.String()) != `{"planned": 1}` {
		t.Fatal(code, out.String(), errOut.String())
	}
	if _, err := os.Stat(archiveState); !os.IsNotExist(err) { // a plan writes no state
		t.Fatal(err)
	}
	expires := 0
	archiveAPI(t, func(_ int, path string, body map[string]any) (int, string) {
		switch path {
		case "/v1/session-archives/chunks":
			return 200, `{"id": "` + str(body["sha256"]) + `"}`
		case "/v1/session-archives/commit":
			return 200, `{"state": "active"}`
		case "/v1/session-archives/expire":
			expires++
			if expires == 1 {
				return 200, `{"expired": ["a", "b"], "orphans": 1}`
			}
			return 200, `{"expired": [], "orphans": 0}`
		}
		return 404, `{}`
	})
	out.Reset()
	if code := sessionArchiveMain([]string{"collect", "--execute"}, nil, &out, &errOut); code != 0 || strings.TrimSpace(out.String()) != `{"committed": 1}` {
		t.Fatal(code, out.String(), errOut.String())
	}
	raw, err := os.ReadFile(archiveState)
	if err != nil || !strings.Contains(string(raw), `"state": "active"`) {
		t.Fatal(err, string(raw))
	}
	out.Reset()
	if code := sessionArchiveMain([]string{"expire", "--execute"}, nil, &out, &errOut); code != 0 || strings.TrimSpace(out.String()) != `{"expired": 2, "orphans": 1}` || expires != 2 {
		t.Fatal(code, out.String(), errOut.String())
	}
	os.WriteFile(archiveState, []byte("{torn"), 0o600)
	if code := sessionArchiveMain([]string{"collect"}, nil, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "JSONDecodeError") {
		t.Fatal(code, errOut.String())
	}
}

func TestArchiveAndUsageShareConfiguredTranscriptRoots(t *testing.T) {
	_, pc := archiveHome(t)
	capProjects = filepath.Join(t.TempDir(), "projects")
	capCodexSessions = filepath.Join(t.TempDir(), "sessions")
	for _, local := range []string{"", "1"} {
		t.Setenv("ARKISM_PC_LOCAL", local)
		for _, roots := range [][]archiveRoot{archiveRoots(), usageSessionRoots()} {
			for _, origin := range []string{"claude_code", "codex"} {
				localRoot, inbox := capProjects, "claude-projects"
				if origin == "codex" {
					localRoot, inbox = capCodexSessions, "codex-sessions"
				}
				var got []string
				for _, r := range roots {
					if r.origin == origin {
						got = append(got, r.device+":"+r.root)
					}
				}
				want := []string{"server:" + localRoot, "pc:" + filepath.Join(pc, inbox)}
				if local == "1" {
					want = []string{"pc:" + localRoot}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("local=%q origin=%s got=%v want=%v", local, origin, got, want)
				}
			}
		}
	}
}
