package memory

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type emptyAnalyzer struct{ countingAnalyzer }

func (a *emptyAnalyzer) Analyze(context.Context, string) (SkillAnalysis, error) {
	return SkillAnalysis{Name: "index helper"}, nil
}

func TestUnusableAnalysisReplyIsRetryable(t *testing.T) {
	f := newFixture(t)
	f.svc.Analyzer = &emptyAnalyzer{}
	in := AnalyzeSkillInput{Scope: scopeA, SkillID: "index-helper", Content: "# index helper\nVerify indexes.", Path: "/skills/index-helper/SKILL.md"}
	if _, e := f.svc.AnalyzeSkill(testCtx, f.human, in); !errors.Is(e, ErrUnavailable) {
		t.Fatalf("a valid request with an unusable model reply must be retryable, got %v", e)
	}
}

// The analysis prompt shows the model a schema, not a zero value whose lists read as null.
func TestSkillAnalysisPromptSchemaHasNoNullLists(t *testing.T) {
	schema := structuredText(jsonSchemaOf(reflect.TypeOf(SkillAnalysis{}), true))
	if strings.Contains(schema, "null") || !strings.Contains(schema, `"array"`) {
		t.Fatal(schema)
	}
}
