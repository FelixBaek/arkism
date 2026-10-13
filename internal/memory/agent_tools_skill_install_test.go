package memory

import (
	"errors"
	"testing"
)

func TestSkillInstallRequestsAreValidatedAndCarryTheHostDetail(t *testing.T) {
	f := newFixture(t)
	preview, err := f.svc.RequestAgentTool(testCtx, f.human, AgentToolSwitch{Scope: scopeA, Agent: "shared", Kind: "skill_install", Name: "anthropics/skills/skills/pdf"})
	if err != nil {
		t.Fatal("preview request", err)
	}
	if _, err := f.svc.RequestAgentTool(testCtx, f.human, AgentToolSwitch{Scope: scopeA, Agent: "shared", Kind: "skill_install", Enabled: true, Update: true,
		Name: "anthropics/skills/skills/pdf@0123456789abcdef0123456789abcdef01234567", Targets: []string{"claude_code", "agents"}}); err != nil {
		t.Fatal("install request", err)
	}
	for _, bad := range []AgentToolSwitch{
		{Scope: scopeA, Agent: "codex", Kind: "skill_install", Name: "o/r"},
		{Scope: scopeA, Agent: "shared", Kind: "skill_install", Name: "o/r/../x"},
		{Scope: scopeA, Agent: "shared", Kind: "skill_install", Name: "o"},
		{Scope: scopeA, Agent: "shared", Kind: "skill_install", Name: "o/r; rm -rf"},
		{Scope: scopeA, Agent: "shared", Kind: "skill_install", Name: "o/r", Enabled: true},                                      // install with no target
		{Scope: scopeA, Agent: "shared", Kind: "skill_install", Name: "o/r", Targets: []string{"codex"}},                         // targets on a preview
		{Scope: scopeA, Agent: "shared", Kind: "skill_install", Name: "o/r", Enabled: true, Targets: []string{"cursor"}},         // unknown folder
		{Scope: scopeA, Agent: "shared", Kind: "skill_install", Name: "o/r", Enabled: true, Targets: []string{"codex", "codex"}}, // duplicate
		{Scope: scopeA, Agent: "shared", Kind: "skill_install", Name: "o/r", URL: "https://github.com/o/r"},
		{Scope: scopeA, Agent: "codex", Kind: "mcp", Name: "x", Targets: []string{"codex"}}, // only skill_install takes targets
		{Scope: scopeA, Agent: "codex", Kind: "mcp", Name: "x", Update: true},
	} {
		if _, err := f.svc.RequestAgentTool(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid skill install accepted", bad, err)
		}
	}
	id := preview.AgentTools.Requests[0].ID
	detail := `{"repo":"anthropics/skills","commit":"0123456789abcdef0123456789abcdef01234567","skills":[{"name":"pdf","files":3,"bytes":1200}]}`
	done, err := f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, Results: []AgentToolResult{{ID: id, Status: "done", Detail: detail}}})
	if err != nil || done.AgentTools.Requests[0].Detail != detail || done.AgentTools.Requests[1].Targets[1] != "agents" || !done.AgentTools.Requests[1].Update {
		t.Fatal("detail not settled", done.AgentTools, err)
	}
	long := make([]byte, 8001)
	if _, err := f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, Results: []AgentToolResult{{ID: id, Status: "done", Detail: string(long)}}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversized detail accepted", err)
	}
}
