package memory

import (
	"encoding/base64"
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

func TestSkillInstallZipRequestsAreCappedAndCleared(t *testing.T) {
	f := newFixture(t)
	zip := base64.StdEncoding.EncodeToString([]byte("PK\x03\x04 zip bytes"))
	first, err := f.svc.RequestAgentTool(testCtx, f.human, AgentToolSwitch{Scope: scopeA, Agent: "shared", Kind: "skill_install", Name: "pdf.zip", Zip: zip})
	if err != nil {
		t.Fatal("zip preview", err)
	}
	// A second zip replaces the one still waiting: the record holds one upload at most.
	second, err := f.svc.RequestAgentTool(testCtx, f.human, AgentToolSwitch{Scope: scopeA, Agent: "shared", Kind: "skill_install", Name: "docx.zip", Zip: zip, Enabled: true, Targets: []string{"codex"}})
	if err != nil || len(second.AgentTools.Requests) != 1 || second.AgentTools.Requests[0].Name != "docx.zip" || first.AgentTools.Requests[0].Zip != zip {
		t.Fatal("zip not replaced", second.AgentTools, err)
	}
	big := base64.StdEncoding.EncodeToString(make([]byte, skillZipMaxBytes+1))
	for _, bad := range []AgentToolSwitch{
		{Scope: scopeA, Agent: "shared", Kind: "skill_install", Name: "big.zip", Zip: big},
		{Scope: scopeA, Agent: "shared", Kind: "skill_install", Name: "bad.zip", Zip: "not base64!"},
		{Scope: scopeA, Agent: "shared", Kind: "skill_install", Name: " pad.zip", Zip: zip},
		{Scope: scopeA, Agent: "codex", Kind: "mcp", Name: "x", Zip: zip}, // only skill_install takes a zip
	} {
		if _, err := f.svc.RequestAgentTool(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid zip accepted", bad.Name, err)
		}
	}
	done, err := f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, Results: []AgentToolResult{{ID: second.AgentTools.Requests[0].ID, Status: "done", Detail: `{"installed":["~/.codex/skills/docx"]}`}}})
	if err != nil || done.AgentTools.Requests[0].Zip != "" || done.AgentTools.Requests[0].Status != "done" {
		t.Fatal("zip kept after it was applied", done.AgentTools, err)
	}
}
