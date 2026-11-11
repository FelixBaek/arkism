//go:build linux

package memory

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type passageTransferDeny struct {
	Authorizer
	id string
}

func (a passageTransferDeny) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	if in.ResourceID == a.id && strings.HasPrefix(in.Context.Destination, "external:") {
		return Decision{}, ErrDenied
	}
	return a.Authorizer.Authorize(ctx, token, in)
}

type passageChangingLLM struct {
	fakeWikiLLM
	change func()
}

func (m *passageChangingLLM) Complete(ctx context.Context, system, user string, n int) (string, ProviderUsage, error) {
	text, u, e := m.fakeWikiLLM.Complete(ctx, system, user, n)
	m.change()
	return text, u, e
}
func TestReflectPassagesRespectRecorderFilters(t *testing.T) {
	f, _, _ := rereadFixture(t)
	f.svc.ReflectPassages = 10
	conversationBatch(f, "needle forbidden-recorder", captureTurnsRef+"filtered", 0, func(r *Record) { r.CreatedBy = "other"; r.Sensitivity = "internal" })
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", CreatedBy: []string{"wanted"}, PromptOnly: true})
	if err != nil || out.Prompt == nil || strings.Contains(out.Prompt.User, "forbidden-recorder") {
		t.Fatal("recorder filter bypass", err, out.Prompt)
	}
}
func TestReflectPassagesRespectSourceTransferPolicy(t *testing.T) {
	f, llm, _ := rereadFixture(t)
	f.svc.ReflectPassages = 10
	r := conversationBatch(f, "needle transfer-forbidden", captureTurnsRef+"denied", 0, func(r *Record) { r.Sensitivity = "private" })
	f.svc.Auth = passageTransferDeny{Authorizer: f.svc.Auth, id: r.ID}
	llm.answers = []string{"ok"}
	_, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", MaxIterations: 1})
	if err != nil || len(llm.prompts) != 1 || strings.Contains(llm.prompts[0], "transfer-forbidden") {
		t.Fatal("source transfer bypass", err, llm.prompts)
	}
}
func TestReflectPassagesRejectSourceErasedDuringAnswer(t *testing.T) {
	f, _, _ := rereadFixture(t)
	f.svc.ReflectPassages = 10
	r := conversationBatch(f, "needle erased-during-answer", captureTurnsRef+"erased", 0, func(r *Record) { r.Sensitivity = "internal" })
	m := &passageChangingLLM{fakeWikiLLM: fakeWikiLLM{answers: []string{"erased-during-answer"}}, change: func() { f.editRecord("sources", r.ID, func(r *Record) { r.ContentErased = true }) }}
	f.svc.Wiki = m
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", MaxIterations: 1})
	if err == nil || out.Answer != "" {
		t.Fatal("erased source delivered", err, out.Answer)
	}
}

// A slow optional lookup must not turn an otherwise usable answer into an error.
type passageSlowSources struct {
	Repository
	ref string
}

func (r passageSlowSources) ListSources(ctx context.Context, in SourceListInput) ([]Record, error) {
	if in.SourceRef == r.ref {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return r.Repository.ListSources(ctx, in)
}
func (r passageSlowSources) SourceMetadata(ctx context.Context, scope Scope, ids []string, text bool) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.Repository.SourceMetadata(ctx, scope, ids, text)
}

func TestReflectRereadLookupDeadlineFallsBack(t *testing.T) {
	f, _, _ := rereadFixture(t)
	f.svc.ReflectReread = &fakeWikiLLM{answers: []string{"NONE"}}
	f.svc.Repo = passageSlowSources{Repository: f.svc.Repo, ref: captureTurnsRef + "ev2"}
	run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "needle", PromptOnly: true}, hits: []Hit{{Tags: []string{"event_id:ev1", "event_id:ev2"}}}}
	ctx, cancel := context.WithTimeout(testCtx, reflectPromptReserve+40*time.Millisecond)
	defer cancel()
	_, err := f.svc.reflectReread(ctx, f.human, run, reflectPromptReserve)
	if err != nil || ctx.Err() != nil || len(run.sourceLines) > 0 {
		t.Fatal("optional lookup broke answer", err, ctx.Err())
	}
}
func TestReflectPassagesCombinedPromptBudget(t *testing.T) {
	run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "needle", MaxIterations: 1}, hits: []Hit{{ID: "m", Text: strings.Repeat("m", 10000)}}, sourceLines: []string{strings.Repeat("s", 65000)}, passages: []string{strings.Repeat("p", 30000)}}
	run.boundSources()
	a, b := reflectPrompt(run, false)
	if utf8.RuneCountInString(a+b)/3 > reflectMaxPromptTokens || len(run.passages) != 0 || len(run.sourceLines) != 1 || len(run.sourceNotes) == 0 {
		t.Fatal("combined budget or fallback", len(a+b), len(run.passages), len(run.sourceLines))
	}
}
func TestReflectPassagesOffKeepsOriginalRulesAndVersion(t *testing.T) {
	f, llm, _ := rereadFixture(t)
	llm.answers = []string{"ok"}
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", MaxIterations: 1})
	if err != nil || out.Generator.PromptVersion != "reflect-v3" || strings.Contains(llm.prompts[0], "gather every item") {
		t.Fatal("default changed", err, out.Generator)
	}
}
func TestReflectPassagesFilterEveryTurn(t *testing.T) {
	f, _, _ := rereadFixture(t)
	now := time.Now().UTC()
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	in := ReflectInput{Scope: scopeA, Query: "needle", Tags: []string{"allowed"}, TagsMatch: "all_strict", CreatedBy: []string{"owner"}, CreatedFrom: &from, CreatedTo: &to}
	run := &reflectRun{in: in}
	good := conversationBatch(f, "needle keep", captureTurnsRef+"constraints", 0, func(r *Record) {
		r.CreatedAt = now
		r.CreatedBy = "owner"
		r.Tags = []string{"allowed"}
		r.Sensitivity = "internal"
	})
	rows := []Record{good}
	for _, change := range []func(*Record){func(r *Record) { r.Tags = []string{"other"} }, func(r *Record) { r.CreatedBy = "other" }, func(r *Record) { r.CreatedAt = now.Add(-2 * time.Hour) }, func(r *Record) { r.Sensitivity = "restricted" }, func(r *Record) { r.Sensitivity = "" }, func(r *Record) { r.ContentErased = true }} {
		r := good
		r.ID = ID()
		r.Text = "needle drop"
		change(&r)
		if err := f.repo.Insert(testCtx, "sources", r); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}
	lines, records := f.svc.turnLines(testCtx, f.human, run, rows, true)
	if len(lines) != 1 || lines[0] != "needle keep" || len(records) != 1 || records[0].ID != good.ID {
		t.Fatal("turn filter", lines, len(records))
	}
}
