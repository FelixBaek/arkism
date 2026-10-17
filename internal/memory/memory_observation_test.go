package memory

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestMemoryOutcomeDoesNotTreatUnobservedAsUnused(t *testing.T) {
	f, m, _ := outcomeFixture(t)
	comparisonRows(t, f.repo, scopeA, m.ID)
	putOutcome(t, f.repo, scopeA, "unknown-tenth", "unknown-tenth", "other-mem", "work_task", true)
	out, err := f.svc.CompareMemoryOutcomes(testCtx, f.human, comparisonQuery(m.ID))
	if err != nil {
		t.Fatal(err)
	}
	if out.NotUsed.Sessions != 0 || out.NotUsed.PassRate != nil || out.Difference != nil {
		t.Fatalf("missing exposure evidence became non-use or an effect estimate: %+v", out)
	}
}

func TestPostgresMemoryComparisonCountsSessionChunksOnce(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	f.auth.actors["session-producer"] = actor("session-producer", roleService, true, allVerbs...)
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("chunk-%d", i)
		_, err := f.svc.IngestCompletedTrace(testCtx, "session-producer", TraceIngestInput{
			Scope: scopeA, TraceID: id, ThreadID: "same-session",
			AgentRun: CompletedAgentRunInput{AgentID: "codex", TaskType: "agent_session", Outcome: "partial", StartedAt: start, FinishedAt: start.Add(time.Second), Text: "session"},
			Events:   []TraceEventInput{{ID: "event-" + id, Kind: "context", OccurredAt: start, Payload: json.RawMessage(`{"retrieved":[]}`)}},
		})
		if err != nil {
			t.Fatal(err)
		}
		putOutcome(t, p, scopeA, "outcome-"+id, id, "mem-1", "work_task", i == 0)
	}
	counts, err := p.MemoryOutcomeSessionCounts(testCtx, comparisonQuery("mem-1"))
	if err != nil || len(counts) != 1 || counts[0].Sessions != 1 || counts[0].Passed != 0 {
		t.Fatalf("one session's two chunks counted as different sessions: %+v %v", counts, err)
	}
}

func TestPostgresMemoryComparisonSeparatesDeliveryFromReturn(t *testing.T) {
	for _, mode := range []string{"confirmed", "mixed_known", "mixed_unknown", "wrong_revision", "legacy", "erased", "quarantined", "quarantined_return", "quarantined_trace", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			p := isolatedPostgres(t)
			f := newFixture(t)
			f.svc.Repo = p
			f.auth.actors["session-producer"] = actor("session-producer", roleService, true, allVerbs...)
			start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
			payload := map[string]any{"retrieved": []Ref{{Collection: colMemories, ID: "mem-1", Revision: 1}}}
			if mode != "legacy" && mode != "quarantined_return" && mode != "quarantined_trace" {
				payload["observation"] = "context_delivery"
				payload["deliveries"] = []any{map[string]any{"collection": colMemories, "id": "mem-1", "revision": 1, "form": "meta"}}
			}
			if mode == "mixed_known" || mode == "mixed_unknown" {
				payload["observation"] = "context_delivery_mixed"
			}
			if mode == "mixed_unknown" {
				payload["deliveries"].([]any)[0].(map[string]any)["form"] = "unknown"
			}
			if mode == "wrong_revision" {
				payload["deliveries"].([]any)[0].(map[string]any)["revision"] = 2
			}
			raw, _ := json.Marshal(payload)
			scope := scopeA
			if mode == "foreign" {
				scope = scopeB
			}
			out, err := f.svc.IngestCompletedTrace(testCtx, "session-producer", TraceIngestInput{
				Scope: scope, TraceID: "delivery-trace", ThreadID: "delivery-session",
				AgentRun: CompletedAgentRunInput{AgentID: "codex", TaskType: "agent_session", Outcome: "partial", StartedAt: start, FinishedAt: start.Add(time.Second), Text: "session"},
				Events:   []TraceEventInput{{ID: "delivery-event", Kind: "context", OccurredAt: start, Payload: raw}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "erased" || mode == "quarantined" {
				source, err := p.Get(testCtx, "sources", scope, "delivery-event")
				if err != nil {
					t.Fatal(err)
				}
				source.ContentErased, source.PrivacyQuarantined = mode == "erased", mode == "quarantined"
				source.Revision++
				if err = p.Replace(testCtx, "sources", source, source.Revision-1); err != nil {
					t.Fatal(err)
				}
			}
			putOutcome(t, p, scopeA, "other-verdict", out.Trace.ID, "other-mem", "work_task", true)
			// A returned search response alone must never become confirmed delivery.
			if mode != "foreign" {
				err = p.Insert(testCtx, "jobs", Record{ID: "returned", Scope: scopeA, Kind: "context_usage", Status: "done", Revision: 1,
					Usage: &ContextUsage{Run: &out.Run, Items: []MemoryUse{{Memory: Ref{Collection: colMemories, ID: "mem-1", Revision: 1}, Rank: 1}}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "quarantined_return" || mode == "quarantined_trace" {
				col, id := "jobs", "returned"
				if mode == "quarantined_trace" {
					col, id = "execution_traces", out.Trace.ID
				}
				r, err := p.Get(testCtx, col, scopeA, id)
				if err != nil {
					t.Fatal(err)
				}
				r.PrivacyQuarantined = true
				r.Revision++
				if err = p.Replace(testCtx, col, r, r.Revision-1); err != nil {
					t.Fatal(err)
				}
			}
			counts, err := p.MemoryOutcomeSessionCounts(testCtx, comparisonQuery("mem-1"))
			want := observationReturned
			if mode == "confirmed" || mode == "mixed_known" {
				want = observationDelivered
			}
			if mode == "foreign" || mode == "quarantined_return" || mode == "quarantined_trace" {
				want = observationUnknown
			}
			if err != nil || len(counts) != 1 || counts[0].Observation != want || counts[0].Sessions != 1 {
				t.Fatalf("%s: %+v %v", mode, counts, err)
			}
		})
	}
}
