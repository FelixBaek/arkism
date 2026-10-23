package capture

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func usageFake(t *testing.T, roots []archiveRoot, index string) {
	oldRoots, oldIndex := usageRoots, usageCodexIndex
	usageRoots = func() []archiveRoot { return roots }
	usageCodexIndex = index
	t.Cleanup(func() { usageRoots, usageCodexIndex = oldRoots, oldIndex })
}

func usageWriteRows(t *testing.T, path string, rows ...any) {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(host.MustDumps(r, host.DumpOpts{}) + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o666); err != nil {
		t.Fatal(err)
	}
}

func usageFind(rows []usageRow, key string) host.Obj {
	for _, r := range rows {
		if r.key == key {
			return r.row
		}
	}
	return nil
}

func usageGet(o host.Obj, keys ...string) any {
	var v any = o
	for _, k := range keys {
		v, _ = v.(host.Obj).Get(k)
	}
	return v
}

func TestUsageClaudeCountsOneMessageOnceAcrossBlocksAndResumedFiles(t *testing.T) {
	d := t.TempDir()
	usage := map[string]any{"input_tokens": 10, "output_tokens": 5, "cache_read_input_tokens": 100, "cache_creation_input_tokens": 0}
	msg := func(block map[string]any, sid string) map[string]any {
		return map[string]any{"type": "assistant", "sessionId": sid, "cwd": "/home/user/project", "timestamp": "2026-10-09T01:00:00Z",
			"message": map[string]any{"id": "m1", "model": "claude-opus-5-5", "usage": usage, "content": []any{block}}}
	}
	usageWriteRows(t, filepath.Join(d, "a.jsonl"),
		map[string]any{"type": "custom-title", "customTitle": "사용량", "sessionId": "s1"},
		map[string]any{"type": "user", "sessionId": "s1", "timestamp": "2026-10-09T00:59:00Z", "message": map[string]any{"content": "hi"}},
		msg(map[string]any{"type": "text", "text": "x"}, "s1"),
		msg(map[string]any{"type": "tool_use", "name": "Skill", "input": map[string]any{"skill": "claude-api"}}, "s1"))
	usageWriteRows(t, filepath.Join(d, "b.jsonl"), msg(map[string]any{"type": "text", "text": "x"}, "s2")) // a resumed copy of m1
	usageFake(t, []archiveRoot{{"claude_code", "server", d, "*.jsonl"}}, usageCodexIndex)
	cache := map[string]*usageFile{}
	rows, _ := usageCollect(host.Obj{}, false, cache)
	raw, _ := json.Marshal(cache) // unchanged files come from the (JSON) cache
	cache = map[string]*usageFile{}
	if err := json.Unmarshal(raw, &cache); err != nil {
		t.Fatal(err)
	}
	again, _ := usageCollect(host.Obj{}, false, cache)
	if canon(usageObj(rows)) != canon(usageObj(again)) {
		t.Fatal("cached result differs")
	}
	row := usageFind(rows, "server|claude_code|s1")
	day := usageGet(row, "days", "2026-10-09", "claude-opus-5-5").(host.Obj)
	for k, want := range map[string]int64{"input": 10, "output": 5, "cache_read": 100, "requests": 1} {
		if v, _ := day.Get(k); v != want {
			t.Fatal(k, v)
		}
	}
	cost, _ := day.Get("cost_usd")
	if f, _ := num(cost); math.Abs(f-(10*4+5*20+100*.2)/1e6) > 1e-12 {
		t.Fatal(cost)
	}
	if usageGet(row, "title") != "사용량" || usageGet(row, "project") != "project" || usageGet(row, "messages") != int64(1) ||
		canon(usageGet(row, "skills")) != `{"claude-api":1}` {
		t.Fatal(row)
	}
	if usageFind(rows, "server|claude_code|s2") != nil { // nothing of its own to count
		t.Fatal("s2 counted")
	}
}

func TestUsageCodexSplitsCumulativeTotalsAndRemovesCachedInput(t *testing.T) {
	d := t.TempDir()
	tc := func(ts string, i, c, o int) map[string]any {
		return map[string]any{"type": "event_msg", "timestamp": ts, "payload": map[string]any{"type": "token_count", "info": map[string]any{
			"total_token_usage": map[string]any{"input_tokens": i, "cached_input_tokens": c, "output_tokens": o}}}}
	}
	usageWriteRows(t, filepath.Join(d, "rollout.jsonl"),
		map[string]any{"type": "session_meta", "timestamp": "2026-10-08T14:00:00Z", "payload": map[string]any{"id": "c1", "cwd": "/root"}},
		map[string]any{"type": "turn_context", "timestamp": "2026-10-08T14:00:01Z", "payload": map[string]any{"model": "gpt-6.1-sol"}},
		map[string]any{"type": "event_msg", "timestamp": "2026-10-08T14:00:01Z", "payload": map[string]any{"type": "user_message"}},
		tc("2026-10-08T14:00:05Z", 100, 40, 10),  // 23:00 in Seoul
		tc("2026-10-08T15:30:00Z", 300, 200, 30)) // next day in Seoul
	index := filepath.Join(d, "index.jsonl")
	usageWriteRows(t, index, map[string]any{"id": "c1", "thread_name": "이름"})
	usageFake(t, []archiveRoot{{"codex", "server", d, "rollout*.jsonl"}}, index)
	rows, _ := usageCollect(host.Obj{}, false, map[string]*usageFile{})
	row := usageFind(rows, "server|codex|c1")
	if usageGet(row, "title") != "이름" {
		t.Fatal(row)
	}
	if got := canon(usageGet(row, "days", "2026-10-08", "gpt-6.1-sol")); got != `{"cache_read":40,"cache_write":0,"cost_usd":0.00018,"input":60,"output":10,"requests":1}` {
		t.Fatal(got)
	}
	if got := canon(usageGet(row, "days", "2026-10-09", "gpt-6.1-sol")); got != `{"cache_read":160,"cache_write":0,"cost_usd":0.00027,"input":40,"output":20,"requests":1}` {
		t.Fatal(got)
	}
}

func usageObj(rows []usageRow) host.Obj {
	o := host.Obj{}
	for _, r := range rows {
		o = append(o, host.KV{Key: r.key, Value: r.row})
	}
	return o
}

// TestUsageCollectGolden compares rows, the changed set, digests and the POST body with
// what `arkism usage-index` produced from testdata/usage_input.
func TestUsageCollectGolden(t *testing.T) {
	src, _ := filepath.Abs("testdata/usage_input")
	usageFake(t, []archiveRoot{
		{"claude_code", "server", filepath.Join(src, "claude"), "*.jsonl"},
		{"codex", "server", filepath.Join(src, "codex"), "rollout*.jsonl"},
		{"grok", "server", filepath.Join(src, "grok"), "*.jsonl"},
		{"grok", "server", filepath.Join(src, "grok"), "summary.json"},
		{"kiro", "server", filepath.Join(src, "kiro"), "messages.jsonl"},
		{"antigravity-cli", "server", filepath.Join(src, "ag"), "*.db"},
		{"codex", "pc", filepath.Join(src, "pc-codex"), "*.jsonl"},
		{"qwen", "server", filepath.Join(src, "missing"), "*.jsonl"},
	}, filepath.Join(src, "codex_index.jsonl"))
	for k, v := range map[string]string{"ARKISM_WORKSPACE": "ws", "ARKISM_PROJECT": "pj", "ARKISM_USER": "us", "ARKISM_AGENT": "ag"} {
		t.Setenv(k, v)
	}
	cache := map[string]*usageFile{}
	rows, _ := usageCollect(host.Obj{}, false, cache)
	state := host.Obj{{Key: "sent", Value: host.Obj{{Key: rows[0].key, Value: usageDigest(rows[0].row)}, {Key: "gone|x|y", Value: "0"}}}}
	rows2, changed := usageCollect(state, true, cache)
	if canon(usageObj(rows)) != canon(usageObj(rows2)) {
		t.Fatal("second pass differs")
	}
	keys, items, digests := []any{}, []any{}, host.Obj{}
	for _, r := range changed {
		keys = append(keys, r.key)
		items = append(items, r.row)
	}
	for _, r := range rows {
		digests = append(digests, host.KV{Key: r.key, Value: usageDigest(r.row)})
	}

	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		if r.URL.Path != "/v1/usage/sessions" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(400)
		}
		w.Write([]byte("{}"))
	}))
	defer srv.Close()
	token := filepath.Join(t.TempDir(), "token")
	os.WriteFile(token, []byte("tok\n"), 0o600)
	oldURL, oldToken := host.BaseURL, host.TokenFile
	host.BaseURL, host.TokenFile = srv.URL, token
	defer func() { host.BaseURL, host.TokenFile = oldURL, oldToken }()
	if err := usagePostRows(items); err != nil {
		t.Fatal(err)
	}

	got := host.MustDumps(host.Obj{{Key: "rows", Value: usageObj(rows)}, {Key: "changed", Value: keys}, {Key: "digests", Value: digests},
		{Key: "payload", Value: body}}, host.DumpOpts{Compact: true, NoASCII: true}) + "\n"
	want, err := os.ReadFile("testdata/usage_collect.json")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("golden mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestUsageMathGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/usage_math.json")
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Sums []struct {
			Stamps []int64
		}
		Prices []struct {
			Model  string
			Tokens [4]int64
		}
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	sums, prices := []any{}, []any{}
	for _, c := range in.Sums {
		s := &usageSession{Stamps: c.Stamps}
		s.tokens(1_000_000, "m", usageTok{Input: 1})
		row := s.row()
		stamps := []any{}
		for _, x := range c.Stamps {
			stamps = append(stamps, x)
		}
		sums = append(sums, host.Obj{{Key: "stamps", Value: stamps}, {Key: "active_seconds", Value: usageGet(row, "active_seconds")},
			{Key: "started_at", Value: usageGet(row, "started_at")}, {Key: "hours", Value: usageGet(row, "hours")}})
	}
	for _, c := range in.Prices {
		var cost any
		if f, ok := usagePrice(c.Model, usageTok{Input: c.Tokens[0], Output: c.Tokens[1], CacheRead: c.Tokens[2], CacheWrite: c.Tokens[3], Requests: 1}); ok {
			cost = f
		}
		prices = append(prices, host.Obj{{Key: "model", Value: c.Model}, {Key: "tokens", Value: []any{c.Tokens[0], c.Tokens[1], c.Tokens[2], c.Tokens[3]}},
			{Key: "cost", Value: cost}})
	}
	got := host.MustDumps(host.Obj{{Key: "sums", Value: sums}, {Key: "prices", Value: prices}}, host.DumpOpts{Compact: true}) + "\n"
	if got != string(raw) {
		t.Fatalf("golden mismatch\n got: %s\nwant: %s", got, raw)
	}
}

func TestUsageMainPlansAndWritesTheJSONCache(t *testing.T) {
	d := t.TempDir()
	usageFake(t, []archiveRoot{{"claude_code", "server", d, "*.jsonl"}}, filepath.Join(d, "none"))
	old := usageState
	usageState = filepath.Join(d, "state", "state.json")
	defer func() { usageState = old }()
	var out, errb strings.Builder
	if code := usageMain(nil, nil, &out, &errb); code != 0 || out.String() != `{"sessions": 0, "changed": 0, "sent": 0, "failed": 0}`+"\n" {
		t.Fatal(code, out.String(), errb.String())
	}
	if _, err := os.Stat(filepath.Join(d, "state", "files.json")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := usageMain([]string{"--bogus"}, nil, &out, &errb); code != 2 || !strings.Contains(errb.String(), "unrecognized arguments: --bogus") {
		t.Fatal(code, errb.String())
	}
}

func TestUsageSendBatchesRowsAndKeepsDigestsOnlyForSentBatches(t *testing.T) {
	old := usagePostRows
	defer func() { usagePostRows = old }()
	var sizes []int
	usagePostRows = func(items []any) error {
		sizes = append(sizes, len(items))
		if len(sizes) == 1 {
			return &host.HTTPError{Code: 503}
		}
		return nil
	}
	rows := make([]usageRow, 150)
	for i := range rows {
		rows[i] = usageRow{key: "k" + string(rune('a'+i%26)) + strings.Repeat("x", i/26), row: host.Obj{}}
		rows[i].row.Set("n", i)
	}
	state, out := host.Obj{}, host.Obj{}
	nsent, nfailed := usageSend(&state, rows, &out)
	if nsent != 50 || nfailed != 100 || len(sizes) != 2 || sizes[0] != 100 || sizes[1] != 50 {
		t.Fatal(nsent, nfailed, sizes)
	}
	if code, _ := out.Get("error"); code != 503 {
		t.Fatal(code)
	}
	v, _ := state.Get("sent")
	sent := v.(host.Obj)
	if _, ok := sent.Get(rows[0].key); ok {
		t.Fatal("failed batch recorded as sent")
	}
	if got, _ := sent.Get(rows[149].key); got != usageDigest(rows[149].row) {
		t.Fatal(got)
	}

	usagePostRows = func([]any) error { return io.EOF }
	out = host.Obj{}
	if _, nfailed := usageSend(&state, rows[:1], &out); nfailed != 1 {
		t.Fatal(nfailed)
	}
	if code, _ := out.Get("error"); code != "unavailable" {
		t.Fatal(code)
	}
}

func TestUsageReadStateRejectsBrokenJSONAndTreatsMissingAsEmpty(t *testing.T) {
	d := t.TempDir()
	old := usageState
	defer func() { usageState = old }()
	usageState = filepath.Join(d, "missing.json")
	if state, err := usageReadState(); err != nil || len(state) != 0 {
		t.Fatal(state, err)
	}
	usageState = filepath.Join(d, "broken.json")
	if err := os.WriteFile(usageState, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := usageReadState(); err == nil {
		t.Fatal("broken state accepted")
	}
}
