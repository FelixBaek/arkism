package discord

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestCommandAndPressRefusals(t *testing.T) {
	h := setup(t)
	h.m.Tick()
	cmd := func(c Command) string {
		h.sess.mu.Lock()
		n := len(h.sess.responses)
		h.sess.mu.Unlock()
		if c.GuildID == "" {
			c.GuildID = "g1"
		}
		h.sess.h.Command(c)
		h.sess.mu.Lock()
		defer h.sess.mu.Unlock()
		if len(h.sess.responses) == n {
			return ""
		}
		return h.sess.responses[len(h.sess.responses)-1]
	}
	if got := cmd(Command{InteractionID: "a", Name: cmdAsk, ChannelID: "c3", UserID: "u1"}); !strings.Contains(got, "연결되어 있지 않아요") {
		t.Fatalf("unlinked channel: %q", got)
	}
	if got := cmd(Command{InteractionID: "b", Name: cmdAsk, ChannelID: "c2", UserID: "intruder"}); !strings.Contains(got, "권한이 없어요") {
		t.Fatalf("disallowed user: %q", got)
	}
	if got := cmd(Command{InteractionID: "c", Name: cmdAsk, ChannelID: "c2", UserID: "u1", Options: map[string]string{optName: "@"}}); !strings.Contains(got, "함께 적어") {
		t.Fatalf("missing options: %q", got)
	}
	if got := cmd(Command{InteractionID: "d", Name: cmdMeeting, Sub: subStop, ChannelID: "c2", UserID: "u1"}); !strings.Contains(got, "스레드 안에서만") {
		t.Fatalf("stop outside a meeting thread: %q", got)
	}
	if got := cmd(Command{InteractionID: "e", Name: "다른것", ChannelID: "c2", UserID: "u1"}); !strings.Contains(got, "모르는 명령") {
		t.Fatalf("unknown command: %q", got)
	}

	press := func(p Press) string {
		h.sess.mu.Lock()
		n := len(h.sess.responses)
		h.sess.mu.Unlock()
		p.GuildID = "g1"
		h.sess.h.Button(p)
		h.sess.mu.Lock()
		defer h.sess.mu.Unlock()
		if len(h.sess.responses) == n {
			return ""
		}
		return h.sess.responses[len(h.sess.responses)-1]
	}
	if got := press(Press{InteractionID: "p1", CustomID: "other:1", UserID: "u1"}); got != "" {
		t.Fatalf("foreign button answered: %q", got)
	}
	if got := press(Press{InteractionID: "p2", CustomID: "ark:s:x:y", UserID: "intruder"}); !strings.Contains(got, "권한이 없어요") {
		t.Fatalf("disallowed press: %q", got)
	}
	if got := press(Press{InteractionID: "p3", CustomID: "ark:s:gone:y", UserID: "u1", UserName: "펠릭스"}); !strings.Contains(got, "오래된 요청") {
		t.Fatalf("stale start: %q", got)
	}
	if got := press(Press{InteractionID: "p4", CustomID: "ark:q", UserID: "u1"}); got != "" {
		t.Fatalf("malformed press answered: %q", got)
	}
	if got := press(Press{InteractionID: "p5", CustomID: "ark:m:missing:n", UserID: "u1"}); !strings.Contains(got, "처리하지 못했어요") {
		t.Fatalf("decision on a missing meeting: %q", got)
	}
}

func TestMainSubcommands(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Main([]string{"invite", "app9"}, nil, &out, &errb); code != 0 || !strings.Contains(out.String(), "client_id=app9") {
		t.Fatalf("invite: %d %q", code, out.String())
	}
	if code := Main([]string{"invite"}, nil, &out, &errb); code != 2 || !strings.Contains(errb.String(), "usage:") {
		t.Fatalf("invite without app: %d", code)
	}
	if code := Main([]string{"nope"}, nil, &out, &errb); code != 2 {
		t.Fatalf("unknown subcommand: %d", code)
	}

	// once and run talk to memoryd at host.BaseURL with the discord service token.
	h := setup(t)
	_, srv := newFakeMemoryd(t)
	dir := t.TempDir()
	oldBase, oldTok := host.BaseURL, TokenFile
	t.Cleanup(func() { host.BaseURL, TokenFile = oldBase, oldTok })
	host.BaseURL, TokenFile = srv.URL, filepath.Join(dir, "discord.token")
	if err := os.WriteFile(TokenFile, []byte("svc-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = h
	errb.Reset()
	if code := Main([]string{"once"}, nil, &out, &errb); code != 0 {
		t.Fatalf("once: %d %s", code, errb.String())
	}
	if _, err := os.Stat(stateFile); err != nil {
		t.Fatalf("once did not save state: %v", err)
	}

	// Run returns after one tick once ctx ends.
	m := NewManager(&httpAPI{base: srv.URL, tokenFile: TokenFile, client: srv.Client()}, func(string) (Session, error) { return newFakeSession(), nil }, io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSaveLogsUnwritableState(t *testing.T) {
	h := setup(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stateFile = filepath.Join(blocker, "state.json") // its parent is a file, so MkdirAll fails
	var log bytes.Buffer
	h.m.log = &log
	h.m.save()
	if !strings.Contains(log.String(), "state not saved") {
		t.Fatalf("save failure not logged: %q", log.String())
	}
}
