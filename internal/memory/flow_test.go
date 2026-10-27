package memory

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func (f *fakeRepo) ListFlowDeliveries(ctx context.Context, scope Scope, connector, botID, status, after string, limit int) ([]Record, error) {
	rows, _ := f.ListRecords(ctx, scope, colJobs, 1<<30)
	out := []Record{}
	for _, r := range rows {
		d := r.FlowDelivery
		if r.Kind != flowDeliveryKind || d == nil || d.Origin == nil || d.Origin.Connector != connector || d.Origin.BotID != botID || r.ID <= after {
			continue
		}
		if status != "" && d.Status != status || status == "" && !slices.Contains([]string{"pending", "sending", "retry_wait"}, d.Status) {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func flowFixture(t *testing.T) (*fixture, Record, CrewRunInput) {
	f, _ := crewRunFixture(t)
	c := deepResearchTemplate()
	if !validCrew(c) {
		t.Fatal("invalid research template")
	}
	it := f.crew(c)
	in := CrewRunInput{Scope: scopeA, RequestID: "discord-event-1", Run: CrewRun{CrewID: it.ID, CrewRevision: it.Revision, Trigger: "discord", Status: "running", InvocationMode: "flow", Input: `{"request":"research"}`, Nodes: map[string]CrewRunNode{"start": {Status: "pending"}, "research": {Status: "pending"}, "end": {Status: "pending"}}, Origin: &CrewRunOrigin{Connector: "discord", GuildID: "123456789012345681", ChannelID: "123456789012345682", UserID: "123456789012345683", BotID: "bot-1", ThreadID: "123456789012345678", EventID: "123456789012345679"}}}
	return f, it, in
}

func leaseFlow(t *testing.T, f *fixture, id string) string {
	t.Helper()
	token := "0123456789abcdef0123456789abcdef"
	_, err := f.svc.FlowLease(testCtx, tokEngine, id, FlowLeaseInput{Scope: scopeA, Action: "acquire", LeaseToken: token})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestFlowCallIdempotencyAndAtomicDelivery(t *testing.T) {
	f, _, in := flowFixture(t)
	r, err := f.svc.CreateCrewRun(testCtx, tokEngine, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.svc.CreateCrewRun(testCtx, tokEngine, in)
	if err != nil || again.ID != r.ID {
		t.Fatalf("duplicate %s %v", again.ID, err)
	}
	if r.CrewRun.Definition == nil {
		t.Fatal("missing pinned definition")
	}
	changed := in
	changed.Run.Input = "different"
	if _, err = f.svc.CreateCrewRun(testCtx, tokEngine, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("key changed input", err)
	}
	d, err := f.repo.Get(testCtx, colJobs, scopeA, flowDeliveryID(r.ID))
	if err != nil || d.FlowDelivery.Status != "waiting_run" {
		t.Fatal("atomic pending delivery", d, err)
	}
	run := *r.CrewRun
	run.Status = "succeeded"
	run.Summary = "done"
	fin, err := f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: r.Revision, Run: run, LeaseToken: leaseFlow(t, f, r.ID)})
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.svc.ListFlowDeliveries(testCtx, tokEngine, scopeA, "discord", "bot-1", "", "", 200)
	if err != nil || len(page.Items) != 1 || page.Items[0].Delivery.ResultRevision != fin.Revision {
		t.Fatal("terminal outbox", page, err)
	}
	if len(f.repo.rows(colCrewRuns)) != 1 {
		t.Fatal("duplicate execution")
	}
}

func TestFlowDeliveryReceiptsAndLostResponse(t *testing.T) {
	f, _, in := flowFixture(t)
	r, err := f.svc.CreateCrewRun(testCtx, tokEngine, in)
	if err != nil {
		t.Fatal(err)
	}
	run := *r.CrewRun
	run.Status = "succeeded"
	run.Summary = "final"
	_, err = f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: r.Revision, Run: run, LeaseToken: leaseFlow(t, f, r.ID)})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := f.repo.Get(testCtx, colJobs, scopeA, flowDeliveryID(r.ID))
	change := FlowDeliveryChange{Scope: scopeA, ExpectedRevision: d.Revision, Action: "claim", ResultHash: strings.Repeat("a", 64), LeaseToken: "0123456789abcdef0123456789abcdef"}
	claimed, err := f.svc.ChangeFlowDelivery(testCtx, tokEngine, d.ID, change)
	if err != nil {
		t.Fatal(err)
	}
	change.ExpectedRevision = claimed.Revision
	change.Action = "complete"
	if _, err = f.svc.ChangeFlowDelivery(testCtx, tokEngine, d.ID, change); !errors.Is(err, ErrConflict) {
		t.Fatal("delivery without an acknowledged part must not complete", err)
	}
	change.Action = "ack_part"
	change.MessageID = "123456789012345680"
	acked, err := f.svc.ChangeFlowDelivery(testCtx, tokEngine, d.ID, change)
	if err != nil || acked.Delivery.Part != 1 {
		t.Fatal(acked, err)
	}
	dup, err := f.svc.ChangeFlowDelivery(testCtx, tokEngine, d.ID, change)
	if err != nil || dup.Revision != acked.Revision {
		t.Fatal("lost ack response", dup, err)
	}
	change.ExpectedRevision = acked.Revision
	change.Action = "claim"
	claimed, err = f.svc.ChangeFlowDelivery(testCtx, tokEngine, d.ID, change)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := f.repo.Get(testCtx, colJobs, scopeA, d.ID)
	stored.LeaseUntil = time.Now().Add(-time.Second)
	if err = f.repo.Replace(testCtx, colJobs, stored, stored.Revision); err != nil {
		t.Fatal(err)
	}
	change.ExpectedRevision = claimed.Revision
	change.Action = "claim"
	change.LeaseToken = "fedcba9876543210fedcba9876543210"
	unknown, err := f.svc.ChangeFlowDelivery(testCtx, tokEngine, d.ID, change)
	if err != nil || unknown.Delivery.Status != "unknown" {
		t.Fatal("expired send must not resend", unknown, err)
	}
}

func TestFlowReadOnlyBoundaryAndAliasCompatibility(t *testing.T) {
	f, it, in := flowFixture(t)
	got, err := f.svc.ResolveCrew(testCtx, f.agent, scopeA, "@딥리서치")
	if err != nil || got.Item.ID != it.ID {
		t.Fatal(got, err)
	}
	for _, typ := range []string{"code", "http", "notify", "subflow", "expert", "team"} {
		c := deepResearchTemplate()
		c.Graph.Nodes[1].Type = typ
		if flowCallable(c) {
			t.Fatal("unsafe graph", typ)
		}
	}
	in.Run.Origin.EventID = ""
	if _, err = f.svc.CreateCrewRun(testCtx, tokEngine, in); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing origin event", err)
	}
}
