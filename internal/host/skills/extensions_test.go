package skills

import (
	"errors"
	"testing"
)

func TestToolCallHookFailsClosed(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{"answer":"a","cited_memory_ids":[],"directives_applied":[],"retrieved":[]}`)
	seen := []string{}
	ToolCallHook = func(name string, args map[string]any) error {
		seen = append(seen, name)
		switch args["query"] {
		case "refuse":
			return errors.New("house rule")
		case "panic":
			panic("boom")
		}
		return nil
	}
	t.Cleanup(func() { ToolCallHook = nil })
	for _, q := range []string{"refuse", "panic"} {
		if _, err := callTool("reflect", j(t, `{"query":"`+q+`"}`)); !errors.Is(err, errToolRefused) {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if len(*calls) != 0 {
		t.Fatal("a refused call reached the server")
	}
	if _, err := callTool("reflect", j(t, `{"query":"ok"}`)); err != nil || len(*calls) != 1 || len(seen) != 3 || seen[2] != "reflect" {
		t.Fatalf("%v %v %v", err, *calls, seen)
	}
}
