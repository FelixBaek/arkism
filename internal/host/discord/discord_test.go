package discord

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host/crew/keystore"
	"github.com/zalando/go-keyring"
)

// ---- fake memoryd ----

type call struct {
	Method, Path string
	Query        url.Values
	Body         map[string]any
}

type fakeMemoryd struct {
	t        *testing.T
	mu       sync.Mutex
	calls    []call
	bots     []map[string]any // handoff items
	settings map[string]any
	crew     []any
	meetings map[string]map[string]any
	assists  map[string]map[string]any
	runs     map[string]map[string]any
	todayUSD float64
	seq      int
}

func newFakeMemoryd(t *testing.T) (*fakeMemoryd, *httptest.Server) {
	f := &fakeMemoryd{t: t, settings: map[string]any{}, meetings: map[string]map[string]any{}, assists: map[string]map[string]any{},
		runs: map[string]map[string]any{}}
	f.crew = []any{
		map[string]any{"id": "ag1", "revision": 3, "crew": map[string]any{"type": "agent", "name": "리서처", "handle": "리서처"}},
		map[string]any{"id": "tm1", "revision": 2, "crew": map[string]any{"type": "team", "name": "기획팀", "handle": "기획팀"}},
		map[string]any{"id": "wf1", "revision": 7, "crew": map[string]any{"type": "workflow", "name": "요약", "handle": "요약"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeMemoryd) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer svc-token" {
		w.WriteHeader(401)
		return
	}
	c := call{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query()}
	if r.Method == http.MethodPost {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &c.Body); err != nil {
			f.t.Errorf("bad json to %s: %v", r.URL.Path, err)
		}
		if _, ok := c.Body["scope"]; !ok {
			f.t.Errorf("%s without scope", r.URL.Path)
		}
	} else if c.Query.Get("workspace_id") == "" {
		f.t.Errorf("%s without scope", r.URL.Path)
	}
	f.calls = append(f.calls, c)
	out := map[string]any{}
	p := r.URL.Path
	switch {
	case p == "/v1/discord/handoff":
		items := []any{}
		for _, b := range f.bots {
			item := map[string]any{}
			for k, v := range b {
				item[k] = v
			}
			items = append(items, item)
			delete(b, "token") // handed out once
		}
		out = map[string]any{"items": items, "settings": map[string]any{"id": "settings", "discord_settings": f.settings}}
	case p == "/v1/discord":
		out = map[string]any{"stats": map[string]any{"today_usd": f.todayUSD}}
	case p == "/v1/discord/report":
	case p == "/v1/crew":
		out = map[string]any{"items": f.crew}
	case p == "/v1/crew/meetings" && r.Method == http.MethodGet:
		if c.Query.Get("status") != "running" {
			f.t.Errorf("meetings listed with status %q", c.Query.Get("status"))
		}
		items := []any{}
		for _, m := range f.meetings {
			if !endedStatus[m["crew_meeting"].(map[string]any)["status"].(string)] {
				items = append(items, m)
			}
		}
		out = map[string]any{"items": items}
	case p == "/v1/crew/meetings":
		f.seq++
		id := "mt" + strconv.Itoa(f.seq)
		mt := c.Body["meeting"].(map[string]any)
		mt["status"] = "pending"
		f.meetings[id] = map[string]any{"id": id, "revision": 1, "crew_meeting": mt}
		out = f.meetings[id]
	case strings.HasPrefix(p, "/v1/crew/meetings/"):
		id := strings.Split(strings.TrimPrefix(p, "/v1/crew/meetings/"), "/")[0]
		if f.meetings[id] == nil {
			w.WriteHeader(404)
			return
		}
		out = f.meetings[id]
	case p == "/v1/crew/assists":
		f.seq++
		id := "as" + strconv.Itoa(f.seq)
		f.assists[id] = map[string]any{"id": id, "crew_assist": map[string]any{"status": "pending"}}
		out = f.assists[id]
	case strings.HasPrefix(p, "/v1/crew/assists/"):
		out = f.assists[strings.TrimPrefix(p, "/v1/crew/assists/")]
	case p == "/v1/crew/runs":
		f.seq++
		id := "run" + strconv.Itoa(f.seq)
		f.runs[id] = map[string]any{"id": id, "revision": 1, "crew_run": map[string]any{"status": "running"}}
		out = f.runs[id]
	case strings.HasPrefix(p, "/v1/crew/runs/"):
		out = f.runs[strings.Split(strings.TrimPrefix(p, "/v1/crew/runs/"), "/")[0]]
	default:
		w.WriteHeader(404)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fakeMemoryd) find(method, path string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.Method == method && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeMemoryd) lastReport(t *testing.T) map[string]any {
	t.Helper()
	rs := f.find("POST", "/v1/discord/report")
	if len(rs) == 0 {
		t.Fatal("no report")
	}
	return rs[len(rs)-1].Body
}

// ---- fake Discord ----

type sent struct {
	Channel string
	Out     Out
}

type fakeSession struct {
	mu        sync.Mutex
	token     string
	refuse    bool
	h         Handlers
	open      bool
	perms     map[string]int64
	channels  map[string]Channel
	sent      []sent
	threads   int
	responses []string
	commands  []string
	failSend  int // when > 0, the send after this many sent messages fails once
}

func newFakeSession() *fakeSession {
	all := invitePerms()
	return &fakeSession{perms: map[string]int64{"c1": all, "c2": all &^ int64(1<<35)},
		channels: map[string]Channel{
			"c1": {ID: "c1", GuildID: "g1", Name: "회의실"},
			"c2": {ID: "c2", GuildID: "g1", Name: "질문"},
			"c3": {ID: "c3", GuildID: "g1", Name: "잡담"},
		}}
}

func (s *fakeSession) Open(h Handlers) error {
	if s.refuse {
		return ErrTokenInvalid
	}
	s.h, s.open = h, true
	return nil
}
func (s *fakeSession) Close() error           { s.open = false; return nil }
func (s *fakeSession) Self() (string, string) { return "app1", "botuser" }
func (s *fakeSession) Guilds() []Guild {
	return []Guild{{ID: "g1", Name: "우리 서버", OwnerID: "owner"}}
}
func (s *fakeSession) Permissions(c string) (int64, error) { return s.perms[c], nil }
func (s *fakeSession) Channels(string) ([]Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Channel{}
	for _, id := range []string{"c1", "c2", "c3"} {
		out = append(out, s.channels[id])
	}
	return out, nil
}
func (s *fakeSession) Channel(id string) (Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.channels[id]
	if !ok {
		return Channel{}, os.ErrNotExist
	}
	return c, nil
}
func (s *fakeSession) Send(ch string, o Out) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failSend > 0 && len(s.sent) == s.failSend {
		s.failSend = 0
		return "", errors.New("discord unavailable")
	}
	s.sent = append(s.sent, sent{ch, o})
	return "m" + strconv.Itoa(len(s.sent)), nil
}
func (s *fakeSession) StartThread(ch, msg, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threads++
	id := "t" + strconv.Itoa(s.threads)
	s.channels[id] = Channel{ID: id, GuildID: "g1", Name: name, ParentID: ch, Thread: true}
	return id, nil
}
func (s *fakeSession) RegisterCommands(g string) error {
	s.commands = append(s.commands, g)
	return nil
}
func (s *fakeSession) Respond(id, content string, private bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses = append(s.responses, content)
	return nil
}

func (s *fakeSession) sentTo(ch string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, x := range s.sent {
		if x.Channel == ch {
			out = append(out, x.Out.Content)
		}
	}
	return out
}

func (s *fakeSession) lastButtons() []Button {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.sent) - 1; i >= 0; i-- {
		if len(s.sent[i].Out.Buttons) > 0 {
			return s.sent[i].Out.Buttons
		}
	}
	return nil
}

// ---- harness ----

type harness struct {
	f    *fakeMemoryd
	m    *Manager
	sess *fakeSession
	now  time.Time
}

func setup(t *testing.T) *harness {
	t.Helper()
	keystore.UseMock(nil)
	dir := t.TempDir()
	stateFile, fallbackFile = filepath.Join(dir, "state.json"), filepath.Join(dir, "tokens.json")
	tf := filepath.Join(dir, "svc.token")
	if err := os.WriteFile(tf, []byte("svc-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARKISM_WORKSPACE", "default")
	f, srv := newFakeMemoryd(t)
	f.bots = []map[string]any{{"bot": map[string]any{"id": "bot1", "revision": 1, "discord_bot": map[string]any{
		"name": "아크봇", "guild_id": "g1",
		"channels": []any{
			map[string]any{"channel_id": "c1", "name": "회의실", "auto_start": true, "action": map[string]any{"kind": "meeting", "id": "tm1", "template_key": "review"}},
			map[string]any{"channel_id": "c2", "name": "질문", "action": map[string]any{"kind": "agent", "id": "ag1"}},
		},
		"allowed": map[string]any{"user_ids": []any{"u1"}},
	}}, "token": "secret-bot-token-1234"}}
	h := &harness{f: f, sess: newFakeSession(), now: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)}
	api := &httpAPI{base: srv.URL, tokenFile: tf, client: srv.Client()}
	h.m = NewManager(api, func(tok string) (Session, error) {
		h.sess.token = tok
		return h.sess, nil
	}, io.Discard)
	h.m.now = func() time.Time { return h.now }
	return h
}

func (h *harness) post(user, ch, text string) {
	h.sess.h.Message(Message{ID: "msg-" + text, ChannelID: ch, GuildID: "g1", AuthorID: user, AuthorName: user, Content: text})
}

// ---- tests ----

func TestConnectStoresTokenAndReports(t *testing.T) {
	h := setup(t)
	h.m.Tick()
	if h.sess.token != "secret-bot-token-1234" || !h.sess.open {
		t.Fatalf("session not opened with the handed-off token: %q", h.sess.token)
	}
	if got, err := keyring.Get(KeyringService, "bot1"); err != nil || got != "secret-bot-token-1234" {
		t.Fatalf("token not in keyring service %s: %q %v", KeyringService, got, err)
	}
	if _, err := os.Stat(fallbackFile); err == nil {
		t.Fatal("token written to the file although the keyring answers")
	}
	rep := h.f.lastReport(t)
	if rep["state"] != stNeedsPerm || rep["token_last4"] != "1234" || rep["application_id"] != "app1" || rep["bot_user_id"] != "botuser" {
		t.Fatalf("report: %v", rep)
	}
	missing := rep["missing"].([]any)
	if len(missing) != 1 || missing[0].(map[string]any)["channel_id"] != "c2" ||
		missing[0].(map[string]any)["perms"].([]any)[0] != "create_public_threads" {
		t.Fatalf("missing: %v", missing)
	}
	if len(rep["channels"].([]any)) != 3 || len(rep["guilds"].([]any)) != 1 {
		t.Fatalf("channels/guilds: %v", rep)
	}
	raw, _ := json.Marshal(h.f.calls)
	if strings.Contains(string(raw), "secret-bot-token") {
		t.Fatal("token sent back to memoryd")
	}
	if len(h.sess.commands) != 1 || h.sess.commands[0] != "g1" {
		t.Fatalf("commands: %v", h.sess.commands)
	}

	// A later run (restart) finds the token in the keyring without a handoff.
	h2 := NewManager(h.m.api, func(tok string) (Session, error) { h.sess.token = tok; return h.sess, nil }, io.Discard)
	h.sess.token = ""
	h2.Tick()
	if h.sess.token != "secret-bot-token-1234" {
		t.Fatalf("restart did not load the token: %q", h.sess.token)
	}
}

func TestPermissionCheckEveryFiveMinutesAndOnRequest(t *testing.T) {
	h := setup(t)
	h.m.Tick()
	n := len(h.f.find("POST", "/v1/discord/report"))
	h.now = h.now.Add(time.Minute)
	h.m.Tick()
	if got := len(h.f.find("POST", "/v1/discord/report")); got != n {
		t.Fatalf("checked again within 5 minutes: %d → %d", n, got)
	}
	h.f.bots[0]["bot"].(map[string]any)["discord_bot"].(map[string]any)["check_requested_at"] = "2026-10-13T09:01:00Z"
	h.sess.perms["c2"] = invitePerms()
	h.m.Tick()
	if rep := h.f.lastReport(t); rep["state"] != stConnected || len(rep["missing"].([]any)) != 0 {
		t.Fatalf("requested check: %v", rep)
	}
	n = len(h.f.find("POST", "/v1/discord/report"))
	h.now = h.now.Add(5 * time.Minute)
	h.m.Tick()
	if len(h.f.find("POST", "/v1/discord/report")) != n+1 {
		t.Fatalf("no check after 5 minutes")
	}
	h.sess.perms["c1"] = permAdministrator | invitePerms()
	h.now = h.now.Add(5 * time.Minute)
	h.m.Tick()
	if rep := h.f.lastReport(t); rep["state"] != stNeedsPerm || !strings.Contains(mustJSON(rep["missing"]), "no_administrator") {
		t.Fatalf("administrator not flagged: %v", rep)
	}
}

func TestInvalidToken(t *testing.T) {
	h := setup(t)
	h.sess.refuse = true
	h.m.Tick()
	if rep := h.f.lastReport(t); rep["state"] != stTokenInvalid {
		t.Fatalf("state: %v", rep["state"])
	}
	n := len(h.f.find("POST", "/v1/discord/report"))
	h.m.Tick() // no retry until a new token arrives
	if len(h.f.find("POST", "/v1/discord/report")) != n {
		t.Fatal("retried a refused token")
	}
	h.sess.refuse = false
	h.f.bots[0]["token"] = "new-token-9999"
	h.m.Tick()
	if h.sess.token != "new-token-9999" || h.f.lastReport(t)["token_last4"] != "9999" {
		t.Fatal("new token not used")
	}
}

func TestAutoStartMeetingMirrorNotesAndStop(t *testing.T) {
	h := setup(t)
	h.m.Tick()
	h.post("intruder", "c1", "무시돼야 해요")
	h.sess.h.Message(Message{ID: "x", ChannelID: "c1", GuildID: "g1", AuthorID: "u1", AuthorBot: true, Content: "봇 글"})
	h.post("u1", "c3", "설정 안 된 채널")
	if len(h.f.find("POST", "/v1/crew/meetings")) != 0 || h.sess.threads != 0 {
		t.Fatal("acted on a disallowed user, a bot or an unconfigured channel")
	}
	h.post("u1", "c1", "다음 분기 로드맵 정하기\n```rm -rf```")
	ms := h.f.find("POST", "/v1/crew/meetings")
	if len(ms) != 1 || h.sess.threads != 1 {
		t.Fatalf("meeting not started: %d threads %d", len(ms), h.sess.threads)
	}
	mt := ms[0].Body["meeting"].(map[string]any)
	d := mt["discord"].(map[string]any)
	if mt["team_id"] != "tm1" || mt["template_key"] != "review" || mt["trigger"] != "discord" || d["thread_id"] != "t1" || d["bot_id"] != "bot1" ||
		d["channel_id"] != "c1" || d["thread_url"] != "https://discord.com/channels/g1/t1" || mt["title"] != "다음 분기 로드맵 정하기" {
		t.Fatalf("meeting body: %v", mt)
	}
	topic := mt["topic"].(string)
	if !strings.Contains(topic, "```text\n") || strings.Count(topic, "```") != 2 {
		t.Fatalf("topic not fenced: %q", topic)
	}

	// The engine writes turns; the host mirrors them.
	h.f.mu.Lock()
	rec := h.f.meetings["mt1"]["crew_meeting"].(map[string]any)
	rec["status"] = "running"
	rec["turns"] = []any{
		map[string]any{"seq": 1, "round": 0, "kind": "brief", "speaker": "서기", "text": "브리핑입니다"},
		map[string]any{"seq": 2, "round": 1, "kind": "turn", "speaker": "민지", "stance": "agree", "text": "좋아요"},
	}
	h.f.mu.Unlock()
	h.m.Tick()
	got := h.sess.sentTo("t1")
	if len(got) != 3 || !strings.HasPrefix(got[1], "**서기** · 브리핑") || !strings.HasPrefix(got[2], "**민지** · 라운드 1 · 찬성\n좋아요") {
		t.Fatalf("thread: %q", got)
	}
	rep := h.f.lastReport(t)
	if p := rep["posted"].([]any)[0].(map[string]any); p["meeting_id"] != "mt1" || p["last_posted_seq"] != float64(2) {
		t.Fatalf("posted: %v", rep["posted"])
	}
	h.m.Tick()
	if len(h.sess.sentTo("t1")) != 3 {
		t.Fatal("turns posted twice")
	}

	h.post("u1", "t1", "예산도 봐 주세요")
	notes := h.f.find("POST", "/v1/crew/meetings/mt1/notes")
	if len(notes) != 1 || notes[0].Body["text"] != "예산도 봐 주세요" || notes[0].Body["by"] != "u1" || notes[0].Body["expected_revision"] != float64(1) {
		t.Fatalf("notes: %v", notes)
	}
	h.post("intruder", "t1", "끼어들기")
	if len(h.f.find("POST", "/v1/crew/meetings/mt1/notes")) != 1 {
		t.Fatal("note from a disallowed user")
	}

	h.sess.h.Command(Command{InteractionID: "i1", Name: cmdMeeting, Sub: subStop, ChannelID: "t1", GuildID: "g1", UserID: "u1"})
	if len(h.f.find("POST", "/v1/crew/meetings/mt1/stop")) != 1 {
		t.Fatal("stop not sent")
	}

	gets0 := len(h.f.find("GET", "/v1/crew/meetings/mt1"))
	h.f.mu.Lock()
	rec["status"], rec["ended_reason"] = "stopped", "human_stop"
	rec["turns"] = append(rec["turns"].([]any), map[string]any{"seq": 3, "round": 1, "kind": "turn", "speaker": "준호", "text": "막차"})
	rec["conclusion"] = map[string]any{"summary": "요약", "decisions": []any{"A로 간다"}}
	h.f.mu.Unlock()
	h.m.Tick()
	h.m.Tick()
	if gets := len(h.f.find("GET", "/v1/crew/meetings/mt1")) - gets0; gets != 1 {
		t.Fatalf("ended meeting fetched %d times, want one final pass", gets)
	}
	end := h.sess.sentTo("t1")
	if len(end) != 5 || !strings.HasPrefix(end[3], "**준호**") || !strings.Contains(end[4], "A로 간다") || !strings.Contains(end[4], "사람이 멈췄어요") {
		t.Fatalf("end: %q", end)
	}
}

func TestAtNameAsksAgentOnce(t *testing.T) {
	h := setup(t)
	h.m.Tick()
	h.post("u1", "c2", "@리서처 이 논문 요약해 줘")
	as := h.f.find("POST", "/v1/crew/assists")
	if len(as) != 1 {
		t.Fatalf("assists: %d", len(as))
	}
	a := as[0].Body["assist"].(map[string]any)
	if a["kind"] != "try" || a["expert"] != "리서처" || !strings.Contains(a["prompt"].(string), "이 논문 요약해 줘") {
		t.Fatalf("assist: %v", a)
	}
	if len(as[0].Body["request_id"].(string)) != 32 {
		t.Fatal("request_id")
	}
	h.m.Tick()
	if len(h.sess.sentTo("t1")) != 0 {
		t.Fatal("posted before the answer")
	}
	h.f.mu.Lock()
	h.f.assists["as1"]["crew_assist"] = map[string]any{"status": "done", "output": "요약입니다"}
	h.f.mu.Unlock()
	h.m.Tick()
	h.m.Tick()
	if got := h.sess.sentTo("t1"); len(got) != 1 || got[0] != "**리서처** · 답변 (읽기 전용)\n요약입니다" {
		t.Fatalf("answer: %q", got)
	}

	h.post("u1", "c2", "@없는이름 안녕")
	if got := h.sess.sentTo("t2"); len(got) != 1 || !strings.Contains(got[0], "찾지 못했어요") {
		t.Fatalf("unknown: %q", got)
	}

	// /물어보기 with a team handle starts a meeting instead.
	h.sess.h.Command(Command{InteractionID: "i2", Name: cmdAsk, Options: map[string]string{optName: "@기획팀", optQuestion: "출시일?"},
		ChannelID: "c2", GuildID: "g1", UserID: "u1", UserName: "u1"})
	ms := h.f.find("POST", "/v1/crew/meetings")
	if len(ms) != 1 || ms[0].Body["meeting"].(map[string]any)["team_id"] != "tm1" {
		t.Fatalf("team ask: %v", ms)
	}
}

func TestWorkflowRunApproval(t *testing.T) {
	h := setup(t)
	h.f.bots[0]["bot"].(map[string]any)["discord_bot"].(map[string]any)["channels"] = []any{
		map[string]any{"channel_id": "c1", "auto_start": true, "action": map[string]any{"kind": "workflow", "id": "wf1"}}}
	h.m.Tick()
	h.post("u1", "c1", "이번 주 정리")
	rs := h.f.find("POST", "/v1/crew/runs")
	if len(rs) != 1 {
		t.Fatal("no run")
	}
	run := rs[0].Body["run"].(map[string]any)
	if run["trigger"] != "discord" || run["crew_id"] != "wf1" || run["crew_revision"] != float64(7) || run["status"] != "running" ||
		run["origin"].(map[string]any)["thread_id"] != "t1" {
		t.Fatalf("run: %v", run)
	}
	h.f.mu.Lock()
	h.f.runs["run1"]["crew_run"] = map[string]any{"status": "waiting", "waiting": map[string]any{"kind": "approval", "node_id": "n2", "question": "보낼까요?"}}
	h.f.mu.Unlock()
	h.m.Tick()
	h.m.Tick()
	bs := h.sess.lastButtons()
	if len(bs) != 2 || bs[0].ID != "ark:r:run1:n2:y" {
		t.Fatalf("buttons: %v", bs)
	}
	if n := strings.Count(strings.Join(h.sess.sentTo("t1"), "|"), "보낼까요?"); n != 1 {
		t.Fatalf("asked %d times", n)
	}
	h.sess.h.Button(Press{InteractionID: "i", CustomID: "ark:r:run1:n2:y", ChannelID: "t1", GuildID: "g1", UserID: "u1"})
	ds := h.f.find("POST", "/v1/crew/runs/run1/decision")
	if len(ds) != 1 || ds[0].Body["approve"] != true || ds[0].Body["node_id"] != "n2" || ds[0].Body["expected_revision"] != float64(1) {
		t.Fatalf("decision: %v", ds)
	}
	h.f.mu.Lock()
	h.f.runs["run1"]["crew_run"] = map[string]any{"status": "succeeded", "summary": "끝"}
	h.f.mu.Unlock()
	h.m.Tick()
	if got := h.sess.sentTo("t1"); got[len(got)-1] != "**요약** · 완료\n끝" {
		t.Fatalf("result: %q", got)
	}
}

func TestBudgetAsksBeforeStartingAndOnWaitingMeeting(t *testing.T) {
	h := setup(t)
	h.f.settings = map[string]any{"daily_usd": 2.0, "guild_id": "g1"}
	h.f.todayUSD = 2.5
	h.m.Tick()
	h.post("u1", "c1", "회의 해 주세요")
	if len(h.f.find("POST", "/v1/crew/meetings")) != 0 {
		t.Fatal("started over budget")
	}
	bs := h.sess.lastButtons()
	if len(bs) != 2 || !strings.HasSuffix(bs[0].ID, ":y") || !bs[1].Danger {
		t.Fatalf("buttons: %v", bs)
	}
	h.sess.h.Button(Press{InteractionID: "i0", CustomID: bs[0].ID, ChannelID: "t1", GuildID: "g1", UserID: "intruder"})
	if len(h.f.find("POST", "/v1/crew/meetings")) != 0 {
		t.Fatal("disallowed press started work")
	}
	h.sess.h.Button(Press{InteractionID: "i1", CustomID: bs[0].ID, ChannelID: "t1", GuildID: "g1", UserID: "u1"})
	if len(h.f.find("POST", "/v1/crew/meetings")) != 1 {
		t.Fatal("approved start not made")
	}
	h.sess.h.Button(Press{InteractionID: "i2", CustomID: bs[0].ID, ChannelID: "t1", GuildID: "g1", UserID: "u1"})
	if len(h.f.find("POST", "/v1/crew/meetings")) != 1 {
		t.Fatal("a second press started it twice")
	}

	h.f.mu.Lock()
	rec := h.f.meetings["mt1"]["crew_meeting"].(map[string]any)
	rec["status"] = "waiting"
	rec["waiting"] = map[string]any{"kind": "budget", "reason": "에이전트 월 예산에 닿았어요.", "at": "2026-10-13T09:05:00Z"}
	h.f.mu.Unlock()
	h.m.Tick()
	h.m.Tick()
	bs = h.sess.lastButtons()
	if bs[1].ID != "ark:m:mt1:n" || strings.Count(strings.Join(h.sess.sentTo("t1"), "|"), "월 예산") != 1 {
		t.Fatalf("waiting ask: %v %q", bs, h.sess.sentTo("t1"))
	}
	h.sess.h.Button(Press{InteractionID: "i3", CustomID: "ark:m:mt1:n", ChannelID: "t1", GuildID: "g1", UserID: "u1"})
	ds := h.f.find("POST", "/v1/crew/meetings/mt1/decision")
	if len(ds) != 1 || ds[0].Body["approve"] != false || ds[0].Body["expected_revision"] != float64(1) {
		t.Fatalf("decision: %v", ds)
	}
}

func TestOwnerOnlyByDefaultAndGuildFilter(t *testing.T) {
	h := setup(t)
	delete(h.f.bots[0]["bot"].(map[string]any)["discord_bot"].(map[string]any), "allowed")
	h.f.settings = map[string]any{"guild_id": "g1"}
	h.m.Tick()
	h.post("u1", "c1", "안녕")
	if len(h.f.find("POST", "/v1/crew/meetings")) != 0 {
		t.Fatal("non-owner heard without an allow list")
	}
	h.sess.h.Message(Message{ID: "o", ChannelID: "c1", GuildID: "other", AuthorID: "owner", Content: "다른 서버"})
	if len(h.f.find("POST", "/v1/crew/meetings")) != 0 {
		t.Fatal("acted in another guild")
	}
	h.post("owner", "c1", "주인 글")
	if len(h.f.find("POST", "/v1/crew/meetings")) != 1 {
		t.Fatal("owner not heard")
	}
}

func TestRemovedBotDropsToken(t *testing.T) {
	h := setup(t)
	h.m.Tick()
	h.f.bots = nil
	h.m.Tick()
	if _, err := keyring.Get(KeyringService, "bot1"); err == nil {
		t.Fatal("token kept after the bot was removed")
	}
	if h.sess.open {
		t.Fatal("session left open")
	}
}

func TestFileFallbackWithoutKeyring(t *testing.T) {
	h := setup(t)
	keystore.UseMock(keyring.ErrUnsupportedPlatform)
	h.m.Tick()
	st, err := os.Stat(fallbackFile)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("fallback file: %v", err)
	}
	if loadToken("bot1") != "secret-bot-token-1234" {
		t.Fatal("fallback token not read back")
	}
}

func TestInviteURL(t *testing.T) {
	u, err := url.Parse(InviteURL("123"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	p, _ := strconv.ParseInt(q.Get("permissions"), 10, 64)
	if q.Get("scope") != "bot applications.commands" || q.Get("client_id") != "123" || p&permAdministrator != 0 || p != invitePerms() {
		t.Fatalf("invite: %s", u)
	}
}

func TestSplitKeepsLimit(t *testing.T) {
	long := strings.Repeat("가", 4500)
	parts := split(long, 1900)
	if len(parts) != 3 || strings.Join(parts, "") != long {
		t.Fatalf("split: %d", len(parts))
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestBotTargetServesChannelsWithoutAction(t *testing.T) {
	h := setup(t)
	db := h.f.bots[0]["bot"].(map[string]any)["discord_bot"].(map[string]any)
	db["target"] = map[string]any{"kind": "meeting", "id": "tm1"}
	db["channels"] = []any{map[string]any{"channel_id": "c1", "auto_start": true}}
	h.m.Tick()
	h.post("u1", "c1", "팀마다 봇")
	ms := h.f.find("POST", "/v1/crew/meetings")
	if len(ms) != 1 || ms[0].Body["meeting"].(map[string]any)["team_id"] != "tm1" {
		t.Fatalf("bot target not used: %v", ms)
	}
}

func TestAtNameOnExternalRunnerAcceptedThenAnswered(t *testing.T) {
	h := setup(t)
	h.m.Tick()
	h.post("u1", "c2", "@리서처 오래 걸리는 조사")
	h.f.mu.Lock()
	h.f.assists["as1"]["crew_assist"] = map[string]any{"status": "running", "trace": map[string]any{"backend": "mine", "task_id": "t-1", "state": "accepted"}}
	h.f.mu.Unlock()
	h.m.Tick()
	h.m.Tick()
	if got := h.sess.sentTo("t1"); len(got) != 1 || !strings.Contains(got[0], "외부 실행기가 받았어요") {
		t.Fatalf("accepted once: %q", got)
	}
	h.now = h.now.Add(5 * time.Hour) // past the 2h a plain job waits; an accepted one keeps waiting
	long := strings.Repeat("가", 1900) + "\n" + strings.Repeat("나", 1000)
	h.f.mu.Lock()
	h.f.assists["as1"]["crew_assist"] = map[string]any{"status": "done", "output": long}
	h.f.mu.Unlock()
	h.sess.mu.Lock()
	h.sess.failSend = len(h.sess.sent) + 1 // the second part fails once
	h.sess.mu.Unlock()
	h.m.Tick()
	h.m.Tick()
	got := h.sess.sentTo("t1")
	if len(got) != 3 || got[1]+got[2] != "**리서처** · 답변 (읽기 전용)\n"+long {
		t.Fatalf("answer parts, each once: %d", len(got))
	}
	h.m.Tick()
	if len(h.sess.sentTo("t1")) != 3 {
		t.Fatal("answer sent again")
	}
}
