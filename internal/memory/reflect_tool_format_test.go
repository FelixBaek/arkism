//go:build linux

package memory

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReflectMixedToolEnvelopeRepairsWithinIterationBudget(t *testing.T) {
	f, llm := reflectFixture(t)
	m := f.activeMemory("bravo supported detail", "bravo")
	mixed := "I will check. " + toolCall("recall", map[string]any{"query": "never-execute-this"})
	llm.answers = []string{mixed, toolCall("recall", map[string]any{"query": "bravo"}), "Supported detail [" + m.ID + "]"}
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "alpha", MaxIterations: 3})
	if err != nil || out.Iterations != 3 || len(llm.prompts) != 3 {
		t.Fatalf("repair failed or exceeded budget: %v %+v", err, out)
	}
	if len(out.ToolTrace) != 1 || out.ToolTrace[0].Args.Query != "bravo" {
		t.Fatalf("extracted mixed request executed: %+v", out.ToolTrace)
	}
	if !strings.Contains(llm.prompts[1], "no tool was executed") {
		t.Fatal("repair instruction missing")
	}
	if n, _ := f.svc.dailyModelCalls(testCtx, projectScope(scopeA), reflectOperation, time.Now()); n != 3 {
		t.Fatalf("unexpected billed calls: %d", n)
	}
}

func TestReflectFinalMixedToolEnvelopeIsNotAnAnswer(t *testing.T) {
	f, llm := reflectFixture(t)
	llm.answers = []string{"I will check. " + toolCall("recall", map[string]any{"query": "secret"})}
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "alpha", MaxIterations: 1})
	if !errors.Is(err, errModelAnswer) || out.Answer != "" || len(out.ToolTrace) != 0 || len(llm.prompts) != 1 {
		t.Fatalf("final mixed envelope escaped or spent extra calls: %v %+v", err, out)
	}
}
