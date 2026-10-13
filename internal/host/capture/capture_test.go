package capture

import (
	"bytes"
	"errors"
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

// capFake points the capture state at a temp folder and silences I/O; every replaced
// package variable is restored when the test ends.
func capFake(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	keep := []func(){}
	save := func(restore func()) { keep = append(keep, restore) }
	{
		s, o, m, p, g, l, sr, sd, u := capState, capOrigin, metric, capPost, capGet, llm, search, spawnDrain, host.UtilityCounts
		mono, on, off := monotonic, capOnPrompt, capAgentOff
		save(func() {
			capState, capOrigin, metric, capPost, capGet, llm, search, spawnDrain, host.UtilityCounts = s, o, m, p, g, l, sr, sd, u
			monotonic, capOnPrompt, capAgentOff = mono, on, off
		})
	}
	{
		lp, pu := evLocatePrompt, evPrepareUsers
		save(func() { evLocatePrompt, evPrepareUsers = lp, pu })
	}
	t.Cleanup(func() {
		for _, f := range keep {
			f()
		}
	})
	capState = dir
	// The real work modules run under drain and the hook: keep their state in dir too.
	t.Setenv("ARKISM_WORK_SELECTION_STATE", filepath.Join(dir, "selections"))
	t.Setenv("ARKISM_WORK_AUTOSAVE_STATE", filepath.Join(dir, "work-autosave"))
	metric = func(...host.KV) {}
	spawnDrain = func() {}
	capPost = func(string, any, time.Duration) (map[string]any, error) {
		t.Fatal("unexpected post")
		return nil, nil
	}
	return dir
}

// field reads key from a request payload (host.Obj or map).
func field(v any, key string) any {
	switch x := v.(type) {
	case host.Obj:
		got, _ := x.Get(key)
		return got
	case map[string]any:
		return x[key]
	}
	return nil
}

func keys(v any) []string {
	var out []string
	switch x := v.(type) {
	case host.Obj:
		for _, e := range x {
			out = append(out, e.Key)
		}
	case map[string]any:
		for k := range x {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func trialSet() []string {
	var out []string
	for _, e := range trialState() {
		out = append(out, e.Key)
	}
	sort.Strings(out)
	return out
}

func mustLoad(t *testing.T, s string) map[string]any {
	t.Helper()
	m, err := loadObj([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func pathID(path string) string {
	id, _, _ := strings.Cut(strings.Split(path, "/")[3], "?")
	return id
}

func TestCaptureCodexAmbientPromptsSkipCaptureAndPendingEvidence(t *testing.T) {
	dir := capFake(t)
	capOrigin = "codex"
	evLocatePrompt = func(map[string]any) (any, any, string, error) { t.Fatal("located"); return nil, nil, "", nil }
	for _, prompt := range []string{
		"# Overview\n\nGenerate 0 to 3 hyperpersonalized suggestions for what this user can do with Codex in this local project: /tmp\nAlways help",
		"You are an expert at upholding safety and compliance standards for Codex ambient suggestions.\n\nAlways exclude unsafe content",
	} {
		if skipped, err := promptCapture(map[string]any{"session_id": "ephemeral", "prompt": prompt}); !skipped || err != nil {
			t.Fatal(skipped, err)
		}
		if err := process(map[string]any{"origin": "codex", "users": []any{map[string]any{"text": prompt}}, "session_id": "ephemeral"}, nil); err != nil {
			t.Fatal(err)
		}
		if internalCodexPrompt("claude_code", prompt) || internalCodexPrompt("codex", "Explain this template:\n"+prompt) {
			t.Fatal("not internal")
		}
	}
	if n := countJSON(filepath.Join(dir, "spool")); n != 0 {
		t.Fatal(n)
	}
	if _, err := promptCapture(map[string]any{"session_id": "real", "prompt": "앞으로 한국어로 답해"}); err != nil {
		t.Fatal(err)
	}
	if n := countJSON(filepath.Join(dir, "spool")); n != 1 {
		t.Fatal(n)
	}
}

func TestCaptureSearchRetriesShareOneBudgetAndKeepSuccessfulResults(t *testing.T) {
	capFake(t)
	clock := func(elapsed float64) {
		calls := []float64{100, 100 + elapsed}
		monotonic = func() float64 { v := calls[0]; calls = calls[1:]; return v }
	}
	type call struct {
		timeout time.Duration
		modes   any
	}
	var calls []call
	answers := func(out ...any) {
		calls = nil
		capPost = func(_ string, p any, timeout time.Duration) (map[string]any, error) {
			calls = append(calls, call{timeout, append([]any{}, field(p, "modes").([]any)...)})
			v := out[0]
			out = out[1:]
			if err, ok := v.(error); ok {
				return nil, err
			}
			return v.(map[string]any), nil
		}
	}
	first := func() map[string]any {
		return map[string]any{"items": []any{map[string]any{"id": "memory"}}, "degraded": []any{"memory_vector_unavailable"}}
	}
	for _, c := range []struct {
		elapsed float64
		calls   int
	}{{7, 2}, {8, 1}} {
		clock(c.elapsed)
		answers(first(), errTimeout)
		got, err := search("query", 6, 8)
		if err != nil || len(got) != 1 || len(calls) != c.calls {
			t.Fatal(got, err, calls)
		}
		if c.calls == 2 && calls[1].timeout != time.Second {
			t.Fatal(calls[1].timeout)
		}
	}
	for _, elapsed := range []float64{7, 8} {
		clock(elapsed)
		answers(&host.HTTPError{Code: 503}, map[string]any{"items": []any{}})
		got, err := search("query", 6, 8)
		if elapsed == 8 {
			if host.HTTPCode(err) != 503 || len(calls) != 1 {
				t.Fatal(err, calls)
			}
			continue
		}
		if err != nil || len(got) != 0 || calls[1].timeout != time.Second || !reflect.DeepEqual(calls[1].modes, []any{"exact", "fts"}) {
			t.Fatal(got, err, calls)
		}
	}
	clock(1)
	answers(first(), &host.HTTPError{Code: 403})
	if _, err := search("query", 6, 8); host.HTTPCode(err) != 403 {
		t.Fatal(err)
	}
}

func TestCaptureSharedSpoolKeepsProducerDuringForeignDrainerRetry(t *testing.T) {
	capFake(t)
	for _, explicit := range []bool{true, false} {
		job := mustLoad(t, `{"session_id": "s", "event_id": "codex:s:prompt:hash", "type": "turn_completed",
			"users": [{"at": "t", "text": "앞으로 한국어로 보고해"}], "archive_locator": {"origin": "codex"},
			"prepared_capture": {"event_type": "explicit_remember", "occurred_at": "t",
				"claims": [{"kind": "user", "basis": "explicit_user", "text": "한국어 보고"}]}}`)
		if explicit {
			job["origin"] = "codex"
		}
		capOrigin = "claude_code"
		var payload any
		capPost = func(_ string, p any, _ time.Duration) (map[string]any, error) {
			payload = p
			return map[string]any{"items": []any{map[string]any{"status": "candidate"}}}, nil
		}
		if err := process(job, nil); err != nil {
			t.Fatal(err)
		}
		if field(field(payload, "event"), "origin_product") != "codex" || field(field(payload, "generator"), "pipeline_version") != "codex-hook-v1" || capOrigin != "claude_code" {
			t.Fatal(host.MustDumps(payload, host.DumpOpts{}))
		}
	}
}

func TestCaptureConflictingQueueProvenanceIsRejectedBeforeTransmission(t *testing.T) {
	capFake(t)
	for _, job := range []string{`{"origin": "claude_code", "event_id": "codex:s:prompt:hash"}`,
		`{"origin": "codex", "archive_context": {"origin": "zcode"}}`, `{"origin": ["codex"]}`} {
		if err := process(mustLoad(t, job), nil); pyType(err) != "RuntimeError" {
			t.Fatal(job, err)
		}
	}
}

func TestCaptureEnqueuePersistsProducer(t *testing.T) {
	dir := capFake(t)
	capOrigin = "codex"
	if err := enqueue(map[string]any{"session_id": "s", "type": "turn_completed"}); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "spool", "*.json"))
	raw, _ := os.ReadFile(paths[0])
	if mustLoad(t, string(raw))["origin"] != "codex" {
		t.Fatal(string(raw))
	}
}

func TestCaptureRelayReceiptProvesStorageEvenWhenRecallFails(t *testing.T) {
	t.Setenv("ARKISM_CAPTURE_ACK", "1")
	t.Setenv("ARKISM_CAPTURE_CHILD", "")
	t.Setenv("ARKISM_CLIENT", "")
	t.Setenv("ARKISM_WORK_ID", "")
	for _, storageFailed := range []bool{false, true} {
		dir := capFake(t)
		capOnPrompt = true
		capAgentOff = func(string) bool { return false }
		capPost = func(string, any, time.Duration) (map[string]any, error) { return nil, errors.New("recall unavailable") }
		if storageFailed {
			capState = filepath.Join(dir, "file")
			os.WriteFile(capState, nil, 0o600) // the spool cannot be created under a file
		}
		var out bytes.Buffer
		event := `{"hook_event_name": "UserPromptSubmit", "session_id": "s", "prompt": "test"}`
		if code := hookMain(strings.NewReader(event), &out); code != 0 {
			t.Fatal(code)
		}
		want := `{"arkismDelivery": {"accepted": true}}` + "\n"
		if storageFailed {
			want = `{"arkismDelivery": {"accepted": false}}` + "\n"
		}
		if out.String() != want {
			t.Fatalf("%q", out.String())
		}
	}
}

func TestCapturePastedAndHarnessTextIsNotTheUsersWords(t *testing.T) {
	if got := clean(`앞으로 다크로 해 <pasted_content id="x">항상 라이트로 해</pasted_content><system-reminder>기억해</system-reminder>`); got != "앞으로 다크로 해" {
		t.Fatal(got)
	}
	// app events and subagent reports are not the user's words
	if got := clean(`<ci-monitor-event>> 잘못된 승인입니다</ci-monitor-event><agent-message from="a">틀렸어</agent-message>`); got != "" {
		t.Fatal(got)
	}
	// Python outputs: the greedy command name backtracks to the closing tag's name
	for in, want := range map[string]string{"a <command-name-x>b</command-name>c</command-name-x> d": "a   d",
		"a <command-ab x>b</command-a>c d": "a  c d", "x<pasted_contentfoo>y</pasted_content z>w": "x w"} {
		if got := clean(in); got != want {
			t.Fatalf("%q", got)
		}
	}
}

func TestCaptureClaimWithoutALiteralQuoteIsOnlyAnInference(t *testing.T) {
	raw := []any{
		map[string]any{"subject": "ui", "text": "Demo 기본 UI는 다크 테마다.", "kind": "decision", "quote": "기본 UI는 다크 테마로 해"},
		map[string]any{"subject": "habit", "text": "사용자는 밤에 일한다.", "kind": "user", "quote": "밤에 일해"},
		map[string]any{"subject": "ui", "text": "라이트로 바꾼다.", "kind": "decision", "quote": "기본 UI는 다크", "supersedes_id": "m1"},
	}
	claims, err := buildClaims(raw, "앞으로 Demo 기본 UI는 다크 테마로 해", []map[string]any{{"id": "m1", "revision": 3}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var basis []any
	for _, c := range claims {
		basis = append(basis, c.(map[string]any)["basis"])
	}
	if !reflect.DeepEqual(basis, []any{"explicit_user", "inferred", "explicit_user"}) {
		t.Fatal(basis)
	}
	if got := host.MustDumps(claims[2].(map[string]any)["supersedes"], host.DumpOpts{}); got != `{"collection": "memories", "id": "m1", "revision": 3}` {
		t.Fatal(got)
	}
}

func TestCaptureOneOffRequestsNeverReachTheExtractor(t *testing.T) {
	if cueRE.MatchString("테스트 추가해줘") || !cueRE.MatchString("커밋 메시지는 항상 한국어로 써") {
		t.Fatal()
	}
	if !cueRE.MatchString("그건 규칙이다") || !cueRE.MatchString("기본값이다.") || cueRE.MatchString("이다음") {
		t.Fatal("이다 word boundary")
	}
}

func TestCaptureReflectionKeepsOnlyLessonsTheLogShows(t *testing.T) {
	lines := []string{"사용자: 테스트 안 돌리고\n완료라고 했잖아", "도구 실패: exit 1", "AI: 죄송합니다"}
	raw := []any{
		map[string]any{"subject": "검증", "text": "완료 보고 전에 테스트를 돌린다.", "quote": "완료라고 했잖아", "action": "replace", "target_id": "m1"},
		map[string]any{"subject": "추측", "text": "사용자는 급하다.", "quote": "빨리 해", "action": "new"},
		map[string]any{"subject": "실패", "text": "exit 1이면 원인부터 본다.", "quote": "exit 1", "action": "replace", "target_id": "c1"},
		map[string]any{"subject": "재발", "text": "같은 교훈.", "quote": "죄송", "action": "confirm", "target_id": "c1"},
	}
	checked, err := reflectionClaims(raw, lines, []map[string]any{{"id": "m1", "status": "active"}}, []map[string]any{{"id": "c1", "status": "candidate"}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range checked {
		target := "None"
		if c.target != nil {
			target = str(c.target["id"])
		}
		got = append(got, c.action+"/"+target+"/"+str(c.fromUser))
	}
	if !reflect.DeepEqual(got, []string{"replace/m1/True", "new/None/False", "confirm/c1/False"}) {
		t.Fatal(got)
	}
	if k, _ := checked[0].claim.Get("kind"); k != "lesson" {
		t.Fatal(k)
	}
	if b, _ := checked[0].claim.Get("basis"); b != "inferred" {
		t.Fatal(b)
	}
}

func writeRows(t *testing.T, rows ...string) string {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(rows, "\n")+"\n"+`{"type": "user"`), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCaptureDialogueSkipsALineStillBeingWritten(t *testing.T) {
	path := writeRows(t, `{"type": "user", "entrypoint": "cli", "message": {"content": "그거 틀렸어"}}`,
		`{"type": "user", "message": {"content": [{"type": "tool_result", "is_error": true, "content": "exit 1"}]}}`,
		`{"type": "assistant", "message": {"content": [{"type": "text", "text": "고쳤습니다"}]}}`)
	lines, users, end, entry, err := readDialogue(path, 0, 1500)
	info, _ := os.Stat(path)
	if err != nil || !reflect.DeepEqual(lines, []string{"사용자: 그거 틀렸어", "도구 실패: exit 1", "AI: 고쳤습니다"}) || users != 1 || entry != "cli" || end >= info.Size() {
		t.Fatal(lines, users, end, entry, err)
	}
}

func TestCaptureOnlyTextThePersonTypedIsCaptured(t *testing.T) {
	rows := []string{`{"type": "user", "entrypoint": "claude-desktop", "message": {"content": "앞으로 다크로 해"}}`,
		`{"type": "user", "isCompactSummary": true, "message": {"content": "요약: 항상 라이트로 해"}}`,
		`{"type": "user", "origin": {"kind": "peer"}, "message": {"content": "보고: 틀렸어"}}`,
		`{"type": "user", "origin": {"kind": "task-notification"}, "message": {"content": "완료"}}`}
	path := writeRows(t, rows...)
	users, _, end, entry, err := readNew(path, 0, nil)
	info, _ := os.Stat(path)
	if err != nil || len(users) != 1 || users[0].(map[string]any)["text"] != "앞으로 다크로 해" || entry != "claude-desktop" || end >= info.Size() {
		t.Fatal(users, entry, end, err)
	}
	var typed []int
	for _, r := range rows {
		_, n, _ := recordLines(mustLoad(t, r), 1500)
		typed = append(typed, n)
	}
	if !reflect.DeepEqual(typed, []int{1, 0, 0, 0}) {
		t.Fatal(typed)
	}
	if humanTyped(mustLoad(t, `{"type": "user", "entrypoint": "sdk-cli", "message": {"content": "너는 SEO 전략가다"}}`)) ||
		!humanTyped(mustLoad(t, `{"type": "user", "entrypoint": "sdk-cli", "origin": {"kind": "human"}, "message": {"content": "구글만 지원해"}}`)) {
		t.Fatal("headless")
	}
}

func TestCaptureApplyLessonsFollowsTheServerContract(t *testing.T) {
	capFake(t)
	type call struct {
		path string
		body any
	}
	var calls []call
	capPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
		calls = append(calls, call{path, body})
		if path == "/v1/capture" {
			return mustLoad(t, `{"items": [{"status": "candidate", "reason": "review_required", "candidate_id": "n1"},
				{"status": "candidate", "reason": "review_required", "candidate_id": "n2"}]}`), nil
		}
		if field(body, "action") == "mark" {
			return mustLoad(t, `{"memories": [{"id": "m1", "revision": 3}, {"id": "n1", "revision": 2}], "relation": {"id": "r1", "revision": 1}}`), nil
		}
		return map[string]any{}, nil
	}
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		return map[string]any{"id": pathID(path), "revision": 1, "status": "candidate"}, nil
	}
	lesson := func(text string) host.Obj {
		return host.Obj{{Key: "subject", Value: "s"}, {Key: "text", Value: text}, {Key: "kind", Value: "lesson"}, {Key: "basis", Value: "inferred"}, {Key: "reason", Value: ""}}
	}
	err := applyLessons([]checkedLesson{
		{lesson("a"), "replace", map[string]any{"id": "m1", "revision": 2, "status": "active"}, true},
		{lesson("b"), "new", nil, false},
		{lesson("c"), "confirm", map[string]any{"id": "c1", "revision": 1, "status": "candidate"}, false},
		// its own session reread, or a suspect candidate: never approved
		{lesson("d"), "confirm", map[string]any{"id": "c2", "status": "candidate", "tags": []any{"session:s1"}}, false},
		{lesson("e"), "confirm", map[string]any{"id": "c3", "status": "candidate", "tags": []any{"session:s0", "review:instruction_like"}}, false},
	}, "e1", "s1", "claude_code")
	if err != nil {
		t.Fatal(err)
	}
	var reviews []string
	captures := 0
	var resolve any
	for _, c := range calls {
		if strings.HasSuffix(c.path, "/review") {
			reviews = append(reviews, c.path)
			if !reflect.DeepEqual(keys(c.body), []string{"action", "expected_revision", "scope"}) || field(c.body, "action") != "approve" {
				t.Fatal(keys(c.body))
			}
		}
		if c.path == "/v1/capture" {
			captures++
		}
		if c.path == "/v1/memories/conflicts" && field(c.body, "action") == "resolve" && resolve == nil {
			resolve = c.body
		}
	}
	if !reflect.DeepEqual(reviews, []string{"/v1/memory_candidates/c1/review", "/v1/memory_candidates/n1/review", "/v1/memory_candidates/n2/review"}) {
		t.Fatal(reviews)
	}
	if got := trialSet(); !reflect.DeepEqual(got, []string{"n2"}) { // the tool-failure lesson is on trial; the user's objection is not
		t.Fatal(got)
	}
	if captures != 1 || field(resolve, "resolution") != "second_supersedes_first" || field(resolve, "conflict_id") != "r1" {
		t.Fatal(captures, resolve)
	}
	var revisions []string
	for _, m := range field(resolve, "memories").([]any) {
		revisions = append(revisions, str(field(m, "revision")))
	}
	if !reflect.DeepEqual(revisions, []string{"3", "2"}) {
		t.Fatal(revisions)
	}
}

func TestCaptureInstructionLikeCandidateIsNeverAutoApproved(t *testing.T) {
	capFake(t)
	capGet = func(string, time.Duration) (map[string]any, error) {
		return mustLoad(t, `{"id": "x", "revision": 1, "status": "candidate", "tags": ["basis:inferred", "review:instruction_like"]}`), nil
	}
	if ok, err := approveCandidate("x", "test"); ok || err != nil {
		t.Fatal(ok, err)
	}
}

func TestCaptureAIReviewRulesModelDecisionsAndTrial(t *testing.T) {
	capFake(t)
	items := `[{"id": "ep", "kind": "episodic", "tags": ["basis:assistant_result"]},
		{"id": "inj", "kind": "lesson", "tags": ["basis:inferred", "review:instruction_like"]},
		{"id": "les", "kind": "lesson", "tags": ["basis:inferred"], "text": "병합 뒤 테스트를 돌린다"},
		{"id": "dup", "kind": "decision", "tags": ["basis:explicit_user"], "text": "결론부터 쓴다"}]`
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		if strings.HasPrefix(path, "/v1/memory_candidates?") {
			if strings.Contains(path, "status=candidate") {
				return mustLoad(t, `{"items": `+items+`}`), nil
			}
			return map[string]any{"items": []any{}}, nil
		}
		return map[string]any{"revision": 1, "status": "candidate"}, nil
	}
	var seen []string
	llm = func(_, user, _ string, _ time.Duration, _, _ string) (any, error) {
		seen = append(seen, user)
		return mustLoad(t, `{"d": [{"id": "les", "action": "edit_approve", "content": "병합 커밋에서 테스트를 돌린 뒤 푸시한다", "reason": "구체적"},
			{"id": "dup", "action": "reject", "content": "", "reason": "이미 있는 기억과 같다"},
			{"id": "zzz", "action": "approve", "content": "", "reason": ""}]}`)["d"], nil
	}
	reviews := map[string]any{}
	capPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
		reviews[pathID(path)] = body
		return map[string]any{}, nil
	}
	search = func(string, int, float64) ([]any, error) {
		return []any{map[string]any{"id": "m1", "text": "결론부터 쓴다"}}, nil
	}
	if err := aiReview(false, nil); err != nil {
		t.Fatal(err)
	}
	acts := map[string]any{}
	for id, b := range reviews {
		acts[id] = field(b, "action")
	}
	if !reflect.DeepEqual(acts, map[string]any{"ep": "reject", "inj": "reject", "les": "edit_approve", "dup": "reject"}) {
		t.Fatal(acts)
	}
	if field(reviews["les"], "content") != "병합 커밋에서 테스트를 돌린 뒤 푸시한다" || !strings.HasPrefix(str(field(reviews["dup"], "reason")), "AI 검토: ") {
		t.Fatal(reviews)
	}
	if got := trialSet(); !reflect.DeepEqual(got, []string{"les"}) {
		t.Fatal(got)
	}
	// rule-decided items never reach the model
	if len(seen) != 1 || strings.Contains(seen[0], "ITEM ep") || !strings.Contains(seen[0], "RELATED: 결론부터 쓴다") {
		t.Fatal(seen)
	}
}

func TestCaptureCutShortTrialApprovalIsFinishedLater(t *testing.T) {
	capFake(t)
	status := map[string]string{"n1": "candidate", "n2": "candidate"}
	var approvals []string
	capPost = func(path string, _ any, _ time.Duration) (map[string]any, error) {
		if path == "/v1/capture" {
			return mustLoad(t, `{"items": [{"status": "candidate", "reason": "review_required", "candidate_id": "n1"},
				{"status": "candidate", "reason": "review_required", "candidate_id": "n2"}]}`), nil
		}
		cid := pathID(path)
		approvals = append(approvals, cid)
		status[cid] = "approved"
		if len(approvals) == 1 {
			return nil, runtimeErr("answer_lost") // committed on the server, the answer never came back
		}
		return map[string]any{}, nil
	}
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		return map[string]any{"revision": 1, "status": status[pathID(path)]}, nil
	}
	search = func(string, int, float64) ([]any, error) { return []any{}, nil }
	host.UtilityCounts = func(string) (map[string][3]int, int, error) { return map[string][3]int{}, 0, nil }
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		return []any{map[string]any{"subject": "s", "text": "가", "kind": "decision", "quote": "남이 한 말", "reason": ""},
			map[string]any{"subject": "s", "text": "나", "kind": "decision", "quote": "남이 한 말", "reason": ""}}, nil
	}
	job := mustLoad(t, `{"session_id": "s1", "event_id": "e1", "type": "turn_completed", "users": [{"at": "t", "text": "앞으로 이렇게 해"}]}`)
	if err := process(job, nil); pyType(err) != "RuntimeError" {
		t.Fatal(err)
	}
	if job["captured"] != true { // drain's retry will not capture again
		t.Fatal(job)
	}
	if got := trialSet(); !reflect.DeepEqual(got, []string{"n1", "n2"}) { // both written before the first approval
		t.Fatal(got)
	}
	if err := trialReview(); err != nil { // n1 is already approved and stays on trial; n2 gets approved now
		t.Fatal(err)
	}
	if !reflect.DeepEqual(approvals, []string{"n1", "n2"}) || !reflect.DeepEqual(trialSet(), []string{"n1", "n2"}) || status["n2"] != "approved" {
		t.Fatal(approvals, trialSet(), status)
	}
}

func TestCaptureTrialRetiresUnusedAndGraduatesOld(t *testing.T) {
	today := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	state := host.Obj{{Key: "a", Value: "2026-11-01T00:00:00.000Z"}, {Key: "b", Value: "2026-11-01T00:00:00.000Z"},
		{Key: "c", Value: "2026-10-01T00:00:00.000Z"}, {Key: "d", Value: "2026-11-01T00:00:00.000Z"}, {Key: "e", Value: "2026-11-01T00:00:00.000Z"}}
	counts := map[string][3]int{"a": {5, 0, 0}, "b": {9, 1, 0}, "c": {2, 0, 0}, "d": {4, 0, 0}, "e": {6, 0, 1}} // e: also recalled by Codex
	got, err := trialVerdicts(state, counts, today)
	if err != nil || host.MustDumps(got, host.DumpOpts{}) != `{"a": "retire", "c": "graduate"}` {
		t.Fatal(got, err)
	}
}

func TestCaptureRetireKeepsAConflictedMemoryOnTrial(t *testing.T) {
	capFake(t)
	status := map[string]string{"a": "conflicted", "b": "expired"}
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		if strings.Contains(path, "candidates") {
			return map[string]any{"revision": 1, "status": "approved"}, nil
		}
		return map[string]any{"revision": 1, "status": status[pathID(path)]}, nil
	}
	var posts []string
	capPost = func(path string, _ any, _ time.Duration) (map[string]any, error) {
		posts = append(posts, path)
		return nil, nil
	}
	host.UtilityCounts = func(string) (map[string][3]int, int, error) {
		return map[string][3]int{"a": {5, 0, 0}, "b": {5, 0, 0}}, 0, nil // both due for retirement
	}
	if err := recordTrial([]string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := trialReview(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(trialSet(), []string{"a"}) || len(posts) != 0 {
		t.Fatal(trialSet(), posts)
	}
}

func TestCaptureUsedNeedsTheAnswerToRestateTheMemory(t *testing.T) {
	memory := "운영 Orbit은 SQLite 전용이고 임베딩은 Gemini다."
	if !used(norm(memory), norm("AI: 운영은 SQLite라서 임베딩도 Gemini로 맞춘다")) || used(norm(memory), norm("AI: 테스트를 통과했다")) {
		t.Fatal()
	}
}

func TestCaptureRemovedAgentIsOff(t *testing.T) {
	old := host.AgentsState
	t.Cleanup(func() { host.AgentsState = old })
	host.AgentsState = t.TempDir()
	if host.AgentOff("codex") {
		t.Fatal()
	}
	os.WriteFile(filepath.Join(host.AgentsState, "agents.json"), []byte(`{"removed": ["codex"]}`), 0o600)
	if !host.AgentOff("codex") || host.AgentOff("claude_code") {
		t.Fatal()
	}
}

func TestCaptureCodexAndZcodeLogsKeepOnlyWhatThePersonTyped(t *testing.T) {
	capFake(t)
	dir := t.TempDir()
	item := func(it string) string {
		return `{"type": "event_msg", "payload": {"type": "item_completed", "item": ` + it + `}}`
	}
	body := []string{item(`{"type": "UserMessage", "content": [{"type": "text", "text": "# Files mentioned by the user:\n## a.png\n## My request for Codex:\n배포해줘"}]}`),
		item(`{"type": "UserMessage", "content": [{"type": "text", "text": "<in-app-browser>탭 내용</in-app-browser>"}]}`),
		item(`{"type": "AgentMessage", "content": [{"type": "Text", "text": "배포할게요"}]}`),
		item(`{"type": "CommandExecution", "status": "failed", "aggregated_output": "permission denied"}`),
		item(`{"type": "CommandExecution", "status": "completed", "aggregated_output": "ok"}`)}
	chatPath := filepath.Join(dir, "rollout-a.jsonl")
	os.WriteFile(chatPath, []byte(strings.Join(append([]string{`{"type": "session_meta", "payload": {"originator": "codex-tui", "source": "cli"}}`}, body...), "\n")+"\n"), 0o600)
	r, err := readCodex(chatPath, 0)
	info, _ := os.Stat(chatPath)
	if err != nil || !reflect.DeepEqual(r.lines, []string{"사용자: 배포해줘", "AI: 배포할게요", "도구 실패: permission denied"}) || r.users != 1 || r.end != info.Size() || r.headless {
		t.Fatal(r, err)
	}
	for _, meta := range []string{`{"originator": "codex_exec", "source": "exec"}`, `{"originator": "codex-tui", "source": {"subagent": {}}}`} {
		os.WriteFile(chatPath, []byte(`{"type": "session_meta", "payload": `+meta+"}\n"+body[0]+"\n"), 0o600)
		if r, _ := readCodex(chatPath, 0); !r.headless { // pipelines and subagents are not conversations
			t.Fatal(meta)
		}
	}

	capZcodeDB = filepath.Join(dir, "z.sqlite")
	t.Cleanup(func() { capZcodeDB = host.Env("ARKISM_ZCODE_DB", filepath.Join(host.Home, ".zcode/cli/db/db.sqlite")) })
	db, err := sqlite.Open(capZcodeDB)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Exec(`CREATE TABLE message (id, session_id, sequence, data); CREATE TABLE part (id, message_id, sequence, data);
		INSERT INTO message VALUES ('m0', 's', 0, '{"role": "user", "semantics": {"origin": "real_user"}}');
		INSERT INTO part VALUES ('m0p0', 'm0', 0, '{"type": "text", "text": "고쳐줘"}');
		INSERT INTO message VALUES ('m1', 's', 1, '{"role": "user", "semantics": {"origin": "agent_runtime"}}');
		INSERT INTO part VALUES ('m1p0', 'm1', 0, '{"type": "text", "text": "The TodoWrite tool…"}');
		INSERT INTO message VALUES ('m2', 's', 2, '{"role": "assistant", "time": {"completed": 1}}');
		INSERT INTO part VALUES ('m2p0', 'm2', 0, '{"type": "text", "text": "고쳤어요"}');
		INSERT INTO part VALUES ('m2p1', 'm2', 1, '{"type": "tool", "state": {"status": "error", "error": "File has not been read"}}');
		INSERT INTO message VALUES ('m3', 's', 3, '{"role": "assistant", "time": {}}');
		INSERT INTO part VALUES ('m3p0', 'm3', 0, '{"type": "text", "text": "쓰는 중"}');`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	r, err = readZcode("s", 0)
	if err != nil || !reflect.DeepEqual(r.lines, []string{"사용자: 고쳐줘", "AI: 고쳤어요", "도구 실패: File has not been read"}) || r.users != 1 || r.end != 3 {
		t.Fatal(r, err)
	}
	if r, err := readZcode("s", 3); err != nil || len(r.lines) != 0 || r.users != 0 || r.end != 3 { // still being written: read next time
		t.Fatal(r, err)
	}
}
