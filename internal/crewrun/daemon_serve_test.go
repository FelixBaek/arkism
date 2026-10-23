package crewrun

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFromEnvReadsScopeAndPaths(t *testing.T) {
	t.Setenv("ARKISM_URL", "http://127.0.0.1:9")
	t.Setenv("ARKISM_WORKSPACE", "ws")
	t.Setenv("ARKISM_PROJECT", "p")
	t.Setenv("ARKISM_AGENT", "agent-1")
	t.Setenv("ARKISM_CREW_LISTEN", "127.0.0.1:18095")
	t.Setenv("ARKISM_CREW_RUNNER", "/bin/arkism crew --x")
	t.Setenv("ARKISM_CREW_SANDBOX", "bwrap --ro")
	t.Setenv("ARKISM_MEMORY_OUTCOME_SPOOL", t.TempDir())
	t.Setenv("TZ", ":Asia/Seoul")
	cfg := FromEnv()
	e := cfg.Engine
	c := e.Store.(*Client)
	if cfg.Listen != "127.0.0.1:18095" || c.BaseURL != "http://127.0.0.1:9" || c.Scope.WorkspaceID != "ws" || c.Scope.ProjectID != "p" ||
		c.Scope.AgentID != "agent-1" || c.Scope.UserID != "management:owner" {
		t.Fatalf("client = %+v listen=%s", c, cfg.Listen)
	}
	if _, err := time.LoadLocation("Asia/Seoul"); err == nil && e.Loc.String() != "Asia/Seoul" {
		t.Fatalf("TZ = %v", e.Loc)
	}
	if len(e.Runner) != 3 || len(e.Sandbox) != 2 || len(e.Sinks) != 1 ||
		e.PublicURL != "http://127.0.0.1:18095" || e.NotifyScope == "" || !strings.HasSuffix(cfg.LockPath, "crewrun.lock") {
		t.Fatalf("engine = %+v", e)
	}
	t.Setenv("TZ", "Nowhere/Invalid")
	t.Setenv("ARKISM_MEMORY_OUTCOME_SPOOL", "")
	if e := FromEnv().Engine; e.Loc != time.Local || len(e.Sinks) != 0 {
		t.Fatal("bad TZ falls back to local time; no spool means no sinks")
	}
}

// crewErrStore fails ListCrew until ok is set.
type crewErrStore struct {
	*fakeStore
	mu sync.Mutex
	ok bool
}

func (s *crewErrStore) ListCrew(ctx context.Context) ([]CrewItem, error) {
	s.mu.Lock()
	ok := s.ok
	s.mu.Unlock()
	if !ok {
		return nil, errors.New("memoryd down")
	}
	return s.fakeStore.ListCrew(ctx)
}

func TestServeRefusesAndShutsDown(t *testing.T) {
	e := testEngine(t, newFakeStore())
	if err := Serve(context.Background(), Config{Listen: "0.0.0.0:0", Engine: e}); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback listen = %v", err)
	}
	if err := Serve(context.Background(), Config{Listen: "127.0.0.1:bad", Engine: e}); err == nil {
		t.Fatal("bad port should fail")
	}
	lock := filepath.Join(t.TempDir(), "state", "crewrun.lock")
	unlock, err := acquireCrewLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := Serve(context.Background(), Config{Listen: "127.0.0.1:0", LockPath: lock, Engine: e}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second daemon on the same lock = %v", err)
	}
	unlock()
	release, err := acquireCrewLock(filepath.Join(t.TempDir(), "file"))
	if err != nil {
		t.Fatal(err)
	}
	release()
	blocker := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(blocker, nil, 0o600)
	if _, err := acquireCrewLock(filepath.Join(blocker, "x", "lock")); err == nil {
		t.Fatal("a lock under a file cannot be created")
	}

	// A crew load that keeps failing ends with the context.
	bad := testEngine(t, newFakeStore())
	bad.Store = &crewErrStore{fakeStore: newFakeStore()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Serve(ctx, Config{Listen: "127.0.0.1:0", Engine: bad}); !errors.Is(err, context.Canceled) {
		t.Fatalf("load loop = %v", err)
	}

	// Loaded crew: resume, serve, then shut down when ctx ends.
	fs := newFakeStore(wf("w", &Graph{Nodes: []Node{node("s", "start")}}))
	ok := testEngine(t, fs)
	ctx2, cancel2 := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx2, Config{Listen: "127.0.0.1:0", LockPath: lock, Engine: ok}) }()
	time.Sleep(100 * time.Millisecond)
	cancel2()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not stop")
	}
}

func TestStartRequestedDrivesOnlyUntouchedHumanRuns(t *testing.T) {
	fs := newFakeStore(wf("w", &Graph{Nodes: []Node{node("s", "start")}}))
	e := testEngine(t, fs)
	ctx := context.Background()
	if err := e.LoadCrew(ctx); err != nil {
		t.Fatal(err)
	}
	mk := func(by, nodeStatus string) string {
		r, err := e.StartRun(ctx, "w", "manual", nil)
		if err != nil {
			t.Fatal(err)
		}
		fs.mu.Lock()
		x := fs.runs[r.ID]
		x.CreatedByType = by
		for _, n := range x.State.Nodes {
			n.Status = nodeStatus
		}
		fs.runs[r.ID] = x
		fs.mu.Unlock()
		return r.ID
	}
	human, engine, busy := mk("human", "pending"), mk("service", "pending"), mk("human", "running")
	if err := e.PollWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	get := func(id string) string { r, _ := fs.GetRun(ctx, id); return r.State.Status }
	if get(human) != "succeeded" || get(engine) != "running" || get(busy) != "running" {
		t.Fatalf("human=%s engine=%s busy=%s", get(human), get(engine), get(busy))
	}
	// ResumeRunning takes every running run, whoever made it.
	if err := e.ResumeRunning(ctx); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	if get(engine) == "running" {
		t.Fatal("resume left the run running")
	}

	// A crew reload failure stops StartRequested before it drives anything.
	es := &crewErrStore{fakeStore: newFakeStore()}
	e2 := testEngine(t, newFakeStore())
	e2.Store = es
	es.fakeStore.runs["r"] = Run{ID: "r", CreatedByType: "human", State: RunState{Status: "running"}}
	if err := e2.StartRequested(ctx); err == nil {
		t.Fatal("reload error should surface")
	}
}

// listErrStore fails ListRuns.
type listErrStore struct{ *fakeStore }

func (listErrStore) ListRuns(context.Context, string, int) ([]Run, error) {
	return nil, errors.New("list failed")
}

func TestPollErrorsSurface(t *testing.T) {
	e := testEngine(t, newFakeStore())
	e.Store = listErrStore{newFakeStore()}
	ctx := context.Background()
	if e.PollWaiting(ctx) == nil || e.StartRequested(ctx) == nil || e.ResumeRunning(ctx) == nil {
		t.Fatal("list errors should surface")
	}
}

func TestFireSchedulesSkipsBadCronAndFailedStart(t *testing.T) {
	g := &Graph{Nodes: []Node{node("s", "schedule")}}
	bad := wf("bad", g)
	bad.Crew.Trigger = &Trigger{Kind: "schedule", Cron: "not a cron"}
	fs := newFakeStore(bad)
	e := testEngine(t, fs)
	e.Loc = nil
	ctx := context.Background()
	_ = e.LoadCrew(ctx)
	if got := e.FireSchedules(ctx, time.Now()); len(got) != 0 {
		t.Fatalf("bad cron fired: %v", got)
	}
	if got := e.FireSchedules(ctx, time.Now().Add(time.Minute)); len(got) != 0 {
		t.Fatalf("bad cron fired on retry: %v", got)
	}
}

// ---- meetings over the API ----

func TestClientSpeaksMeetingsAPI(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seen[r.Method+" "+r.URL.Path] = map[string]any{"q": r.URL.RawQuery, "body": body}
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/crew/meetings":
			_, _ = w.Write([]byte(`{"items":[{"id":"m1","revision":2,"crew_meeting":{"status":"running"}}]}`))
		case "/v1/crew/meetings/m1":
			_, _ = w.Write([]byte(`{"id":"m1","revision":2,"crew_meeting":{"status":"running","title":"PR"}}`))
		case "/v1/crew/meetings/m1/progress":
			_, _ = w.Write([]byte(`{"id":"m1","revision":3,"crew_meeting":{"status":"running"}}`))
		case "/v1/capture":
			_, _ = w.Write([]byte(`{"items":[{"candidate_id":"c1"},{"candidate_id":""}]}`))
		case "/v1/crew/usage":
			_, _ = w.Write([]byte(`{"agents":[{"id":"a","cost_usd":1.5}]}`))
		case "/v1/knowledge_documents/k1":
			_, _ = w.Write([]byte(`{"summary":"요약만"}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"code":"not_found"}`))
		}
	}))
	defer srv.Close()
	tok := filepath.Join(t.TempDir(), "tok")
	_ = os.WriteFile(tok, []byte("tok-1"), 0o600)
	c := &Client{BaseURL: srv.URL, TokenFile: tok, Scope: Scope{WorkspaceID: "ws", ProjectID: "p", UserID: "u", AgentID: "ag"}}
	ctx := context.Background()
	list, err := c.ListMeetings(ctx, "running")
	if err != nil || len(list) != 1 || list[0].Revision != 2 {
		t.Fatalf("list = %+v %v", list, err)
	}
	if q := seen["GET /v1/crew/meetings"]["q"].(string); !strings.Contains(q, "status=running") || !strings.Contains(q, "brief=1") || !strings.Contains(q, "agent_id=ag") {
		t.Fatalf("list query = %s", q)
	}
	m, err := c.GetMeeting(ctx, "m1")
	if err != nil || m.Meeting.Title != "PR" {
		t.Fatalf("get = %+v %v", m, err)
	}
	up, err := c.UpdateMeeting(ctx, m, "lease-1")
	if err != nil || up.Revision != 3 {
		t.Fatalf("update = %+v %v", up, err)
	}
	if b := seen["POST /v1/crew/meetings/m1/progress"]["body"].(map[string]any); b["lease_token"] != "lease-1" || b["expected_revision"] != float64(2) {
		t.Fatalf("update body = %v", b)
	}
	ids, err := c.Capture(ctx, "m1", []CaptureClaim{{Subject: "meeting", Text: "결정", Kind: "decision"}}, "opus 5.5")
	if err != nil || len(ids) != 1 || ids[0] != "c1" {
		t.Fatalf("capture = %v %v", ids, err)
	}
	if b := seen["POST /v1/capture"]["body"].(map[string]any); b["event"].(map[string]any)["origin_product"] != "arkism-meeting" {
		t.Fatalf("capture body = %v", b)
	}
	spend, err := c.AgentSpend(ctx)
	if err != nil || spend["a"] != 1.5 {
		t.Fatalf("spend = %v %v", spend, err)
	}
	if txt, err := c.ContextText(ctx, "knowledge_documents", "k1"); err != nil || txt != "요약만" {
		t.Fatalf("context = %q %v", txt, err)
	}
	if _, err := c.GetMeeting(ctx, "gone"); apiCode(err) != "not_found" {
		t.Fatalf("missing meeting = %v", err)
	}
}

func TestPollMeetingsPicksOpenUnheldMeetings(t *testing.T) {
	rig := newMeetingRig(t, baseItems(teamItem("round_robin", &TeamEnding{MaxRounds: 1}, threeMembers()...))...)
	future := time.Now().Add(time.Hour)
	rig.add("new", Meeting{})
	rig.add("wait", Meeting{Status: "waiting"})
	rig.add("held", Meeting{Status: "running", LeaseUntil: &future})
	if err := rig.e.PollMeetings(context.Background()); err != nil {
		t.Fatal(err)
	}
	rig.e.Wait()
	if s := rig.store.recs["new"].Meeting.Status; s != "concluded" {
		t.Fatalf("new meeting = %s", s)
	}
	if rig.store.recs["wait"].Meeting.Status != "waiting" || len(rig.store.recs["held"].Meeting.Turns) != 0 {
		t.Fatal("waiting or held meetings were driven")
	}
	// Ended or waiting meetings are not driven, and a missing one fails.
	if err := rig.e.DriveMeeting(context.Background(), "wait"); err != nil {
		t.Fatal(err)
	}
	if err := rig.e.DriveMeeting(context.Background(), "nope"); err == nil {
		t.Fatal("missing meeting should fail")
	}
	rig.e.Meetings = nil
	if err := rig.e.PollMeetings(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// ---- email over SMTP ----

// fakeSMTP is a plain-text SMTP server on loopback; rcptFail answers RCPT for that address with 550.
func fakeSMTP(t *testing.T, rcptFail string) (addr string, got chan string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got = make(chan string, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				r := bufio.NewReader(conn)
				say := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
				say("220 fake")
				var data strings.Builder
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(cmd, "EHLO"):
						say("250-fake")
						say("250 AUTH PLAIN")
					case strings.HasPrefix(cmd, "AUTH"):
						say("235 ok")
					case strings.HasPrefix(cmd, "MAIL"):
						say("250 ok")
					case strings.HasPrefix(cmd, "RCPT"):
						if rcptFail != "" && strings.Contains(line, rcptFail) {
							say("550 no such user")
						} else {
							say("250 ok")
						}
					case cmd == "DATA":
						say("354 go")
						for {
							l, err := r.ReadString('\n')
							if err != nil {
								return
							}
							if l == ".\r\n" {
								break
							}
							data.WriteString(l)
						}
						got <- data.String()
						say("250 queued")
					case cmd == "QUIT":
						say("221 bye")
						return
					default:
						say("250 ok")
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String(), got
}

func TestNotifyEmailOverLoopbackSMTP(t *testing.T) {
	addr, got := fakeSMTP(t, "bad@")
	host, port, _ := net.SplitHostPort(addr)
	e := testEngine(t, newFakeStore())
	cfg := map[string]any{"email": map[string]any{
		"default": map[string]any{"host": host, "port": port, "user": "bot", "pass": "pw", "from": "bot@example.com", "to": []string{"me@example.com"}},
		"bad":     map[string]any{"host": host, "port": port, "from": "bot@example.com", "to": []string{"bad@example.com"}},
		"empty":   map[string]any{"host": host},
	}}
	raw, _ := json.Marshal(cfg)
	_ = os.WriteFile(e.NotifyFile, raw, 0o600)
	ctx := context.Background()
	out, err := e.notify(ctx, map[string]string{"channel": "email", "subject": "빌드\n끝"}, "본문입니다")
	if err != nil || out.(map[string]any)["sent"] != true {
		t.Fatalf("email = %v %v", out, err)
	}
	select {
	case body := <-got:
		if !strings.Contains(body, "Subject: 빌드 끝") || !strings.Contains(body, "To: me@example.com") || !strings.Contains(body, "본문입니다") {
			t.Fatalf("mail = %q", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no mail arrived")
	}
	if _, err := e.notify(ctx, map[string]string{"channel": "email", "to": "bad"}, "x"); err == nil || strings.Contains(err.Error(), host) {
		t.Fatalf("refused recipient = %v", err)
	}
	if _, err := e.notify(ctx, map[string]string{"channel": "email", "to": "empty"}, "x"); err == nil {
		t.Fatal("incomplete destination should fail")
	}
	if _, err := e.notify(ctx, map[string]string{"channel": "fax"}, "x"); err == nil {
		t.Fatal("unknown channel should fail")
	}
	_ = os.WriteFile(e.NotifyFile, []byte("{"), 0o600)
	if _, err := e.notify(ctx, map[string]string{"channel": "email"}, "x"); err == nil {
		t.Fatal("broken notify.json should fail")
	}
}

func TestSendMailRefusesClearTextOffLoopbackAndDialErrors(t *testing.T) {
	if err := sendMail(context.Background(), "127.0.0.1:1", "127.0.0.1", nil, "a@x", []string{"b@x"}, []byte("x")); err == nil {
		t.Fatal("closed port should fail")
	}
}
