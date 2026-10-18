package capture

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	sqlite "github.com/ncruces/go-sqlite3"

	"github.com/FelixBaek/arkism/internal/host"
)

func setArchiveSettings(t *testing.T, settings any) {
	t.Helper()
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(captureSettingsPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestArchivePrivateScansEveryJSONExportBeforeFirstRequest(t *testing.T) {
	for _, format := range []string{"jsonl", "reconstructed_zcode", "reconstructed_agentsview"} {
		t.Run(format, func(t *testing.T) {
			archiveTestSettings(t)
			// The encoded tag begins after one entire transport chunk and ends
			// in another scanner read. No initial public chunk may be uploaded.
			raw := `{"message":{"role":"user","content":"` + strings.Repeat("x", archiveChunk+(64<<10)-42) + `\u003c\u0050\u0052\u0049\u0056\u0041\u0054\u0045\u003e synthetic"}}` + "\n"
			path := put(t, filepath.Join(t.TempDir(), "session.jsonl"), raw)
			client := newFakeArchive(t)
			state := host.Obj{}
			c := newCollector(client, &state, true)
			if err := c.upload(path, "server", "codex", "s", "s", timeNow(), format, nil); err != nil {
				t.Fatal(err)
			}
			if len(client.chunks) != 0 || len(client.commits) != 0 || len(state) != 0 || c.counts.n["private_skipped"] != 1 {
				t.Fatal("private export reached transport", c.counts.n)
			}
		})
	}
}

// Find a real SQLite overflow-page boundary which interrupts a tag in the file
// while preserving it in the logical TEXT/BLOB value. The test does not assume a
// driver-specific record layout, and proves the logical scan is necessary.
func sqliteOverflowPrivateFixture(t *testing.T, blob bool) string {
	t.Helper()
	dir := t.TempDir()
	for padding := 460; padding < 1100; padding++ {
		path := filepath.Join(dir, "candidate.db")
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		db, err := sqlite.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`PRAGMA page_size=512; CREATE TABLE transcript (content);`); err != nil {
			t.Fatal(err)
		}
		stmt, _, err := db.Prepare(`INSERT INTO transcript VALUES (?)`)
		if err != nil {
			t.Fatal(err)
		}
		value := strings.Repeat("x", padding) + "<private>synthetic-overflow-span</private>" + strings.Repeat("y", 4000-padding)
		if blob {
			err = stmt.BindBlob(1, []byte(value))
		} else {
			err = stmt.BindText(1, value)
		}
		if err != nil {
			t.Fatal(err)
		}
		if !stmt.Step() && stmt.Err() != nil {
			t.Fatal(stmt.Err())
		}
		stmt.Close()
		db.Close()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		marked, err := scanPrivate(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if !marked {
			return path
		}
	}
	t.Fatal("could not construct an interrupted SQLite marker")
	return ""
}

func TestArchivePrivateSQLiteLogicalTextAndBlobBeforeTransport(t *testing.T) {
	for _, blob := range []bool{false, true} {
		name := "TEXT"
		if blob {
			name = "BLOB"
		}
		t.Run(name, func(t *testing.T) {
			archiveTestSettings(t)
			path := sqliteOverflowPrivateFixture(t, blob)
			client := newFakeArchive(t)
			c := newCollector(client, nil, true)
			if err := c.upload(path, "server", "antigravity-cli", "s", "s", timeNow(), "sqlite_snapshot", nil); err != nil {
				t.Fatal(err)
			}
			if len(client.chunks) != 0 || len(client.commits) != 0 || c.counts.n["private_skipped"] != 1 {
				t.Fatal(c.counts.n)
			}
		})
	}
}

func TestArchiveUploadRespectsSourceAndRecordedProjectOptOut(t *testing.T) {
	for _, mode := range []string{"paused", "source_excluded", "source_ignore", "cwd_excluded", "cwd_ignore", "nested_project", "bom_cwd", "original_source"} {
		t.Run(mode, func(t *testing.T) {
			archiveTestSettings(t)
			dir := t.TempDir()
			project := filepath.Join(dir, "project")
			if err := os.MkdirAll(project, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "inbox", "s.jsonl")
			header := map[string]any{"payload": map[string]any{"cwd": project}}
			if mode == "nested_project" {
				header = map[string]any{"record": map[string]any{"data": host.MustDumps(map[string]any{"project_path": project}, host.DumpOpts{})}}
			}
			raw := host.MustDumps(header, host.DumpOpts{}) + "\n" + codexRow(timeNow(), "synthetic visible message")
			if mode == "bom_cwd" {
				raw = "\xef\xbb\xbf" + raw
			}
			put(t, path, raw)
			var sourcePaths []string
			switch mode {
			case "paused":
				setArchiveSettings(t, map[string]any{"paused": true})
			case "source_excluded":
				setArchiveSettings(t, map[string]any{"exclude": []string{filepath.Dir(path)}})
			case "source_ignore":
				put(t, filepath.Join(filepath.Dir(path), captureIgnoreFile), "")
			case "cwd_ignore":
				put(t, filepath.Join(project, captureIgnoreFile), "")
			case "original_source":
				source := put(t, filepath.Join(dir, "original", "source.db"), "synthetic source")
				put(t, filepath.Join(filepath.Dir(source), captureIgnoreFile), "")
				sourcePaths = []string{source}
			default:
				setArchiveSettings(t, map[string]any{"exclude": []string{project}})
			}
			client := newFakeArchive(t)
			state := host.Obj{}
			c := newCollector(client, &state, true)
			if err := c.upload(path, "server", "codex", "s", "s", timeNow(), "jsonl", nil, sourcePaths...); err != nil {
				t.Fatal(err)
			}
			if len(client.chunks) != 0 || len(client.commits) != 0 || len(state) != 0 || c.counts.n["capture_skipped"] != 1 {
				t.Fatal(c.counts.n)
			}
		})
	}
}

func TestArchivePolicyRecheckedBeforeFollowingChunkAndCommit(t *testing.T) {
	for _, mode := range []string{"pause_before_chunk", "exclude_before_chunk", "ignore_before_chunk", "pause_before_commit"} {
		t.Run(mode, func(t *testing.T) {
			archiveTestSettings(t)
			text := "synthetic message"
			if strings.HasSuffix(mode, "chunk") {
				text = strings.Repeat("x", archiveChunk+20)
			}
			path := put(t, filepath.Join(t.TempDir(), "session.jsonl"), codexRow(timeNow(), text))
			client := newFakeArchive(t)
			client.mutate = func() {
				switch mode {
				case "exclude_before_chunk":
					setArchiveSettings(t, map[string]any{"exclude": []string{filepath.Dir(path)}})
				case "ignore_before_chunk":
					put(t, filepath.Join(filepath.Dir(path), captureIgnoreFile), "")
				default:
					setArchiveSettings(t, map[string]any{"paused": true})
				}
			}
			state := host.Obj{}
			c := newCollector(client, &state, true)
			if err := c.upload(path, "server", "codex", "s", "s", timeNow(), "jsonl", nil); err != nil {
				t.Fatal(err)
			}
			if len(client.chunks) != 1 || len(client.commits) != 0 || len(state) != 0 || c.counts.n["capture_skipped"] != 1 {
				t.Fatal(c.counts.n)
			}
		})
	}
}

func TestArchiveNativeTransportRetryRechecksPause(t *testing.T) {
	archiveHome(t)
	path := put(t, filepath.Join(t.TempDir(), "s.jsonl"), codexRow(timeNow(), "synthetic content"))
	calls := 0
	archiveAPI(t, func(_ int, _ string, _ map[string]any) (int, string) {
		calls++
		setArchiveSettings(t, map[string]any{"paused": true})
		return 503, `{"code":"busy"}`
	})
	state := host.Obj{}
	c := newCollector(archiveClient{}, &state, true)
	if err := c.upload(path, "server", "codex", "s", "s", timeNow(), "jsonl", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(state) != 0 || c.counts.n["capture_skipped"] != 1 {
		t.Fatal(calls, c.counts.n)
	}
}

func TestArchiveProjectionRetryRechecksPolicy(t *testing.T) {
	archiveTestSettings(t)
	path := put(t, filepath.Join(t.TempDir(), "s.jsonl"), codexRow(timeNow(), "synthetic content"))
	calls := 0
	poster := posterFunc(func(endpoint string, payload map[string]any) (map[string]any, error) {
		calls++
		if endpoint == "chunks" {
			return map[string]any{"id": payload["sha256"]}, nil
		}
		setArchiveSettings(t, map[string]any{"paused": true})
		return nil, &APIError{Status: 422, Code: "secret_detected"}
	})
	state := host.Obj{}
	c := newCollector(poster, &state, true)
	if err := c.upload(path, "server", "codex", "s", "s", timeNow(), "jsonl", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(state) != 0 || c.counts.n["capture_skipped"] != 1 {
		t.Fatal(calls, c.counts.n)
	}
}

func TestArchiveInspectionFailuresNeverReachTransport(t *testing.T) {
	for _, mode := range []string{"malformed_sqlite", "malformed_metadata", "oversized_metadata", "source_directory"} {
		t.Run(mode, func(t *testing.T) {
			archiveTestSettings(t)
			path := filepath.Join(t.TempDir(), "s")
			format := "jsonl"
			switch mode {
			case "malformed_sqlite":
				format = "sqlite_snapshot"
				put(t, path, "not a database")
			case "malformed_metadata":
				put(t, path, `{"cwd":"/synthetic",`)
			case "oversized_metadata":
				put(t, path, `{"content":"`+strings.Repeat("x", archiveMetadataRecordBytes)+`"}`)
			case "source_directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			client := newFakeArchive(t)
			c := newCollector(client, nil, true)
			if err := c.upload(path, "server", "synthetic", "s", "s", timeNow(), format, nil); err == nil {
				t.Fatal("inspection failure accepted")
			}
			if len(client.chunks) != 0 || len(client.commits) != 0 {
				t.Fatal("inspection failure reached transport")
			}
		})
	}
}

func TestArchiveSourceMutationCannotAddUninspectedBytes(t *testing.T) {
	archiveTestSettings(t)
	raw := []byte(codexRow(timeNow(), strings.Repeat("visible ", archiveChunk/8+100)))
	path := put(t, filepath.Join(t.TempDir(), "s.jsonl"), string(raw))
	var sent []byte
	calls, commits := 0, 0
	client := posterFunc(func(endpoint string, payload map[string]any) (map[string]any, error) {
		if endpoint == "commit" {
			commits++
			return map[string]any{"state": "active"}, nil
		}
		part, err := base64.StdEncoding.DecodeString(payload["data"].(string))
		if err != nil {
			t.Fatal(err)
		}
		sent = append(sent, part...)
		calls++
		if calls == 1 {
			put(t, path, codexRow(timeNow(), "<private>synthetic replacement</private>"))
		}
		return map[string]any{"id": payload["sha256"]}, nil
	})
	c := newCollector(client, nil, true)
	if err := c.upload(path, "server", "codex", "s", "s", timeNow(), "jsonl", nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sent, raw) || commits != 0 || c.counts.n["changed_during_read"] != 1 {
		t.Fatal("source mutation altered staged transport")
	}
}

func TestArchiveStagingCleanupAndExactBytes(t *testing.T) {
	archiveTestSettings(t)
	path := put(t, filepath.Join(t.TempDir(), "s.jsonl"), codexRow(timeNow(), "synthetic visible content"))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, cleanup, err := archiveSnapshot(path, "jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	actual, err := os.ReadFile(snapshot)
	if err != nil || !bytes.Equal(actual, raw) {
		t.Fatal("snapshot changed source bytes", err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(filepath.Dir(snapshot)); err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatal("staging directory not private", err)
		}
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(snapshot)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned staging directory remains", err)
	}
	if still, err := os.ReadFile(path); err != nil || !bytes.Equal(still, raw) {
		t.Fatal("source changed during cleanup", err)
	}
}

func TestArchivePrettyJSONKeepsExactBytesAndChecksProject(t *testing.T) {
	archiveTestSettings(t)
	project := t.TempDir()
	raw, err := json.MarshalIndent(map[string]any{"cwd": project, "message": map[string]any{"role": "user", "content": "synthetic visible message"}}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := put(t, filepath.Join(t.TempDir(), "summary.json"), string(raw)+"\n")
	client := newFakeArchive(t)
	c := newCollector(client, nil, true)
	if err := c.upload(path, "server", "grok", "s", "s", timeNow(), "jsonl", nil); err != nil {
		t.Fatal(err)
	}
	if len(client.commits) != 1 {
		t.Fatal(c.counts.n)
	}
	var received []byte
	for _, id := range client.commits[0]["chunks"].([]any) {
		received = append(received, client.chunks[id.(string)]...)
	}
	if !bytes.Equal(received, append(raw, '\n')) {
		t.Fatal("pretty JSON changed in transport")
	}
	setArchiveSettings(t, map[string]any{"exclude": []string{project}})
	blocked := newFakeArchive(t)
	c = newCollector(blocked, nil, true)
	if err := c.upload(path, "server", "grok", "s", "s", timeNow(), "jsonl", nil); err != nil {
		t.Fatal(err)
	}
	if len(blocked.chunks) != 0 || c.counts.n["capture_skipped"] != 1 {
		t.Fatal(c.counts.n)
	}
}

func TestArchiveSQLiteRecordedProjectPathsBeforeTransport(t *testing.T) {
	for _, mode := range []string{"column_text", "column_blob", "json_text", "json_blob"} {
		t.Run(mode, func(t *testing.T) {
			archiveTestSettings(t)
			project := t.TempDir()
			path := filepath.Join(t.TempDir(), "source.db")
			db, err := sqlite.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			column := "cwd"
			value := project
			if strings.HasPrefix(mode, "json") {
				column = "data"
				value = host.MustDumps(map[string]any{"cwd": project}, host.DumpOpts{})
			}
			if err := db.Exec(`CREATE TABLE session (` + column + `);`); err != nil {
				t.Fatal(err)
			}
			stmt, _, err := db.Prepare(`INSERT INTO session VALUES (?)`)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(mode, "blob") {
				err = stmt.BindBlob(1, []byte(value))
			} else {
				err = stmt.BindText(1, value)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !stmt.Step() && stmt.Err() != nil {
				t.Fatal(stmt.Err())
			}
			stmt.Close()
			db.Close()
			setArchiveSettings(t, map[string]any{"exclude": []string{project}})
			client := newFakeArchive(t)
			c := newCollector(client, nil, true)
			if err := c.file(path, "server", "antigravity-cli", "s", "s", timeNow()); err != nil {
				t.Fatal(err)
			}
			if len(client.chunks) != 0 || len(client.commits) != 0 || c.counts.n["capture_skipped"] != 1 {
				t.Fatal(c.counts.n)
			}
		})
	}
}

func TestArchiveDatabaseSourcePolicyIsPreservedAfterReconstruction(t *testing.T) {
	for _, mode := range []string{"sqlite", "zcode", "agentsview"} {
		t.Run(mode, func(t *testing.T) {
			archiveTestSettings(t)
			path := filepath.Join(t.TempDir(), "source.db")
			db, err := sqlite.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "sqlite":
				err = db.Exec(`CREATE TABLE transcript(content TEXT); INSERT INTO transcript VALUES ('synthetic content');`)
			case "zcode":
				err = db.Exec(`CREATE TABLE session(id TEXT,time_updated INTEGER); INSERT INTO session VALUES('s',` + str(int64(timeNow()*1000)) + `);`)
			case "agentsview":
				err = db.Exec(`CREATE TABLE sessions(id TEXT,agent TEXT,source_session_id TEXT,file_path TEXT,ended_at TEXT,file_mtime INTEGER,message_count INTEGER,deleted_at TEXT); CREATE TABLE messages(id INTEGER,session_id TEXT,ordinal INTEGER,role TEXT,content TEXT); CREATE TABLE tool_calls(id INTEGER,session_id TEXT,call_index INTEGER); INSERT INTO sessions VALUES('s','claude','s','/synthetic/source.jsonl','` + archiveISO(timeNow()) + `',0,1,NULL); INSERT INTO messages VALUES(1,'s',1,'user','synthetic content');`)
			}
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			client := newFakeArchive(t)
			client.mutate = func() { setArchiveSettings(t, map[string]any{"exclude": []string{filepath.Dir(path)}}) }
			state := host.Obj{}
			c := newCollector(client, &state, true)
			switch mode {
			case "sqlite":
				err = c.file(path, "server", "antigravity-cli", "s", "s", timeNow())
			case "zcode":
				err = c.zcode(path)
			case "agentsview":
				err = c.migrate(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(client.chunks) != 1 || len(client.commits) != 0 || len(state) != 0 || c.counts.n["capture_skipped"] != 1 {
				t.Fatal(c.counts.n)
			}
		})
	}
}

func TestArchiveJSONDryRunInspectsWithoutRequestsOrStaging(t *testing.T) {
	archiveTestSettings(t)
	project := t.TempDir()
	for _, mode := range []string{"public", "private", "excluded"} {
		t.Run(mode, func(t *testing.T) {
			setArchiveSettings(t, map[string]any{})
			raw := codexRow(timeNow(), "synthetic public message")
			if mode == "private" {
				raw = codexRow(timeNow(), "<private>synthetic hidden message</private>")
			}
			if mode == "excluded" {
				raw = host.MustDumps(map[string]any{"cwd": project}, host.DumpOpts{}) + "\n" + raw
				setArchiveSettings(t, map[string]any{"exclude": []string{project}})
			}
			path := put(t, filepath.Join(t.TempDir(), "s.jsonl"), raw)
			client := newFakeArchive(t)
			state := host.Obj{}
			c := newCollector(client, &state, false)
			if err := c.file(path, "server", "codex", "s", "s", timeNow()); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"public": "planned", "private": "private_skipped", "excluded": "capture_skipped"}[mode]
			if c.counts.n[want] != 1 || len(client.chunks) != 0 || len(client.commits) != 0 || len(state) != 0 {
				t.Fatal(c.counts.n)
			}
		})
	}
}

func TestArchiveBoundedWriterRejectsOverflowAndPreservesSuccess(t *testing.T) {
	var output bytes.Buffer
	writer := &archiveBoundedWriter{writer: &output, remaining: 5}
	if n, err := writer.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	if n, err := writer.Write([]byte("def")); err == nil || err.Error() != "too_large" || n != 0 {
		t.Fatal(n, err)
	}
	if output.String() != "abc" {
		t.Fatal("oversized output was partially accepted")
	}
	if n, err := writer.Write([]byte("de")); err != nil || n != 2 || output.String() != "abcde" {
		t.Fatal(n, err)
	}
}

func TestArchiveReconstructionPropagatesActualOutputLimit(t *testing.T) {
	archiveTestSettings(t)
	path := filepath.Join(t.TempDir(), "source.db")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE session(id TEXT,time_updated INTEGER); INSERT INTO session VALUES('s',1); CREATE TABLE messages(id INTEGER,session_id TEXT,ordinal INTEGER,role TEXT,content TEXT); CREATE TABLE tool_calls(id INTEGER,session_id TEXT,call_index INTEGER); INSERT INTO messages VALUES(1,'s',1,'user','synthetic content');`); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, format := range []string{"zcode", "agentsview"} {
		t.Run(format, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "session.jsonl")
			var err error
			if format == "zcode" {
				_, err = writeZcodeSnapshotLimit(path, "s", target, 8)
			} else {
				err = writeAgentsViewLimit(db, host.Obj{{Key: "id", Value: "s"}}, "s", target, 8)
			}
			if err == nil || err.Error() != "too_large" {
				t.Fatal("actual reconstruction writer accepted truncated output", err)
			}
			info, err := os.Stat(target)
			if err != nil || info.Size() > 8 {
				t.Fatal("reconstruction wrote beyond its bound", err)
			}
		})
	}
}

func TestArchiveInspectionFailureCleansOnlyOwnedStaging(t *testing.T) {
	archiveTestSettings(t)
	staging := t.TempDir()
	t.Setenv("TMPDIR", staging)
	t.Setenv("TMP", staging)
	t.Setenv("TEMP", staging)
	sourceDir := t.TempDir()
	for _, mode := range []string{"private", "invalid_sqlite"} {
		format := "jsonl"
		raw := codexRow(timeNow(), "<private>synthetic private input</private>")
		if mode == "invalid_sqlite" {
			format = "sqlite_snapshot"
			raw = "invalid database"
		}
		path := put(t, filepath.Join(sourceDir, mode), raw)
		_, cleanup, err := archiveSnapshot(path, format)
		if err == nil {
			t.Fatal("inspection failure accepted")
		}
		cleanup()
		entries, err := os.ReadDir(staging)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "arkism-archive-inspect-") {
				t.Fatal("failed staging copy remains")
			}
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != raw {
			t.Fatal("source changed by staging cleanup", err)
		}
	}
}

func TestArchiveWindowsRecordedFilePathsHonorExclusions(t *testing.T) {
	for _, project := range []string{`C:\Synthetic\Project`, `\\fixture-host\share\Project`} {
		t.Run(project, func(t *testing.T) {
			archiveTestSettings(t)
			setArchiveSettings(t, map[string]any{"exclude": []string{strings.ToLower(project)}})
			path := put(t, filepath.Join(t.TempDir(), "s.jsonl"), host.MustDumps(map[string]any{"session": map[string]any{"file_path": project + `\session.jsonl`}}, host.DumpOpts{})+"\n")
			client := newFakeArchive(t)
			c := newCollector(client, nil, true)
			if err := c.upload(path, "pc", "codex", "s", "s", timeNow(), "reconstructed_agentsview", nil); err != nil {
				t.Fatal(err)
			}
			if len(client.chunks) != 0 || c.counts.n["capture_skipped"] != 1 {
				t.Fatal(c.counts.n)
			}
		})
	}
}
