package memory

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
)

var capabilityPinKinds = map[string]bool{
	"role": true, "skill": true, "knowledge": true,
	"tool": true, "artifact": true, "evaluation": true,
}

// CapabilityResolution is metadata only. The resolved context remains in the
// execution trace and is never copied into the handoff record.
type CapabilityVersionPin struct {
	Kind    string `json:"kind" bson:"kind"`
	ID      string `json:"id" bson:"id"`
	Version string `json:"version" bson:"version"`
}

type CapabilityResolution struct {
	ContextHash      string                 `json:"context_hash" bson:"context_hash"`
	Authority        string                 `json:"authority" bson:"authority"`
	Status           string                 `json:"status" bson:"status"`
	VersionPins      []CapabilityVersionPin `json:"version_pins" bson:"version_pins"`
	SourceReferences []SourceRef            `json:"source_references" bson:"source_references"`
}

func validCapabilityResolution(in *CapabilityResolution) bool {
	if in == nil || !sourceHashPattern.MatchString(in.ContextHash) || in.Authority != "data_only_untrusted" || in.Status != "resolved" || len(in.VersionPins) < 6 || len(in.VersionPins) > 32 || len(in.SourceReferences) != 1 {
		return false
	}
	seen := map[string]bool{}
	kinds := map[string]bool{}
	for _, pin := range in.VersionPins {
		if !capabilityPinKinds[pin.Kind] || pin.ID == "" || pin.Version == "" || len(pin.ID) > 256 || len(pin.Version) > 256 {
			return false
		}
		key := pin.Kind + "\x00" + pin.ID
		if seen[key] {
			return false
		}
		seen[key] = true
		kinds[pin.Kind] = true
	}
	if len(seen) < 6 || len(kinds) != len(capabilityPinKinds) {
		return false
	}
	for _, ref := range in.SourceReferences {
		if ref.SourceID == "" || ref.Locator != "original" || ref.Span != nil {
			return false
		}
	}
	return true
}

func capabilityTraceResolution(ctx context.Context, tx Tx, scope Scope, traceID string, expected *CapabilityResolution) error {
	trace, err := tx.Get(ctx, "execution_traces", scope, traceID)
	if err != nil {
		return err
	}
	if !live(trace) {
		return ErrNotFound
	}
	// The trace must carry every evidence reference used by the resolver.
	for _, want := range expected.SourceReferences {
		if !slices.ContainsFunc(trace.Sources, func(got SourceRef) bool { return got.SourceID == want.SourceID }) {
			return ErrConflict
		}
		if err := tx.FenceSource(ctx, scope, want.SourceID); err != nil {
			return err
		}
	}
	var found int
	for _, ref := range trace.Sources {
		source, e := tx.Get(ctx, "sources", scope, ref.SourceID)
		if e != nil {
			return e
		}
		if source.TraceID != traceID {
			continue
		}
		var body struct {
			TraceID              string                `json:"trace_id"`
			CapabilityResolution *CapabilityResolution `json:"capability_resolution"`
			Input                struct {
				CapabilityResolution *CapabilityResolution `json:"capability_resolution"`
			} `json:"input"`
		}
		if json.Unmarshal([]byte(source.Text), &body) != nil || body.CapabilityResolution == nil {
			if body.Input.CapabilityResolution == nil {
				continue
			}
			body.CapabilityResolution = body.Input.CapabilityResolution
		}
		if body.TraceID != "" && body.TraceID != traceID || !reflect.DeepEqual(body.CapabilityResolution, expected) {
			return ErrConflict
		}
		found++
	}
	if found != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Service) applyCapabilityResolution(ctx context.Context, tx Tx, r *Record, c *Consultation, p ConsultationEventPayload) error {
	if c.Handoff == nil || c.Child == nil || (c.Child.Status != "active" && c.Child.Status != "completed") || (c.Handoff.Status != "active" && c.Handoff.Status != "awaiting_user") || p.TraceID != c.Child.TraceID || !validCapabilityResolution(p.CapabilityResolution) {
		return ErrConflict
	}
	if c.Handoff.CapabilityResolution != nil {
		if reflect.DeepEqual(c.Handoff.CapabilityResolution, p.CapabilityResolution) {
			return nil
		}
		return ErrConflict
	}
	if err := handoffTrace(ctx, tx, c.Scope, *c.Child, p.TraceID, c.SourceEventID); err != nil {
		return err
	}
	for _, ref := range p.CapabilityResolution.SourceReferences {
		if ref.SourceID != c.SourceEventID || ref.Locator != "original" {
			return ErrConflict
		}
	}
	if err := capabilityTraceResolution(ctx, tx, c.Scope, p.TraceID, p.CapabilityResolution); err != nil {
		return err
	}
	c.Handoff.CapabilityResolution = p.CapabilityResolution
	return nil
}
