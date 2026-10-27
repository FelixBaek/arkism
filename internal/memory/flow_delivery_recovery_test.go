package memory

import (
	"strings"
	"testing"
)

func uncertainDelivery(t *testing.T) (*fixture, flowHTTPHarness, Record, FlowDeliveryView) {
	t.Helper()
	f, h, in := flowHTTPFixture(t)
	r, _ := finishFlowHTTP(t, h, in)
	stored, e := f.repo.Get(testCtx, colJobs, scopeA, flowDeliveryID(r.ID))
	if e != nil {
		t.Fatal(e)
	}
	d := flowDeliveryView(stored)
	change := FlowDeliveryChange{Scope: scopeA, ExpectedRevision: d.Revision, Action: "claim", LeaseToken: "fixture-delivery-lease-2026", ResultHash: strings.Repeat("b", 64)}
	path := "/v1/crew/deliveries/" + d.ID + "/change"
	h.call("POST", path, tokEngine, change, 200, &d)
	change.ExpectedRevision, change.Action, change.Part, change.MessageID = d.Revision, "ack_part", 0, "123456789012345687"
	h.call("POST", path, tokEngine, change, 200, &d)
	change.ExpectedRevision, change.Action, change.Part = d.Revision, "claim", 1
	h.call("POST", path, tokEngine, change, 200, &d)
	change.ExpectedRevision, change.Action, change.ErrorCode = d.Revision, "unknown", "response_lost"
	h.call("POST", path, tokEngine, change, 200, &d)
	return f, h, r, d
}

func TestFlowDeliveryRecoveryNeedsHumanConfirmedDisplayedRevision(t *testing.T) {
	f, h, run, d := uncertainDelivery(t)
	in := FlowDeliveryRecovery{Scope: scopeA, ExpectedRevision: d.Revision, Action: "retry", Confirmed: true}
	path := "/v1/crew/deliveries/" + d.ID + "/recover"
	h.call("POST", path, tokEngine, in, 403, nil)
	h.call("POST", path, f.agent, in, 403, nil)
	in.Confirmed = false
	h.call("POST", path, f.human, in, 400, nil)
	in.Confirmed = true
	in.ExpectedRevision--
	h.call("POST", path, f.human, in, 409, nil)
	in.ExpectedRevision = d.Revision
	h.call("POST", path, f.human, in, 200, &d)
	if d.Delivery.Status != "pending" || d.Delivery.Part != 1 || len(d.Delivery.MessageIDs) != 1 || d.Delivery.ResultHash != strings.Repeat("b", 64) || len(d.Delivery.RecoveryHistory) != 1 {
		t.Fatal("recovery lost receipts", d)
	}
	h.call("POST", path, f.human, in, 409, nil)
	after, e := f.svc.GetCrewRun(testCtx, f.human, scopeA, run.ID)
	if e != nil || after.Revision != run.Revision || len(f.repo.rows(colCrewRuns)) != 1 {
		t.Fatal("delivery recovery reran or changed work", e)
	}
}

func TestFlowDeliveryRecoveryAcknowledgesObservedPartOrRedirects(t *testing.T) {
	for _, action := range []string{"ack_part", "redirect"} {
		t.Run(action, func(t *testing.T) {
			f, h, _, d := uncertainDelivery(t)
			original := *d.Delivery.Origin
			in := FlowDeliveryRecovery{Scope: scopeA, ExpectedRevision: d.Revision, Action: action, Confirmed: true}
			if action == "ack_part" {
				in.MessageID = "123456789012345688"
			} else {
				in.Destination = &FlowDeliveryDestination{BotID: "bot-2", GuildID: "223456789012345681", ChannelID: "223456789012345682", ThreadID: "223456789012345683"}
			}
			h.call("POST", "/v1/crew/deliveries/"+d.ID+"/recover", f.human, in, 200, &d)
			prior := d.Delivery.RecoveryHistory[0]
			if prior.Part != 1 || len(prior.MessageIDs) != 1 || *prior.Origin != original || prior.By == "" {
				t.Fatal("prior receipt lost", prior)
			}
			if action == "ack_part" && (d.Delivery.Part != 2 || d.Delivery.MessageIDs[1] != in.MessageID || prior.ConfirmedMessageID != in.MessageID) {
				t.Fatal(d)
			}
			if action == "redirect" && (d.Delivery.Part != 0 || len(d.Delivery.MessageIDs) != 0 || d.Delivery.Origin.ThreadID != in.Destination.ThreadID || d.Delivery.Origin.UserID != original.UserID) {
				t.Fatal(d)
			}
		})
	}
}

func TestFlowDeliveryQuarantineHidesReceiptAndPreventsRecovery(t *testing.T) {
	f, h, r, d := uncertainDelivery(t)
	stored, _ := f.repo.Get(testCtx, colJobs, scopeA, d.ID)
	stored.PrivacyQuarantined = true
	if e := f.repo.Replace(testCtx, colJobs, stored, stored.Revision); e != nil {
		t.Fatal(e)
	}
	var result FlowResultView
	h.call("GET", "/v1/crew/runs/"+r.ID+"/result"+flowHTTPQuery, f.human, nil, 200, &result)
	if result.Delivery != nil {
		t.Fatal("quarantined receipt exposed", result.Delivery)
	}
	h.call("POST", "/v1/crew/deliveries/"+d.ID+"/recover", f.human, FlowDeliveryRecovery{Scope: scopeA, ExpectedRevision: d.Revision, Action: "retry", Confirmed: true}, 404, nil)
}

func TestFlowDeliveryResultRevisionSurvivesAccountingRevision(t *testing.T) {
	f, h, r, d := uncertainDelivery(t)
	stored, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, r.ID)
	old := stored.Revision
	stored.Revision++
	stored.FlowBudget = &FlowBudget{CommittedTokens: 17}
	if e := f.repo.Replace(testCtx, colCrewRuns, stored, old); e != nil {
		t.Fatal(e)
	}
	h.call("POST", "/v1/crew/deliveries/"+d.ID+"/recover", f.human, FlowDeliveryRecovery{Scope: scopeA, ExpectedRevision: d.Revision, Action: "retry", Confirmed: true}, 200, &d)
	if d.Delivery.ResultRevision != r.CrewRun.ResultRevision || d.Delivery.ResultRevision == stored.Revision {
		t.Fatal("accounting moved result identity", d)
	}
	h.call("POST", "/v1/crew/deliveries/"+d.ID+"/change", tokEngine, FlowDeliveryChange{Scope: scopeA, ExpectedRevision: d.Revision, Action: "claim", LeaseToken: "new-delivery-fixture-lease", ResultHash: strings.Repeat("b", 64)}, 200, nil)
}
