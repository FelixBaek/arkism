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
	{
		rp, rs, tn := revPost, reviewSearch, timeNow
		save(func() { revPost, reviewSearch, timeNow = rp, rs, tn })
		// never the live reviewer credential: a test that classifies sets revPost itself
		revPost = func(string, any, time.Duration) (map[string]any, error) { return nil, errReviewerCredential }
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
		got, err := search("query", "", 6, 8)
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
		got, err := search("query", "", 6, 8)
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
	if _, err := search("query", "", 6, 8); host.HTTPCode(err) != 403 {
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
	checked, err := reflectionClaims(raw, lines, []map[string]any{{"id": "m1", "status": "active", "revision": 4}}, []map[string]any{{"id": "c1", "status": "candidate"}})
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
	// A replacement is only suggested: the server holds the new lesson for a person, the old one stays in use.
	if sup, _ := checked[0].claim.Get("supersedes"); host.MustDumps(sup, host.DumpOpts{}) != `{"collection": "memories", "id": "m1", "revision": 4}` {
		t.Fatal(sup)
	}
	for _, c := range checked[1:] {
		if _, has := c.claim.Get("supersedes"); has {
			t.Fatal("only a replace names the lesson it supersedes")
		}
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

func TestCaptureApplyLessonsOnlyCapturesAndNeverApproves(t *testing.T) {
	capFake(t)
	var paths []string
	var sent any
	capPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
		paths = append(paths, path)
		sent = body
		return mustLoad(t, `{"items": [{"status": "candidate", "reason": "review_required", "candidate_id": "n1"},
			{"status": "candidate", "reason": "review_required", "candidate_id": "n2"}]}`), nil
	}
	lesson := func(text string) host.Obj {
		return host.Obj{{Key: "subject", Value: "s"}, {Key: "text", Value: text}, {Key: "kind", Value: "lesson"}, {Key: "basis", Value: "inferred"}, {Key: "reason", Value: ""}}
	}
	err := applyLessons([]checkedLesson{
		{lesson("a"), "replace", map[string]any{"id": "m1", "revision": 2, "status": "active"}, true},
		{lesson("b"), "new", nil, false},
		{lesson("c"), "confirm", map[string]any{"id": "c1", "revision": 1, "status": "candidate"}, false},
	}, "e1", "s1", "claude_code")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"/v1/capture"}) || len(field(sent, "claims").([]any)) != 2 {
		t.Fatal(paths, sent)
	}
	if len(trialState()) != 0 {
		t.Fatal(trialState())
	}
}

func TestCaptureTrialRetiresUnusedAndGraduatesOld(t *testing.T) {
	today := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	state := host.Obj{{Key: "a", Value: "2026-11-01T00:00:00.000Z"}, {Key: "b", Value: "2026-11-01T00:00:00.000Z"},
		{Key: "c", Value: "2026-10-01T00:00:00.000Z"}, {Key: "d", Value: "2026-11-01T00:00:00.000Z"}, {Key: "e", Value: "2026-11-01T00:00:00.000Z"}}
	counts := map[string][3]int{"a": {5, 0, 0}, "b": {9, 1, 0}, "c": {2, 0, 0}, "d": {4, 0, 0}, "e": {6, 0, 1}} // e: also recalled by Codex
	never := func(string) bool { return false }
	got, err := trialVerdicts(state, counts, today, never)
	if err != nil || host.MustDumps(got, host.DumpOpts{}) != `{"a": "retire", "c": "graduate"}` {
		t.Fatal(got, err)
	}
	state = append(state, host.KV{Key: "f", Value: "2026-10-01T00:00:00.000Z"})
	counts["c"], counts["f"] = [3]int{5, 0, 0}, [3]int{5, 0, 0}
	kept := func(id string) bool { return id != "a" }
	got, err = trialVerdicts(state, counts, today, kept) // a kept one due for retirement waits, then graduates
	if err != nil || host.MustDumps(got, host.DumpOpts{}) != `{"a": "retire", "c": "graduate", "f": "graduate"}` {
		t.Fatal(got, err)
	}
}

func TestCaptureKeptOnTrialFindsProhibitionsAndPreferences(t *testing.T) {
	for text, want := range map[string]bool{
		"공유 서버에서 /tmp가 차면 다른 세션의 파일을 지우지 말고 빌드 경로를 바꾼다 (no space left로 실패했다).":                                     true,
		"제품명을 일괄 치환할 때는 경로까지 바뀌지 않도록 범위를 좁힌다 (경로가 소문자로 바뀌었다).":                                                     true,
		"n8n에서 단순 조건이면 AI 노드로 그래프를 구성하지 않는다.":                                                                      true,
		"Prefer table-driven tests; never commit generated files.":                                                 true,
		"PR에 push하기 전에는 단위 테스트가 전부 통과했는지 확인한다 (실패를 확인하지 않고 push했다).":                                               false, // negation only in the incident
		"pgrep으로 판정할 때는 -x 옵션으로 자기 명령을 제외한다 (자기 명령을 잡았다).":                                                         false,
		"공개 저장소 (FelixBaek/arkism)에는 .env 파일을 push하지 않는다.":                                                         true, // a parenthesis inside the rule
		"Tunnel check (GatewayTunnel.cs): readiness comes from a health check, never by listener alone (it hung).": true,
		"확인 전에는 링크 없이 내용만 안내한다 (링크가 깨져 있었다).":                                                                      true,
		"내부 세션 ID 대신 사이드바 제목을 알려 준다.":                                                                              true,
		"/tmp가 아니라 영구 위치에 저장한다.":                                                                                   true,
		"공개 레포에는 아바타를 넣지마.":                                                                                        true,
		"주석에 이모지를 안 쓴다.":                                                                                           true,
		"Docker를 요구하지 않기로 했다.":                                                                                     true,
		"Don’t force-push to main.":                                  true,
		"도커 컨테이너 말고도 pip 패키지까지 확인한다.":                                false,
		"장애가 나면 먼저 피해 범위를 확인해 알린다.":                                  false,
		"목록을 빠짐없이 읽고 마지막에 결과를 낸다.":                                   false,
		"끝났는지 마지막으로 로그를 확인한다.":                                       false,
		"Whenever the build changes, record preferences in the log.": false,
		"고친 부분뿐 아니라 기존 기능도 화면으로 확인한다.":                               false,
		"usage 화면에는 잔여사용량만이 아니라 모든 사용량을 표시한다.":                       false,
		"허용 규칙과 같은 명령 형태로만 실행한다.":                                    true,
		"주석에 이모지를 쓰지 마세요.":                                           true,
		"코드를 검토한다 (변경 파일을 빠뜨리지 않는다).":                                true, // a directive in the note, not an incident
		"사용자가 표시가 안 됐다고 보고하면 서버 응답을 먼저 확인한다.":                        false,
		"메모리는 이 폴더에 넣지 않고 DB에 저장한다.":                                 true,
		"임시 폴더는 홈 바로 아래가 아니라 스크래치패드에 만든다.":                           true,
	} {
		if got := keptOnTrial(map[string]any{"kind": "lesson", "text": text}); got != want {
			t.Errorf("%q: %v", text, got)
		}
	}
	if !keptOnTrial(map[string]any{"kind": "user", "text": "결론부터 쉬운 말로 쓴다."}) || keptOnTrial(map[string]any{"kind": "lesson"}) {
		t.Fatal("kind")
	}
}

func TestCaptureTrialReviewKeepsAnUnusedProhibition(t *testing.T) {
	capFake(t)
	reads := map[string]int{}
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		if strings.Contains(path, "candidates") {
			return map[string]any{"revision": 1, "status": "approved"}, nil
		}
		id := pathID(path)
		reads[id]++
		if id == "e" {
			return nil, &host.HTTPError{Code: 404} // deleted meanwhile
		}
		if id == "c" && reads[id] == 1 {
			return nil, &host.HTTPError{Code: 503} // read once by keep, not retired on a guess
		}
		status := map[string]string{"d": "expired"}[id]
		text := map[string]string{"a": "주석에 이모지를 쓰지 않는다.", "b": "배포 전에 백업을 확인한다."}[id]
		return map[string]any{"revision": 1, "status": or(status, "active"), "kind": "lesson", "text": or(text, "주석에 이모지를 쓰지 않는다.")}, nil
	}
	var posts []string
	capPost = func(path string, _ any, _ time.Duration) (map[string]any, error) {
		posts = append(posts, path)
		return nil, nil
	}
	host.UtilityCounts = func(string) (map[string][3]int, int, error) {
		return map[string][3]int{"a": {5, 0, 0}, "b": {5, 0, 0}, "c": {5, 0, 0}, "d": {5, 0, 0}, "e": {5, 0, 0}}, 0, nil // injected five times, never restated
	}
	if err := seedTrial([]string{"a", "b", "c", "d", "e"}); err != nil {
		t.Fatal(err)
	}
	if err := trialUpdate(func(st *host.Obj) { st.Set("c", "2026-01-01T00:00:00.000Z") }); err != nil { // c is due to graduate too
		t.Fatal(err)
	}
	if err := trialReview(); err != nil {
		t.Fatal(err)
	}
	// a kept; b leaves as unused without any write; c unread, so undecided this run; d expired by a person and e deleted leave as before
	if !reflect.DeepEqual(trialSet(), []string{"a", "c"}) || len(posts) != 0 {
		t.Fatal(trialSet(), posts)
	}
}

func TestCaptureTrialNeverWritesAMemoryStatus(t *testing.T) {
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
	if err := seedTrial([]string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := trialReview(); err != nil {
		t.Fatal(err)
	}
	if len(trialSet()) != 0 || len(posts) != 0 {
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

// seedTrial puts entries on trial the way approvals did before ADR-0022; trialReview still settles them.
func seedTrial(ids []string) error {
	return trialUpdate(func(st *host.Obj) {
		for _, id := range ids {
			st.Set(id, host.NowISO())
		}
	})
}
