package capture

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

type retentionClient struct {
	view  map[string]any
	err   error
	calls [][]any
}

func (c *retentionClient) post(endpoint string, payload map[string]any) (map[string]any, error) {
	c.calls = append(c.calls, []any{endpoint, payload})
	return c.view, c.err
}

func retentionView(sid, state string) map[string]any {
	days := -1
	if state == "active" {
		days = 1
	}
	return map[string]any{"id": retentionArchiveID(sid), "session_id": sid, "device": "pc", "origin": "zcode", "format": "reconstructed_zcode",
		"revision": int64(3), "state": state, "sha256": strings.Repeat("a", 64),
		"expires_at": time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour).Format("2006-01-02T15:04:05.000000+00:00"),
		"text":       "private projection must not cross SSH"}
}

func retentionItems(sha string) []any {
	return []any{map[string]any{"session_id": "session", "sha256": strings.Repeat(sha, 64)}}
}

func retentionState(t *testing.T, out host.Obj) string {
	items, _ := out.Get("items")
	s, _ := items.([]any)[0].(host.Obj).Get("state")
	return s.(string)
}

func TestRetentionServerRenewalAndHashIdentity(t *testing.T) {
	client := &retentionClient{}
	for _, state := range []string{"expired", "active"} {
		client.view = retentionView("session", state)
		out, err := retentionDecide(client, retentionItems("a"), time.Now())
		if err != nil || retentionState(t, out) != state || strings.Contains(host.MustDumps(out, host.DumpOpts{}), "private projection") {
			t.Fatal(out, err)
		}
		last := client.calls[len(client.calls)-1]
		if last[0] != "get" || !eq(last[1], map[string]any{"id": retentionArchiveID("session")}) {
			t.Fatal(last)
		}
	}
	out, _ := retentionDecide(client, retentionItems("b"), time.Now())
	if retentionState(t, out) != "changed" {
		t.Fatal(out)
	}
}

func TestRetentionUnknownOrInvalidServerMetadataNeverExpires(t *testing.T) {
	out, err := retentionDecide(&retentionClient{err: &APIError{Status: 404}}, retentionItems("a"), time.Now())
	if err != nil || retentionState(t, out) != "unknown" {
		t.Fatal(out, err)
	}
	if _, err := retentionDecide(&retentionClient{err: &APIError{Status: 500}}, retentionItems("a"), time.Now()); err == nil {
		t.Fatal("500 accepted")
	}
	future := time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02T15:04:05+00:00")
	for _, c := range []host.KV{{Key: "device", Value: "foreign"}, {Key: "origin", Value: "codex"}, {Key: "session_id", Value: "other"},
		{Key: "id", Value: strings.Repeat("b", 64)}, {Key: "revision", Value: true}, {Key: "state", Value: "deleted"}, {Key: "sha256", Value: "invalid"},
		{Key: "expires_at", Value: time.Now().Format("2006-01-02T15:04:05")}, {Key: "expires_at", Value: future}} {
		view := retentionView("session", "expired")
		view[c.Key] = c.Value
		if _, err := retentionDecide(&retentionClient{view: view}, retentionItems("a"), time.Now()); err == nil {
			t.Fatal(c)
		}
	}
}

func TestRetentionSSHFixedCommandAndResponseBinding(t *testing.T) {
	winFixture(t)
	t.Setenv("ARKISM_SERVER_BIN", "/srv/arkism/bin/arkism")
	items := retentionItems("a")
	out, _ := retentionDecide(&retentionClient{view: retentionView("session", "expired")}, items, time.Now())
	var calls []winCall
	winRun = winResults(&calls, host.MustDumps(out, host.DumpOpts{}))
	if states, err := retentionRemoteStates(items); err != nil || len(states) != 1 || states[0] != "expired" {
		t.Fatal(states, err)
	}
	remote := calls[0].argv[len(calls[0].argv)-1]
	if remote != "/srv/arkism/bin/arkism archive-retention" || strings.Contains(remote, "session") {
		t.Fatal(remote)
	}
	if string(calls[0].input) != `{"version": 1, "items": [{"session_id": "session", "sha256": "`+strings.Repeat("a", 64)+`"}]}` {
		t.Fatal(string(calls[0].input))
	}
	list, _ := out.Get("items")
	item := list.([]any)[0].(host.Obj)
	item.Set("sha256", strings.Repeat("b", 64))
	winRun = winResults(&calls, host.MustDumps(out, host.DumpOpts{}))
	if _, err := retentionRemoteStates(items); err == nil {
		t.Fatal("rebound hash accepted")
	}
	if _, err := winSSHCommand("codex", "archive-retention"); err == nil {
		t.Fatal("codex retention accepted")
	}
	if _, err := retentionParse([]byte(`{"version":1,"version":1}`)); err == nil {
		t.Fatal("duplicate key accepted")
	}
	many := make([]any, 65)
	for i := range many {
		many[i] = items[0]
	}
	if err := retentionValidate(many); err == nil {
		t.Fatal("65 items accepted")
	}
}

func retentionOldCopy(t *testing.T, root string) (string, string) {
	out := filepath.Join(root, "out")
	os.Mkdir(out, 0o755)
	path := filepath.Join(out, sha256Hex([]byte("old"))+".jsonl")
	line := fmt.Sprintf(`{"session": {"id": "old", "time_updated": %d}}`, int64((timeNow()-8*86400)*1000))
	os.WriteFile(path, []byte(line+"\n"), 0o644)
	return out, path
}

func TestExportRetainsRenewedUnknownChangedAndOffline(t *testing.T) {
	for _, state := range []string{"active", "unknown", "changed", ""} {
		root := t.TempDir()
		out, path := retentionOldCopy(t, root)
		check := func(items []any) ([]string, error) {
			raw, _ := os.ReadFile(path)
			want := []any{host.Obj{{Key: "session_id", Value: "old"}, {Key: "sha256", Value: sha256Hex(raw)}}}
			if !eq(items, want) {
				t.Fatal(items)
			}
			if state == "" {
				return nil, errors.New("offline")
			}
			return []string{state}, nil
		}
		counts, err := exportSessions(filepath.Join(root, "missing"), out, true, -1, check)
		key := "retention_" + state
		if state == "" {
			key = "retention_unavailable"
		}
		if v, _ := counts.Get(key); err != nil || !exists(path) || v != 1 {
			t.Fatal(state, counts, err)
		}
		if _, has := counts.Get("expired_copies"); has {
			t.Fatal(counts)
		}
	}
}

func TestExportFileReplacedDuringConfirmationIsRetained(t *testing.T) {
	root := t.TempDir()
	out, path := retentionOldCopy(t, root)
	change := func([]any) ([]string, error) {
		raw, _ := os.ReadFile(path)
		replacement := filepath.Join(out, "new")
		os.WriteFile(replacement, append(raw, []byte(`{"new":"message"}`+"\n")...), 0o644)
		os.Rename(replacement, path)
		return []string{"expired"}, nil
	}
	counts, err := exportSessions(filepath.Join(root, "missing"), out, true, -1, change)
	if v, _ := counts.Get("changed_before_cleanup"); err != nil || !exists(path) || v != 1 {
		t.Fatal(counts, err)
	}
}

func TestExportMissingCheckerIsNotDeleteAuthority(t *testing.T) {
	root := t.TempDir()
	out, path := retentionOldCopy(t, root)
	counts, err := exportSessions(filepath.Join(root, "missing"), out, true, -1, nil)
	if v, _ := counts.Get("retention_unavailable"); err != nil || v != 1 || !exists(path) {
		t.Fatal(counts, err)
	}
}

func TestExportExpiredCopyAndRestoredCopyAreRemoved(t *testing.T) {
	root := t.TempDir()
	out, path := retentionOldCopy(t, root)
	original, _ := os.ReadFile(path)
	for _, restored := range []bool{false, true} {
		if restored {
			os.WriteFile(path, original, 0o644)
		}
		counts, err := exportSessions(filepath.Join(root, "missing"), out, true, -1, func([]any) ([]string, error) { return []string{"expired"}, nil })
		if v, _ := counts.Get("expired_copies"); err != nil || v != 1 || exists(path) {
			t.Fatal(restored, counts, err)
		}
	}
}

func TestExportHandleTimeDifferenceDoesNotHideHandleChanges(t *testing.T) {
	root := t.TempDir()
	_, path := retentionOldCopy(t, root)
	orig := exportHandleStamp
	t.Cleanup(func() { exportHandleStamp = orig })
	for _, changed := range []bool{false, true} {
		calls := int64(0)
		exportHandleStamp = func(f *os.File) (fileStamp, error) {
			calls++
			s, err := orig(f)
			s.CtimeNs += 1000
			if changed {
				s.CtimeNs += calls
			}
			return s, err
		}
		info, err := exportCopyInfo(path, 0, false)
		if changed {
			boundaryWantErr(t, err, "copy_changed")
		} else if err != nil || info == nil {
			t.Fatal(info, err)
		}
	}
}

// Golden: the exporter's counts and copies match Python for the shared Zcode fixture.
func TestExportGolden(t *testing.T) {
	var golden struct {
		Now    float64           `json:"now"`
		Plan   map[string]int    `json:"plan"`
		Counts map[string]int    `json:"counts"`
		Again  map[string]int    `json:"again"`
		Files  map[string]string `json:"files"`
		Mtimes map[string]int64  `json:"mtimes"`
	}
	boundaryGolden(t, "export_zcode.json", &golden)
	out := filepath.Join(t.TempDir(), "out")
	database := filepath.Join("testdata", "zcode_fixture.sqlite")
	asMap := func(o host.Obj) map[string]int {
		m := map[string]int{}
		for _, kv := range o {
			m[kv.Key] = kv.Value.(int)
		}
		return m
	}
	plan, err := exportSessions(database, out, false, golden.Now, nil)
	if err != nil || !eq(asMap(plan), golden.Plan) {
		t.Fatal(plan, err)
	}
	counts, err := exportSessions(database, out, true, golden.Now, nil)
	if err != nil || !eq(asMap(counts), golden.Counts) {
		t.Fatal(counts, err)
	}
	again, _ := exportSessions(database, out, true, golden.Now, nil)
	if !eq(asMap(again), golden.Again) {
		t.Fatal(again)
	}
	paths, _ := filepath.Glob(filepath.Join(out, "*.jsonl"))
	if len(paths) != len(golden.Files) {
		t.Fatal(paths)
	}
	for _, p := range paths {
		raw, _ := os.ReadFile(p)
		fi, _ := os.Stat(p)
		name := filepath.Base(p)
		want := golden.Mtimes[name]
		if runtime.GOOS == "windows" {
			want -= want % 100 // NTFS keeps file times in 100 ns ticks
		}
		if sha256Hex(raw) != golden.Files[name] || fi.ModTime().UnixNano() != want {
			t.Fatal(name, fi.ModTime().UnixNano(), want)
		}
	}
}
