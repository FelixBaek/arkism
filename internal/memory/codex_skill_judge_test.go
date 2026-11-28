package memory

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

type routingTestModel struct{ reply string }

func (m routingTestModel) Generator() Generator { return Generator{Model: "synthetic"} }
func (m routingTestModel) Complete(context.Context, string, string, int) (string, ProviderUsage, error) {
	return m.reply, ProviderUsage{Requests: 1}, nil
}
func TestCodexSkillRouting(t *testing.T) {
	items := []SkillCandidate{{ID: "first", Name: "first"}, {ID: "second", Name: "second"}}
	v := `[{"id":"first","suitability":2,"confidence":0.9,"conflict":false},{"id":"second","suitability":2,"confidence":0.8,"conflict":false,"explicitly_requested":true}]`
	j := CodexSkillJudge{Model: routingTestModel{v}}
	pick, err := j.Judge(t.Context(), "Use second.", items)
	if err != nil || pick.SelectedID != "second" || len(pick.Judgments) != 2 {
		t.Fatal(pick, err)
	}
	for _, bad := range []string{"not json", strings.Replace(v, "second", "first", 1), strings.Replace(v, "first", "omitted", 1), strings.Replace(v, `"suitability":2`, `"suitability":4`, 1)} {
		j.Model = routingTestModel{bad}
		if _, err = j.Judge(t.Context(), "select", items); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
}
func TestDesktopCaptureReadiness(t *testing.T) {
	f := newFixture(t)
	f.auth.actors[f.work] = actor("pc-capture", "service", true, allVerbs...)
	f.svc.CaptureRuntimeID = strings.Repeat("a", 64)
	h := Handler(f.svc)
	path := "/v1/capture/readiness?workspace_id=" + scopeA.WorkspaceID + "&project_id=" + scopeA.ProjectID
	for _, c := range []struct {
		token, path string
		code        int
	}{{f.work, path, 200}, {f.work, path + "&agent_id=local", 200}, {f.agent, path, 403}, {f.work, path + "&unknown=1", 400}} {
		req := httptest.NewRequest("GET", c.path, nil)
		req.Header.Set("Authorization", "Bearer "+c.token)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code != c.code || (out.Code == 200 && !strings.Contains(out.Body.String(), f.svc.CaptureRuntimeID)) {
			t.Fatalf("%d %s", out.Code, out.Body.String())
		}
	}
}
