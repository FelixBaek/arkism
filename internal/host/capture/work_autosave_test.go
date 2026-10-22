package capture

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

type autosaveRecorded struct {
	path string
	body string // the request body as sorted JSON
}

type autosaveFixture struct {
	t        *testing.T
	scope    map[string]any
	job      host.Obj
	pkg      func() map[string]any
	status   string
	messages []map[string]any
	ref      map[string]any
	claims   any
	extracts int
	requests []autosaveRecorded
	saved    []host.Obj
}

func newAutosaveFixture(t *testing.T) *autosaveFixture {
	t.Setenv("ARKISM_CAPTURE_SETTINGS", filepath.Join(t.TempDir(), "capture.json"))
	f := &autosaveFixture{t: t, scope: map[string]any{"workspace_id": "w", "project_id": "p"}, status: "in_progress"}
	f.job = host.Obj{kv("scope", f.scope), kv("id", "work"), kv("event_id", "turn"), kv("expected_revision", 1),
		kv("archive_id", strings.Repeat("a", 64)), kv("sha256", strings.Repeat("b", 64))}
	f.pkg = func() map[string]any {
		return map[string]any{"title": "작업", "goal": "전체 목표", "origin": "claude_code", "repository": "/repo", "status": f.status,
			"checks": []any{map[string]any{"command": "never execute", "outcome": "passed"}}}
	}
	f.messages = []map[string]any{
		{"role": "user", "text": "전체 목표를 유지해. 새 제약은 외부 게시 금지야."},
		{"role": "assistant", "text": "구현을 마쳤다. 남은 일은 실제 프로그램 검증이다."},
	}
	f.ref = map[string]any{"archive_id": strings.Repeat("a", 64), "sha256": strings.Repeat("b", 64), "chunk_index": 0, "chunk_offset": 0}
	f.claims = []any{
		map[string]any{"field": "constraints", "message_index": 0, "quote": "외부 게시 금지"},
		map[string]any{"field": "completed", "message_index": 1, "quote": "구현을 마쳤다."},
		map[string]any{"field": "next_steps", "message_index": 1, "quote": "남은 일은 실제 프로그램 검증이다."},
	}
	return f
}

func (f *autosaveFixture) request(path string, body any) (map[string]any, error) {
	f.requests = append(f.requests, autosaveRecorded{path, canon(body)})
	switch {
	case strings.HasSuffix(path, "/get") && strings.Contains(path, "archives"):
		return map[string]any{"id": strings.Repeat("a", 64), "sha256": strings.Repeat("b", 64), "state": "active", "session_id": "session",
			"origin": "codex", "authority": "data_only_untrusted"}, nil
	case strings.HasSuffix(path, "/get"):
		return map[string]any{"id": "work", "revision": int64(1), "package": f.pkg(), "authority": "data_only_untrusted"}, nil
	case strings.HasSuffix(path, "/search"):
		items := []any{}
		for _, m := range f.messages {
			item := map[string]any{"ref": f.ref, "text_offset": 0, "authority": "data_only_untrusted"}
			for k, v := range m {
				item[k] = v
			}
			items = append(items, item)
		}
		return map[string]any{"items": items, "authority": "data_only_untrusted", "invalid_records": 3}, nil
	case strings.HasSuffix(path, "/save"):
		return map[string]any{"id": "work", "revision": int64(2), "authority": "data_only_untrusted"}, nil
	}
	f.t.Fatal(path)
	return nil, nil
}

// newer is a server where another writer already moved the work to revision 3.
func (f *autosaveFixture) newer(path string, body any) (map[string]any, error) {
	result, err := f.request(path, body)
	if path == "/v1/work-checkpoints/get" {
		result["revision"] = int64(3)
		pkg := result["package"].(map[string]any)
		pkg["decisions"] = []any{"다른 프로그램에서 정한 결정"}
		pkg["source_refs"] = []any{map[string]any{"collection": "sources", "id": "other", "revision": 1}}
	}
	if strings.HasSuffix(path, "/save") {
		if !eq(autosaveGet(body, "expected_revision"), 3) {
			f.t.Fatal("save not based on the latest revision")
		}
		result["revision"] = int64(4)
	}
	return result, err
}

func (f *autosaveFixture) extract(any, []any) (any, error) {
	f.extracts++
	return f.claims, nil
}

func (f *autosaveFixture) process(request autosaveRequest, persist func() error) (any, error) {
	if request == nil {
		request = f.request
	}
	if persist == nil {
		persist = func() error {
			v, _ := loadsOrdered([]byte(host.MustDumps(f.job, host.DumpOpts{})))
			f.saved = append(f.saved, v.(host.Obj))
			return nil
		}
	}
	return autosaveProcess(&f.job, request, f.scope, f.extract, persist)
}

func (f *autosaveFixture) saves() []string {
	var out []string
	for _, r := range f.requests {
		if strings.HasSuffix(r.path, "/save") {
			out = append(out, r.body)
		}
	}
	return out
}

func autosavePath(v any, keys ...any) any {
	for _, k := range keys {
		switch x := k.(type) {
		case string:
			v = autosaveItem(v, x)
		case int:
			v = v.([]any)[x]
		}
	}
	return v
}

func autosaveLost(f *autosaveFixture, next autosaveRequest) autosaveRequest {
	return func(path string, body any) (map[string]any, error) {
		result, err := next(path, body)
		if strings.HasSuffix(path, "/save") {
			return nil, errors.New("lost response")
		}
		return result, err
	}
}

func TestAutosaveQuotedProgressIsSavedWithoutClaimingVerification(t *testing.T) {
	f := newAutosaveFixture(t)
	result, err := f.process(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pkg := autosavePath(f.saved[0], "prepared", "package")
	if autosaveItem(pkg, "goal") != "전체 목표" || autosaveItem(pkg, "status") != "in_progress" ||
		!eq(autosaveItem(pkg, "checks"), f.pkg()["checks"]) ||
		!eq(autosaveItem(pkg, "completed"), []any{"에이전트 보고: 구현을 마쳤다."}) ||
		!eq(autosaveItem(pkg, "constraints"), []any{"사용자 발언: 외부 게시 금지"}) {
		t.Fatal(pkg)
	}
	if !eq(autosaveItem(result, "excluded_records"), 3) || autosaveHas(f.job, "prepared") || len(f.saves()) != 1 {
		t.Fatal(result, f.job)
	}
}

func TestAutosaveLostResponseRetriesExactPreparedRequestWithoutExtraction(t *testing.T) {
	f := newAutosaveFixture(t)
	if _, err := f.process(autosaveLost(f, f.request), nil); err == nil {
		t.Fatal("lost response ignored")
	}
	prepared := canon(autosaveItem(f.job, "prepared"))
	if _, err := f.process(nil, nil); err != nil {
		t.Fatal(err)
	}
	if saves := f.saves(); f.extracts != 1 || len(saves) != 2 || saves[0] != prepared || saves[1] != prepared {
		t.Fatal(f.extracts, saves)
	}
	before := len(f.requests)
	f.process(nil, nil)
	if len(f.requests) != before {
		t.Fatal("receipt did not stop a second save")
	}
}

func TestAutosaveFailuresNeverSaveOrRebase(t *testing.T) {
	for _, mutation := range []string{"backwards", "expired", "oversized", "secret"} {
		f := newAutosaveFixture(t)
		request := func(path string, body any) (map[string]any, error) {
			result, err := f.request(path, body)
			switch {
			case mutation == "backwards" && path == "/v1/work-checkpoints/get":
				result["revision"] = int64(0)
			case mutation == "expired" && path == "/v1/session-archives/get":
				result["state"] = "expired"
			case mutation == "oversized" && strings.HasSuffix(path, "/search"):
				result["oversized_records"] = 1
			case mutation == "secret" && strings.HasSuffix(path, "/search"):
				result["secret_records"] = 1
			}
			return result, err
		}
		_, err := f.process(request, nil)
		var ve valueErr
		if !errors.As(err, &ve) || len(f.saves()) != 0 {
			t.Fatal(mutation, err)
		}
	}
	f := newAutosaveFixture(t)
	if _, err := f.process(nil, func() error { return errors.New("disk full") }); err == nil || len(f.saves()) != 0 {
		t.Fatal(err)
	}
}

func TestAutosaveSkipsWithheldSecretsOnlyWhenTheServerSaysWhere(t *testing.T) {
	ref := func(archive string, offset int) any {
		return map[string]any{"archive_id": archive, "sha256": strings.Repeat("b", 64), "chunk_index": 0, "chunk_offset": offset}
	}
	for name, c := range map[string]struct {
		refs []any
		ok   bool
	}{
		"outside window": {[]any{ref(strings.Repeat("a", 64), 500)}, true},
		"inside window":  {[]any{ref(strings.Repeat("a", 64), 50)}, true},
		"no refs":        {nil, false},
		"count mismatch": {[]any{ref(strings.Repeat("a", 64), 50), ref(strings.Repeat("a", 64), 60)}, false},
		"other archive":  {[]any{ref(strings.Repeat("c", 64), 50)}, false},
	} {
		f := newAutosaveFixture(t)
		f.job = append(f.job, kv("stop_byte", 100))
		request := func(path string, body any) (map[string]any, error) {
			result, err := f.request(path, body)
			if strings.HasSuffix(path, "/search") {
				result["secret_records"] = 1
				if c.refs != nil {
					result["secret_refs"] = c.refs
				}
			}
			return result, err
		}
		_, err := f.process(request, nil)
		if c.ok != (err == nil) || c.ok != (len(f.saves()) == 1) {
			t.Fatal(name, err, f.saves())
		}
		if !c.ok && !errors.Is(err, valueErr("work_dialogue_incomplete")) {
			t.Fatal(name, err)
		}
	}
}

func TestAutosaveReturningWriterAppendsToLatestWithoutLosingOtherDecisions(t *testing.T) {
	f := newAutosaveFixture(t)
	result, err := f.process(f.newer, nil)
	if err != nil {
		t.Fatal(err)
	}
	pkg := autosavePath(f.saved[0], "prepared", "package")
	if !eq(autosaveItem(result, "revision"), 4) || !eq(autosaveItem(f.job, "expected_revision"), 1) ||
		!eq(autosaveItem(pkg, "decisions"), []any{"다른 프로그램에서 정한 결정"}) ||
		autosavePath(pkg, "source_refs", 0, "id") != "other" || autosaveItem(pkg, "goal") != "전체 목표" ||
		!eq(autosaveItem(pkg, "checks"), f.pkg()["checks"]) ||
		!eq(autosaveItem(pkg, "completed"), []any{"에이전트 보고: 구현을 마쳤다."}) {
		t.Fatal(result, pkg)
	}
}

func TestAutosaveReturningWriterLostResponseAndConflictKeepExactRequest(t *testing.T) {
	f := newAutosaveFixture(t)
	lost := autosaveLost(f, f.newer)
	if _, err := f.process(lost, nil); err == nil {
		t.Fatal("lost response ignored")
	}
	prepared := canon(autosaveItem(f.job, "prepared"))
	comparison, err := autosaveCompare(f.job, f.newer, f.scope, timeNow())
	if err != nil || !eq(autosaveItem(comparison, "expected_revision"), 3) || !eq(autosaveItem(comparison, "current_revision"), 3) {
		t.Fatal(comparison, err)
	}
	if _, err := f.process(lost, nil); err == nil {
		t.Fatal("lost response ignored")
	}
	if _, err := f.process(f.newer, nil); err != nil {
		t.Fatal(err)
	}
	saves := f.saves()
	if f.extracts != 1 || len(saves) != 3 || saves[0] != prepared || saves[1] != prepared || saves[2] != prepared {
		t.Fatal(f.extracts, saves)
	}
}

func TestAutosaveReturningWriterCannotChangeGoalOrReopenCompletedWork(t *testing.T) {
	f := newAutosaveFixture(t)
	f.claims = []any{map[string]any{"field": "goal", "message_index": 0, "quote": "전체 목표를 유지해."}}
	_, err := f.process(f.newer, nil)
	wzcodeWantErr(t, err, "work_revision_changed")
	if len(f.saves()) != 0 {
		t.Fatal("saved a goal change")
	}
	f = newAutosaveFixture(t)
	f.status = "completed"
	_, err = f.process(f.newer, nil)
	wzcodeWantErr(t, err, "work_revision_changed")
	if f.extracts != 0 {
		t.Fatal("extracted for completed work")
	}
}

func TestAutosaveReturningWriterWithoutNewClaimsAdvancesToObservedRevision(t *testing.T) {
	f := newAutosaveFixture(t)
	f.claims = []any{}
	result, err := f.process(f.newer, nil)
	if err != nil || !eq(autosaveItem(result, "revision"), 3) || len(f.saves()) != 0 {
		t.Fatal(result, err)
	}
}

func TestAutosaveMadeUpQuotesAndAssistantGoalChangesRejected(t *testing.T) {
	f := newAutosaveFixture(t)
	pkg := f.pkg()
	messages := []any{}
	for _, m := range f.messages {
		messages = append(messages, m)
	}
	for _, claim := range []map[string]any{
		{"field": "goal", "message_index": 1, "quote": "구현을 마쳤다."},
		{"field": "constraints", "message_index": 1, "quote": "구현을 마쳤다."},
		{"field": "completed", "message_index": 1, "quote": "모든 검사를 통과했다."},
		{"field": "checks", "message_index": 1, "quote": "구현을 마쳤다."},
	} {
		_, err := autosaveApplyClaims(pkg, messages, []any{claim})
		var ve valueErr
		if !errors.As(err, &ve) {
			t.Fatal(claim, err)
		}
	}
	if pkg["goal"] != "전체 목표" {
		t.Fatal("package changed")
	}
}

func TestAutosaveLongUTF8MessageIsReassembledAndCursorLoopsFail(t *testing.T) {
	f := newAutosaveFixture(t)
	first := strings.Repeat("긴 내용 ", 1000)
	second := "끝에 있는 중요한 제약"
	request := func(path string, body any) (map[string]any, error) {
		if strings.HasSuffix(path, "/search") {
			return map[string]any{"items": []any{map[string]any{"ref": f.ref, "role": "user", "text": first, "text_offset": 0,
				"next_text_offset": len(first), "authority": "data_only_untrusted"}}, "authority": "data_only_untrusted"}, nil
		}
		return map[string]any{"ref": f.ref, "role": "user", "text": second, "text_offset": len(first), "authority": "data_only_untrusted"}, nil
	}
	messages, _, err := autosaveDialogue(f.job, request, f.scope)
	if err != nil || autosaveItem(messages[0], "text") != first+second {
		t.Fatal(err)
	}
	_, _, err = autosaveDialogue(f.job, func(string, any) (map[string]any, error) {
		return map[string]any{"items": []any{}, "next_cursor": "loop", "authority": "data_only_untrusted"}, nil
	}, f.scope)
	wzcodeWantErr(t, err, "cursor_invalid")
}

func TestAutosaveChangedSourceAfterExtractionIsNotSaved(t *testing.T) {
	f := newAutosaveFixture(t)
	count := 0
	request := func(path string, body any) (map[string]any, error) {
		result, err := f.request(path, body)
		if path == "/v1/session-archives/get" {
			count++
			if count > 1 {
				result["sha256"] = strings.Repeat("c", 64)
			}
		}
		return result, err
	}
	_, err := f.process(request, nil)
	wzcodeWantErr(t, err, "archive_changed")
	if !autosaveHas(f.job, "prepared") || len(f.saves()) != 0 {
		t.Fatal(f.job)
	}
}

func TestAutosaveCLIOutboxScrubsSuccessAndExpiryUsesCreationTime(t *testing.T) {
	f := newAutosaveFixture(t)
	tmp := t.TempDir()
	t.Setenv("ARKISM_WORK_AUTOSAVE_STATE", tmp)
	s, sc, p, l := capState, capScope, capPost, llm
	t.Cleanup(func() { capState, capScope, capPost, llm = s, sc, p, l })
	capState = tmp
	capScope = func() map[string]any { return f.scope }
	capPost = func(path string, body any, _ time.Duration) (map[string]any, error) { return f.request(path, body) }
	llm = func(_, _, _ string, _ time.Duration, _, key string) (any, error) {
		if key != "claims" {
			t.Fatal(key)
		}
		return f.claims, nil
	}
	args := []string{}
	for _, key := range autosaveDescriptor {
		v, _ := f.job.Get(key)
		args = append(args, "--"+strings.ReplaceAll(key, "_", "-"), str(v))
	}
	run := func() {
		t.Helper()
		var out, errOut bytes.Buffer
		if code := autosaveMain(args, nil, &out, &errOut); code != 0 {
			t.Fatal(code, out.String(), errOut.String())
		}
	}
	run()
	paths, _ := filepath.Glob(filepath.Join(tmp, "*.json"))
	if len(paths) != 1 {
		t.Fatal(paths)
	}
	path := paths[0]
	raw, _ := os.ReadFile(path)
	stored, _ := loadObj(raw)
	if _, has := stored["prepared"]; has || strings.Contains(string(raw), "구현을 마쳤다") {
		t.Fatal(string(raw))
	}
	count := len(f.requests)
	run()
	again, _ := os.ReadFile(path)
	if len(f.requests) != count || !eq(autosaveItem(loadsOrderedMust(again), "created_at"), stored["created_at"]) {
		t.Fatal("receipt not reused")
	}
	old := timeNow() - autosaveTTL - 1
	stored["created_at"] = old
	os.WriteFile(path, []byte(host.MustDumps(stored, host.DumpOpts{})), 0o600) // a fresh mtime must not extend its lifetime
	run()
	latest, _ := os.ReadFile(path)
	created, _ := num(autosaveItem(loadsOrderedMust(latest), "created_at"))
	if created <= old || len(f.requests) <= count {
		t.Fatal(created, old)
	}
	var out bytes.Buffer
	if code := autosaveMain(args[:2], nil, &out, &out); code != 2 {
		t.Fatal(code)
	}
}

func loadsOrderedMust(raw []byte) any {
	v, err := loadsOrdered(raw)
	if err != nil {
		panic(err)
	}
	return v
}

func TestAutosaveExactRetryNeedsNoArchiveReadButRequiresBoundEvidence(t *testing.T) {
	f := newAutosaveFixture(t)
	if _, err := f.process(autosaveLost(f, f.request), nil); err == nil {
		t.Fatal("lost response ignored")
	}
	retry := func(path string, body any) (map[string]any, error) {
		if path != "/v1/work-checkpoints/save" {
			t.Fatal(path)
		}
		return f.request(path, body)
	}
	if _, err := f.process(retry, nil); err != nil || f.extracts != 1 {
		t.Fatal(err)
	}
	f.job.Del("receipt")
	prepared := loadsOrderedMust([]byte(host.MustDumps(autosaveItem(f.saved[0], "prepared"), host.DumpOpts{}))).(host.Obj)
	refs := autosaveItem(prepared, "archive_messages").([]any)
	ref := refs[0].(host.Obj)
	ref.Set("archive_id", strings.Repeat("c", 64))
	f.job.Set("prepared", prepared)
	_, err := f.process(retry, nil)
	wzcodeWantErr(t, err, "evidence_invalid")
}

// test_arkism_work_zcode: the writer filters record keys before reading message bodies.
func TestAutosaveWriterFiltersRecordKeysBeforeReadingMessageBody(t *testing.T) {
	ref := map[string]any{"archive_id": strings.Repeat("a", 64), "sha256": strings.Repeat("b", 64), "record_key": strings.Repeat("c", 64),
		"chunk_index": 0, "chunk_offset": 0}
	item := func(key, text string) map[string]any {
		r := map[string]any{}
		for k, v := range ref {
			r[k] = v
		}
		r["record_key"] = key
		return map[string]any{"ref": r, "role": "user", "authority": "data_only_untrusted", "text_offset": 0, "text": text}
	}
	request := func(path string, _ any) (map[string]any, error) {
		if !strings.HasSuffix(path, "/search") {
			t.Fatal(path)
		}
		return map[string]any{"items": []any{item(strings.Repeat("c", 64), "selected"), item(strings.Repeat("d", 64), "excluded")},
			"authority": "data_only_untrusted"}, nil
	}
	job := host.Obj{kv("archive_id", strings.Repeat("a", 64)), kv("sha256", strings.Repeat("b", 64)),
		kv("eligible_record_keys", []any{strings.Repeat("c", 64)})}
	messages, _, err := autosaveDialogue(job, request, map[string]any{})
	if err != nil || len(messages) != 1 || autosaveItem(messages[0], "text") != "selected" {
		t.Fatal(messages, err)
	}
	job.Set("eligible_record_keys", []any{})
	if messages, _, err := autosaveDialogue(job, request, map[string]any{}); err != nil || len(messages) != 0 {
		t.Fatal(messages, err)
	}
	job.Set("eligible_record_keys", []any{"invalid"})
	_, _, err = autosaveDialogue(job, request, map[string]any{})
	wzcodeWantErr(t, err, "keys_invalid")
}

// The prompt input, schema and outbox key bytes come from Python.
func TestAutosaveExtractMatchesPythonGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/autosave_extract.json")
	if err != nil {
		t.Fatal(err)
	}
	want := loadsOrderedMust(raw)
	get := func(k string) any { v, _ := objGet(want, k); return v }
	keep := llm
	t.Cleanup(func() { llm = keep })
	var system, user, schema string
	llm = func(s, u, _ string, _ time.Duration, sc, _ string) (any, error) {
		system, user, schema = s, u, sc
		return []any{}, nil
	}
	messages := get("messages").([]any)
	if _, err := autosaveExtract(get("package"), messages); err != nil {
		t.Fatal(err)
	}
	if system != get("prompt") || user != get("user") || schema != get("schema") || autosaveSchema != get("schema_const") {
		t.Fatalf("prompt/user/schema differ:\n%s\n%s", user, get("user"))
	}
	if autosaveOutboxKey(get("outbox_scope"), get("outbox_id").(string), get("outbox_event_id").(string)) != get("outbox_key") {
		t.Fatal("outbox key differs")
	}
}
