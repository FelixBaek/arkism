package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sqlite "github.com/ncruces/go-sqlite3"

	"github.com/FelixBaek/arkism/internal/host"
)

// Prompt and Zcode evidence.

func promptRows(text string) [][]byte {
	rows := []host.Obj{
		{{Key: "type", Value: "session_meta"}, {Key: "payload", Value: host.Obj{{Key: "id", Value: "s"}}}},
		{{Key: "type", Value: "response_item"}, {Key: "payload", Value: host.Obj{{Key: "type", Value: "message"}, {Key: "role", Value: "user"},
			{Key: "content", Value: []any{host.Obj{{Key: "type", Value: "input_text"}, {Key: "text", Value: text}}}}}}},
	}
	out := make([][]byte, len(rows))
	for i, r := range rows {
		out[i] = []byte(host.MustDumps(r, host.DumpOpts{NoASCII: true}) + "\n")
	}
	return out
}

// evidenceHome points the archive roots at a temp home; it returns the server and PC
// Codex folders.
func evidenceHome(t *testing.T) (server, pc string) {
	dir := t.TempDir()
	home, pcRoot, codex := host.Home, archivePC, capCodexSessions
	t.Cleanup(func() { host.Home, archivePC, capCodexSessions = home, pcRoot, codex })
	host.Home, archivePC = filepath.Join(dir, "home"), filepath.Join(dir, "pc")
	server, pc = filepath.Join(host.Home, ".codex/sessions"), filepath.Join(archivePC, "codex-sessions")
	capCodexSessions = server
	os.MkdirAll(server, 0o700)
	os.MkdirAll(pc, 0o700)
	return server, pc
}

const evidencePrompt = "앞으로 짧게 답해"

func TestPromptEvidenceExactPromptUnderDeviceRootWithByteOffset(t *testing.T) {
	server, pc := evidenceHome(t)
	raw := promptRows(evidencePrompt)
	os.WriteFile(filepath.Join(pc, "rollout.jsonl"), bytes.Join(raw, nil), 0o600)
	os.WriteFile(filepath.Join(server, "wrong.jsonl"), bytes.Join(raw, nil), 0o600)
	context, hint, root, err := evidenceLocatePrompt(evidencePromptLocator("codex", "pc", "s", evidencePrompt))
	if err != nil || root != pc {
		t.Fatal(err, root)
	}
	offset, _ := hint.Get("chunk_offset")
	aid, _ := context.Get("archive_id")
	digest, _ := hint.Get("message_sha256")
	want, _ := evidenceRowHint(context, raw[1], int64(len(raw[0])), map[string]any{"role": "user"}, "").Get("message_sha256")
	if !eq(offset, len(raw[0])) || aid != archiveIdentity("pc", "codex", "s", "rollout.jsonl") || digest != want {
		t.Fatal(context, hint)
	}
}

func TestPromptEvidenceMissingAmbiguousAndFilenameOnlyNeverGuess(t *testing.T) {
	for _, mode := range []string{"missing", "filename", "duplicate", "files", "substring"} {
		root, _ := evidenceHome(t)
		raw := promptRows(evidencePrompt)
		path := filepath.Join(root, "s.jsonl")
		switch mode {
		case "filename":
			os.WriteFile(path, raw[1], 0o600)
		case "duplicate":
			os.WriteFile(path, bytes.Join(append(raw, raw[1]), nil), 0o600)
		case "files":
			os.WriteFile(path, bytes.Join(raw, nil), 0o600)
			os.WriteFile(filepath.Join(root, "copy.jsonl"), bytes.Join(raw, nil), 0o600)
		case "substring":
			os.WriteFile(path, bytes.Join(promptRows("추가로 "+evidencePrompt), nil), 0o600)
		}
		want := "archive_prompt_pending"
		if mode == "duplicate" || mode == "files" {
			want = "archive_prompt_ambiguous"
		}
		_, _, _, err := evidenceLocatePrompt(evidencePromptLocator("codex", "server", "s", evidencePrompt))
		if err == nil || err.Error() != want {
			t.Fatal(mode, err)
		}
	}
}

func TestPromptEvidencePcHookMarksDeviceAndSeparatesEventIDs(t *testing.T) {
	dir := capFake(t)
	capOrigin = "codex"
	jobs := map[string]map[string]any{}
	for _, device := range []string{"server", "pc"} {
		t.Setenv("ARKISM_CLIENT", device)
		before := len(spoolJobs(t, filepath.Join(dir, "spool")))
		if _, err := promptCapture(map[string]any{"session_id": "s", "prompt": evidencePrompt}); err != nil {
			t.Fatal(err)
		}
		for _, j := range spoolJobs(t, filepath.Join(dir, "spool")) {
			if l, _ := j["archive_locator"].(map[string]any); l["device"] == device {
				jobs[device] = j
			}
		}
		if len(spoolJobs(t, filepath.Join(dir, "spool"))) != before+1 {
			t.Fatal("not spooled", device)
		}
	}
	locator := jobs["pc"]["archive_locator"].(map[string]any)
	if jobs["server"]["event_id"] == jobs["pc"]["event_id"] || len(locator) != 4 ||
		!evidenceKeys(host.Obj{{Key: "origin"}, {Key: "device"}, {Key: "session_id"}, {Key: "prompt_sha256"}}, keys(locator)...) {
		t.Fatal(jobs)
	}
	argv, err := winSSHCommand("codex", "hook")
	if err != nil || !strings.Contains(argv[len(argv)-1], "ARKISM_CLIENT=pc") {
		t.Fatal(err, argv)
	}
}

// pendingDrain makes every drained job wait on its archive prompt and counts the calls.
func pendingDrain(t *testing.T) *int {
	calls := 0
	keep := processJob
	t.Cleanup(func() { processJob = keep })
	processJob = func(map[string]any, func() error) error { calls++; return runtimeErr("archive_prompt_pending") }
	return &calls
}

func TestPromptEvidenceSyncWaitKeepsRetryBudgetAndRespectsBackoff(t *testing.T) {
	dir := capFake(t)
	spool := filepath.Join(dir, "spool")
	os.MkdirAll(spool, 0o700)
	path := filepath.Join(spool, "job.json")
	os.WriteFile(path, []byte(`{"session_id": "s", "queued_at": "`+host.NowISO()+`", "attempts": 4}`), 0o600)
	calls := pendingDrain(t)
	if err := drain(false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	saved := mustLoad(t, string(raw))
	if _, has := saved["archive_retry_after"]; !eq(saved["attempts"], 4) || !has {
		t.Fatal(saved)
	}
	if err := drain(false); err != nil || *calls != 1 {
		t.Fatal(err, *calls)
	}
}

func TestPromptEvidenceMutatingFileWaitsAndExpiredWaitIsRemoved(t *testing.T) {
	root, _ := evidenceHome(t)
	os.WriteFile(filepath.Join(root, "s.jsonl"), bytes.Join(promptRows(evidencePrompt), nil), 0o600)
	keep := archiveSignature
	t.Cleanup(func() { archiveSignature = keep })
	signatures := [][]any{{1, 2, 3}, {1, 3, 4}}
	archiveSignature = func(string) ([]any, error) { s := signatures[0]; signatures = signatures[1:]; return s, nil }
	_, _, _, err := evidenceLocatePrompt(evidencePromptLocator("codex", "server", "s", evidencePrompt))
	if err == nil || err.Error() != "archive_prompt_pending" {
		t.Fatal(err)
	}

	dir := capFake(t)
	spool := filepath.Join(dir, "spool")
	os.MkdirAll(spool, 0o700)
	path := filepath.Join(spool, "old.json")
	os.WriteFile(path, []byte(`{"session_id": "s", "queued_at": "2020-01-01T00:00:00Z", "attempts": `+strconv.Itoa(capMaxAttempts-1)+`}`), 0o600)
	calls := pendingDrain(t)
	if err := drain(false); err != nil {
		t.Fatal(err)
	}
	if exists(path) || *calls != 0 || exists(filepath.Join(dir, "failed", "old.json")) {
		t.Fatal(*calls)
	}
}

// zcodeDB writes sessions "s" and "other", each with a real-user and a runtime message
// split into two parts stored out of order.
func zcodeDB(t *testing.T, path string, sessionColumn bool) {
	t.Helper()
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	extra := ""
	if sessionColumn {
		extra = ", session_id TEXT"
	}
	sql := []string{`CREATE TABLE session(id TEXT PRIMARY KEY, time_updated INTEGER);
		CREATE TABLE message(id TEXT PRIMARY KEY, session_id TEXT, data TEXT);
		CREATE TABLE part(id TEXT PRIMARY KEY, message_id TEXT, sequence INTEGER, data TEXT` + extra + `);`}
	quote := func(v any) string { return "'" + host.MustDumps(v, host.DumpOpts{}) + "'" }
	for _, sid := range []string{"s", "other"} {
		sql = append(sql, "INSERT INTO session VALUES ('"+sid+"', "+strconv.FormatInt(time.Now().UnixMilli(), 10)+")")
		for _, origin := range []string{"real_user", "agent_runtime"} {
			mid := sid + origin
			sql = append(sql, "INSERT INTO message VALUES ('"+mid+"', '"+sid+"', "+
				quote(host.Obj{{Key: "role", Value: "user"}, {Key: "semantics", Value: host.Obj{{Key: "origin", Value: origin}}}})+")")
			for _, p := range []struct {
				seq  int
				text string
			}{{1, "답은 짧게"}, {0, "앞으로"}} {
				row := "'" + mid + strconv.Itoa(p.seq) + "', '" + mid + "', " + strconv.Itoa(p.seq) + ", " +
					quote(host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: p.text}})
				if sessionColumn {
					row += ", '" + sid + "'"
				}
				sql = append(sql, "INSERT INTO part VALUES ("+row+")")
			}
		}
	}
	if err := db.Exec(strings.Join(sql, ";\n")); err != nil {
		t.Fatal(err)
	}
}

const zcodePrompt = "앞으로\n답은 짧게"

func TestZcodeEvidenceSnapshotJoinsPartsInBothSchemasAndPreservesWireBytes(t *testing.T) {
	for _, sessionColumn := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "db.sqlite")
		zcodeDB(t, path, sessionColumn)
		var kept string
		err := zcodeSnapshot(path, "s", func(snapshot string, observed float64, revision any) error {
			kept = snapshot
			raw, err := os.ReadFile(snapshot)
			if err != nil {
				return err
			}
			var parts int
			for _, line := range bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n")) {
				var row struct {
					Table  string         `json:"table"`
					Record map[string]any `json:"record"`
				}
				json.Unmarshal(line, &row)
				if row.Table == "part" {
					parts++
					if !strings.HasPrefix(str(row.Record["message_id"]), "s") {
						t.Fatal(row)
					}
				}
			}
			hints, err := evidenceZcodeHints(snapshot, evidencePromptLocator("zcode", "server", "s", zcodePrompt))
			if err != nil || parts != 4 || len(hints) != 2 {
				t.Fatal(err, parts, hints)
			}
			at := func(i int) int64 { v, _ := hints[i].(host.Obj).Get("chunk_offset"); f, _ := num(v); return int64(f) }
			if at(0) <= at(1) {
				t.Fatal(hints)
			}
			for i := range hints {
				line, _, _ := bytes.Cut(raw[at(i):], []byte("\n"))
				if !strings.Contains(string(line), `"message_id": "sreal_user"`) && !strings.Contains(string(line), `"message_id":"sreal_user"`) {
					t.Fatal(string(line))
				}
			}
			client := newFakeArchive(t)
			if err := newCollector(client, nil, true).upload(snapshot, "server", "zcode", "s", "db-session", observed, "reconstructed_zcode", revision); err != nil {
				return err
			}
			var saved []byte
			for _, c := range client.commits[0]["chunks"].([]any) {
				saved = append(saved, client.chunks[c.(string)]...)
			}
			first, _ := hints[0].(host.Obj).Get("archive_id")
			if !bytes.Equal(saved, raw) || client.commits[0]["id"] != first {
				t.Fatal(client.commits[0])
			}
			return nil
		})
		if err != nil || exists(kept) {
			t.Fatal(err, kept)
		}
	}
}

func TestZcodeEvidenceRuntimeAndSyntheticPartsDoNotBecomeHumanEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	zcodeDB(t, path, false)
	db, _ := sqlite.Open(path)
	data := host.MustDumps(host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: zcodePrompt}, {Key: "synthetic", Value: true}}, host.DumpOpts{})
	if err := db.Exec("UPDATE part SET data='" + data + "' WHERE message_id='sreal_user'"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	err := zcodeSnapshot(path, "s", func(snapshot string, _ float64, _ any) error {
		_, err := evidenceZcodeHints(snapshot, evidencePromptLocator("zcode", "server", "s", zcodePrompt))
		return err
	})
	if err == nil || err.Error() != "archive_prompt_pending" {
		t.Fatal(err)
	}
}

func TestZcodeEvidenceMultipartReferenceIsCarriedByUniqueClaim(t *testing.T) {
	refs := []any{map[string]any{"archive_id": "a", "chunk_offset": 10}, map[string]any{"archive_id": "a", "chunk_offset": 20}}
	users := []any{map[string]any{"text": zcodePrompt, "archive_refs": refs}}
	claims, err := buildClaims([]any{map[string]any{"text": "짧은 답변 선호", "quote": zcodePrompt}}, zcodePrompt, nil, users)
	if err != nil || len(claims) != 1 ||
		host.MustDumps(claims[0].(map[string]any)["archive_messages"], host.DumpOpts{}) != host.MustDumps(refs, host.DumpOpts{}) {
		t.Fatal(err, claims)
	}
}

func TestZcodeEvidenceMissingSessionAndSymlinkNeverReadUnrelatedDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db.sqlite")
	zcodeDB(t, path, false)
	never := func(string, float64, any) error { t.Fatal("exported"); return nil }
	var missing zcodeMissing
	if err := zcodeSnapshot(path, "missing", never); !errors.As(err, &missing) {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if os.Symlink(path, alias) != nil {
		t.Skip("no symlinks")
	}
	if err := zcodeSnapshot(alias, "s", never); err == nil || err.Error() != "zcode_path_alias" {
		t.Fatal(err)
	}
}

func TestZcodeEvidencePermissionDenialNeverUploads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	zcodeDB(t, path, false)
	var calls []string
	denied := func(route string, _ any, _ time.Duration) (map[string]any, error) {
		calls = append(calls, route)
		return nil, &host.HTTPError{Code: 403, Body: []byte("{}")}
	}
	_, err := evidencePrepareZcode(evidencePromptLocator("zcode", "server", "s", zcodePrompt),
		[]any{map[string]any{"text": zcodePrompt}}, path, denied, map[string]any{})
	var api *APIError
	if !errors.As(err, &api) || len(calls) != 1 || calls[0] != "/v1/session-archives/get" {
		t.Fatal(err, calls)
	}
}

// Codex keeps a prompt with images as input_text blocks around input_image ones and hands
// its hook the blocks joined with nothing; the locator and the server archive text
// (memory TestArchiveCodexInputTextBlocksJoinLikeHookPrompt) must join them the same way.
var codexBlocks = []string{"\n# Browser comments:\n\n## User Comment 1\n버튼 위치", "The next image is page evidence for Comment 1."}

func TestPromptEvidenceCodexMultiBlockPromptFromHookToArchiveCheck(t *testing.T) {
	dir := capFake(t)
	capOrigin = "codex"
	t.Setenv("ARKISM_CLIENT", "server")
	server, _ := evidenceHome(t)
	prompt := strings.Join(codexBlocks, "")
	row := host.Obj{{Key: "type", Value: "response_item"}, {Key: "payload", Value: host.Obj{{Key: "type", Value: "message"}, {Key: "role", Value: "user"},
		{Key: "content", Value: []any{host.Obj{{Key: "type", Value: "input_text"}, {Key: "text", Value: codexBlocks[0]}},
			host.Obj{{Key: "type", Value: "input_image"}, {Key: "image_url", Value: "data:image/png;base64,AA=="}},
			host.Obj{{Key: "type", Value: "input_text"}, {Key: "text", Value: codexBlocks[1]}}}}}}}
	raw := promptRows("")
	raw[1] = []byte(host.MustDumps(row, host.DumpOpts{NoASCII: true}) + "\n")
	os.WriteFile(filepath.Join(server, "rollout-s.jsonl"), bytes.Join(raw, nil), 0o600)

	if _, err := promptCapture(map[string]any{"session_id": "s", "prompt": prompt}); err != nil {
		t.Fatal(err)
	}
	jobs := spoolJobs(t, filepath.Join(dir, "spool"))
	if len(jobs) != 1 || !truthy(jobs[0]["archive_locator"]) {
		t.Fatal(jobs)
	}
	evPrepareUsers = func(_ any, users []any, _ string) ([]any, error) {
		u := users[0].(map[string]any)
		return []any{map[string]any{"text": u["text"], "archive_ref": u["archive_hint"]}}, nil
	}
	for _, archived := range []string{prompt, strings.Join(codexBlocks, "\n")} {
		capPost = func(route string, body any, _ time.Duration) (map[string]any, error) {
			if route != "/v1/session-archives/message" {
				t.Fatal(route)
			}
			ref := body.(map[string]any)["ref"]
			return map[string]any{"authority": "data_only_untrusted", "role": "user", "ref": ref, "text": archived}, nil
		}
		records, _, err := sessionLinkedUsers(jobs[0], "codex")
		if archived == prompt {
			if err != nil || len(records) != 1 || autosaveGet(records[0], "text") != clean(prompt) {
				t.Fatal(err, records)
			}
		} else if err == nil || !strings.Contains(err.Error(), "session_archive_text_mismatch") {
			t.Fatal("newline-joined archive text must not pass for the hook prompt", err)
		}
	}
}
