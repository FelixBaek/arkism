package host

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sum(b string) string { h := sha256.Sum256([]byte(b)); return hex.EncodeToString(h[:]) }

type fakeRequest struct {
	calls  [][2]any
	answer map[string]any
	err    error
}

func (f *fakeRequest) do(path string, payload map[string]any) (map[string]any, error) {
	f.calls = append(f.calls, [2]any{path, payload})
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]any{}
	for k, v := range f.answer {
		out[k] = v
	}
	return out, nil
}

func TestResumeComparesFilesButDoesNotExecuteSavedChecks(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "app.py"), []byte("current"), 0o600)
	req := &fakeRequest{answer: map[string]any{"package": map[string]any{
		"repository": "/a/different/machine", "files": []any{map[string]any{"path": "app.py", "sha256": sum("old")}},
		"checks": []any{map[string]any{"command": "touch " + filepath.Join(root, "should-not-exist"), "outcome": "passed"}}}}}
	result, err := WorkCall("resume_work", map[string]any{"id": "task", "cwd": root}, req.do, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"checks_executed": false, "saved_results_are_current": false,
		"files": []any{map[string]any{"path": "app.py", "state": "changed"}}}
	if !reflect.DeepEqual(result["verification"], want) {
		t.Fatalf("verification = %v", result["verification"])
	}
	if _, err := os.Stat(filepath.Join(root, "should-not-exist")); err == nil {
		t.Fatal("a saved check ran")
	}
	if _, has := req.calls[0][1].(map[string]any)["cwd"]; has {
		t.Fatal("cwd was sent to the server")
	}
}

func TestCompareDoesNotFollowExternalSymlinks(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "checkout")
	os.Mkdir(root, 0o700)
	os.WriteFile(filepath.Join(root, "main.py"), []byte("changed"), 0o600)
	os.WriteFile(filepath.Join(root, "same.py"), []byte("original"), 0o600)
	os.WriteFile(filepath.Join(tmp, "outside"), []byte("original"), 0o600)
	os.Symlink(filepath.Join(tmp, "outside"), filepath.Join(root, "link"))
	os.Symlink(tmp, filepath.Join(root, "linked_dir"))
	var files []any
	for _, p := range []string{"main.py", "same.py", "missing", "../outside", "link", "linked_dir/outside"} {
		files = append(files, map[string]any{"path": p, "sha256": sum("original")})
	}
	rows, err := CompareFiles(root, files)
	if err != nil {
		t.Fatal(err)
	}
	var states []string
	for _, r := range rows {
		states = append(states, r.(map[string]any)["state"].(string))
	}
	want := []string{"changed", "match", "missing", "invalid_path_or_hash", "unreadable_or_symlink", "unreadable_or_symlink"}
	if !reflect.DeepEqual(states, want) {
		t.Fatalf("states = %v", states)
	}
}

func TestSaveKeepsRetryIdentityAndScopeStaysOutOfArguments(t *testing.T) {
	scope := map[string]any{"workspace_id": "workspace", "project_id": "project", "user_id": "owner", "agent_id": ""}
	pkg := map[string]any{"title": "배포 점검", "goal": "동일 작업 이어가기", "repository": "repo", "origin": "claude_code",
		"checks": []any{map[string]any{"command": "do-not-execute", "outcome": "not_run"}}}
	req := &fakeRequest{answer: map[string]any{"id": "task-1", "revision": 1}}
	args := map[string]any{"id": "task-1", "event_id": "turn-1", "expected_revision": 0, "package": pkg}
	WorkCall("save_work", args, req.do, scope)
	WorkCall("save_work", args, req.do, scope)
	if !reflect.DeepEqual(req.calls[0], req.calls[1]) {
		t.Fatal("a retry changed the request")
	}
	if _, has := args["scope"]; has {
		t.Fatal("scope leaked into the caller's arguments")
	}
	req.answer = map[string]any{"id": "task-1", "revision": 1, "package": pkg}
	resumed, _ := WorkCall("resume_work", map[string]any{"id": "task-1"}, req.do, scope)
	if !reflect.DeepEqual(resumed["package"], pkg) || resumed["authority"] != "data_only_untrusted" {
		t.Fatalf("resumed = %v", resumed)
	}
	last := req.calls[len(req.calls)-1]
	if last[0] != "/v1/work-checkpoints/get" || !reflect.DeepEqual(last[1], map[string]any{"id": "task-1", "scope": scope}) {
		t.Fatalf("last call = %v", last)
	}
}

func TestWorkRejectsScopeOverrideBooleanRevisionAndInvalidCursor(t *testing.T) {
	req := &fakeRequest{}
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"resume_work", map[string]any{"id": "ok", "scope": map[string]any{}}},
		{"resume_work", map[string]any{"id": "../other"}},
		{"save_work", map[string]any{"id": "ok", "event_id": "evt", "expected_revision": true, "package": map[string]any{}}},
		{"list_work", map[string]any{"limit": true}},
		{"list_work", map[string]any{"after": "\n"}},
	} {
		if _, err := WorkCall(c.name, c.args, req.do, map[string]any{}); err == nil {
			t.Errorf("%s %v accepted", c.name, c.args)
		}
	}
	if len(req.calls) != 0 {
		t.Fatal("a rejected call reached the server")
	}
}

func TestPaginationKeepsCursorAndConflictsAreNotRetried(t *testing.T) {
	req := &fakeRequest{answer: map[string]any{"items": []any{}, "next_cursor": "next"}}
	WorkCall("list_work", map[string]any{"after": "previous", "limit": 5}, req.do, map[string]any{})
	want := [2]any{"/v1/work-checkpoints/list", map[string]any{"scope": map[string]any{}, "after": "previous", "limit": 5}}
	if !reflect.DeepEqual(req.calls[0], want) {
		t.Fatalf("call = %v", req.calls[0])
	}
	req = &fakeRequest{err: errors.New("conflict")}
	if _, err := WorkCall("save_work", map[string]any{"id": "ok", "event_id": "evt", "expected_revision": 2, "package": map[string]any{}}, req.do, map[string]any{}); err == nil {
		t.Fatal("conflict swallowed")
	}
	if len(req.calls) != 1 {
		t.Fatalf("calls = %d", len(req.calls))
	}
}

func contextState() map[string]any {
	return map[string]any{"id": "selected-work", "revision": 7, "package": map[string]any{
		"title": "진행 작업", "goal": "프로그램 전환 후 이어가기",
		"constraints": []any{"현재 요청 확인"}, "decisions": []any{strings.Repeat("결정", 1500)},
		"next_steps": []any{strings.Repeat("다음 작업", 500)}, "checks": []any{map[string]any{"command": "never execute", "outcome": "passed"}}}}
}

func TestAssembleSmallBlocksUnchangedAndEmpty(t *testing.T) {
	if got, _ := Assemble("memory", "work", "crew", ContextMaxChars); got != "memory\n\nwork\n\ncrew" {
		t.Fatalf("got %q", got)
	}
	if got, _ := Assemble("", "", "", ContextMaxChars); got != "" {
		t.Fatalf("got %q", got)
	}
}

func workBlock(t *testing.T, result string) map[string]any {
	t.Helper()
	text := strings.SplitN(result, HookHeader, 2)[1]
	text = text[:strings.LastIndex(text, "\n\n")]
	v, err := Loads(text)
	if err != nil {
		t.Fatal(err)
	}
	return v.(map[string]any)
}

func TestAssembleCombinedBudgetPreservesMetadataAndWholeFields(t *testing.T) {
	state := contextState()
	selected, err := RenderHookContext(state, 8000)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Assemble(strings.Repeat("기억", 1950), selected, strings.Repeat("정의", 900), ContextMaxChars)
	if err != nil || Len(result) > 9500 {
		t.Fatalf("len %d err %v", Len(result), err)
	}
	data := workBlock(t, result)
	if data["id"] != "selected-work" || data["revision"].(interface{ String() string }).String() != "7" || data["authority"] != "data_only_untrusted" {
		t.Fatalf("metadata = %v", data)
	}
	omitted := stringList(data["omitted_fields"])
	if len(omitted) == 0 {
		t.Fatal("nothing omitted")
	}
	pkg := state["package"].(map[string]any)
	seen := map[string]bool{}
	for k, v := range data["package"].(map[string]any) {
		if !reflect.DeepEqual(v, pkg[k]) {
			t.Fatalf("%s changed", k)
		}
		seen[k] = true
	}
	for _, k := range omitted {
		seen[k] = true
	}
	if len(seen) != len(pkg) {
		t.Fatalf("fields %v vs %v", seen, pkg)
	}
}

func TestFirstRenderOmissionsSurviveSecondBudget(t *testing.T) {
	state := contextState()
	state["package"].(map[string]any)["decisions"] = []any{strings.Repeat("too big", 3000)}
	selected, _ := RenderHookContext(state, 8000)
	if !containsStr(stringList(workBlock(t, selected+"\n\n")["omitted_fields"]), "decisions") {
		t.Fatal("first render kept decisions")
	}
	result, _ := Assemble(strings.Repeat("기억", 1950), selected, strings.Repeat("정의", 900), ContextMaxChars)
	if !containsStr(stringList(workBlock(t, result)["omitted_fields"]), "decisions") {
		t.Fatal("second budget lost the omission")
	}
}

func TestLargeBlocksAreOmittedWholeWithRetrievalNotice(t *testing.T) {
	selected, _ := RenderHookContext(contextState(), 8000)
	result, err := Assemble(strings.Repeat("sensitive memory", 3000), selected,
		"<arkism-crew>"+strings.Repeat("private", 3000)+"</arkism-crew>", ContextMaxChars)
	if err != nil || Len(result) > 9500 {
		t.Fatalf("len %d err %v", Len(result), err)
	}
	for _, s := range []string{MemoryOmitted, CrewOmitted, "selected-work"} {
		if !strings.Contains(result, s) {
			t.Errorf("missing %q", s)
		}
	}
	for _, s := range []string{"sensitive memory", "<arkism-crew>"} {
		if strings.Contains(result, s) {
			t.Errorf("kept %q", s)
		}
	}
}
