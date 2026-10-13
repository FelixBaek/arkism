package capture

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// evidenceTranscript writes one Claude Code user row for session "s" under root.
func evidenceTranscript(t *testing.T, root string) (string, []byte, host.Obj) {
	t.Helper()
	message := host.Obj{{Key: "role", Value: "user"}, {Key: "content", Value: "앞으로 답은 짧게 해주세요"}}
	row := host.Obj{{Key: "type", Value: "user"}, {Key: "sessionId", Value: "s"}, {Key: "timestamp", Value: host.NowISO()}, {Key: "message", Value: message}}
	raw := []byte(host.MustDumps(row, host.DumpOpts{NoASCII: true}) + "\n")
	path := filepath.Join(root, "s.jsonl")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, raw, message
}

func TestEvidenceLocationUsesBytesAndCollectorIdentity(t *testing.T) {
	root := t.TempDir()
	path, raw, message := evidenceTranscript(t, root)
	padding := append([]byte(strings.Repeat(" ", archiveChunk+9)), '\n')
	os.WriteFile(path, append(append([]byte{}, padding...), raw...), 0o600)
	context := evidenceClaudeContext(path, root, "s", "server")
	hint := evidenceRowHint(context, raw, int64(len(padding)), message, "")
	index, _ := hint.Get("chunk_index")
	offset, _ := hint.Get("chunk_offset")
	aid, _ := hint.Get("archive_id")
	digest, _ := hint.Get("message_sha256")
	if !eq(index, 1) || !eq(offset, 10) || aid != archiveIdentity("server", "claude_code", "s", "s.jsonl") || len(digest.(string)) != 64 {
		t.Fatal(hint)
	}
}

func TestEvidenceContextRejectsAliasesOutsideRootAndWrongSession(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside")
	os.Mkdir(inside, 0o700)
	path, _, _ := evidenceTranscript(t, root)
	if evidenceClaudeContext(path, inside, "s", "server") != nil || evidenceClaudeContext(path, root, "different", "server") != nil {
		t.Fatal("accepted a foreign transcript")
	}
	alias := filepath.Join(root, "alias.jsonl")
	os.Symlink(path, alias)
	if evidenceClaudeContext(alias, root, "s", "server") != nil {
		t.Fatal("accepted an alias")
	}
	if evidenceClaudeContext(path, root, "s", "server") == nil {
		t.Fatal("rejected the transcript")
	}
}

func TestEvidenceMissingArchiveIsUploadedOnceThenVerified(t *testing.T) {
	root := t.TempDir()
	path, raw, message := evidenceTranscript(t, root)
	context := evidenceClaudeContext(path, root, "s", "server")
	hint := evidenceRowHint(context, raw, 0, message, "")
	users := []any{map[string]any{"text": "앞으로 답은 짧게 해주세요", "archive_hint": hint}}
	var calls []string
	committed := false
	post := func(route string, payload any, _ time.Duration) (map[string]any, error) {
		p := payload.(map[string]any)
		endpoint := route[strings.LastIndex(route, "/")+1:]
		calls = append(calls, endpoint)
		switch endpoint {
		case "get":
			if !committed {
				return nil, &host.HTTPError{Code: 404, Body: []byte("{}")}
			}
			reply := map[string]any{"state": "active", "sha256": sha256Hex(raw)}
			for _, kv := range context {
				reply[kv.Key] = kv.Value
			}
			return reply, nil
		case "chunks":
			data, _ := base64.StdEncoding.DecodeString(p["data"].(string))
			if string(data) != string(raw) {
				t.Fatal("chunk bytes differ")
			}
			return map[string]any{"id": p["sha256"]}, nil
		case "commit":
			committed = true
			return map[string]any{"state": "active"}, nil
		case "message":
			return map[string]any{"ref": p["ref"], "role": "user", "authority": "data_only_untrusted"}, nil
		}
		t.Fatal(endpoint)
		return nil, nil
	}
	result, err := evidencePrepareUsers(context, users, root, post, map[string]any{"workspace_id": "w", "project_id": "p"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "get,chunks,commit,get,message" {
		t.Fatal(calls)
	}
	ref, _ := objGet(result[0], "archive_ref")
	got, _ := objGet(ref, "message_sha256")
	want, _ := hint.Get("message_sha256")
	if got != want {
		t.Fatal(ref)
	}
	if _, has := users[0].(map[string]any)["archive_ref"]; has {
		t.Fatal("input user changed")
	}
}

func TestEvidenceCollectorErrorContractPreservesSecretProjectionFallback(t *testing.T) {
	client := evidenceClient{func(string, any, time.Duration) (map[string]any, error) {
		return nil, &host.HTTPError{Code: 422, Body: []byte(`{"error":{"code":"secret_detected"}}`)}
	}, map[string]any{}}
	_, err := client.post("commit", map[string]any{})
	var api *APIError
	if !errors.As(err, &api) || api.Status != 422 || api.Code != "secret_detected" {
		t.Fatal(err)
	}
}

// test_arkism_pc_zcode: a missing PC export never falls back to the server database.
func TestEvidencePcMissingNeverFallsBackToServerDatabase(t *testing.T) {
	prompt := "앞으로\n답은 짧게"
	locator := evidencePromptLocator("zcode", "pc", "s", prompt)
	post := func(string, any, time.Duration) (map[string]any, error) {
		t.Fatal("unexpected post")
		return nil, nil
	}
	_, err := evidencePrepareZcode(locator, []any{map[string]any{"text": prompt}}, filepath.Join(t.TempDir(), "missing"), post, map[string]any{})
	if err == nil || err.Error() != "archive_prompt_pending" {
		t.Fatal(err)
	}
}

// The row hint across a chunk boundary and the Zcode part hints come from Python.
func TestEvidenceHintsMatchPythonGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/evidence_hints.json")
	if err != nil {
		t.Fatal(err)
	}
	want, err := loadsOrdered(raw)
	if err != nil {
		t.Fatal(err)
	}
	get := func(k string) any { v, _ := objGet(want, k); return v }
	offset, _ := num(get("offset"))
	hint := evidenceRowHint(host.Obj{{Key: "archive_id", Value: strings.Repeat("b", 64)}}, []byte(get("raw").(string)), int64(offset),
		map[string]any{"role": "user"}, "")
	if canon(hint) != canon(get("row_hint")) || host.MustDumps(hint, host.DumpOpts{}) != host.MustDumps(get("row_hint"), host.DumpOpts{}) {
		t.Fatalf("row hint %v, want %v", hint, get("row_hint"))
	}
	hints, err := evidenceZcodeHints("testdata/evidence_zcode_snapshot.jsonl", get("locator").(host.Obj))
	if err != nil {
		t.Fatal(err)
	}
	if host.MustDumps(hints, host.DumpOpts{}) != host.MustDumps(get("zcode_hints"), host.DumpOpts{}) {
		t.Fatalf("zcode hints %s\nwant %s", host.MustDumps(hints, host.DumpOpts{}), host.MustDumps(get("zcode_hints"), host.DumpOpts{}))
	}
}
