package crewrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStore keeps runs like the Arkism runs API: revisions, finished runs
// frozen, decisions only through decide, credential-looking outputs refused.
type fakeStore struct {
	mu   sync.Mutex
	crew []CrewItem
	runs map[string]Run
	n    int
	// refuse makes outputs containing it fail as the server's invalid_request.
	refuse string
}

func newFakeStore(items ...CrewItem) *fakeStore {
	return &fakeStore{crew: items, runs: map[string]Run{}}
}

func clone(r Run) Run {
	b, _ := json.Marshal(r)
	var c Run
	_ = json.Unmarshal(b, &c)
	return c
}

func (f *fakeStore) ListCrew(context.Context) ([]CrewItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]CrewItem{}, f.crew...), nil
}

func (f *fakeStore) ListRuns(_ context.Context, status string, limit int) ([]Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Run
	for _, r := range f.runs {
		if status == "" || r.State.Status == status {
			out = append(out, clone(r))
		}
	}
	return out, nil
}

func (f *fakeStore) GetRun(_ context.Context, id string) (Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[id]
	if !ok {
		return Run{}, &APIError{404, "not_found"}
	}
	return clone(r), nil
}

func (f *fakeStore) CreateRun(_ context.Context, st RunState) (Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	r := clone(Run{ID: fmt.Sprintf("run-%d", f.n), Revision: 1, State: st})
	f.runs[r.ID] = r
	return clone(r), nil
}

func (f *fakeStore) UpdateRun(_ context.Context, r Run) (Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	old, ok := f.runs[r.ID]
	if !ok {
		return Run{}, &APIError{404, "not_found"}
	}
	if old.Revision != r.Revision || terminal(old.State.Status) {
		return Run{}, &APIError{409, "revision_conflict"}
	}
	if strings.Contains(r.State.Summary, "sk-live-") || strings.Contains(r.State.Input, "sk-live-") {
		return Run{}, &APIError{422, "secret_detected"}
	}
	for _, n := range r.State.Nodes {
		if strings.Contains(n.Output, "sk-live-") || strings.Contains(n.Error, "sk-live-") {
			return Run{}, &APIError{422, "secret_detected"}
		}
		if len([]rune(n.Output)) > maxStored || f.refuse != "" && strings.Contains(n.Output, f.refuse) {
			return Run{}, &APIError{400, "invalid_request"}
		}
		// Like the server's keepsApprovals: a decided approval keeps its {approved, note} output.
		if n.Status == "done" && (n.Port == "approve" || n.Port == "reject") && !strings.Contains(n.Output, `"approved"`) {
			return Run{}, &APIError{400, "invalid_request"}
		}
	}
	if (r.State.Status == "waiting") != (r.State.Waiting != nil) {
		return Run{}, &APIError{400, "invalid_request"}
	}
	c := clone(r)
	c.State.Decisions = nil // kept from the stored run, except for nodes sent back to pending
	for id, d := range old.State.Decisions {
		if n := r.State.Nodes[id]; n == nil || n.Status != "pending" {
			if c.State.Decisions == nil {
				c.State.Decisions = map[string]Decision{}
			}
			c.State.Decisions[id] = d
		}
	}
	c.Revision = old.Revision + 1
	f.runs[r.ID] = c
	return clone(c), nil
}

func (f *fakeStore) decide(t *testing.T, id, node string, approve bool) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.runs[id]
	if r.State.Status != "waiting" || r.State.Waiting.NodeID != node {
		t.Fatalf("not waiting on %s: %+v", node, r.State)
	}
	if r.State.Decisions == nil {
		r.State.Decisions = map[string]Decision{}
	}
	r.State.Decisions[node] = Decision{Approve: approve, Note: "ok", DecidedBy: "human-1", DecidedAt: time.Now()}
	r.Revision++
	f.runs[id] = r
}

func (f *fakeStore) stop(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.runs[id]
	r.State.Status, r.State.Waiting = "stopped", nil
	r.Revision++
	f.runs[id] = r
}

func testEngine(t *testing.T, fs *fakeStore) *Engine {
	dir := t.TempDir()
	return &Engine{Store: fs, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), DefaultModel: "opus 5.5", Loc: time.UTC,
		KeysFile: filepath.Join(dir, "provider-keys.json"), NotifyFile: filepath.Join(dir, "notify.json"),
		HooksFile: filepath.Join(dir, "crew-hooks.json"), PublicURL: "http://127.0.0.1:1"}
}

func wf(id string, g *Graph) CrewItem {
	return CrewItem{ID: id, Revision: 1, Crew: Crew{Type: "workflow", Name: id, Handle: id, Enabled: true, Trigger: &Trigger{Kind: "webhook"}, Graph: g}}
}

func node(id, typ string, kv ...string) Node {
	c := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		c[kv[i]] = kv[i+1]
	}
	return Node{ID: id, Type: typ, Title: id, Config: c}
}

// back is a back edge followed at most max times.
func back(from, to, port string, maxN int) Edge {
	return Edge{From: from, To: to, Port: port, Max: maxN}
}

func edge(from, to string, port ...string) Edge {
	e := Edge{From: from, To: to}
	if len(port) > 0 {
		e.Port = port[0]
	}
	return e
}

func runOnce(t *testing.T, e *Engine, crewID string, input any) Run {
	t.Helper()
	ctx := context.Background()
	if err := e.LoadCrew(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := e.StartRun(ctx, crewID, "manual", input)
	if err != nil {
		t.Fatal("start", err)
	}
	if err := e.Drive(ctx, r.ID); err != nil {
		t.Fatal("drive", err)
	}
	got, _ := e.Store.GetRun(ctx, r.ID)
	return got
}

func statuses(r Run) map[string]string {
	m := map[string]string{}
	for id, n := range r.State.Nodes {
		m[id] = n.Status + "/" + n.Port
	}
	return m
}

func TestCronMatching(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		spec, when string
		want       bool
	}{
		{"* * * * *", "2026-10-02 11:07", true},
		{"0 8 * * 1-5", "2026-10-02 08:00", true}, // Friday
		{"0 8 * * 1-5", "2026-10-03 08:00", false},
		{"0 8 * * 1-5", "2026-10-02 08:01", false},
		{"*/15 * * * *", "2026-10-02 11:45", true},
		{"*/15 * * * *", "2026-10-02 11:46", false},
		{"5-20/5 9,18 * * *", "2026-10-02 18:15", true},
		{"5-20/5 9,18 * * *", "2026-10-02 18:25", false},
		{"0 0 1 * *", "2026-11-01 00:00", true},
		{"0 0 * * 7", "2026-10-04 00:00", true}, // Sunday as 7
		{"0 0 * * 0", "2026-10-04 00:00", true},
		{"0 0 13 * 5", "2026-10-02 00:00", true},   // dom or dow: Friday matches
		{"0 0 */2 * *", "2026-10-02 00:00", false}, // stepped day of month still limits days
		{"0 0 */2 * *", "2026-10-03 00:00", true},
		{"0 0 13 * 5", "2026-10-13 00:00", true}, // the 13th matches
		{"0 0 13 * 5", "2026-10-14 00:00", false},
		{"30 12 * 2 *", "2026-02-10 12:30", true},
		{"30 12 * 2 *", "2026-03-10 12:30", false},
	}
	for _, c := range cases {
		cr, err := ParseCron(c.spec)
		if err != nil {
			t.Fatal(c.spec, err)
		}
		if got := cr.Match(at(c.when)); got != c.want {
			t.Errorf("%q at %s = %v", c.spec, c.when, got)
		}
	}
	for _, bad := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "*/0 * * * *", "5-1 * * * *", "a * * * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestConditionExpressions(t *testing.T) {
	var in any
	_ = json.Unmarshal([]byte(`{"tests":{"failed":0,"passed":12},"status":"ok","items":[1,2],"tag":"v1.2","flag":false,"name":"배포"}`), &in)
	for expr, want := range map[string]bool{
		`in.tests.failed == 0`:                              true,
		`in.tests.failed != 0`:                              false,
		`in.tests.passed > 10 and in.status == "ok"`:        true,
		`in.tests.passed >= 12 && in.tests.passed <= 12`:    true,
		`in.tests.passed < 3 or in.status == 'ok'`:          true,
		`not in.flag`:                                       true,
		`!(in.tests.failed == 0)`:                           false,
		`in.missing == null`:                                true,
		`in.missing`:                                        false,
		`in.items`:                                          true,
		`in.items.1 == 2`:                                   true,
		`in.tag == "v1.2" and (in.flag or in.items.0 == 1)`: true,
		`in.name == "배포"`:                                   true,
		`in.status == 1`:                                    false,
		`-1 < in.tests.failed`:                              true,
	} {
		got, err := EvalCondition(expr, in)
		if err != nil || got != want {
			t.Errorf("%s = %v (%v), want %v", expr, got, err, want)
		}
	}
	for _, bad := range []string{``, `in.a ==`, `(in.a`, `in.a = 1`, `os.exit(1)`, `in.a == "x`, `in.a and`, `1 2`, `in.status > 1`} {
		if _, err := EvalCondition(bad, in); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := EvalCondition(strings.Repeat("(", 100)+"1"+strings.Repeat(")", 100), in); err == nil {
		t.Error("accepted a too-deep expression")
	}
}

func TestBranchesSkipsAndMerge(t *testing.T) {
	g := &Graph{
		Nodes: []Node{node("start", "start"), node("check", "condition", "expr", "in.n > 1"),
			node("big", "code", "lang", "sh", "code", `echo '"big"'`), node("small", "code", "lang", "sh", "code", `echo '"small"'`),
			node("side", "wait", "duration", "0s"), node("join", "merge"), node("end", "end", "summary", "결과 {{in.big}}")},
		Edges: []Edge{edge("start", "check"), edge("check", "big", "yes"), edge("check", "small", "no"), edge("start", "side"),
			edge("big", "join"), edge("small", "join"), edge("side", "join"), edge("join", "end")},
	}
	fs := newFakeStore(wf("auto", g))
	r := runOnce(t, testEngine(t, fs), "auto", map[string]any{"n": 5})
	st := statuses(r)
	if r.State.Status != "succeeded" || st["check"] != "done/yes" || st["big"] != "done/" || st["small"] != "skipped/" || st["join"] != "done/" {
		t.Fatalf("branch run: %s %v", r.State.Status, st)
	}
	var merged map[string]any
	_ = json.Unmarshal([]byte(r.State.Nodes["join"].Output), &merged)
	if len(merged) != 2 || merged["big"] != "big" || merged["side"] == nil {
		t.Fatal("merge output keyed by followed sources", merged)
	}
	if r.State.Summary != "결과 big" {
		t.Fatal("summary", r.State.Summary)
	}
	// The other branch: nothing follows "big", so a node only reachable from it is skipped.
	r = runOnce(t, testEngine(t, fs), "auto", map[string]any{"n": 0})
	if st = statuses(r); st["big"] != "skipped/" || st["small"] != "done/" || r.State.Status != "succeeded" {
		t.Fatal("no branch", st)
	}
}

func TestFailedNodeFailsRunAndSkipsRest(t *testing.T) {
	g := &Graph{Nodes: []Node{node("start", "start"), node("boom", "code", "lang", "sh", "code", "echo nope >&2; exit 3"), node("end", "end")},
		Edges: []Edge{edge("start", "boom"), edge("boom", "end")}}
	r := runOnce(t, testEngine(t, newFakeStore(wf("a", g))), "a", nil)
	if r.State.Status != "failed" || r.State.Nodes["boom"].Status != "failed" || r.State.Nodes["end"].Status != "skipped" ||
		!strings.HasPrefix(r.State.Nodes["boom"].Error, "exit: exit 3: nope") {
		t.Fatal("failure", r.State.Status, statuses(r), r.State.Nodes["boom"].Error)
	}
}

// Two read nodes meet at a rendezvous, which only works if they run together.
func TestReadNodesRunTogether(t *testing.T) {
	script := func(me, other string) string {
		return `d=$(cat | sed 's/.*"dir":"\([^"]*\)".*/\1/'); touch "$d/` + me + `"; i=0; while [ $i -lt 50 ]; do [ -f "$d/` + other + `" ] && { echo '"` + me + `"'; exit 0; }; sleep 0.1; i=$((i+1)); done; exit 1`
	}
	g := &Graph{Nodes: []Node{node("start", "start"), node("a", "code", "lang", "sh", "access", "read", "code", script("a", "b")),
		node("b", "code", "lang", "sh", "access", "read", "code", script("b", "a"))},
		Edges: []Edge{edge("start", "a"), edge("start", "b")}}
	r := runOnce(t, testEngine(t, newFakeStore(wf("a", g))), "a", map[string]any{"dir": t.TempDir()})
	if r.State.Status != "succeeded" {
		t.Fatal("read nodes did not run together", statuses(r), r.State.Nodes["a"].Error)
	}
	// The same pair without access=read runs one at a time and the rendezvous fails.
	for _, n := range g.Nodes {
		delete(n.Config, "access")
	}
	e := testEngine(t, newFakeStore(wf("a", g)))
	e.CodeTimeout = 2 * time.Second
	if r = runOnce(t, e, "a", map[string]any{"dir": t.TempDir()}); r.State.Status != "failed" {
		t.Fatal("write nodes ran together", statuses(r))
	}
}

func TestLoopRepeatsNextNode(t *testing.T) {
	g := &Graph{Nodes: []Node{node("start", "start"), node("each", "loop", "path", "in.items", "concurrency", "2"),
		node("double", "code", "lang", "sh", "code", `read x; echo $((x*2))`), node("end", "end")},
		Edges: []Edge{edge("start", "each"), edge("each", "double"), edge("double", "end")}}
	r := runOnce(t, testEngine(t, newFakeStore(wf("a", g))), "a", map[string]any{"items": []int{1, 2, 3}})
	if r.State.Status != "succeeded" || r.State.Nodes["each"].Output != "[2,4,6]" || r.State.Nodes["double"].Output != "[2,4,6]" ||
		r.State.Nodes["end"].Status != "done" || r.State.Summary != "[2,4,6]" {
		t.Fatal("loop", r.State.Status, statuses(r), r.State.Nodes["each"].Output, r.State.Summary)
	}
	bad := &Graph{Nodes: []Node{node("start", "start"), node("each", "loop", "path", "in.items"), node("ok", "approval")},
		Edges: []Edge{edge("start", "each"), edge("each", "ok")}}
	if r = runOnce(t, testEngine(t, newFakeStore(wf("a", bad))), "a", map[string]any{"items": []int{1}}); r.State.Status != "failed" {
		t.Fatal("a loop repeated an approval", statuses(r))
	}
}

func TestApprovalWaitsForHumanDecision(t *testing.T) {
	g := &Graph{Nodes: []Node{node("start", "start"), node("ok", "approval", "question", "{{in.version}} 배포할까요?"),
		node("deploy", "wait", "duration", "0s"), node("cancel", "wait", "duration", "0s")},
		Edges: []Edge{edge("start", "ok"), edge("ok", "deploy", "approve"), edge("ok", "cancel", "reject")}}
	fs := newFakeStore(wf("a", g))
	e := testEngine(t, fs)
	r := runOnce(t, e, "a", map[string]any{"version": "v2"})
	if r.State.Status != "waiting" || r.State.Waiting.Kind != "approval" || r.State.Waiting.Question != "v2 배포할까요?" || r.State.Nodes["ok"].Status != "waiting" {
		t.Fatal("approval wait", r.State.Status, r.State.Waiting)
	}
	if readyToResume(r, time.Now()) {
		t.Fatal("resumable before a decision")
	}
	if err := e.Drive(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := fs.GetRun(context.Background(), r.ID); got.State.Status != "waiting" {
		t.Fatal("moved without a decision")
	}
	fs.decide(t, r.ID, "ok", true)
	if err := e.PollWaiting(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	got, _ := fs.GetRun(context.Background(), r.ID)
	if st := statuses(got); got.State.Status != "succeeded" || st["ok"] != "done/approve" || st["deploy"] != "done/" || st["cancel"] != "skipped/" {
		t.Fatal("after approval", got.State.Status, st)
	}
}

func TestStoppedRunEndsDrive(t *testing.T) {
	g := &Graph{Nodes: []Node{node("start", "start"), node("ok", "approval")}, Edges: []Edge{edge("start", "ok")}}
	fs := newFakeStore(wf("a", g))
	e := testEngine(t, fs)
	r := runOnce(t, e, "a", nil)
	fs.stop(r.ID)
	if err := e.Drive(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := fs.GetRun(context.Background(), r.ID); got.State.Status != "stopped" {
		t.Fatal("stopped run changed", got.State.Status)
	}
}

func TestWaitResumesAfterRestart(t *testing.T) {
	g := &Graph{Nodes: []Node{node("start", "start"), node("w", "wait", "duration", "1h"), node("end", "end")},
		Edges: []Edge{edge("start", "w"), edge("w", "end")}}
	fs := newFakeStore(wf("a", g))
	t0 := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	e := testEngine(t, fs)
	e.Now = func() time.Time { return t0 }
	r := runOnce(t, e, "a", map[string]any{"x": 1})
	if r.State.Status != "waiting" || r.State.Waiting.Kind != "wait" || !r.State.Waiting.ResumeAt.Equal(t0.Add(time.Hour)) {
		t.Fatal("wait", r.State.Status, r.State.Waiting)
	}
	// A new engine process: before resume_at nothing moves, after it the run ends.
	later := testEngine(t, fs)
	now := t0.Add(30 * time.Minute)
	later.Now = func() time.Time { return now }
	if err := later.LoadCrew(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = later.PollWaiting(context.Background())
	later.Wait()
	if got, _ := fs.GetRun(context.Background(), r.ID); got.State.Status != "waiting" {
		t.Fatal("resumed early")
	}
	now = t0.Add(61 * time.Minute)
	_ = later.PollWaiting(context.Background())
	later.Wait()
	got, _ := fs.GetRun(context.Background(), r.ID)
	if got.State.Status != "succeeded" || got.State.Nodes["w"].Output != `{"x":1}` || got.State.Summary != `{"x":1}` {
		t.Fatal("after wait", got.State.Status, statuses(got), got.State.Summary)
	}
}

func TestInterruptedWriteNodeIsNotRepeated(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	g := &Graph{Nodes: []Node{node("start", "start"), node("deploy", "code", "lang", "sh", "code", "touch "+marker), node("look", "code", "lang", "sh", "access", "read", "code", `echo '"seen"'`)},
		Edges: []Edge{edge("start", "deploy"), edge("start", "look")}}
	fs := newFakeStore(wf("a", g))
	ctx := context.Background()
	r, _ := fs.CreateRun(ctx, RunState{CrewID: "a", CrewRevision: 1, Trigger: "manual", Status: "running",
		Nodes: map[string]*NodeState{"start": {Status: "done", Output: "{}"}, "deploy": {Status: "running"}, "look": {Status: "pending"}}})
	e := testEngine(t, fs)
	_ = e.LoadCrew(ctx)
	if err := e.ResumeRunning(ctx); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	got, _ := fs.GetRun(ctx, r.ID)
	if got.State.Status != "failed" || !strings.HasPrefix(got.State.Nodes["deploy"].Error, "interrupted") {
		t.Fatal("interrupted", got.State.Status, statuses(got))
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("an interrupted write step ran again")
	}
}

func TestCrewChangedFailsResume(t *testing.T) {
	g := &Graph{Nodes: []Node{node("start", "start"), node("ok", "approval")}, Edges: []Edge{edge("start", "ok")}}
	fs := newFakeStore(wf("a", g))
	e := testEngine(t, fs)
	r := runOnce(t, e, "a", nil)
	fs.decide(t, r.ID, "ok", true)
	fs.crew[0].Revision = 2
	_ = e.LoadCrew(context.Background())
	_ = e.Drive(context.Background(), r.ID)
	if got, _ := fs.GetRun(context.Background(), r.ID); got.State.Status != "failed" || !strings.HasPrefix(got.State.Summary, "crew_changed") {
		t.Fatal("changed definition", got.State.Status, got.State.Summary)
	}
}

func TestWebhookTokenCheck(t *testing.T) {
	g := &Graph{Nodes: []Node{node("hook", "webhook"), node("end", "end", "summary", "{{in.event}}")}, Edges: []Edge{edge("hook", "end")}}
	off := wf("off", g)
	off.Crew.Enabled = false
	fs := newFakeStore(wf("auto", g), off)
	e := testEngine(t, fs)
	ctx := context.Background()
	_ = e.LoadCrew(ctx)
	tok, err := HookToken(e.HooksFile, "auto", true)
	if err != nil || len(tok) != 64 {
		t.Fatal("hook token", err)
	}
	if again, _ := HookToken(e.HooksFile, "auto", true); again != tok {
		t.Fatal("token not kept")
	}
	if st, _ := os.Stat(e.HooksFile); st.Mode().Perm() != 0o600 {
		t.Fatal("hooks file mode", st.Mode())
	}
	offTok, _ := HookToken(e.HooksFile, "off", true)
	srv := httptest.NewServer(e.Handler(ctx))
	defer srv.Close()
	post := func(id, token string) (int, string) {
		req, _ := http.NewRequest("POST", srv.URL+"/hooks/"+id, strings.NewReader(`{"event":"push"}`))
		if token != "" {
			req.Header.Set("X-Arkism-Hook-Token", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out["run_id"]
	}
	if code, _ := post("auto", ""); code != 403 {
		t.Fatal("no token", code)
	}
	if code, _ := post("auto", tok[:63]+"x"); code != 403 {
		t.Fatal("wrong token", code)
	}
	if code, _ := post("off", offTok); code != 404 {
		t.Fatal("disabled automation", code)
	}
	code, id := post("auto", tok)
	if code != 202 || id == "" {
		t.Fatal("webhook", code)
	}
	e.Wait()
	if got, _ := fs.GetRun(ctx, id); got.State.Trigger != "webhook" || got.State.Status != "succeeded" || got.State.Summary != "push" {
		t.Fatal("webhook run", got.State)
	}
	_ = os.Chmod(e.HooksFile, 0o644)
	if code, _ := post("auto", tok); code != 403 {
		t.Fatal("hooks file readable by others still used", code)
	}
}

func TestScheduleFiresOncePerMinute(t *testing.T) {
	g := &Graph{Nodes: []Node{node("s", "schedule")}}
	a := wf("brief", g)
	a.Crew.Trigger = &Trigger{Kind: "schedule", Cron: "*/5 * * * *"}
	off := wf("off", g)
	off.Crew.Trigger, off.Crew.Enabled = &Trigger{Kind: "schedule", Cron: "* * * * *"}, false
	fs := newFakeStore(a, off)
	e := testEngine(t, fs)
	ctx := context.Background()
	_ = e.LoadCrew(ctx)
	at := time.Date(2026, 10, 2, 10, 5, 10, 0, time.UTC)
	if got := e.FireSchedules(ctx, at); len(got) != 1 {
		t.Fatal("first tick", got)
	}
	for _, later := range []time.Duration{20 * time.Second, 40 * time.Second, time.Minute} {
		if got := e.FireSchedules(ctx, at.Add(later)); len(got) != 0 {
			t.Fatal("fired again", later, got)
		}
	}
	if got := e.FireSchedules(ctx, at.Add(5*time.Minute)); len(got) != 1 {
		t.Fatal("next slot", got)
	}
	e.Wait()
	runs, _ := fs.ListRuns(ctx, "", 10)
	for _, r := range runs {
		if r.State.Trigger != "schedule" || r.State.CrewID != "brief" || r.State.Status != "succeeded" {
			t.Fatal("schedule run", r.State)
		}
	}
}

func TestCodeNodes(t *testing.T) {
	e := testEngine(t, newFakeStore())
	e.CodeTimeout = 500 * time.Millisecond
	start := time.Now()
	_, err := e.runCode(context.Background(), map[string]string{"lang": "sh", "code": "sleep 5 & sleep 5"}, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "timeout") || time.Since(start) > 4*time.Second {
		t.Fatal("sh timeout", err, time.Since(start))
	}
	out, err := e.runCode(context.Background(), map[string]string{"lang": "sh", "code": `echo "{\"secret_env\":\"${ARKISM_SECRET_TEST:-none}\"}"`}, nil)
	if err != nil || out.(map[string]any)["secret_env"] != "none" {
		t.Fatal("environment leaked into code", out, err)
	}
	if _, err = exec.LookPath("go"); err != nil {
		t.Skip("go toolchain missing")
	}
	e.CodeTimeout = 2 * time.Minute
	src := `package main

import (
	"encoding/json"
	"os"
)

func main() {
	var in struct{ N int }
	_ = json.NewDecoder(os.Stdin).Decode(&in)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]int{"square": in.N * in.N})
}
`
	out, err = e.runCode(context.Background(), map[string]string{"lang": "go", "code": src}, map[string]any{"N": 7})
	if err != nil || out.(map[string]any)["square"] != float64(49) {
		t.Fatal("go code", out, err)
	}
}

func init() { _ = os.Setenv("ARKISM_SECRET_TEST", "should-not-reach-code") }

func TestHTTPNodeUsesProviderKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key-value" || r.Header.Get("X-Trace") != "7" {
			w.WriteHeader(401)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"got":` + string(b) + `,"path":"` + r.URL.Path + `"}`))
	}))
	defer srv.Close()
	e := testEngine(t, newFakeStore())
	keys := `{"keys":[{"id":"k1","provider":"openai","name":"main","key":"test-key-value"}]}`
	if err := os.WriteFile(e.KeysFile, []byte(keys), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]string{"url": srv.URL + "/items/{{in.id}}", "method": "POST", "headers": "X-Trace: {{in.trace}}", "body": `{"q":"{{in.id}}"}`, "key": "main"}
	out, err := e.runHTTP(context.Background(), cfg, map[string]any{"id": "a1", "trace": 7})
	m, _ := out.(map[string]any)
	if err != nil || m["path"] != "/items/a1" || m["got"].(map[string]any)["q"] != "a1" {
		t.Fatal("http", out, err)
	}
	_ = os.Chmod(e.KeysFile, 0o644)
	if _, err = e.runHTTP(context.Background(), cfg, map[string]any{"id": "a1", "trace": 7}); err == nil || strings.Contains(err.Error(), "test-key-value") {
		t.Fatal("world-readable keys file used", err)
	}
	cfg["key"] = ""
	if _, err = e.runHTTP(context.Background(), cfg, map[string]any{"id": "a1", "trace": 7}); err == nil || !strings.HasPrefix(err.Error(), "http_status") {
		t.Fatal("401 not a failure", err)
	}
}

func TestNotifyChannels(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got[r.URL.Path] = fmt.Sprint(body["text"], "|", body["chat_id"])
		mu.Unlock()
	}))
	defer srv.Close()
	e := testEngine(t, newFakeStore())
	e.TelegramAPI = srv.URL
	cfg := `{"telegram":{"default":{"bot_token":"123:abc","chat_id":"42"}},"slack":{"ops":{"webhook_url":"` + srv.URL + `/slack/hook"}}}`
	_ = os.WriteFile(e.NotifyFile, []byte(cfg), 0o600)
	if _, err := e.notify(context.Background(), map[string]string{"channel": "telegram", "message": "끝: {{in.n}}"}, map[string]any{"n": 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.notify(context.Background(), map[string]string{"channel": "slack", "to": "ops"}, "hello"); err != nil {
		t.Fatal(err)
	}
	if got["/bot123:abc/sendMessage"] != "끝: 3|42" || got["/slack/hook"] != "hello|<nil>" {
		t.Fatal("notify", got)
	}
	e.TelegramAPI = "http://127.0.0.1:1"
	_, err := e.notify(context.Background(), map[string]string{"channel": "telegram"}, "x")
	if err == nil || strings.Contains(err.Error(), "123:abc") {
		t.Fatal("a failed send exposed the bot token", err)
	}
	if _, err = e.notify(context.Background(), map[string]string{"channel": "slack", "to": "nobody"}, "x"); err == nil {
		t.Fatal("unknown destination")
	}
}

func TestSecretOutputIsWithheld(t *testing.T) {
	g := &Graph{Nodes: []Node{node("start", "start"), node("leak", "code", "lang", "sh", "code", `echo '"sk-live-abc"'`), node("end", "end")},
		Edges: []Edge{edge("start", "leak"), edge("leak", "end")}}
	r := runOnce(t, testEngine(t, newFakeStore(wf("a", g))), "a", nil)
	if r.State.Status != "failed" || r.State.Nodes["leak"].Output != "" || !strings.HasPrefix(r.State.Nodes["leak"].Error, "secret_detected") {
		t.Fatal("secret", r.State.Status, r.State.Nodes["leak"])
	}
}

func TestSecretInAFailureStillEndsTheRun(t *testing.T) {
	g := &Graph{Nodes: []Node{node("start", "start"), node("ok", "code", "lang", "sh", "code", `echo fine`),
		node("leak", "code", "lang", "sh", "code", `echo sk-live-abc >&2; exit 3`), node("end", "end")},
		Edges: []Edge{edge("start", "ok"), edge("ok", "leak"), edge("leak", "end")}}
	r := runOnce(t, testEngine(t, newFakeStore(wf("a", g))), "a", nil)
	if r.State.Status != "failed" || !strings.HasPrefix(r.State.Summary, "secret_detected") {
		t.Fatal("run left open or summary kept the secret", r.State.Status, r.State.Summary)
	}
	for id, n := range r.State.Nodes {
		if strings.Contains(n.Output+n.Error, "sk-live-") {
			t.Fatal("secret stored in", id)
		}
	}
}

func TestStoredValuesAreCapped(t *testing.T) {
	long := strings.Repeat("가", 30000)
	s := encodeStored(long)
	var v map[string]any
	if json.Unmarshal([]byte(s), &v) != nil || v["truncated"] != true || len([]rune(s)) > maxStored {
		t.Fatal("cap", len([]rune(s)))
	}
	for _, tricky := range []string{strings.Repeat(`"\\`, 15000), strings.Repeat("<&>", 10000)} {
		if s := encodeStored(tricky); len([]rune(s)) > maxStored {
			t.Fatal("escaped text over the cap", len([]rune(s)))
		}
	}
	if render("a {{input}} b {{ in.x.y }} c {{in.none}}", map[string]any{"x": map[string]any{"y": 2}}) != `a {"x":{"y":2}} b 2 c ` {
		t.Fatal("render")
	}
}

func TestClientSpeaksRunsAPI(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/v1/crew":
			if r.URL.Query().Get("workspace_id") != "ws" || r.URL.Query().Get("project_id") != "p" {
				w.WriteHeader(400)
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"id":"a","revision":3,"crew":{"type":"workflow","name":"x","enabled":true}}]}`))
		case strings.HasSuffix(r.URL.Path, "/progress"):
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			if strings.Contains(string(body["run"]), "decisions") || string(body["expected_revision"]) != "4" {
				w.WriteHeader(400)
				return
			}
			_, _ = w.Write([]byte(`{"id":"r1","revision":5,"crew_run":{"status":"running"}}`))
		default:
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"code":"revision_conflict"}`))
		}
	}))
	defer srv.Close()
	tokFile := filepath.Join(t.TempDir(), "tok")
	_ = os.WriteFile(tokFile, []byte("tok-1\n"), 0o600)
	c := &Client{BaseURL: srv.URL, TokenFile: tokFile, Scope: Scope{WorkspaceID: "ws", ProjectID: "p"}}
	items, err := c.ListCrew(context.Background())
	if err != nil || len(items) != 1 || items[0].Revision != 3 {
		t.Fatal("list crew", items, err)
	}
	r, err := c.UpdateRun(context.Background(), Run{ID: "r1", Revision: 4, State: RunState{Status: "running", Decisions: map[string]Decision{"x": {}}}})
	if err != nil || r.Revision != 5 {
		t.Fatal("update", r, err)
	}
	if _, err = c.GetRun(context.Background(), "r1"); apiCode(err) != "revision_conflict" {
		t.Fatal("error code", err)
	}
	if seen[0] != "GET /v1/crew Bearer tok-1" || seen[1] != "POST /v1/crew/runs/r1/progress Bearer tok-1" {
		t.Fatal("requests", seen)
	}
}

func TestRefusedProgressEndsTheRun(t *testing.T) {
	g := &Graph{Nodes: []Node{node("start", "start"), node("big", "code", "lang", "sh", "code", `echo REFUSE-ME`), node("end", "end")},
		Edges: []Edge{edge("start", "big"), edge("big", "end")}}
	fs := newFakeStore(wf("a", g))
	fs.refuse = "REFUSE-ME"
	r := runOnce(t, testEngine(t, fs), "a", nil)
	if r.State.Status != "failed" || !strings.HasPrefix(r.State.Summary, "store_refused") || r.State.Nodes["big"].Status != "failed" {
		t.Fatal("run left open after a refused save", r.State.Status, r.State.Summary, r.State.Nodes["big"])
	}
}

func TestDesktopRunsStartAndOthersAreLeft(t *testing.T) {
	g := &Graph{Nodes: []Node{node("start", "start"), node("w", "code", "lang", "sh", "code", "echo hi"), node("end", "end")},
		Edges: []Edge{edge("start", "w"), edge("w", "end")}}
	fs := newFakeStore(wf("a", g))
	e := testEngine(t, fs)
	if err := e.LoadCrew(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Saved again after the engine cached revision 1: the desktop run asks for revision 2.
	fs.mu.Lock()
	fs.crew[0].Revision = 2
	fs.mu.Unlock()
	pending := map[string]*NodeState{"start": {Status: "pending"}, "w": {Status: "pending"}, "end": {Status: "pending"}}
	st := RunState{CrewID: "a", CrewRevision: 2, Trigger: "manual", Status: "running", Nodes: pending}
	mine, _ := fs.CreateRun(context.Background(), st)
	theirs, _ := fs.CreateRun(context.Background(), st)
	fs.mu.Lock()
	r := fs.runs[mine.ID]
	r.CreatedByType = "human"
	fs.runs[mine.ID] = r
	fs.mu.Unlock()
	if err := e.PollWaiting(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	got, _ := fs.GetRun(context.Background(), mine.ID)
	other, _ := fs.GetRun(context.Background(), theirs.ID)
	if got.State.Status != "succeeded" || other.State.Status != "running" {
		t.Fatal("desktop run", got.State.Status, "engine-made run", other.State.Status)
	}
}

func TestOneNodeRunSkipsTheRest(t *testing.T) {
	g := &Graph{Nodes: []Node{node("start", "start"), node("a", "code", "lang", "sh", "code", "echo first"), node("w", "code", "lang", "sh", "code", "echo hi"), node("end", "end")},
		Edges: []Edge{edge("start", "a"), edge("a", "w"), edge("w", "end")}}
	fs := newFakeStore(wf("a1", g))
	e := testEngine(t, fs)
	if err := e.LoadCrew(context.Background()); err != nil {
		t.Fatal(err)
	}
	pending := map[string]*NodeState{"start": {Status: "pending"}, "a": {Status: "pending"}, "w": {Status: "pending"}, "end": {Status: "pending"}}
	r, _ := fs.CreateRun(context.Background(), RunState{CrewID: "a1", CrewRevision: 1, Trigger: "manual", Status: "running", Nodes: pending, Only: "w", Input: `"시험 값"`})
	if err := e.Drive(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := fs.GetRun(context.Background(), r.ID)
	st := got.State
	if st.Status != "succeeded" || st.Nodes["w"].Status != "done" || st.Nodes["a"].Status != "skipped" || st.Nodes["end"].Status != "skipped" {
		t.Fatal("one-node run", st.Status, st.Nodes["a"].Status, st.Nodes["w"].Status, st.Nodes["end"].Status)
	}
}

// The test binary doubles as the AI runner: with CREWRUN_FAKE_RUNNER set it
// reads a run-node spec on stdin, logs it to CREWRUN_FAKE_LOG and answers.
func TestMain(m *testing.M) {
	if os.Getenv("CREWRUN_FAKE_RUNNER") != "" {
		fakeRunner()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeRunner() {
	var s aiSpec
	raw, _ := io.ReadAll(os.Stdin)
	_ = json.Unmarshal(raw, &s)
	if p := os.Getenv("CREWRUN_FAKE_LOG"); p != "" {
		f, _ := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		_, _ = f.Write(append(raw, '\n'))
		_ = f.Close()
	}
	out := fmt.Sprintf("%s|%s|%v|%s", s.Model, s.Effort, s.Once, s.Task)
	switch {
	case strings.Contains(s.Task, "FAIL"):
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": false, "error": "모델 CLI가 없어요"})
		return
	case strings.Contains(s.Task, "하위 작업으로 나눠"):
		out = `계획: [{"worker":"조사원","task":"A 조사"},{"worker":"작성자","task":"B 쓰기"},{"worker":"없는이","task":"C"}]`
	case strings.Contains(s.Task, "하위 작업 결과"):
		out = "종합 끝\nROUTE: done"
	case strings.Contains(s.Task, "골라 적어"):
		out = "고른 답\n**ROUTE: b**"
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "output": out, "model": s.Model, "effort": s.Effort, "cli": "fake", "duration_ms": 1,
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 20, "estimated": true}})
}

// fakeAI points the engine at the fake runner and returns the spec log path.
func fakeAI(t *testing.T, e *Engine) string {
	log := filepath.Join(t.TempDir(), "specs.jsonl")
	t.Setenv("CREWRUN_FAKE_RUNNER", "1")
	t.Setenv("CREWRUN_FAKE_LOG", log)
	e.Runner = []string{os.Args[0]}
	return log
}

func specs(t *testing.T, path string) []aiSpec {
	raw, _ := os.ReadFile(path)
	var out []aiSpec
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var s aiSpec
		if line != "" && json.Unmarshal([]byte(line), &s) == nil {
			out = append(out, s)
		}
	}
	return out
}

func TestOnlyWorkflowsRun(t *testing.T) {
	persona := CrewItem{ID: "p", Revision: 1, Crew: Crew{Type: "persona", Name: "p", Instructions: "x"}}
	empty := wf("empty", &Graph{})
	e := testEngine(t, newFakeStore(persona, empty))
	_ = e.LoadCrew(context.Background())
	for _, id := range []string{"p", "empty"} {
		if _, err := e.StartRun(context.Background(), id, "manual", nil); err == nil {
			t.Fatal("not runnable but started", id)
		}
	}
}

func TestAINodeCallsRunner(t *testing.T) {
	persona := CrewItem{ID: "per-1", Revision: 1, Crew: Crew{Type: "persona", Name: "편집자", Instructions: "차분한 편집자다."}}
	g := &Graph{Nodes: []Node{node("start", "start"),
		node("rev", "ai", "persona", "per-1", "instructions", "짧게 쓴다.", "fence", "파일은 고치지 마", "done", "세 줄 요약", "goal", "검토: {{in.pr}}", "cwd", "{{in.dir}}", "skills", "design/a, b/c\nb/c"),
		node("ask", "ai_once", "model", "glm-5", "prompt", "한 줄: {{in.output}}"), node("end", "end")},
		Edges: []Edge{edge("start", "rev"), edge("rev", "ask"), edge("ask", "end")}}
	w := wf("a", g)
	w.Crew.Model = "deepseek-v4-flash"
	e := testEngine(t, newFakeStore(persona, w))
	log := fakeAI(t, e)
	dir := t.TempDir()
	r := runOnce(t, e, "a", map[string]any{"pr": 42, "dir": dir})
	if r.State.Status != "succeeded" || r.State.Summary != "glm-5|medium|true|한 줄: deepseek-v4-flash|medium|false|검토: 42" {
		t.Fatal("ai", r.State.Status, r.State.Summary, statuses(r))
	}
	got := specs(t, log)
	if len(got) != 2 || got[0].Instructions != "차분한 편집자다.\n\n짧게 쓴다.\n\n울타리: 파일은 고치지 마\n\n완료 기준: 세 줄 요약" ||
		got[0].CWD != dir || !slices.Equal(got[0].Skills, []string{"design/a", "b/c"}) || got[1].Skills != nil || got[0].TimeoutS != 20*60-5 || got[1].TimeoutS != 5*60-5 || got[1].Instructions != "" {
		t.Fatalf("specs %+v", got)
	}
	if r.State.Nodes["rev"].Runs != 1 {
		t.Fatal("runs not counted", r.State.Nodes["rev"].Runs)
	}
	g.Nodes[1].Config["goal"] = "FAIL {{in.pr}}"
	g.Nodes[1].Config["retries"] = "2"
	if r = runOnce(t, e, "a", map[string]any{"pr": 1}); r.State.Status != "failed" || r.State.Nodes["rev"].Error != "ai_failed: 모델 CLI가 없어요" {
		t.Fatal("ai failure", r.State.Nodes["rev"].Error)
	}
	if n := len(specs(t, log)); n != 2+3 {
		t.Fatal("retries", n)
	}
	g.Nodes[1].Config["persona"] = "no-such"
	if r = runOnce(t, e, "a", map[string]any{"pr": 1}); r.State.Status != "failed" || !strings.HasPrefix(r.State.Nodes["rev"].Error, "bad_config") {
		t.Fatal("missing persona", r.State.Nodes["rev"].Error)
	}
}

func TestRoutePick(t *testing.T) {
	ports := []string{"a", "b"}
	for answer, want := range map[string][2]string{
		"답\nROUTE: b":               {"답", "b"},
		"ROUTE: a\n답\n  ROUTE: `b`": {"ROUTE: a\n답", "b"},
		"답\nROUTE: c":               {"답", "a"},
		"답만":                        {"답만", "a"},
	} {
		if text, port := routePick(answer, ports, "a"); text != want[0] || port != want[1] {
			t.Errorf("%q → %q %q", answer, text, port)
		}
	}
	if text, port := routePick("x\nROUTE: b", []string{"b"}, "b"); text != "x\nROUTE: b" || port != "" {
		t.Error("one port still routed", text, port)
	}
	if routeAsk([]string{"a"}) != "" || !strings.Contains(routeAsk(ports), "ROUTE: <이름>` 으로 다음 중 하나를 골라 적어: a, b") {
		t.Error("route ask")
	}
}

func TestAINodeRoutesByPort(t *testing.T) {
	g := &Graph{Nodes: []Node{node("start", "start"), node("pick", "ai"), node("na", "end", "summary", "A"), node("nb", "end", "summary", "B")},
		Edges: []Edge{edge("start", "pick"), edge("pick", "na", "a"), edge("pick", "nb", "b")}}
	e := testEngine(t, newFakeStore(wf("w", g)))
	log := fakeAI(t, e)
	r := runOnce(t, e, "w", "질문")
	if st := statuses(r); r.State.Status != "succeeded" || st["pick"] != "done/b" || st["na"] != "skipped/" || r.State.Summary != "B" {
		t.Fatal("route", r.State.Status, st, r.State.Summary)
	}
	if out := r.State.Nodes["pick"].Output; !strings.Contains(out, `"output":"고른 답"`) {
		t.Fatal("ROUTE line kept in the output", out)
	}
	if s := specs(t, log); len(s) != 1 || !strings.HasSuffix(s[0].Task, "골라 적어: a, b") {
		t.Fatal("route ask missing", s)
	}
}

func TestCollabPlansRunsWorkersAndSumsUp(t *testing.T) {
	workers := `[{"name":"조사원","model":"glm-5","instructions":"조사한다"},{"name":"작성자"}]`
	g := &Graph{Nodes: []Node{node("start", "start"),
		node("team", "ai", "collab", "on", "workers", workers, "max_workers", "2", "fence", "읽기만", "goal", "보고서: {{input}}"),
		node("ok", "end"), node("more", "end")},
		Edges: []Edge{edge("start", "team"), edge("team", "ok", "done"), edge("team", "more", "more")}}
	e := testEngine(t, newFakeStore(wf("w", g)))
	log := fakeAI(t, e)
	r := runOnce(t, e, "w", "주제")
	var out struct {
		Output  string         `json:"output"`
		Workers []collabResult `json:"workers"`
	}
	_ = json.Unmarshal([]byte(r.State.Nodes["team"].Output), &out)
	if r.State.Status != "succeeded" || r.State.Nodes["team"].Port != "done" || out.Output != "종합 끝" || len(out.Workers) != 2 ||
		out.Workers[0].Worker != "조사원" || !out.Workers[0].OK || out.Workers[0].Output != "glm-5|medium|false|A 조사" || out.Workers[1].Output != "opus 5.5|medium|false|B 쓰기" {
		t.Fatalf("collab %s %+v %v", r.State.Status, out, statuses(r))
	}
	s := specs(t, log)
	if len(s) != 4 || !s[0].Once || !strings.Contains(s[0].Task, "최대 2개") || s[1].Instructions != "조사한다\n\n울타리: 읽기만" && s[2].Instructions != "조사한다\n\n울타리: 읽기만" ||
		!strings.Contains(s[3].Task, "하위 작업 결과") || !strings.HasSuffix(s[3].Task, "골라 적어: done, more") {
		t.Fatalf("collab specs %+v", s)
	}
}

// A check that fails sends the run back; within max the work repeats, at max the run fails.
func loopGraph(maxN int) *Graph {
	count := `d=$(cat | sed 's/.*"dir":"\([^"]*\)".*/\1/'); echo x >> "$d/n"; wc -l < "$d/n" | tr -d ' '`
	check := `read n; if [ "$n" -ge 3 ]; then echo '{"ok":true}'; else echo '{"ok":false}'; fi`
	return &Graph{Nodes: []Node{node("start", "start"), node("work", "code", "lang", "sh", "code", count), node("check", "code", "lang", "sh", "code", check),
		node("end", "end", "summary", "통과")},
		Edges: []Edge{edge("start", "work"), edge("work", "check"), edge("check", "end", "pass"), back("check", "work", "fail", maxN)}}
}

func TestBackEdgeRepeatsUntilPassOrLimit(t *testing.T) {
	e := testEngine(t, newFakeStore(wf("w", loopGraph(5))))
	r := runOnce(t, e, "w", map[string]any{"dir": t.TempDir()})
	if r.State.Status != "succeeded" || r.State.Summary != "통과" || r.State.Loops["check>work:fail"] != 2 ||
		r.State.Nodes["work"].Runs != 3 || r.State.Nodes["start"].Runs != 1 || r.State.Nodes["check"].Port != "pass" {
		t.Fatal("loop", r.State.Status, r.State.Summary, r.State.Loops, statuses(r))
	}
	e = testEngine(t, newFakeStore(wf("w", loopGraph(1))))
	r = runOnce(t, e, "w", map[string]any{"dir": t.TempDir()})
	if r.State.Status != "failed" || r.State.Summary != "loop_limit: check → work 최대 1번" || r.State.Nodes["work"].Runs != 2 {
		t.Fatal("loop limit", r.State.Status, r.State.Summary, statuses(r))
	}
}

func TestStepAndTimeLimits(t *testing.T) {
	w := wf("w", loopGraph(5))
	w.Crew.Limits = &Limits{MaxSteps: 4}
	r := runOnce(t, testEngine(t, newFakeStore(w)), "w", map[string]any{"dir": t.TempDir()})
	if r.State.Status != "failed" || !strings.HasPrefix(r.State.Summary, "step_limit") || r.State.Nodes["work"].Runs != 2 {
		t.Fatal("step limit", r.State.Status, r.State.Summary, statuses(r))
	}
	w.Crew.Limits = &Limits{MaxMinutes: 30}
	fs := newFakeStore(w)
	e := testEngine(t, fs)
	t0 := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	now := t0
	e.Now = func() time.Time { return now }
	_ = e.LoadCrew(context.Background())
	past := t0.Add(-time.Minute)
	run, _ := fs.CreateRun(context.Background(), RunState{CrewID: "w", CrewRevision: 1, Trigger: "manual", Status: "running", Deadline: &past,
		Nodes: map[string]*NodeState{"start": {Status: "pending"}, "work": {Status: "pending"}, "check": {Status: "pending"}, "end": {Status: "pending"}}})
	_ = e.Drive(context.Background(), run.ID)
	if got, _ := fs.GetRun(context.Background(), run.ID); got.State.Status != "failed" || !strings.HasPrefix(got.State.Summary, "time_limit") {
		t.Fatal("time limit", got.State.Status, got.State.Summary)
	}
	r = runOnce(t, e, "w", map[string]any{"dir": t.TempDir()})
	if r.State.Status != "succeeded" || r.State.Deadline == nil || !r.State.Deadline.Equal(t0.Add(30*time.Minute)) {
		t.Fatal("deadline", r.State.Status, r.State.Deadline)
	}
}

// A rejected approval goes back to the draft and asks again.
func TestApprovalBackEdgeAsksAgain(t *testing.T) {
	g := &Graph{Nodes: []Node{node("start", "start"), node("draft", "code", "lang", "sh", "code", `echo '"초안"'`), node("ok", "approval"), node("end", "end")},
		Edges: []Edge{edge("start", "draft"), edge("draft", "ok"), edge("ok", "end", "approve"), back("ok", "draft", "reject", 2)}}
	fs := newFakeStore(wf("w", g))
	e := testEngine(t, fs)
	r := runOnce(t, e, "w", nil)
	fs.decide(t, r.ID, "ok", false)
	_ = e.Drive(context.Background(), r.ID)
	got, _ := fs.GetRun(context.Background(), r.ID)
	if got.State.Status != "waiting" || got.State.Nodes["draft"].Runs != 2 || got.State.Loops["ok>draft:reject"] != 1 || len(got.State.Decisions) != 0 {
		t.Fatal("after reject", got.State.Status, statuses(got), got.State.Loops, got.State.Decisions)
	}
	fs.decide(t, r.ID, "ok", true)
	_ = e.Drive(context.Background(), r.ID)
	if got, _ = fs.GetRun(context.Background(), r.ID); got.State.Status != "succeeded" || got.State.Nodes["ok"].Port != "approve" {
		t.Fatal("after approve", got.State.Status, statuses(got))
	}
}

func TestCodePortPassFail(t *testing.T) {
	e := testEngine(t, newFakeStore())
	x := &runExec{e: e}
	for code, port := range map[string]string{`echo '{"ok":true}'`: "pass", `echo '{"ok":false}'`: "fail", `echo '{"ok":"yes"}'`: "", `echo 1`: ""} {
		res, err := e.execute(context.Background(), x, node("c", "code", "lang", "sh", "code", code), nil)
		if err != nil || res.port != port {
			t.Errorf("%s → %q %v", code, res.port, err)
		}
	}
}

func TestSubflowRunsChildAndStopsAtDepth(t *testing.T) {
	leaf := wf("leaf", &Graph{Nodes: []Node{node("s", "start"), node("e", "end", "summary", "잎: {{in.request}}")}, Edges: []Edge{edge("s", "e")}})
	mid := wf("mid", &Graph{Nodes: []Node{node("s", "start"), node("call", "subflow", "workflow", "$leaf", "task", "중간 {{in.request}}")}, Edges: []Edge{edge("s", "call")}})
	top := wf("top", &Graph{Nodes: []Node{node("s", "start"), node("call", "subflow", "workflow", "mid", "task", "{{in.request}}"), node("e", "end")},
		Edges: []Edge{edge("s", "call"), edge("call", "e")}})
	self := wf("self", &Graph{Nodes: []Node{node("s", "start"), node("call", "subflow", "workflow", "self")}, Edges: []Edge{edge("s", "call")}})
	waits := wf("waits", &Graph{Nodes: []Node{node("s", "start"), node("ok", "approval")}, Edges: []Edge{edge("s", "ok")}})
	asks := wf("asks", &Graph{Nodes: []Node{node("s", "start"), node("call", "subflow", "workflow", "waits")}, Edges: []Edge{edge("s", "call")}})
	fs := newFakeStore(leaf, mid, top, self, waits, asks)
	e := testEngine(t, fs)
	r := runOnce(t, e, "top", map[string]any{"request": "일"})
	if r.State.Status != "succeeded" || r.State.Summary != "잎: 중간 일" {
		t.Fatal("subflow", r.State.Status, r.State.Summary, statuses(r))
	}
	r = runOnce(t, e, "self", nil)
	if r.State.Status != "failed" || !strings.Contains(r.State.Summary, "subflow_depth") {
		t.Fatal("depth", r.State.Status, r.State.Summary)
	}
	if runs, _ := fs.ListRuns(context.Background(), "", 0); len(runs) != 3+4 {
		t.Fatal("runs made", len(runs)) // top, mid, leaf; self at depths 0-3
	}
	r = runOnce(t, e, "asks", nil)
	if r.State.Status != "failed" || !strings.HasPrefix(r.State.Nodes["call"].Error, "subflow_waiting") {
		t.Fatal("waiting child", r.State.Status, r.State.Nodes["call"].Error)
	}
	for _, c := range func() []Run { rs, _ := fs.ListRuns(context.Background(), "waiting", 0); return rs }() {
		t.Fatal("a child run was left waiting", c.ID)
	}
}

// 실패한 곳부터 다시: the desktop copies the done nodes of a failed run into a
// new one; the engine continues from the pending nodes only.
func TestResumedRunContinuesFromPending(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	g := &Graph{Nodes: []Node{node("start", "start"), node("a", "code", "lang", "sh", "code", "touch "+marker+"; echo '\"again\"'"),
		node("b", "code", "lang", "sh", "code", `cat`), node("end", "end")},
		Edges: []Edge{edge("start", "a"), edge("a", "b"), edge("b", "end")}}
	fs := newFakeStore(wf("w", g))
	e := testEngine(t, fs)
	_ = e.LoadCrew(context.Background())
	r, _ := fs.CreateRun(context.Background(), RunState{CrewID: "w", CrewRevision: 1, Trigger: "manual", Status: "running",
		Nodes: map[string]*NodeState{"start": {Status: "done", Output: `{}`}, "a": {Status: "done", Output: `"그때 결과"`}, "b": {Status: "pending"}, "end": {Status: "pending"}}})
	fs.mu.Lock()
	run := fs.runs[r.ID]
	run.CreatedByType = "human"
	fs.runs[r.ID] = run
	fs.mu.Unlock()
	if err := e.PollWaiting(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	got, _ := fs.GetRun(context.Background(), r.ID)
	if got.State.Status != "succeeded" || got.State.Summary != "그때 결과" || got.State.Nodes["a"].Runs != 0 {
		t.Fatal("resumed", got.State.Status, got.State.Summary, statuses(got))
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a done node ran again")
	}
}

// A code node's own timeout overrides the engine default (30 s unless set).
func TestCodeNodeTimeout(t *testing.T) {
	for _, c := range []struct {
		timeout, want string
	}{{"", "failed"}, {"5s", "done"}} {
		g := &Graph{Nodes: []Node{node("start", "start"),
			node("slow", "code", "lang", "sh", "timeout", c.timeout, "code", `sleep 0.3; echo '"ok"'`)},
			Edges: []Edge{edge("start", "slow")}}
		e := testEngine(t, newFakeStore(wf("a", g)))
		e.CodeTimeout = 100 * time.Millisecond
		if r := runOnce(t, e, "a", nil); r.State.Nodes["slow"].Status != c.want {
			t.Fatal("timeout", c.timeout, statuses(r), r.State.Nodes["slow"].Error)
		}
	}
}

// An http node's own timeout outlives a shorter daemon client timeout.
func TestHTTPNodeTimeoutBeatsClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	for _, c := range []struct{ timeout, want string }{{"50ms", "failed"}, {"5s", "done"}} {
		g := &Graph{Nodes: []Node{node("start", "start"), node("call", "http", "url", srv.URL, "timeout", c.timeout)},
			Edges: []Edge{edge("start", "call")}}
		e := testEngine(t, newFakeStore(wf("a", g)))
		e.HTTP = &http.Client{Timeout: 100 * time.Millisecond}
		if r := runOnce(t, e, "a", nil); r.State.Nodes["call"].Status != c.want {
			t.Fatal("http timeout", c.timeout, statuses(r), r.State.Nodes["call"].Error)
		}
	}
}

// A credential after an approval still ends the run: the approval keeps the
// person's answer (the server refuses a done approval without it).
func TestSecretAfterApprovalEndsTheRun(t *testing.T) {
	g := &Graph{Nodes: []Node{node("start", "start"), node("ok", "approval"),
		node("leak", "code", "lang", "sh", "code", `echo '"sk-live-abc"'`)},
		Edges: []Edge{edge("start", "ok"), edge("ok", "leak", "approve")}}
	fs := newFakeStore(wf("a", g))
	e := testEngine(t, fs)
	r := runOnce(t, e, "a", nil)
	fs.decide(t, r.ID, "ok", true)
	if err := e.PollWaiting(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	got, _ := fs.GetRun(context.Background(), r.ID)
	if got.State.Status != "failed" || !strings.HasPrefix(got.State.Summary, "secret_detected") || got.State.Nodes["ok"].Port != "approve" {
		t.Fatal("stuck after secret", got.State.Status, got.State.Summary, statuses(got))
	}
}

func TestSharedStateReadsAnyFinishedNode(t *testing.T) {
	g := &Graph{
		Nodes: []Node{node("start", "start"), node("a", "code", "lang", "sh", "code", `echo '{"v":7}'`),
			node("b", "code", "lang", "sh", "code", `echo '"{{trigger.who}}"'`), // code never takes shared text
			node("end", "end", "summary", "{{in}} {{nodes.a.v}} {{trigger.who}} {{trigger.in}} [{{nodes.later}}]"), node("later", "merge")},
		Edges: []Edge{edge("start", "a"), edge("a", "b"), edge("b", "end"), edge("end", "later")},
	}
	fs := newFakeStore(wf("auto", g))
	r := runOnce(t, testEngine(t, fs), "auto", map[string]any{"who": "felix", "in": "x"})
	if r.State.Status != "succeeded" || r.State.Summary != "{{trigger.who}} 7 felix x []" {
		t.Fatalf("shared state: %s %q", r.State.Status, r.State.Summary)
	}
}

func TestTestRunFakesOutsideSteps(t *testing.T) {
	g := &Graph{
		Nodes: []Node{node("start", "start"), node("ask", "ai_once", "prompt", "hi"), node("w", "code", "lang", "sh", "code", "exit 9"),
			node("pause", "wait", "duration", "1h"), node("end", "end", "summary", "{{nodes.ask.test}} {{nodes.w.type}}")},
		Edges: []Edge{edge("start", "ask"), edge("ask", "w"), edge("w", "pause"), edge("pause", "end")},
	}
	fs := newFakeStore(wf("auto", g))
	e := testEngine(t, fs)
	if err := e.LoadCrew(context.Background()); err != nil {
		t.Fatal(err)
	}
	r, err := e.StartRun(context.Background(), "auto", "test", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Drive(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = fs.GetRun(context.Background(), r.ID)
	if r.State.Status != "succeeded" || r.State.Summary != "true code" {
		t.Fatalf("test run: %s %q", r.State.Status, r.State.Summary)
	}
}
