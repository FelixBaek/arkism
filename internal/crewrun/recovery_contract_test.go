package crewrun

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func recoveryTestBinding() RecoveryBinding {
	return RecoveryBinding{Scope: ObservationScope{"ws", "proj", "usr", "agt"}, RunRef: ObservationRef{"crew_runs", "run", 7}, NodeID: "notify", NodeAttempt: 2, IntentID: "intent", EffectID: "effect", AdapterID: "local_fixture.v1", TargetID: "target", TargetVersion: 3, PremiseID: "policy", PremiseVersion: 4, ArgsSHA256: strings.Repeat("a", 64)}
}
func recoveryTestInput() RecoveryInput {
	binding := recoveryTestBinding()
	return RecoveryInput{Intent: binding, Current: binding, Approval: &RecoveryApproval{Binding: binding, ActorID: "human", ActorRole: "human", Verdict: "approved", AuthorizationState: "confirmed", DecisionAtNS: observationInt(100)}, Capabilities: RecoveryCapabilities{AdapterID: binding.AdapterID}, LedgerKind: "local_fixture", LedgerState: "approved", CurrentUse: "confirmed", LeaseState: "current", RunState: "running", Contexts: []ContextUseInput{}}
}
func recoveryTestObserved(binding RecoveryBinding, outcome string) *RecoveryAdapterObservation {
	value := &RecoveryAdapterObservation{Binding: binding, Outcome: outcome}
	if outcome == "committed" {
		value.ReceiptSHA256 = observationString(strings.Repeat("b", 64))
	}
	return value
}
func recoveryTestDecision(t *testing.T, input RecoveryInput) RecoveryDecision {
	t.Helper()
	got, err := EvaluateEffectRecovery(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Authority != "data_only_untrusted" || !got.CanonicalLedgerUnchanged {
		t.Fatal("advisory promoted authority", got)
	}
	return got
}
func TestRecoveryContractMissingSuccessReceipt(t *testing.T) {
	for _, outcome := range []string{"not_observed", "unknown", "absent"} {
		t.Run(outcome, func(t *testing.T) {
			input := recoveryTestInput()
			input.LedgerKind = "native_flow"
			input.LedgerState = "succeeded"
			if outcome != "not_observed" {
				input.AdapterObservation = recoveryTestObserved(input.Intent, outcome)
			}
			got := recoveryTestDecision(t, input)
			if got.Disposition == "reuse" {
				t.Fatal("success without bound receipt", got)
			}
		})
	}
}
func TestRecoveryContractCommittedHasPriority(t *testing.T) {
	input := recoveryTestInput()
	input.Capabilities.StatusLookup = true
	input.AdapterObservation = recoveryTestObserved(input.Intent, "committed")
	if got := recoveryTestDecision(t, input); got.Disposition != "reconcile" {
		t.Fatal("repeated already committed effect", got)
	}
}
func TestRecoveryContractDispositionMatrix(t *testing.T) {
	cases := []struct {
		name, kind, state, outcome, want string
		lookup, key, atomic              bool
	}{
		{name: "first_opaque_nonatomic", kind: "local_fixture", state: "approved", want: "retry_same_effect"},
		{name: "native_first_claim", kind: "native_flow", state: "approved", want: "reconcile"},
		{name: "native_claimed_opaque", kind: "native_flow", state: "claimed", want: "unknown"},
		{name: "native_unknown_opaque", kind: "native_flow", state: "unknown", want: "unknown"},
		{name: "native_claimed_local_commit", kind: "native_flow", state: "claimed", outcome: "committed", lookup: true, want: "reconcile"},
		{name: "native_unknown_local_commit", kind: "native_flow", state: "unknown", outcome: "committed", lookup: true, want: "reconcile"},
		{name: "native_success_receipt", kind: "native_flow", state: "succeeded", outcome: "committed", want: "reuse"},
		{name: "local_success_receipt", kind: "local_fixture", state: "succeeded", outcome: "committed", want: "reuse"},
		{name: "local_claimed_lookup_commit", kind: "local_fixture", state: "claimed", outcome: "committed", lookup: true, want: "reconcile"},
		{name: "local_unknown_lookup_commit", kind: "local_fixture", state: "unknown", outcome: "committed", lookup: true, want: "reconcile"},
		{name: "local_unknown_no_caps", kind: "local_fixture", state: "unknown", want: "unknown"},
		{name: "local_target_only", kind: "local_fixture", state: "unknown", lookup: true, key: true, want: "unknown"},
		{name: "local_atomic_retry", kind: "local_fixture", state: "unknown", key: true, atomic: true, want: "retry_same_effect"},
		{name: "native_atomic_cannot_retry", kind: "native_flow", state: "unknown", key: true, atomic: true, want: "unknown"},
		{name: "local_absent_atomic", kind: "local_fixture", state: "claimed", outcome: "absent", lookup: true, key: true, atomic: true, want: "retry_same_effect"},
		{name: "awaiting", kind: "local_fixture", state: "awaiting_approval", want: "block"},
		{name: "not_prepared", kind: "local_fixture", state: "not_prepared", want: "block"},
		{name: "rejected", kind: "local_fixture", state: "rejected", want: "block"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := recoveryTestInput()
			input.LedgerKind = tc.kind
			input.LedgerState = tc.state
			input.Capabilities.StatusLookup = tc.lookup
			input.Capabilities.IdempotencyKey = tc.key
			input.Capabilities.AtomicTargetVersion = tc.key
			input.Capabilities.AtomicPremiseVersion = tc.atomic
			if tc.outcome != "" {
				input.AdapterObservation = recoveryTestObserved(input.Intent, tc.outcome)
			}
			got := recoveryTestDecision(t, input)
			if got.Disposition != tc.want {
				t.Fatalf("want%s got%+v", tc.want, got)
			}
		})
	}
}
func TestRecoveryContractCurrentGatesPreserveHistory(t *testing.T) {
	mutations := map[string]func(*RecoveryInput){
		"current_unknown": func(v *RecoveryInput) { v.CurrentUse = "unknown" }, "deleted": func(v *RecoveryInput) { v.CurrentUse = "deleted" }, "revoked": func(v *RecoveryInput) { v.CurrentUse = "revoked" }, "denied": func(v *RecoveryInput) { v.CurrentUse = "denied" },
		"lease_unknown": func(v *RecoveryInput) { v.LeaseState = "unknown" }, "foreign_lease": func(v *RecoveryInput) { v.LeaseState = "foreign" }, "expired_lease": func(v *RecoveryInput) { v.LeaseState = "expired" },
		"cancelled": func(v *RecoveryInput) { v.RunState = "cancelled" }, "stopped": func(v *RecoveryInput) { v.RunState = "stopped" }, "failed": func(v *RecoveryInput) { v.RunState = "failed" }, "completed": func(v *RecoveryInput) { v.RunState = "succeeded" },
		"approval_missing": func(v *RecoveryInput) { v.Approval = nil }, "service": func(v *RecoveryInput) { v.Approval.ActorRole = "service" }, "approval_unknown": func(v *RecoveryInput) { v.Approval.AuthorizationState = "not_recorded" }, "approval_revoked": func(v *RecoveryInput) { v.Approval.AuthorizationState = "revoked" }, "approval_rejected": func(v *RecoveryInput) { v.Approval.Verdict = "rejected" }, "no_actor": func(v *RecoveryInput) { v.Approval.ActorID = " " }, "no_clock": func(v *RecoveryInput) { v.Approval.DecisionAtNS = nil }, "negative_clock": func(v *RecoveryInput) { v.Approval.DecisionAtNS = observationInt(-1) },
		"nil_context": func(v *RecoveryInput) { v.Contexts = nil }, "capability_other": func(v *RecoveryInput) { v.Capabilities.AdapterID = "foreign" },
	}
	for name, change := range mutations {
		t.Run(name, func(t *testing.T) {
			input := recoveryTestInput()
			input.LedgerState = "succeeded"
			input.AdapterObservation = recoveryTestObserved(input.Intent, "committed")
			change(&input)
			got := recoveryTestDecision(t, input)
			if got.Disposition != "block" || got.ObservedOutcome != "committed" {
				t.Fatalf("current gate lost history or allowed work: %+v", got)
			}
		})
	}
}
func TestRecoveryContractExactBinding(t *testing.T) {
	mutations := map[string]func(*RecoveryBinding){
		"workspace": func(v *RecoveryBinding) { v.Scope.WorkspaceID = "other" }, "project": func(v *RecoveryBinding) { v.Scope.ProjectID = "other" }, "user": func(v *RecoveryBinding) { v.Scope.UserID = "other" }, "agent": func(v *RecoveryBinding) { v.Scope.AgentID = "other" },
		"runid": func(v *RecoveryBinding) { v.RunRef.ID = "other" }, "runCAS": func(v *RecoveryBinding) { v.RunRef.Revision++ }, "node": func(v *RecoveryBinding) { v.NodeID = "other" }, "node_attempt": func(v *RecoveryBinding) { v.NodeAttempt++ }, "intent": func(v *RecoveryBinding) { v.IntentID = "other" }, "effect": func(v *RecoveryBinding) { v.EffectID = "other" }, "adapter": func(v *RecoveryBinding) { v.AdapterID = "foreign" }, "target": func(v *RecoveryBinding) { v.TargetID = "other" }, "targetversion": func(v *RecoveryBinding) { v.TargetVersion++ }, "premise": func(v *RecoveryBinding) { v.PremiseID = "other" }, "premiseversion": func(v *RecoveryBinding) { v.PremiseVersion++ }, "args": func(v *RecoveryBinding) { v.ArgsSHA256 = strings.Repeat("f", 64) },
	}
	for name, change := range mutations {
		t.Run(name, func(t *testing.T) {
			for _, position := range []string{"current", "approval", "adapter_observation"} {
				t.Run(position, func(t *testing.T) {
					input := recoveryTestInput()
					input.Capabilities.StatusLookup = true
					input.AdapterObservation = recoveryTestObserved(input.Intent, "committed")
					switch position {
					case "current":
						change(&input.Current)
					case "approval":
						change(&input.Approval.Binding)
					case "adapter_observation":
						change(&input.AdapterObservation.Binding)
					}
					if got := recoveryTestDecision(t, input); got.Disposition != "block" {
						t.Fatal("foreign effect reused", got)
					}
				})
			}
		})
	}
}
func TestRecoveryContractMalformed(t *testing.T) {
	for name, change := range map[string]func(*RecoveryInput){
		"namespace": func(v *RecoveryInput) { v.Intent.RunRef.Collection = "jobs" }, "zeroCAS": func(v *RecoveryInput) { v.Intent.RunRef.Revision = 0 }, "empty_scope": func(v *RecoveryInput) { v.Intent.Scope.ProjectID = "" }, "empty_intent": func(v *RecoveryInput) { v.Intent.IntentID = "" }, "control_label": func(v *RecoveryInput) { v.Intent.IntentID = "secret\n" }, "zero_target": func(v *RecoveryInput) { v.Intent.TargetVersion = 0 }, "negative_premise": func(v *RecoveryInput) { v.Current.PremiseVersion = -1 }, "zero_attempt": func(v *RecoveryInput) { v.Intent.NodeAttempt = 0 }, "badsha": func(v *RecoveryInput) { v.Intent.ArgsSHA256 = strings.Repeat("A", 64) },
		"ledgerkind": func(v *RecoveryInput) { v.LedgerKind = "anything" }, "ledgerstate": func(v *RecoveryInput) { v.LedgerState = "done" }, "currentuse": func(v *RecoveryInput) { v.CurrentUse = "yes" }, "lease": func(v *RecoveryInput) { v.LeaseState = "owned" }, "run": func(v *RecoveryInput) { v.RunState = "done" },
		"commit_no_receipt": func(v *RecoveryInput) {
			v.AdapterObservation = &RecoveryAdapterObservation{Binding: v.Intent, Outcome: "committed"}
		}, "absent_has_receipt": func(v *RecoveryInput) {
			v.AdapterObservation = recoveryTestObserved(v.Intent, "committed")
			v.AdapterObservation.Outcome = "absent"
		}, "invalid_outcome": func(v *RecoveryInput) {
			v.AdapterObservation = &RecoveryAdapterObservation{Binding: v.Intent, Outcome: "succeeded"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := recoveryTestInput()
			change(&input)
			_, err := EvaluateEffectRecovery(input)
			if err == nil {
				t.Fatal("malformed input accepted")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("raw leak")
			}
		})
	}
}
func TestRecoveryContractContextEnforcement(t *testing.T) {
	for _, mode := range []ContextReceiptMode{ContextReceiptOff, ContextReceiptObserve, ContextReceiptEnforceCurrentUse} {
		t.Run(string(mode), func(t *testing.T) {
			input := recoveryTestInput()
			current := contextReceiptInput()
			current.Mode = mode
			current.ExecutionRef = input.Intent.RunRef
			input.Contexts = []ContextUseInput{current}
			if got := recoveryTestDecision(t, input); got.Disposition != "retry_same_effect" {
				t.Fatal(got)
			}
			input.Contexts[0].Current.Facts.Lifecycle = "deleted"
			if got := recoveryTestDecision(t, input); got.Disposition != "block" {
				t.Fatal("off/observe bypassed current gate", got)
			}
		})
	}
	for _, name := range []string{"duplicate", "foreign_scope", "foreign_run", "invalid_mode", "unknown", "refresh", "resume_missing"} {
		t.Run(name, func(t *testing.T) {
			input := recoveryTestInput()
			current := contextReceiptInput()
			current.ExecutionRef = input.Intent.RunRef
			input.Contexts = []ContextUseInput{current}
			switch name {
			case "duplicate":
				input.Contexts = append(input.Contexts, current)
			case "foreign_scope":
				input.Contexts[0].Source.Scope.AgentID = "other"
			case "foreign_run":
				input.Contexts[0].ExecutionRef.ID = "other"
			case "invalid_mode":
				input.Contexts[0].Mode = "bogus"
			case "unknown":
				input.Contexts[0].Current.Facts.ReadState = "unavailable"
			case "refresh":
				input.Contexts[0].Previous = &ContextSourceState{current.Current.Observation, current.Current.Facts.GuardSHA256}
				input.Contexts[0].Current.Observation.Revision = observationInt(6)
				input.Contexts[0].Current.Facts.Ref.Revision = 6
				input.Contexts[0].Current.Facts.GuardSHA256 = strings.Repeat("d", 64)
				input.Contexts[0].Premise = ContextPremiseRefresh
			case "resume_missing":
				input.Contexts[0].Resume = true
			}
			if got := recoveryTestDecision(t, input); got.Disposition != "block" {
				t.Fatal("context bypass", got)
			}
		})
	}
}
func TestRecoveryContractStableAndClosedProjection(t *testing.T) {
	input := recoveryTestInput()
	before, _ := json.Marshal(input)
	first := recoveryTestDecision(t, input)
	second := recoveryTestDecision(t, input)
	after, _ := json.Marshal(input)
	if !reflect.DeepEqual(first, second) || string(before) != string(after) {
		t.Fatal("input mutated or nondeterministic")
	}
	raw, _ := json.Marshal(input.Intent)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 12 || fields["checked_premise_version"] == nil || fields["premise_version"] != nil {
		t.Fatal("binding field projection changed")
	}
	binding := input.Intent
	binding.TargetVersion = 9223372036854775807
	binding.PremiseVersion = 9007199254740993
	raw, _ = json.Marshal(binding)
	var decoded RecoveryBinding
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded != binding {
		t.Fatal("version precision lost", err)
	}
}
