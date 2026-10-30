package memory

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAgentToolSwitchesFlowThroughTheHostReport(t *testing.T) {
	f := newFixture(t)
	empty, err := f.svc.AgentTools(testCtx, f.human, scopeA)
	if err != nil || empty.AgentTools == nil || len(empty.AgentTools.Servers) != 0 {
		t.Fatal("empty document", empty, err)
	}
	gh := AgentToolEntry{Name: "github", Transport: "stdio", Target: "npx @github/mcp-server", Agents: map[string]string{"claude_code": "on", "codex": "off"}}
	if _, err = f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, Servers: []AgentToolEntry{gh}}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.RequestAgentTool(testCtx, f.agent, AgentToolSwitch{Scope: scopeA, Agent: "codex", Kind: "mcp", Name: "github", Enabled: true}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent token flipped a switch", err)
	}
	first, _ := f.svc.RequestAgentTool(testCtx, f.human, AgentToolSwitch{Scope: scopeA, Agent: "codex", Kind: "mcp", Name: "github", Enabled: false})
	got, err := f.svc.RequestAgentTool(testCtx, f.human, AgentToolSwitch{Scope: scopeA, Agent: "codex", Kind: "mcp", Name: "github", Enabled: true})
	if err != nil || len(got.AgentTools.Requests) != 1 || !got.AgentTools.Requests[0].Enabled || got.AgentTools.Requests[0].ID == first.AgentTools.Requests[0].ID {
		t.Fatal("a newer switch must replace the waiting one", got.AgentTools.Requests, err)
	}
	gh.Agents["codex"] = "on"
	done, err := f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, Servers: []AgentToolEntry{gh},
		Results: []AgentToolResult{{ID: got.AgentTools.Requests[0].ID, Status: "done"}}})
	if err != nil || done.AgentTools.Requests[0].Status != "done" || done.AgentTools.Servers[0].Agents["codex"] != "on" || done.AgentTools.CollectedAt == nil {
		t.Fatal("report did not settle the switch", done.AgentTools, err)
	}
	// #466: the replaced request is gone from the list, but both switches stay counted.
	for _, kind := range []string{"agent_tool_off", "agent_tool_on"} {
		if d := requireDecision(t, f.repo, kind, "mcp"); d.Audit.Reason != gh.Name || d.Target.ID != "codex" {
			t.Fatalf("%s: %+v %+v", kind, d.Audit, d.Target)
		}
	}
}

func TestAgentToolReportsRefuseSecretsAndUnknownShapes(t *testing.T) {
	f := newFixture(t)
	leak := AgentToolEntry{Name: "slack", Transport: "http", Target: "https://user:hunter2secret@mcp.slack.com/x", Agents: map[string]string{"codex": "on"}}
	if _, err := f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, Servers: []AgentToolEntry{leak}}); !errors.Is(err, ErrSecretDetected) {
		t.Fatal("a credential reached the store", err)
	}
	for _, bad := range []AgentToolEntry{
		{Name: "x", Transport: "carrier-pigeon", Agents: map[string]string{"codex": "on"}},
		{Name: "x", Transport: "stdio", Agents: map[string]string{"windsurf": "on"}},
		{Name: "x", Transport: "stdio", Agents: map[string]string{"codex": "maybe"}},
	} {
		if _, err := f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, Servers: []AgentToolEntry{bad}}); !errors.Is(err, ErrInvalid) {
			t.Fatal("bad entry accepted", bad, err)
		}
	}
	if _, err := f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, Plugins: []AgentToolEntry{{Name: "p", Transport: "stdio", Agents: map[string]string{"claude_code": "on"}}}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("a server was listed as a plugin", err)
	}
}

func TestSkillChecksRideTheHostReportAndPersist(t *testing.T) {
	f := newFixture(t)
	score, risk := 80.2, 82
	check := SkillCheck{Agent: "claude_code", Name: "commit-and-push", Path: "~/.claude/skills/commit-and-push", QualityScore: &score, QualityGrade: "B",
		RiskScore: &risk, Verdict: "DO_NOT_INSTALL", Findings: 4, TopFindings: []string{"Prompt Injection · Instruction Override · HIGH"}, ScannedAt: time.Now().UTC()}
	checks := []SkillCheck{check}
	got, err := f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, SkillChecks: &checks, SkillCheckTools: "skillevaluator 0.4.0 · skillspector 2.12.0"})
	if err != nil || len(got.AgentTools.SkillChecks) != 1 || got.AgentTools.SkillCheckTools == "" {
		t.Fatal("skill checks not stored", got.AgentTools, err)
	}
	// The every-minute inventory report carries no checks and must not erase them.
	got, err = f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA})
	if err != nil || len(got.AgentTools.SkillChecks) != 1 || got.AgentTools.SkillChecks[0].Verdict != "DO_NOT_INSTALL" {
		t.Fatal("a report without checks dropped them", got.AgentTools, err)
	}
	for _, bad := range []SkillCheck{
		func() SkillCheck { c := check; c.Verdict = "PROBABLY_FINE"; return c }(),
		func() SkillCheck { c := check; c.QualityGrade = "S"; return c }(),
		func() SkillCheck { c := check; c.Agent = "windsurf"; return c }(),
		func() SkillCheck { c := check; c.TopFindings = []string{"a", "b", "c", "d"}; return c }(),
	} {
		list := []SkillCheck{bad}
		if _, err := f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, SkillChecks: &list}); !errors.Is(err, ErrInvalid) {
			t.Fatal("bad check accepted", bad, err)
		}
	}
}

func TestImagePromptsCreateListUpdateArchive(t *testing.T) {
	f := newFixture(t)
	p := ImagePrompt{Title: "흑백 인물 일러스트", Kind: "image", Engine: "comfyui", Prompt: "minimal line art portrait of {인물}", Size: "1024×1024", Count: 4, Seed: "무작위", ScriptName: "portrait.workflow.json", Script: `{"3":{"class_type":"KSampler"}}`, Experts: []string{"글쓰기 도우미"}}
	if _, err := f.svc.CreatePrompt(testCtx, f.agent, PromptInput{Scope: scopeA, Prompt: p}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent created a prompt", err)
	}
	bad := p
	bad.Engine = "midjourney"
	if _, err := f.svc.CreatePrompt(testCtx, f.human, PromptInput{Scope: scopeA, Prompt: bad}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown engine accepted", err)
	}
	secret := p
	secret.Script = `{"api_key": "sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	if _, err := f.svc.CreatePrompt(testCtx, f.human, PromptInput{Scope: scopeA, Prompt: secret}); !errors.Is(err, ErrSecretDetected) {
		t.Fatal("a key in a script was stored", err)
	}
	r, err := f.svc.CreatePrompt(testCtx, f.human, PromptInput{Scope: scopeA, Prompt: p})
	if err != nil {
		t.Fatal(err)
	}
	page, _ := f.svc.ListPrompts(testCtx, f.human, scopeA)
	if len(page.Items) != 1 || page.Items[0].ImagePrompt.Title != p.Title {
		t.Fatal("list", page.Items)
	}
	img := p
	img.Images = []string{"data:text/html;base64,PHNjcmlwdD4="}
	if _, err := f.svc.UpdatePrompt(testCtx, f.human, r.ID, PromptUpdate{Scope: scopeA, ExpectedRevision: r.Revision, Prompt: &img}); !errors.Is(err, ErrInvalid) {
		t.Fatal("a non-image data URL was stored", err)
	}
	// Base64 that happens to spell a key prefix is still just an image.
	p.Title, p.Engine, p.Tags = "흑백 인물 일러스트 v2", "gemini", []string{"인물"}
	p.Images = []string{"data:image/webp;base64,AIzaAAAAAAAAAAAAAAAAAAAAAAAA" + strings.Repeat("A", 1000) + "=="}
	r, err = f.svc.UpdatePrompt(testCtx, f.human, r.ID, PromptUpdate{Scope: scopeA, ExpectedRevision: r.Revision, Prompt: &p})
	if err != nil || r.ImagePrompt.Title != p.Title {
		t.Fatal("update", err)
	}
	if _, err = f.svc.UpdatePrompt(testCtx, f.human, r.ID, PromptUpdate{Scope: scopeA, ExpectedRevision: r.Revision - 1, Archive: true}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale update accepted", err)
	}
	if _, err = f.svc.UpdatePrompt(testCtx, f.human, r.ID, PromptUpdate{Scope: scopeA, ExpectedRevision: r.Revision, Archive: true}); err != nil {
		t.Fatal("archive", err)
	}
	if page, _ = f.svc.ListPrompts(testCtx, f.human, scopeA); len(page.Items) != 0 {
		t.Fatal("archived prompt still listed")
	}
}

func TestDesignSystemsCreateListUpdateArchive(t *testing.T) {
	f := newFixture(t)
	d := DesignSystem{Title: "Demo 메일", Product: "Demo", Purpose: "앱 화면", Source: "https://www.figma.com/design/abc", Colors: []DesignColor{{Name: "accent", Value: "#e4e1db"}}, Fonts: []string{"IBM Plex Sans KR"}, Guide: "버튼은 하나만 강조한다."}
	if _, err := f.svc.CreateDesignSystem(testCtx, f.agent, DesignSystemInput{Scope: scopeA, System: d}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent created a design system", err)
	}
	bad := d
	bad.Colors = []DesignColor{{Name: "", Value: "#fff"}}
	if _, err := f.svc.CreateDesignSystem(testCtx, f.human, DesignSystemInput{Scope: scopeA, System: bad}); !errors.Is(err, ErrInvalid) {
		t.Fatal("a nameless color was accepted", err)
	}
	secret := d
	secret.Guide = "token sk-ant-api03-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := f.svc.CreateDesignSystem(testCtx, f.human, DesignSystemInput{Scope: scopeA, System: secret}); !errors.Is(err, ErrSecretDetected) {
		t.Fatal("a secret was stored", err)
	}
	r, err := f.svc.CreateDesignSystem(testCtx, f.human, DesignSystemInput{Scope: scopeA, System: d})
	if err != nil {
		t.Fatal(err)
	}
	page, _ := f.svc.ListDesignSystems(testCtx, f.human, scopeA)
	if len(page.Items) != 1 || page.Items[0].DesignSystem.Product != "Demo" {
		t.Fatal("list", page.Items)
	}
	d.Purpose = "슬라이드"
	r, err = f.svc.UpdateDesignSystem(testCtx, f.human, r.ID, DesignSystemUpdate{Scope: scopeA, ExpectedRevision: r.Revision, System: &d})
	if err != nil || r.DesignSystem.Purpose != "슬라이드" {
		t.Fatal("update", r, err)
	}
	if _, err = f.svc.UpdateDesignSystem(testCtx, f.human, r.ID, DesignSystemUpdate{Scope: scopeA, ExpectedRevision: r.Revision - 1, Archive: true}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision archived", err)
	}
	if _, err = f.svc.UpdateDesignSystem(testCtx, f.human, r.ID, DesignSystemUpdate{Scope: scopeA, ExpectedRevision: r.Revision, Archive: true}); err != nil {
		t.Fatal(err)
	}
	if page, _ = f.svc.ListDesignSystems(testCtx, f.human, scopeA); len(page.Items) != 0 {
		t.Fatal("archived system still listed", page.Items)
	}
}

func TestToolDetailsPluginContentsAndSkillUpdates(t *testing.T) {
	f := newFixture(t)
	gh := AgentToolEntry{Name: "github", Transport: "stdio", Agents: map[string]string{"claude_code": "on"},
		Tools: []AgentTool{{Name: "create_pull_request", Risky: true}, {Name: "list_issues"}},
		Calls: &AgentToolCalls{Today: 3, Recent: []AgentToolCall{{Tool: "create_pull_request", OK: true, At: time.Now().UTC()}}}}
	mail := AgentToolEntry{Name: "demo-mail", Transport: "plugin", Agents: map[string]string{"claude_code": "on"}, Version: "0.3.1",
		Contents: []PluginItem{{Kind: "skill", Name: "mail-triage"}, {Kind: "command", Name: "/mail"}}}
	updates := []SkillUpdate{{Name: "release-notes", Repo: "/home/user/skills", From: "1.2.0", To: "1.3.0", Commits: 2}}
	got, err := f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, Servers: []AgentToolEntry{gh}, Plugins: []AgentToolEntry{mail}, SkillUpdates: &updates})
	if err != nil || len(got.AgentTools.Servers[0].Tools) != 2 || got.AgentTools.Plugins[0].Contents[1].Name != "/mail" || len(got.AgentTools.SkillUpdates) != 1 {
		t.Fatal("details", got.AgentTools, err)
	}
	if got, _ = f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, Servers: []AgentToolEntry{gh}}); len(got.AgentTools.SkillUpdates) != 1 {
		t.Fatal("a report without updates dropped the last list")
	}
	bad := mail
	bad.Contents = []PluginItem{{Kind: "theme", Name: "x"}}
	if _, err = f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, Plugins: []AgentToolEntry{bad}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown plugin item kind accepted", err)
	}
	if _, err = f.svc.RequestAgentTool(testCtx, f.human, AgentToolSwitch{Scope: scopeA, Agent: "shared", Kind: "skill_update", Name: "/home/user/skills"}); err != nil {
		t.Fatal("skill update request", err)
	}
	if _, err = f.svc.RequestAgentTool(testCtx, f.human, AgentToolSwitch{Scope: scopeA, Agent: "shared", Kind: "skill_update", Name: "../etc"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad repo accepted", err)
	}
}

// A server address with a port and a later "@" in another field is not user:password@host.
func TestAgentToolReportWithPortAndAtSignIsNotASecret(t *testing.T) {
	f := newFixture(t)
	in := AgentToolsReport{Scope: scopeA,
		Servers: []AgentToolEntry{{Name: "browser_tools", Transport: "http", Target: "http://127.0.0.1:8888", Agents: map[string]string{"claude_code": "on"}}},
		Plugins: []AgentToolEntry{{Name: "figma@claude-plugins-official", Transport: "plugin", Agents: map[string]string{"claude_code": "on"}}}}
	if _, err := f.svc.ReportAgentTools(testCtx, f.human, in); err != nil {
		t.Fatal("false secret", err)
	}
	for _, secret := range []string{`"url":"https://admin:hunter2@db.example.com"`, `postgresql://admin:p'ass@db.example/app`, `{"dsn":"mysql://root:p\"w@db/x"}`} {
		if !containsSecret(secret) {
			t.Fatal("a real user:password@host must still be caught", secret)
		}
	}
}

func TestPCLinksAreStoredKeptAndValidated(t *testing.T) {
	f := newFixture(t)
	seen := time.Now().UTC().Add(-time.Hour)
	links := []PCLink{{Agent: "codex", LastSeen: &seen, Pending: 2}, {Agent: "zcode", LastSeen: &seen, Failing: true}}
	got, err := f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, PCLinks: &links})
	if err != nil || len(got.AgentTools.PCLinks) != 2 || !got.AgentTools.PCLinks[1].Failing {
		t.Fatal("pc links not stored", got.AgentTools, err)
	}
	if got, _ = f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA}); len(got.AgentTools.PCLinks) != 2 {
		t.Fatal("a report without pc links dropped the last list")
	}
	none := []PCLink{}
	if got, _ = f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, PCLinks: &none}); len(got.AgentTools.PCLinks) != 0 {
		t.Fatal("an empty list should clear the pc links")
	}
	for _, bad := range [][]PCLink{{{Agent: "Codex"}}, {{Agent: "codex"}, {Agent: "codex"}}, {{Agent: "codex", Pending: -1}}} {
		bad := bad
		if _, err = f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, PCLinks: &bad}); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid pc links accepted", bad, err)
		}
	}
}

func TestAnalysisRunIsStoredKeptAndValidated(t *testing.T) {
	f := newFixture(t)
	at := time.Now().UTC()
	run := AnalysisRun{At: at, Jobs: 3, Done: 2, Waiting: 1, Reason: "session_batch_wait", LastDoneAt: &at}
	got, err := f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, AnalysisRun: &run})
	if err != nil || got.AgentTools.AnalysisRun == nil || got.AgentTools.AnalysisRun.Done != 2 || got.AgentTools.AnalysisRun.Reason != "session_batch_wait" {
		t.Fatal("analysis run not stored", got.AgentTools, err)
	}
	if got, _ = f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA}); got.AgentTools.AnalysisRun == nil {
		t.Fatal("a report without an analysis run dropped the last one")
	}
	neg := -1
	for _, bad := range []AnalysisRun{{Jobs: 1}, {At: at, Done: -1}, {At: at, Reason: " x"}, {At: at, Error: "a\nb"}, {At: at, LastDoneCandidates: &neg}} {
		bad := bad
		if _, err = f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, AnalysisRun: &bad}); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid analysis run accepted", bad, err)
		}
	}
}

func TestSkillReposAreStoredKeptAndValidated(t *testing.T) {
	f := newFixture(t)
	repos := []SkillRepo{{Repo: "~/skills/example-skill", Remote: "github.com/example/example-skill", Head: "2f3d943", Behind: 0, CheckedAt: time.Now().UTC()}}
	got, err := f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, SkillRepos: &repos})
	if err != nil || len(got.AgentTools.SkillRepos) != 1 || got.AgentTools.SkillRepos[0].Remote != "github.com/example/example-skill" {
		t.Fatal("skill repos not stored", got.AgentTools, err)
	}
	if got, _ = f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA}); len(got.AgentTools.SkillRepos) != 1 {
		t.Fatal("a report without skill repos dropped the last list")
	}
	for _, bad := range []SkillRepo{
		{Repo: "~/x", Remote: "user:token@github.com/o/r", CheckedAt: time.Now().UTC()},
		{Repo: "../x", Remote: "github.com/o/r", CheckedAt: time.Now().UTC()},
		{Repo: "~/x", Remote: "github.com/o/r", Behind: -1, CheckedAt: time.Now().UTC()},
		{Repo: "~/x", Remote: "github.com/o/r"},
	} {
		list := []SkillRepo{bad}
		if _, err = f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, SkillRepos: &list}); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid skill repo accepted", bad, err)
		}
	}
}

func TestSkillCheckRequestIsAcceptedLikeAnUpdateRequest(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.RequestAgentTool(testCtx, f.human, AgentToolSwitch{Scope: scopeA, Agent: "shared", Kind: "skill_check", Name: "~/skills/example-skill"}); err != nil {
		t.Fatal("skill check request", err)
	}
	for _, bad := range []AgentToolSwitch{
		{Scope: scopeA, Agent: "codex", Kind: "skill_check", Name: "~/x"},
		{Scope: scopeA, Agent: "shared", Kind: "skill_check", Name: "../x"},
		{Scope: scopeA, Agent: "shared", Kind: "skill_check", Name: "~/x", URL: "https://example.org"},
	} {
		if _, err := f.svc.RequestAgentTool(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid skill check accepted", bad, err)
		}
	}
}

func TestAgentFoldersAcceptWindowsPaths(t *testing.T) {
	for _, d := range []string{"~/team/skills", "/srv/skills", `C:\Users\me\skills`, "D:/team/skills", `\\nas\team`} {
		if err := validAgentToolSwitch(AgentToolSwitch{Agent: "shared", Kind: "shared", Name: d, Enabled: true}); err != nil {
			t.Errorf("shared %q: %v", d, err)
		}
		if err := validAgentToolSwitch(AgentToolSwitch{Agent: "my-tool", Kind: "agent", Name: "My Tool", Enabled: true, SkillsDir: d, ChatsDir: d}); err != nil {
			t.Errorf("agent %q: %v", d, err)
		}
	}
	for _, d := range []string{"relative", "C:x", `C:\a\..\b`, "C:/a/../b", `\\\x`, "../x", "~/a\nb"} {
		if err := validAgentToolSwitch(AgentToolSwitch{Agent: "shared", Kind: "shared", Name: d, Enabled: true}); !errors.Is(err, ErrInvalid) {
			t.Errorf("shared %q must be refused, got %v", d, err)
		}
	}
}

// The host's memory_use window (internal/host/skills/agents.go) must pass the strict
// decoder: an unknown field turned every agents sync into a 400 and stalled installs.
func TestHostMemoryUseReportDecodesAndIsKept(t *testing.T) {
	body := `{"scope":{"workspace_id":"` + scopeA.WorkspaceID + `","project_id":"` + scopeA.ProjectID + `"},"servers":[],"plugins":[],"results":[],
		"memory_use":{"days":7,"sessions":2,"injections":1,"used":2,"estimated":1,"unused_after":5,"inject_sessions":2,"inject_chars":900,
		"items":[{"id":"m1","injected":1,"used":2,"estimated":1,"unreceipted":0,"unused":false},{"id":"m2","injected":0,"used":0,"estimated":0,"unreceipted":6,"unused":true}]}}`
	d := json.NewDecoder(strings.NewReader(body))
	d.DisallowUnknownFields()
	var in AgentToolsReport
	if err := d.Decode(&in); err != nil {
		t.Fatal("host report refused by the strict decoder", err)
	}
	f := newFixture(t)
	got, err := f.svc.ReportAgentTools(testCtx, f.human, in)
	if err != nil {
		t.Fatal("host report refused", err)
	}
	u := got.AgentTools.MemoryUse
	if u == nil || u.Estimated != 1 || u.UnusedAfter != 5 || len(u.Items) != 2 || u.Items[0].Used != 2 || !u.Items[1].Unused || u.Items[1].Unreceipted != 6 {
		t.Fatal("memory use not kept", u)
	}
}
