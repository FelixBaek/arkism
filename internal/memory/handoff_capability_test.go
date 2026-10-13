//go:build linux

package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func capabilityResolutionFixture(c Consultation, hash string) CapabilityResolution {
	return CapabilityResolution{
		ContextHash: hash, Authority: "data_only_untrusted", Status: "resolved",
		VersionPins: []CapabilityVersionPin{
			{Kind: "role", ID: "illustrator_expert", Version: "role-v1"},
			{Kind: "skill", ID: "poster-concepts", Version: "skill-v1"},
			{Kind: "knowledge", ID: "synthetic-brief", Version: "knowledge-v1"},
			{Kind: "tool", ID: "text-artifact", Version: "tool-v1"},
			{Kind: "artifact", ID: "poster-contract", Version: "artifact-v1"},
			{Kind: "evaluation", ID: "synthetic-eval", Version: "evaluation-v1"},
		},
		SourceReferences: []SourceRef{{SourceID: c.SourceEventID, Locator: "original"}},
	}
}

func capabilityTraceFixture(t *testing.T, f *fixture, c Consultation, resolution CapabilityResolution) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"input": map[string]any{"capability_resolution": resolution}})
	if err != nil {
		t.Fatal(err)
	}
	eventID := "capability-event-" + c.ID
	event := Record{ID: eventID, Scope: c.Scope, Kind: "trace_input", Status: statusActive, Revision: 1,
		Text: string(payload), TraceID: c.Child.TraceID, SourceEventID: eventID, CreatedBy: "runtime", Trust: .5,
		Sources: []SourceRef{{SourceID: c.SourceEventID, Locator: "memory_context"}}}
	if err := f.repo.Insert(testCtx, "sources", event); err != nil {
		t.Fatal(err)
	}
	trace := consultationRecord(c.Child.TraceID, c.Scope, "runtime")
	trace.Kind = "execution_trace"
	trace.Relations = []Ref{{Collection: "agent_runs", ID: c.Child.AgentRunID, Revision: 1}}
	trace.Sources = []SourceRef{{SourceID: c.SourceEventID, Locator: "memory_context"}, {SourceID: eventID, Locator: "original"}}
	if err := f.repo.Insert(testCtx, "execution_traces", trace); err != nil {
		t.Fatal(err)
	}
}

func capabilityEvent(t *testing.T, f *fixture, c Consultation, token, id string, resolution CapabilityResolution) (Consultation, error) {
	t.Helper()
	return f.svc.ConsultationEvent(testCtx, token, c.ID, ConsultationEventInput{
		Scope: c.Scope, EventID: id, ExpectedRevision: c.Revision, Type: "capability_resolved",
		Payload: ConsultationEventPayload{TraceID: c.Child.TraceID, CapabilityResolution: &resolution},
	})
}

func TestCapabilityResolvedServiceOnlyAndTraceBinding(t *testing.T) {
	f := handoffFixture(t)
	c := handoffApproved(t, f, "capability-service-only")
	resolution := capabilityResolutionFixture(c, fmt.Sprintf("%064x", 1))
	capabilityTraceFixture(t, f, c, resolution)
	if _, err := capabilityEvent(t, f, c, f.human, "cap-human", resolution); !errors.Is(err, ErrDenied) {
		t.Fatalf("human capability event error = %v", err)
	}
	if _, err := capabilityEvent(t, f, c, f.agent, "cap-agent", resolution); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent capability event error = %v", err)
	}
	out, err := capabilityEvent(t, f, c, f.work, "cap-service", resolution)
	if err != nil || out.Handoff.CapabilityResolution == nil {
		t.Fatalf("service capability event: %v", err)
	}
}

func TestCapabilityResolvedRejectsMismatchMissingPinsAndHashChange(t *testing.T) {
	f := handoffFixture(t)
	c := handoffApproved(t, f, "capability-validation")
	resolution := capabilityResolutionFixture(c, fmt.Sprintf("%064x", 2))
	badTrace := resolution
	badTrace.ContextHash = fmt.Sprintf("%064x", 3)
	capabilityTraceFixture(t, f, c, badTrace)
	if _, err := capabilityEvent(t, f, c, f.work, "cap-mismatch", resolution); !errors.Is(err, ErrConflict) {
		t.Fatalf("trace metadata mismatch error = %v", err)
	}

	f = handoffFixture(t)
	c = handoffApproved(t, f, "capability-pins")
	resolution = capabilityResolutionFixture(c, fmt.Sprintf("%064x", 4))
	resolution.VersionPins = resolution.VersionPins[:5]
	capabilityTraceFixture(t, f, c, resolution)
	if _, err := capabilityEvent(t, f, c, f.work, "cap-missing-pin", resolution); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing pin error = %v", err)
	}

	f = handoffFixture(t)
	c = handoffApproved(t, f, "capability-hash")
	resolution = capabilityResolutionFixture(c, fmt.Sprintf("%064x", 5))
	capabilityTraceFixture(t, f, c, resolution)
	if _, err := capabilityEvent(t, f, c, f.work, "cap-hash", resolution); err != nil {
		t.Fatal(err)
	}
	changed := resolution
	changed.ContextHash = fmt.Sprintf("%064x", 6)
	if _, err := capabilityEvent(t, f, c, f.work, "cap-hash-changed", changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed hash error = %v", err)
	}
}

func TestCapabilityResolvedStableReplayAndSourceFence(t *testing.T) {
	f := handoffFixture(t)
	c := handoffApproved(t, f, "capability-replay")
	resolution := capabilityResolutionFixture(c, fmt.Sprintf("%064x", 7))
	capabilityTraceFixture(t, f, c, resolution)
	out, err := capabilityEvent(t, f, c, f.work, "cap-replay", resolution)
	if err != nil {
		t.Fatal(err)
	}
	again, err := capabilityEvent(t, f, out, f.work, "cap-replay", resolution)
	if err != nil || again.Revision != out.Revision {
		t.Fatalf("stable replay: %v revision %d/%d", err, again.Revision, out.Revision)
	}

	f = handoffFixture(t)
	c = handoffApproved(t, f, "capability-fence")
	resolution = capabilityResolutionFixture(c, fmt.Sprintf("%064x", 8))
	capabilityTraceFixture(t, f, c, resolution)
	source, err := f.repo.Get(testCtx, "sources", c.Scope, c.SourceEventID)
	if err != nil {
		t.Fatal(err)
	}
	source.PrivacyQuarantined = true
	source.Revision++
	if err = f.repo.Replace(testCtx, "sources", source, source.Revision-1); err != nil {
		t.Fatal(err)
	}
	if _, err = capabilityEvent(t, f, c, f.work, "cap-fenced", resolution); !errors.Is(err, ErrNotFound) {
		t.Fatalf("fenced source error = %v", err)
	}
}
