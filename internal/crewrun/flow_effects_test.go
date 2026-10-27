package crewrun

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type notifyEffectTransport func(*http.Request) (*http.Response, error)

func (f notifyEffectTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFlowEffectPreparedTelegramBytesStayFrozen(t *testing.T) {
	file := filepath.Join(t.TempDir(), "notify.json")
	if e := os.WriteFile(file, []byte(`{"telegram":{"ops":{"bot_token":"12345:synthetic","chat_id":"-123"}}}`), 0600); e != nil {
		t.Fatal(e)
	}
	e := &Engine{NotifyFile: file}
	p, err := e.prepareFlowNotify(map[string]string{"channel": "telegram", "to": "ops", "message": "exact {{input}}"}, "message")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.request.Preview, "synthetic") || !strings.Contains(p.request.Preview, "-123") {
		t.Fatal(p.request.Preview)
	}
	// Configuration changing after claim cannot mutate the already approved request.
	if err = os.WriteFile(file, []byte(`{"telegram":{"ops":{"bot_token":"67890:changed","chat_id":"-456"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	sent := 0
	e.HTTP = &http.Client{Transport: notifyEffectTransport(func(r *http.Request) (*http.Response, error) {
		sent++
		b, _ := io.ReadAll(r.Body)
		if r.URL.String() != "https://api.telegram.org/bot12345:synthetic/sendMessage" || string(b) != `{"chat_id":"-123","text":"exact message"}` {
			t.Fatal("prepared request changed", r.URL, string(b))
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":42}}`)), Header: http.Header{}}, nil
	})}
	if r := e.sendFlowNotify(context.Background(), p); r.Status != "succeeded" || sent != 1 {
		t.Fatal(r, sent)
	}
}

func TestFlowEffectRefusesArbitraryTargetsAndOversizePreview(t *testing.T) {
	for _, u := range []string{"http://hooks.slack.com/services/a/b/c", "https://example.com/services/a/b/c", "https://hooks.slack.com:443/services/a/b/c", "https://hooks.slack.com/services/a/b/c?next=x", "https://hooks.slack.com/services/a%2fb/c/d"} {
		if validFlowSlackURL(u) {
			t.Fatal("arbitrary endpoint accepted", u)
		}
	}
	file := filepath.Join(t.TempDir(), "notify.json")
	if err := os.WriteFile(file, []byte(`{"slack":{"ops":{"webhook_url":"https://hooks.slack.com/services/a/b/c"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	e := &Engine{NotifyFile: file}
	for _, c := range []map[string]string{{"channel": "slack", "to": "ops", "message": strings.Repeat("한", 1501)}, {"channel": "slack", "to": "{{input}}"}, {"channel": "email", "to": "ops"}} {
		if _, err := e.prepareFlowNotify(c, "synthetic"); err == nil {
			t.Fatal("unsupported effect accepted", c)
		}
	}
}

func TestFlowEffectNoRedirectNoRetryAndPositiveAcknowledgementRequired(t *testing.T) {
	for _, tc := range []struct {
		channel string
		status  int
		body    string
	}{{"slack", 302, ""}, {"slack", 500, "error"}, {"slack", 200, "not ok"}, {"telegram", 200, `{"ok":false}`}, {"telegram", 200, `{"ok":true}`}} {
		t.Run(tc.channel+tc.body, func(t *testing.T) {
			calls := 0
			e := &Engine{HTTP: &http.Client{Transport: notifyEffectTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": []string{"https://elsewhere.test"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}}
			p := preparedFlowNotify{channel: tc.channel, url: "https://hooks.slack.com/services/a/b/c", body: []byte(`{"text":"test"}`)}
			if r := e.sendFlowNotify(context.Background(), p); r.Status != "unknown" || calls != 1 {
				t.Fatal(r, calls)
			}
		})
	}
}

func TestFlowEffectImplicitProgressDoesNotSend(t *testing.T) {
	x := &runExec{flow: &flowExecution{}, item: CrewItem{Crew: Crew{Notify: &CrewNotify{Channels: []NotifyChannel{{Kind: "slack"}}}}}}
	x.progress("approval", "bounded approval preview")
	if len(x.events) != 0 {
		t.Fatal("FLOW queued an implicit external effect")
	}
}
