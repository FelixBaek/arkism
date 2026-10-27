package discord

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
)

// fakeDiscord is a tiny Discord REST API plus gateway, enough for dgSession to connect, read state and post.
type fakeDiscord struct {
	t            *testing.T
	srv          *httptest.Server
	mu           sync.Mutex
	userCode     int  // status for GET /users/@me (0 = 200)
	noApp        bool // GET /oauth2/applications/@me fails
	closeGW      int  // gateway close code sent instead of READY (0 = send READY)
	sendCode     int
	emptyReceipt bool
	events       []string
	calls        []string
	bodies       map[string]string
}

func newFakeDiscord(t *testing.T) *fakeDiscord {
	f := &fakeDiscord{t: t, bodies: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDiscord) reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeDiscord) serve(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSuffix(r.URL.Path, "/") == "/ws" {
		f.gateway(w, r)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/v"+discordgo.APIVersion)
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+p)
	f.bodies[r.Method+" "+p] = string(b)
	userCode, noApp, sendCode, emptyReceipt := f.userCode, f.noApp, f.sendCode, f.emptyReceipt
	f.mu.Unlock()
	switch {
	case p == "/users/@me":
		if userCode != 0 {
			f.reply(w, userCode, map[string]any{"message": "nope", "code": 0})
			return
		}
		f.reply(w, 200, map[string]any{"id": "botuser", "username": "bot"})
	case p == "/oauth2/applications/@me":
		if noApp {
			f.reply(w, 403, map[string]any{"message": "nope"})
			return
		}
		f.reply(w, 200, map[string]any{"id": "app1"})
	case p == "/gateway":
		f.reply(w, 200, map[string]any{"url": "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/ws"})
	case p == "/guilds/g1/channels":
		f.reply(w, 200, []map[string]any{
			{"id": "c1", "guild_id": "g1", "name": "회의실", "type": 0},
			{"id": "n1", "guild_id": "g1", "name": "공지", "type": 5, "parent_id": "cat"},
			{"id": "v1", "guild_id": "g1", "name": "음성", "type": 2},
		})
	case p == "/guilds/gx/channels":
		f.reply(w, 404, map[string]any{"message": "unknown guild"})
	case p == "/guilds/g1/members/u1":
		f.reply(w, 200, map[string]any{"user": map[string]any{"id": "u1"}, "roles": []string{"role1"}})
	case p == "/channels/rest1":
		f.reply(w, 200, map[string]any{"id": "rest1", "guild_id": "g1", "name": "스레드", "type": 11, "parent_id": "c1"})
	case strings.HasPrefix(p, "/channels/") && r.Method == http.MethodGet:
		f.reply(w, 404, map[string]any{"message": "unknown channel"})
	case p == "/channels/c1/messages":
		if sendCode != 0 {
			f.reply(w, sendCode, map[string]any{"message": "unavailable"})
			return
		}
		if emptyReceipt {
			f.reply(w, 200, map[string]any{})
			return
		}
		f.reply(w, 200, map[string]any{"id": "m9", "channel_id": "c1"})
	case p == "/channels/bad/messages", p == "/channels/bad/threads":
		f.reply(w, 403, map[string]any{"message": "missing access"})
	case p == "/channels/c1/messages/m9/threads":
		f.reply(w, 200, map[string]any{"id": "t1", "type": 11})
	case p == "/channels/c1/messages/rest1/threads":
		f.reply(w, 400, map[string]any{"message": "Thread already created", "code": 160004})
	case p == "/channels/c1/threads":
		f.reply(w, 200, map[string]any{"id": "t2", "type": 11})
	case p == "/applications/app1/guilds/g1/commands":
		f.reply(w, 200, []any{})
	case strings.HasPrefix(p, "/interactions/"):
		w.WriteHeader(http.StatusNoContent)
	default:
		f.t.Errorf("unexpected discord call %s %s", r.Method, p)
		f.reply(w, 404, map[string]any{"message": "?"})
	}
}

func (f *fakeDiscord) gateway(w http.ResponseWriter, r *http.Request) {
	c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	send := func(v any) { _ = c.WriteJSON(v) }
	send(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 60000}})
	if _, _, err := c.ReadMessage(); err != nil { // identify
		return
	}
	f.mu.Lock()
	closeGW, events := f.closeGW, f.events
	f.mu.Unlock()
	if closeGW != 0 {
		_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(closeGW, "Authentication failed."))
		return
	}
	send(map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]any{
		"v": 9, "session_id": "sess", "user": map[string]any{"id": "botuser", "username": "bot"},
		"guilds": []any{map[string]any{"id": "g1", "name": "우리 서버", "owner_id": "owner",
			"roles":    []any{map[string]any{"id": "g1", "permissions": "3072"}},
			"members":  []any{map[string]any{"user": map[string]any{"id": "botuser"}, "roles": []any{}}},
			"channels": []any{map[string]any{"id": "c1", "guild_id": "g1", "name": "회의실", "type": 0}},
		}},
	}})
	for i, e := range events {
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"op":0,"s":`+itoa(i+2)+`,`+e+`}`))
	}
	for { // keep the connection until the client closes it
		if _, _, err := c.ReadMessage(); err != nil {
			return
		}
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

type redirect struct{ to *url.URL }

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host, req.Host = r.to.Scheme, r.to.Host, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

func (f *fakeDiscord) session(t *testing.T) *dgSession {
	t.Helper()
	s, err := newDiscordgo("tok")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(f.srv.URL)
	d := s.(*dgSession)
	d.s.Client = &http.Client{Transport: redirect{u}, Timeout: 5 * time.Second}
	d.s.MaxRestRetries = 0
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestDiscordgoSessionEventsAndCalls(t *testing.T) {
	f := newFakeDiscord(t)
	f.events = []string{
		`"t":"MESSAGE_CREATE","d":{"id":"m1","channel_id":"c1","guild_id":"g1","content":"안녕","author":{"id":"u1","username":"felix"},"member":{"nick":"펠릭스","roles":["r1"]}}`,
		`"t":"MESSAGE_CREATE","d":{"id":"m2","channel_id":"c1","content":"hook","webhook_id":"w1","author":{"id":"u9","username":"hook"}}`,
		`"t":"INTERACTION_CREATE","d":{"id":"i1","application_id":"app1","type":2,"token":"it1","channel_id":"c1","guild_id":"g1","member":{"user":{"id":"u1","username":"felix"},"roles":["r1"]},"data":{"id":"x","name":"회의","type":1,"options":[{"name":"멈춤","type":1}]}}`,
		`"t":"INTERACTION_CREATE","d":{"id":"i2","application_id":"app1","type":2,"token":"it2","channel_id":"c1","user":{"id":"u2","username":"dm"},"data":{"id":"x","name":"물어보기","type":1,"options":[{"name":"이름","type":3,"value":"리뷰어"},{"name":"n","type":4,"value":3}]}}`,
		`"t":"INTERACTION_CREATE","d":{"id":"i3","application_id":"app1","type":3,"token":"it3","channel_id":"c1","guild_id":"g1","member":{"user":{"id":"u1","username":"felix"},"roles":[]},"data":{"custom_id":"approve:1","component_type":2}}`,
	}
	d := f.session(t)
	var mu sync.Mutex
	var msgs []Message
	var cmds []Command
	var presses []Press
	done := make(chan struct{}, 8)
	err := d.Open(Handlers{
		Message: func(m Message) { mu.Lock(); msgs = append(msgs, m); mu.Unlock(); done <- struct{}{} },
		Command: func(c Command) { mu.Lock(); cmds = append(cmds, c); mu.Unlock(); done <- struct{}{} },
		Button:  func(p Press) { mu.Lock(); presses = append(presses, p); mu.Unlock(); done <- struct{}{} },
	})
	if err != nil {
		t.Fatal(err)
	}
	for range f.events {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("gateway events never reached the handlers")
		}
	}
	mu.Lock()
	byID := map[string]Message{}
	for _, m := range msgs {
		byID[m.ID] = m
	}
	if m := byID["m1"]; m.AuthorName != "펠릭스" || m.AuthorBot || len(m.RoleIDs) != 1 || m.Content != "안녕" || m.GuildID != "g1" {
		t.Fatalf("member message = %+v", m)
	}
	if m := byID["m2"]; !m.AuthorBot || m.AuthorName != "hook" {
		t.Fatalf("webhook message should count as a bot: %+v", m)
	}
	cmdByID := map[string]Command{}
	for _, c := range cmds {
		cmdByID[c.InteractionID] = c
	}
	if c := cmdByID["i1"]; c.Name != "회의" || c.Sub != "멈춤" || c.UserID != "u1" || c.RoleIDs[0] != "r1" {
		t.Fatalf("subcommand = %+v", c)
	}
	if c := cmdByID["i2"]; c.UserName != "dm" || c.Options["이름"] != "리뷰어" || len(c.Options) != 1 || c.Sub != "" {
		t.Fatalf("command options (non-string ones dropped) = %+v", c)
	}
	if len(presses) != 1 || presses[0].CustomID != "approve:1" || presses[0].UserName != "felix" {
		t.Fatalf("presses = %+v", presses)
	}
	mu.Unlock()

	if app, user := d.Self(); app != "app1" || user != "botuser" {
		t.Fatalf("Self = %q %q", app, user)
	}
	if g := d.Guilds(); len(g) != 1 || g[0].Name != "우리 서버" || g[0].OwnerID != "owner" {
		t.Fatalf("Guilds = %+v", g)
	}
	chs, err := d.Channels("g1")
	if err != nil || len(chs) != 2 || chs[1].ID != "n1" || chs[1].ParentID != "cat" {
		t.Fatalf("Channels keeps text and news only: %+v %v", chs, err)
	}
	if _, err := d.Channels("gx"); err == nil {
		t.Fatal("unknown guild should fail")
	}
	// c1 is in state: @everyone has view_channel|send_messages (3072).
	if p, err := d.Permissions("c1"); err != nil || p != 3072 {
		t.Fatalf("Permissions(c1) = %d %v", p, err)
	}
	if _, err := d.Permissions("gone"); err == nil {
		t.Fatal("a channel neither in state nor on Discord should fail")
	}
	if c, err := d.Channel("c1"); err != nil || c.Name != "회의실" || c.Thread {
		t.Fatalf("Channel(c1) from state = %+v %v", c, err)
	}
	if c, err := d.Channel("rest1"); err != nil || !c.Thread || c.ParentID != "c1" {
		t.Fatalf("Channel(rest1) over REST = %+v %v", c, err)
	}
	if _, err := d.Channel("gone"); err == nil {
		t.Fatal("unknown channel should fail")
	}
	id, err := d.Send("c1", Out{Content: "결과", Buttons: []Button{{ID: "ok", Label: "승인"}, {ID: "no", Label: "거절", Danger: true}}})
	if err != nil || id != "m9" {
		t.Fatalf("Send = %q %v", id, err)
	}
	var sentBody struct {
		Content         string `json:"content"`
		AllowedMentions struct {
			Parse []string `json:"parse"`
		} `json:"allowed_mentions"`
		Components []struct {
			Components []struct {
				Style    int    `json:"style"`
				CustomID string `json:"custom_id"`
			} `json:"components"`
		} `json:"components"`
	}
	f.mu.Lock()
	_ = json.Unmarshal([]byte(f.bodies["POST /channels/c1/messages"]), &sentBody)
	f.mu.Unlock()
	if sentBody.Content != "결과" || len(sentBody.AllowedMentions.Parse) != 0 || len(sentBody.Components) != 1 ||
		sentBody.Components[0].Components[1].Style != int(discordgo.DangerButton) || sentBody.Components[0].Components[0].CustomID != "ok" {
		t.Fatalf("sent body = %+v", sentBody)
	}
	if _, err := d.Send("bad", Out{Content: "x"}); err == nil {
		t.Fatal("refused send should fail")
	}
	if id, err := d.StartThread("c1", "m9", "회의"); err != nil || id != "t1" {
		t.Fatalf("StartThread on a message = %q %v", id, err)
	}
	if id, err := d.StartThread("c1", "", "회의"); err != nil || id != "t2" {
		t.Fatalf("bare StartThread = %q %v", id, err)
	}
	if _, err := d.StartThread("bad", "", "회의"); err == nil {
		t.Fatal("refused thread should fail")
	}
	if err := d.RegisterCommands("g1"); err != nil {
		t.Fatal(err)
	}
	if err := d.Respond("i1", "멈췄어요", true); err != nil {
		t.Fatal(err)
	}
	if err := d.Respond("i1", "again", false); err == nil {
		t.Fatal("an interaction is answered once")
	}
	if err := d.Respond("i3", "ok", false); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	private := f.bodies["POST /interactions/i1/it1/callback"]
	public := f.bodies["POST /interactions/i3/it3/callback"]
	f.mu.Unlock()
	if !strings.Contains(private, `"flags":64`) || strings.Contains(public, `"flags":64`) {
		t.Fatalf("ephemeral flag: private=%s public=%s", private, public)
	}
}

func TestDiscordgoSessionOpenFailures(t *testing.T) {
	f := newFakeDiscord(t)
	f.userCode = 401
	if err := f.session(t).Open(Handlers{}); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("401 from /users/@me = %v", err)
	}
	f.userCode = 403
	if err := f.session(t).Open(Handlers{}); err == nil || errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("403 from /users/@me = %v", err)
	}
	f.userCode, f.closeGW = 0, 4004
	if err := f.session(t).Open(Handlers{}); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("gateway close 4004 = %v", err)
	}
	f.closeGW, f.noApp = 4000, true
	d := f.session(t)
	if err := d.Open(Handlers{}); err == nil || errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("gateway close 4000 = %v", err)
	}
	if err := d.RegisterCommands("g1"); err == nil {
		t.Fatal("without an application id commands cannot be registered")
	}
	if restCode(errors.New("plain")) != 0 {
		t.Fatal("non-REST errors have no status")
	}
}

func TestDiscordgoDurableSendNonceAndUncertainErrors(t *testing.T) {
	f := newFakeDiscord(t)
	d := f.session(t)
	nonce := deliveryNonce("delivery-1", 4, 0)
	if id, err := d.Send("c1", Out{Content: "@everyone 결과", Nonce: nonce}); err != nil || id != "m9" {
		t.Fatalf("receipt=%s err=%v", id, err)
	}
	f.mu.Lock()
	var payload map[string]any
	_ = json.Unmarshal([]byte(f.bodies["POST /channels/c1/messages"]), &payload)
	f.mu.Unlock()
	if payload["nonce"] != nonce || payload["enforce_nonce"] != true || len(list(obj(payload["allowed_mentions"])["parse"])) != 0 {
		t.Fatalf("unsafe send payload %v", payload)
	}
	roles, err := d.MemberRoles("g1", "u1")
	if err != nil || len(roles) != 1 || roles[0] != "role1" {
		t.Fatalf("membership=%v %v", roles, err)
	}
	if id, err := d.StartThread("c1", "rest1", "duplicate"); err != nil || id != "rest1" {
		t.Fatalf("duplicate thread=%s %v", id, err)
	}
	// Override the SDK's default retry setting: the durable send must still
	// make exactly one request when a server error could hide an accepted write.
	d.s.MaxRestRetries = 3
	f.mu.Lock()
	f.sendCode = 500
	before := len(f.calls)
	f.mu.Unlock()
	if _, err = d.Send("c1", Out{Content: "result", Nonce: nonce}); err == nil {
		t.Fatal("500 must be uncertain")
	}
	f.mu.Lock()
	after := len(f.calls)
	f.sendCode = 0
	f.emptyReceipt = true
	f.mu.Unlock()
	if after-before != 1 {
		t.Fatalf("uncertain send retried %d times", after-before)
	}
	if _, err = d.Send("c1", Out{Content: "result", Nonce: nonce}); err == nil {
		t.Fatal("missing receipt accepted")
	}
	f.mu.Lock()
	before = len(f.calls)
	f.mu.Unlock()
	if _, err = d.Send("c1", Out{Content: "expired", Nonce: nonce, Deadline: time.Now().Add(-time.Second)}); err == nil {
		t.Fatal("expired lease sent")
	}
	f.mu.Lock()
	after = len(f.calls)
	f.mu.Unlock()
	if after != before {
		t.Fatal("expired lease reached Discord")
	}
}

func TestDiscordgoGuildsWithoutState(t *testing.T) {
	d := &dgSession{s: &discordgo.Session{}}
	if g := d.Guilds(); g == nil || len(g) != 0 {
		t.Fatalf("Guilds without state = %v", g)
	}
	if len(commands()) != 2 || components(nil) != nil {
		t.Fatal("commands/components shape")
	}
}
