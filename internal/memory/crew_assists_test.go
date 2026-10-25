package memory

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCrewKit(t *testing.T) {
	e := crewTemplate("릴리스 매니저")
	e.SkillRules = []CrewSkillRule{{Field: "name", Value: "release-*"}, {Field: "category", Value: "Git·릴리스"}}
	e.SkillsOff, e.FileName, e.Install = []string{"changelog"}, "release-manager", []string{"claude_code", "codex"}
	e.Memory = map[string]string{"user": "all", "procedural": "category", "lesson": "off"}
	e.Scripts = []CrewScript{{Name: "bump-version.sh", Lang: "bash", Code: "npm version patch"}}
	if !validCrew(e) {
		t.Fatal("full template refused")
	}
	for _, bad := range []func(*Crew){
		func(c *Crew) { c.Install = []string{"gemini"} },
		func(c *Crew) { c.FileName = "Release Manager" },
		func(c *Crew) { c.Memory = map[string]string{"user": "everything"} },
		func(c *Crew) { c.SkillRules = []CrewSkillRule{{Field: "tag", Value: "x"}} },
		func(c *Crew) { c.Scripts = []CrewScript{{Name: "x.sh", Lang: "ruby", Code: "1"}} },
	} {
		c := e
		bad(&c)
		if validCrew(c) {
			t.Fatal("bad template accepted", c)
		}
	}
}

func TestCrewTemplateAndPersona(t *testing.T) {
	tpl := Crew{Type: "template", Name: "문서 편집자", Category: "글쓰기·문서", Instructions: "문서를 고친다"}
	if !validCrew(tpl) {
		t.Fatal("template refused")
	}
	if tpl.Category = ""; validCrew(tpl) {
		t.Fatal("template without a category accepted")
	}
	p := Crew{Type: "persona", Name: "전문가 설계자", Role: "전문가 편집", Instructions: "너는 전문가 설계자다."}
	if !validCrew(p) {
		t.Fatal("persona refused")
	}
	if p.Handle = "설계자"; validCrew(p) {
		t.Fatal("persona with a $handle accepted")
	}
}

// A harness (agent token) may call an @expert (try) and nothing else.
func TestHarnessMayOnlyAskAnExpert(t *testing.T) {
	f, _ := crewRunFixture(t)
	f.auth.actors["tok-harness"] = actor("harness-1", "agent", true, "read", "write")
	fill := CrewAssistInput{Scope: scopeA, Assist: CrewAssist{Kind: "fill", Persona: "p", Prompt: "문서 고치는 전문가"}}
	if _, err := f.svc.CreateCrewAssist(testCtx, "tok-harness", fill); !errors.Is(err, ErrDenied) {
		t.Fatal("a harness asked for fill", err)
	}
	try := CrewAssistInput{Scope: scopeA, Assist: CrewAssist{Kind: "try", Expert: "@리뷰어", Prompt: "봐 줘"}}
	if r, err := f.svc.CreateCrewAssist(testCtx, "tok-harness", try); err != nil || r.CreatedByType != "agent" {
		t.Fatal("a harness could not call an expert", r, err)
	}
}

func TestCrewAssistLifecycle(t *testing.T) {
	f, _ := crewRunFixture(t)
	in := CrewAssistInput{Scope: scopeA, Assist: CrewAssist{Kind: "fill", Persona: "너는 전문가 설계자다.", Prompt: "문서 고치는 전문가"}}
	if _, err := f.svc.CreateCrewAssist(testCtx, tokEngine, in); !errors.Is(err, ErrDenied) {
		t.Fatal("the runner asked for help", err)
	}
	bad := in
	bad.Assist.Kind = "try"
	if _, err := f.svc.CreateCrewAssist(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
		t.Fatal("try without an expert", err)
	}
	r, err := f.svc.CreateCrewAssist(testCtx, f.human, in)
	if err != nil || r.CrewAssist.Status != "pending" {
		t.Fatal("create", r, err)
	}
	page, err := f.svc.PendingCrewAssists(testCtx, tokEngine, scopeA, false)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != r.ID {
		t.Fatal("queue", page.Items, err)
	}
	if _, err = f.svc.FinishCrewAssist(testCtx, f.human, r.ID, CrewAssistResult{Scope: scopeA, Status: "done", Output: "x"}); !errors.Is(err, ErrDenied) {
		t.Fatal("a person answered for the runner", err)
	}
	done, err := f.svc.FinishCrewAssist(testCtx, tokEngine, r.ID, CrewAssistResult{Scope: scopeA, Status: "done", Output: `{"name":"문서 편집자"}`, Model: "sonnet 5.5"})
	if err != nil || done.CrewAssist.Status != "done" || done.CrewAssist.Prompt != in.Assist.Prompt {
		t.Fatal("finish", done, err)
	}
	if _, err = f.svc.FinishCrewAssist(testCtx, tokEngine, r.ID, CrewAssistResult{Scope: scopeA, Status: "failed"}); !errors.Is(err, ErrConflict) {
		t.Fatal("answered twice", err)
	}
	if page, _ = f.svc.PendingCrewAssists(testCtx, tokEngine, scopeA, false); len(page.Items) != 0 {
		t.Fatal("answered request still queued")
	}
	got, err := f.svc.GetCrewAssist(testCtx, f.human, scopeA, r.ID)
	if err != nil || got.CrewAssist.Output != `{"name":"문서 편집자"}` {
		t.Fatal("get", got, err)
	}
	// A request may name the model it wants; a malformed name is refused.
	asked := in
	asked.Assist.Model = "opus 5.5"
	if r, err = f.svc.CreateCrewAssist(testCtx, f.human, asked); err != nil || r.CrewAssist.Model != "opus 5.5" {
		t.Fatal("asked model", r, err)
	}
	asked.Assist.Model = "opus/5.5;rm"
	if _, err = f.svc.CreateCrewAssist(testCtx, f.human, asked); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad model name", err)
	}
}

func TestCrewAssistExpires(t *testing.T) {
	old := Record{CreatedAt: time.Now().Add(-crewAssistTTL - time.Minute), CrewAssist: &CrewAssist{Status: "pending"}}
	if expireAssist(old, time.Now()).CrewAssist.Status != "failed" {
		t.Fatal("an unanswered request never expires")
	}
}

func TestCrewInstallEnvelopeBoundary(t *testing.T) {
	for _, body := range []string{strings.Repeat("가", 20000), strings.Repeat("\x00", 20000), strings.Repeat("\"\n", 10000)} {
		prompt, err := json.Marshal(map[string]any{"name": "imported-draft", "content": body, "agents": []string{"claude_code", "codex", "gemini", "zcode"}})
		if err != nil || !validCrewAssist(CrewAssist{Kind: "install_skill", Prompt: string(prompt)}) {
			t.Fatal("maximum body envelope refused", err)
		}
	}
	if validCrewAssist(CrewAssist{Kind: "install_skill", Prompt: strings.Repeat("x", 120513)}) || validCrewAssist(CrewAssist{Kind: "draft", Prompt: strings.Repeat("x", 20001)}) {
		t.Fatal("input bound bypassed")
	}
}

// An external runner's accepted try is reported running (with its trace), stays in the runner's queue only when asked,
// outlives the ten-minute TTL, and still takes exactly one answer.
func TestCrewAssistRunningByExternalRunner(t *testing.T) {
	f, _ := crewRunFixture(t)
	r, err := f.svc.CreateCrewAssist(testCtx, f.human, CrewAssistInput{Scope: scopeA, Assist: CrewAssist{Kind: "try", Expert: "@리서치", Prompt: "조사해 줘",
		Trace: &CrewAssistTrace{Backend: "forged"}}})
	if err != nil || r.CrewAssist.Trace != nil {
		t.Fatal("a caller set the trace", r.CrewAssist, err)
	}
	trace := &CrewAssistTrace{Backend: "lab", TaskID: "t-1", State: "accepted", RequestedModel: "sonnet 5.5", NotApplied: []string{"model"}, Usage: "unknown"}
	if _, err = f.svc.FinishCrewAssist(testCtx, tokEngine, r.ID, CrewAssistResult{Scope: scopeA, Status: "running"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("running without a trace", err)
	}
	if _, err = f.svc.FinishCrewAssist(testCtx, tokEngine, r.ID, CrewAssistResult{Scope: scopeA, Status: "running", Trace: &CrewAssistTrace{Backend: "lab", State: "bogus"}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown trace state", err)
	}
	run, err := f.svc.FinishCrewAssist(testCtx, tokEngine, r.ID, CrewAssistResult{Scope: scopeA, Status: "running", Trace: trace})
	if err != nil || run.CrewAssist.Status != "running" || run.CrewAssist.Trace.TaskID != "t-1" {
		t.Fatal("accepted", run.CrewAssist, err)
	}
	if page, _ := f.svc.PendingCrewAssists(testCtx, tokEngine, scopeA, false); len(page.Items) != 0 {
		t.Fatal("an old runner would see a running assist and run it again")
	}
	if page, _ := f.svc.PendingCrewAssists(testCtx, tokEngine, scopeA, true); len(page.Items) != 1 {
		t.Fatal("a restarted runner cannot find the accepted assist")
	}
	run.CreatedAt = time.Now().Add(-time.Hour)
	if expireAssist(run, time.Now()).CrewAssist.Status != "running" {
		t.Fatal("an accepted task expired after the pending TTL")
	}
	run.UpdatedAt = time.Now().Add(-crewAssistRunningTTL - time.Minute)
	if expireAssist(run, time.Now()).CrewAssist.Status != "failed" {
		t.Fatal("a silent runner keeps the assist running for ever")
	}
	done, err := f.svc.FinishCrewAssist(testCtx, tokEngine, r.ID, CrewAssistResult{Scope: scopeA, Status: "done", Output: "결과", Trace: &CrewAssistTrace{Backend: "lab", TaskID: "t-1", State: "succeeded"}})
	if err != nil || done.CrewAssist.Status != "done" || done.CrewAssist.Trace.State != "succeeded" {
		t.Fatal("answer", done.CrewAssist, err)
	}
	if _, err = f.svc.FinishCrewAssist(testCtx, tokEngine, r.ID, CrewAssistResult{Scope: scopeA, Status: "running", Trace: trace}); !errors.Is(err, ErrConflict) {
		t.Fatal("a late progress report reopened an answered assist", err)
	}
	// Only try assists run on an external runner.
	fill, _ := f.svc.CreateCrewAssist(testCtx, f.human, CrewAssistInput{Scope: scopeA, Assist: CrewAssist{Kind: "fill", Persona: "p", Prompt: "x"}})
	if _, err = f.svc.FinishCrewAssist(testCtx, tokEngine, fill.ID, CrewAssistResult{Scope: scopeA, Status: "running", Trace: trace}); !errors.Is(err, ErrConflict) {
		t.Fatal("a fill assist went running", err)
	}
}

func TestCrewAgentBackendField(t *testing.T) {
	a := Crew{Type: "agent", Name: "리서치", Handle: "리서치", Instructions: "조사한다", Model: "sonnet 5.5", Backend: "lab-runner"}
	if !validCrew(a) {
		t.Fatal("agent with a backend refused")
	}
	for _, bad := range []string{"Lab", "lab runner", "-x", "https://x", strings.Repeat("a", 41)} {
		if a.Backend = bad; validCrew(a) {
			t.Fatal("bad backend name accepted", bad)
		}
	}
	p := Crew{Type: "persona", Name: "p", Instructions: "x", Backend: "lab"}
	if validCrew(p) {
		t.Fatal("a persona took a backend")
	}
}

// A workflow step's node keeps the external runner's trace under the same rules as an @call's: names and ids only.
func TestCrewRunNodeBackendTrace(t *testing.T) {
	n := CrewRunNode{Status: "done", Backend: &CrewAssistTrace{Backend: "lab-runner", TaskID: "t-1", State: "succeeded", NotApplied: []string{"cwd"}, Usage: "unknown"}}
	if !validCrewRunNode(n) {
		t.Fatal("a node with a trace refused")
	}
	for _, bad := range []CrewAssistTrace{{Backend: ""}, {Backend: "x", State: "done"}, {Backend: "x", Model: "a\nb"}, {Backend: "x", Usage: "12"}} {
		n.Backend = &bad
		if validCrewRunNode(n) {
			t.Fatalf("bad trace accepted %+v", bad)
		}
	}
}
