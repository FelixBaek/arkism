package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/subscription"
)

// Account logins: copilot, nous and xai (no API mode) reach their Chat API with the login's token;
// cursor runs its CLI. Fake login and fake CLI only.

func newOAuthAPI(t *testing.T) *providerAPI {
	p := &providerAPI{fixture: newFixture(t)}
	p.root = p.gw.URL
	swap(t, &target, resolve)
	swap(t, &host.APIMode, map[string]bool{})
	swap(t, &oauthConnection, func(id string) (string, string, string, error) {
		if id == "nous" {
			return "", "", "", subscription.ErrOAuthLogin
		}
		return p.base + "/v1/", "oauth-secret-" + id, "Copilot-Integration-Id: test", nil
	})
	p.reply(false, false, true)
	return p
}

func TestOAuthProvidersRouteWithLoginToken(t *testing.T) {
	p := newOAuthAPI(t)
	for _, provider := range []string{"copilot", "xai"} {
		for _, protocol := range []string{"chat", "messages"} {
			t.Run(provider+"/"+protocol, func(t *testing.T) {
				reply := p.postObj(p.request(protocol, provider+"/first", false))
				if reply.status != 200 || !bytes.Contains(pyDumpsUnicode(parsed(t, reply.body)), []byte("안녕")) ||
					bytes.Contains(reply.body, []byte("oauth-secret")) {
					t.Fatalf("%d %s", reply.status, reply.body)
				}
				sent := p.last()
				if sent.path != "/v1/chat/completions" || sent.header.Get("authorization") != "Bearer oauth-secret-"+provider ||
					sent.header.Get("Copilot-Integration-Id") != "test" || get(sent.body, "model") != "first" {
					t.Fatal(sent.path, sent.header, string(sent.raw))
				}
			})
		}
	}
	// No login yet: the model has no connection, nothing is sent.
	before := p.seenCount()
	if reply := p.postObj(p.request("messages", "nous/first", false)); reply.status != 400 || p.seenCount() != before {
		t.Fatalf("signed-out provider must not run: %d %s", reply.status, reply.body)
	}
}

func TestOAuthProviderUnauthorizedRetiresToken(t *testing.T) {
	p := newOAuthAPI(t)
	var expired []string
	swap(t, &oauthExpire, func(id, token string) { expired = append(expired, id+":"+token) })
	p.replies["first"] = upstreamReply{401, "application/json", []byte(`{"error":{"message":"bad token"}}`)}
	reply := p.postObj(p.request("messages", "copilot/first", false))
	if reply.status != 401 || len(expired) != 1 || expired[0] != "copilot:oauth-secret-copilot" {
		t.Fatalf("%d %v %s", reply.status, expired, reply.body)
	}
	// A refresh that cannot reach the provider is a safe message, not the token or an address.
	swap(t, &oauthConnection, func(string) (string, string, string, error) { return "", "", "", errors.New("oauth_unreachable") })
	reply = p.postObj(p.request("messages", "copilot/first", false))
	if reply.status != 500 || bytes.Contains(reply.body, []byte("oauth_unreachable")) {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
}

func TestCursorRunsTextOnlyThroughCLI(t *testing.T) {
	p := newOAuthAPI(t)
	bin := filepath.Join(t.TempDir(), "cursor-agent")
	writeFile(t, bin, "")
	swap(t, &cursorBin, func() string { return bin })
	var prompts []string
	swap(t, &cursorRun, func(_ context.Context, model, prompt string) (string, int64, int64, error) {
		prompts = append(prompts, model+"|"+prompt)
		return "커서 답", 7, 3, nil
	})
	ask := func(protocol string, stream bool) response {
		body := p.request(protocol, "cursor/auto", stream)
		body.Del("tools")
		if protocol == "messages" {
			body.Set("system", "짧게 답한다")
		}
		return p.postObj(body)
	}
	for _, protocol := range []string{"messages", "chat"} {
		for _, stream := range []bool{false, true} {
			reply := ask(protocol, stream)
			if reply.status != 200 || !bytes.Contains(reply.body, []byte("커서 답")) && !bytes.Contains(reply.body, []byte(`\ucee4\uc11c \ub2f5`)) {
				t.Fatalf("%s %v: %d %s", protocol, stream, reply.status, reply.body)
			}
		}
	}
	if !strings.HasPrefix(prompts[0], "auto|짧게 답한다\n\n도구를 써 주세요") {
		t.Fatalf("%q", prompts[0])
	}
	if reply := p.postObj(p.request("messages", "cursor/auto", false)); reply.status != 400 || len(prompts) != 4 {
		t.Fatalf("a request with tools must be refused before the CLI: %d %s", reply.status, reply.body)
	}
	swap(t, &cursorRun, func(context.Context, string, string) (string, int64, int64, error) {
		return "", 0, 0, errors.New("login_unavailable")
	})
	if reply := ask("messages", false); reply.status != 503 || !bytes.Contains(reply.body, []byte("login_unavailable")) {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	os.Remove(bin)
	if reply := ask("messages", false); reply.status != 400 {
		t.Fatalf("without the CLI there is no connection: %d", reply.status)
	}
}

func TestCursorResultParsing(t *testing.T) {
	out := []byte("noise\n" + `{"type":"result","result":"답","is_error":false,"usage":{"inputTokens":5,"outputTokens":2}}` + "\n")
	if text, in, o, err := cursorResult(out, nil); err != nil || text != "답" || in != 5 || o != 2 {
		t.Fatal(text, in, o, err)
	}
	if _, _, _, err := cursorResult([]byte(`{"type":"result","is_error":true,"result":"secret prompt"}`), nil); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	if _, _, _, err := cursorResult(nil, fmt.Errorf("exit 1")); err == nil || err.Error() != "login_unavailable" {
		t.Fatal(err)
	}
	var b bytes.Buffer
	w := &limitWriter{&b, 4}
	w.Write([]byte("abcdef"))
	if b.String() != "abcd" {
		t.Fatal(b.String())
	}
}
