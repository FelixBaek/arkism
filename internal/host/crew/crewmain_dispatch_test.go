package crew

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func runCrewMain(argv []string, stdin string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := crewMain(argv, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestCrewMainListResolveStatusAndDecide(t *testing.T) {
	patch(t, &runSleep, func(float64) {})
	patch(t, &apiGet, func(path string, _ time.Duration) (map[string]any, error) {
		switch {
		case strings.HasPrefix(path, "/v1/crew/resolve"):
			if strings.Contains(path, "handle=%EB%A6%AC%EC%84%9C%EC%B9%98") { // 리서치
				return workflowFixture(t, "리서치", "주제를 받아 정리까지"), nil
			}
			return nil, &host.HTTPError{Code: 404}
		case strings.HasPrefix(path, "/v1/crew/runs/run-1"):
			return runRec("failed", nil, nil, "안 됐어요", 2), nil
		case strings.HasPrefix(path, "/v1/crew/runs/run-2"):
			return runRec("waiting", map[string]any{"node_id": "a", "kind": "approval", "question": "q"}, nil, "", 4), nil
		case strings.HasPrefix(path, "/v1/crew?"):
			return map[string]any{"items": []any{workflowFixture(t, "리서치", "정리")["item"]}}, nil
		}
		return nil, &host.HTTPError{Code: 500}
	})
	patch(t, &apiPost, func(string, any, time.Duration) (map[string]any, error) {
		return runRec("succeeded", nil, nil, "보냈어요", 5), nil
	})

	if code, out, errOut := runCrewMain([]string{"list"}, ""); code != 0 || !strings.Contains(out, "리서치") {
		t.Fatalf("list = %d %q %q", code, out, errOut)
	}
	if code, out, _ := runCrewMain([]string{"resolve", "$리서치"}, ""); code != 0 || !strings.Contains(out, "리서치") {
		t.Fatalf("resolve = %d %q", code, out)
	}
	if code, _, errOut := runCrewMain([]string{"resolve", "없음"}, ""); code != 2 || !strings.Contains(errOut, "워크플로우가 없어요") {
		t.Fatalf("resolve unknown = %d %q", code, errOut)
	}
	if code, out, _ := runCrewMain([]string{"status", "run-1"}, ""); code != 1 || !strings.Contains(out, `"failed"`) {
		t.Fatalf("status of a failed run = %d %q", code, out)
	}
	if code, _, errOut := runCrewMain([]string{"status", "../x"}, ""); code != 2 || errOut == "" {
		t.Fatalf("bad run id = %d %q", code, errOut)
	}
	if code, out, _ := runCrewMain([]string{"decide", "run-2", "--approve"}, ""); code != 0 || !strings.Contains(out, "보냈어요") {
		t.Fatalf("decide = %d %q", code, out)
	}
	if code, out, _ := runCrewMain([]string{"hook-context"}, "그냥 질문"); code != 0 || out != "" {
		t.Fatalf("hook-context without handles = %d %q", code, out)
	}
}

func TestCrewMainSurfacesAPIFailures(t *testing.T) {
	patch(t, &apiGet, func(string, time.Duration) (map[string]any, error) { return nil, &host.HTTPError{Code: 500} })
	if code, _, errOut := runCrewMain([]string{"list"}, ""); code == 0 || errOut == "" {
		t.Fatalf("list failure = %d %q", code, errOut)
	}
	if code, _, errOut := runCrewMain([]string{"resolve", "리서치"}, ""); code == 0 || errOut == "" {
		t.Fatalf("resolve failure = %d %q", code, errOut)
	}
	if code, _, _ := runCrewMain([]string{"keys", "remove", "nope"}, ""); code == 0 {
		t.Fatal("removing an unknown key should fail")
	}
	if code, _, errOut := runCrewMain([]string{"accounts", "remove", "nobody"}, ""); code == 0 || errOut == "" {
		t.Fatalf("removing an unknown account = %d %q", code, errOut)
	}
	if code, _, errOut := runCrewMain([]string{"keys", "add", "--provider", "openai", "--name", "x"}, "\n"); code == 0 || errOut == "" {
		t.Fatalf("empty key = %d %q", code, errOut)
	}
}
