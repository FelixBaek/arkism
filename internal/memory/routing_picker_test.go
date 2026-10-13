package memory

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The built-in group needs no saved group: every model turned on in 모델 설정 is in it.
func TestAutoGroupCoversEveryModelInModelSettings(t *testing.T) {
	f := routingGroupsFixture(t)
	d, err := f.svc.DecideRoute(testCtx, f.agent, RoutingDecideInput{Scope: scopeA, Task: "review", Group: AutoGroupID})
	if err != nil || d.Group != AutoGroupID || d.ID != "alpha/shared" {
		t.Fatal("auto group decision", d, err)
	}
	if d, err = f.svc.DecideRoute(testCtx, f.agent, RoutingDecideInput{Scope: scopeA, Task: "review", Group: AutoGroupID, Providers: []string{"gamma"}}); err != nil || d.ID != "gamma/outside" {
		t.Fatal("auto group keeps every provider", d, err)
	}
	page, err := f.svc.RoutingGroups(testCtx, f.human, scopeA)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("auto group is not listed with the saved groups", page, err)
	}
}

func TestChatModelPickerAnswersWithOptionKey(t *testing.T) {
	var got map[string]any
	reply := `{"choices":[{"message":{"content":" m1\n"}}],"usage":{"prompt_tokens":40,"completion_tokens":2,"total_tokens":42}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "" {
			w.WriteHeader(400)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	p := &ChatModelPicker{Endpoint: srv.URL + "/v1", Model: "qwen3:4b"}
	options := []ModelOption{{ID: "a/x", Name: "X"}, {ID: "b/y", Name: "Y", When: "tests"}}
	id, usage, err := p.PickModel(testCtx, "write the tests", options)
	if err != nil || id != "b/y" || usage.Provider != "chat" || usage.Model != "qwen3:4b" || usage.TotalTokens != 42 || got["model"] != "qwen3:4b" {
		t.Fatal(id, usage, err, got)
	}
	if prov, model := pickerIdentity(p); prov != "chat" || model != "qwen3:4b" {
		t.Fatal("identity", prov, model)
	}
	reply = `{"choices":[{"message":{"content":"m7"}}]}`
	if _, _, err = p.PickModel(testCtx, "write the tests", options); !errors.Is(err, ErrUnavailable) {
		t.Fatal("an answer outside the options is refused", err)
	}
	if _, _, err = (&ChatModelPicker{Endpoint: "http://example.com/v1", Model: "m"}).PickModel(testCtx, "t", options); !errors.Is(err, ErrUnavailable) {
		t.Fatal("plain http to a remote host is refused", err)
	}
}
