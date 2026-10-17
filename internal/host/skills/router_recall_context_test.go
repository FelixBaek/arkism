package skills

import (
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestRecallPreservesMemoryContextAndDegradation(t *testing.T) {
	for _, mode := range []string{"id", "query", "relation"} {
		t.Run(mode, func(t *testing.T) {
			swapRouter(t)
			context := `{"domains":["work"],"applicability":"release only","evidence_kind":"user_statement","semantic_kind":"fact","uncertainty":"needs confirmation"}`
			memory := `{"id":"m","revision":1,"kind":"decision","status":"active","approved_by":"human","text":"decision","memory_context":` + context + `}`
			var out host.Obj
			var err error
			if mode == "id" {
				fakeRequest(t, memory)
				out, err = recall("", []any{"m"}, nil)
			} else {
				fakeRequest(t, `{"items":[`+memory+`],"degraded":["vector_unavailable"]}`)
				var anchor any
				if mode == "relation" {
					anchor = j(t, `{"collection":"memories","id":"anchor","revision":1}`)
				}
				out, err = recall("decision", nil, anchor)
			}
			if err != nil {
				t.Fatal(err)
			}
			m := asList(field(out, "memories"))[0].(host.Obj)
			if canon(t, field(m, "memory_context")) != canon(t, j(t, context)) {
				t.Fatal("memory conditions lost", m)
			}
			if mode != "id" && canon(t, field(out, "degraded")) != `["vector_unavailable"]` {
				t.Fatal("degradation lost", out)
			}
		})
	}
}

func TestRecallEmptyDegradedIsNotUnqualifiedNoResult(t *testing.T) {
	swapRouter(t)
	fakeRequest(t, `{"items":[],"degraded":["vector_unavailable"]}`)
	out, err := recall("decision", nil, nil)
	if err != nil || canon(t, field(out, "degraded")) != `["vector_unavailable"]` {
		t.Fatal(err, out)
	}
}

func TestRecallPreservesVerifiedSemanticEvidence(t *testing.T) {
	swapRouter(t)
	t.Setenv("ARKISM_RECALL_MIN_SCORE", "")
	match := `{"space":"test-space","distance":0.3,"max_distance":0.38}`
	fakeRequest(t, `{"items":[{"id":"semantic","revision":1,"kind":"procedural","text":"verified related memory","score":0.016393,"lanes":["vector"],"vector_match":`+match+`}]}`)
	out, err := recall("a different expression", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := asList(field(out, "memories"))
	if len(items) != 1 || canon(t, field(items[0].(host.Obj), "vector_match")) != canon(t, j(t, match)) {
		t.Fatalf("semantic evidence lost across MCP: %+v", out)
	}
}
