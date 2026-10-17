package skills

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMemorySignalsPagesUntilDoneAndPrintsCountsOnly(t *testing.T) {
	replies := []string{`{"computed":100,"changed":3,"next_cursor":"c1"}`, `{"computed":7,"changed":0,"next_cursor":""}`}
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if r.URL.Path != "/v1/memory-signals/recompute" || r.Header.Get("Authorization") != "Bearer PRIVATE_TOKEN" || len(bodies) > len(replies) {
			w.WriteHeader(500)
			return
		}
		io.WriteString(w, replies[len(bodies)-1])
	}))
	defer srv.Close()
	t.Setenv("MEMORY_API_TOKEN", "PRIVATE_TOKEN")
	code, out, errOut := runCLI(t, memorySignalsMain, "--workspace-id", "w", "--project-id", "p", "--url", srv.URL, "--execute")
	if code != 0 || out != `{"batches": 2, "computed": 107, "changed": 3, "more_possible": false}`+"\n" || strings.Contains(out+errOut, "PRIVATE_TOKEN") {
		t.Fatal(code, out, errOut)
	}
	if len(bodies) != 2 || strings.Contains(bodies[0], "cursor") || !strings.Contains(bodies[1], `"cursor": "c1"`) {
		t.Fatal(bodies)
	}
	if code, out, _ = runCLI(t, memorySignalsMain, "--workspace-id", "w", "--project-id", "p"); code != 0 || !strings.Contains(out, `"dry_run": true`) {
		t.Fatal("dry run:", code, out)
	}
	replies = []string{`{"computed":5,"changed":9,"next_cursor":""}`}
	bodies = nil
	if code, _, errOut = runCLI(t, memorySignalsMain, "--workspace-id", "w", "--project-id", "p", "--url", srv.URL, "--execute"); code != 2 || strings.Contains(errOut, "PRIVATE_TOKEN") {
		t.Fatal("impossible counts accepted:", code, errOut)
	}
}
