package crew

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func crewCLI(argv []string, stdin string) (int, string, string) {
	var out, errOut bytes.Buffer
	rc := crewMain(argv, strings.NewReader(stdin), &out, &errOut)
	return rc, out.String(), errOut.String()
}

func TestCrewRunNodeReportsEveryOutcomeAsJSON(t *testing.T) {
	patch(t, &runnerCall, func(string, any, time.Duration) (map[string]any, error) { return map[string]any{}, nil })
	if rc, out, _ := crewCLI([]string{"run-node", "--json"}, "[1]"); rc != 1 || !strings.Contains(out, "표준입력이 JSON 객체가 아니에요") {
		t.Fatal(rc, out)
	}
	var seen map[string]any
	patch(t, &runNodeFn, func(spec map[string]any) (map[string]any, error) {
		seen = spec
		return map[string]any{"ok": true, "model": "sonnet", "output": "답"}, nil
	})
	rc, out, _ := crewCLI([]string{"run-node"}, `{"model":"sonnet","task":"요약"}`)
	if rc != 0 || seen["task"] != "요약" || !strings.Contains(out, `"output": "답"`) || !strings.Contains(out, `"usage"`) {
		t.Fatal(rc, out, seen)
	}
	// A plain failure still ends as a result the engine can read, and is reported for the caller it worked for.
	reported := 0
	patch(t, &runnerCall, func(path string, _ any, _ time.Duration) (map[string]any, error) {
		if path == "/v1/routing/calls" {
			reported++
		}
		return map[string]any{}, nil
	})
	patch(t, &runNodeFn, func(map[string]any) (map[string]any, error) { return nil, errors.New("boom") })
	if rc, out, _ := crewCLI([]string{"run-node"}, `{"model":"sonnet","origin":{"kind":"assist"}}`); rc != 1 || !strings.Contains(out, "boom") || reported != 1 {
		t.Fatal(rc, out, reported)
	}
}

func TestCrewRouteDefaultsAndDecideOnly(t *testing.T) {
	var spec map[string]any
	patch(t, &runRoutedFn, func(s map[string]any, _ decideFunc, _ runFn, _ reportFunc) (map[string]any, error) {
		spec = s
		return map[string]any{"ok": false, "error": "no model"}, nil
	})
	if rc, out, _ := crewCLI([]string{"route", "--task", "고쳐 줘"}, ""); rc != 1 || !strings.Contains(out, "no model") {
		t.Fatal(rc, out)
	}
	if spec["access"] != "read" || spec["effort"] != "medium" || spec["task"] != "고쳐 줘" || spec["group"] != "" {
		t.Fatal(spec)
	}
	patch(t, &runRoutedFn, func(map[string]any, decideFunc, runFn, reportFunc) (map[string]any, error) {
		return nil, crewErr("막혔어요")
	})
	if rc, _, errOut := crewCLI([]string{"route", "--task", "x"}, ""); rc != 2 || !strings.Contains(errOut, "막혔어요") {
		t.Fatal(rc, errOut)
	}

	patch(t, &runnerCall, func(path string, _ any, _ time.Duration) (map[string]any, error) {
		if path != "/v1/routing/decide" {
			t.Errorf("unexpected call %s", path)
		}
		return map[string]any{"model": "haiku"}, nil
	})
	if rc, out, _ := crewCLI([]string{"route", "--task", "x", "--decide-only"}, ""); rc != 0 || !strings.Contains(out, `"haiku"`) {
		t.Fatal(rc, out)
	}
	if rc, _, errOut := crewCLI([]string{"route", "--task", "x", "--decide-only", "--group", "../bad"}, ""); rc != 2 || !strings.Contains(errOut, "라우팅 그룹") {
		t.Fatal(rc, errOut)
	}
	if rc, _, errOut := crewCLI([]string{"route", "--task", "x", "--access", "root"}, ""); rc != 2 || errOut == "" {
		t.Fatal(rc, errOut)
	}
}
