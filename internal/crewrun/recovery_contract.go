package crewrun

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// RecoveryBinding pins one intended effect. RunRef is the observed intent
// snapshot, not the advancing run progress CAS. Target/premise versions belong
// to the explicit adapter domain; they are never invented product revisions.
type RecoveryBinding struct {
	Scope          ObservationScope `json:"scope"`
	RunRef         ObservationRef   `json:"run_ref"`
	NodeID         string           `json:"node_id"`
	NodeAttempt    int64            `json:"node_attempt"`
	IntentID       string           `json:"intent_id"`
	EffectID       string           `json:"effect_id"`
	AdapterID      string           `json:"adapter_id"`
	TargetID       string           `json:"target_id"`
	TargetVersion  int64            `json:"target_version"`
	PremiseID      string           `json:"premise_id"`
	PremiseVersion int64            `json:"checked_premise_version"`
	ArgsSHA256     string           `json:"args_sha256"`
}

// RecoveryApproval is supplied evidence, not a new approval API. Matching this
// projection cannot grant external dispatch or attest physical human presence.
type RecoveryApproval struct {
	Binding            RecoveryBinding `json:"binding"`
	ActorID            string          `json:"actor_id"`
	ActorRole          string          `json:"actor_role"`
	Verdict            string          `json:"verdict"`
	AuthorizationState string          `json:"authorization_state"`
	DecisionAtNS       *int64          `json:"decision_at_ns"`
}

// RecoveryCapabilities describes the tested adapter contract. Zero-value
// capabilities mean unknown/unsupported. A fake declaration is not a guarantee
// about Telegram/Slack or any external provider.
type RecoveryCapabilities struct {
	AdapterID            string `json:"adapter_id"`
	StatusLookup         bool   `json:"status_lookup"`
	IdempotencyKey       bool   `json:"idempotency_key"`
	AtomicTargetVersion  bool   `json:"atomic_target_version"`
	AtomicPremiseVersion bool   `json:"atomic_premise_version"`
}
type RecoveryAdapterObservation struct {
	Binding       RecoveryBinding `json:"binding"`
	Outcome       string          `json:"outcome"`
	ReceiptSHA256 *string         `json:"receipt_sha256"`
}
type RecoveryInput struct {
	Intent             RecoveryBinding             `json:"intent"`
	Current            RecoveryBinding             `json:"current"`
	Approval           *RecoveryApproval           `json:"approval"`
	Capabilities       RecoveryCapabilities        `json:"capabilities"`
	AdapterObservation *RecoveryAdapterObservation `json:"adapter_observation"`
	LedgerKind         string                      `json:"ledger_kind"`
	LedgerState        string                      `json:"ledger_state"`
	CurrentUse         string                      `json:"current_use"`
	LeaseState         string                      `json:"lease_state"`
	RunState           string                      `json:"run_state"`
	Contexts           []ContextUseInput           `json:"contexts"`
}

// RecoveryDecision is advisory data only. No disposition authorizes a claim,
// dispatch, settlement, node completion, refund or canonical ledger rewrite.
// ObservedOutcome preserves known history even when current action is blocked.
type RecoveryDecision struct {
	Disposition              string `json:"disposition"`
	Reason                   string `json:"reason"`
	ObservedOutcome          string `json:"observed_outcome"`
	Authority                string `json:"authority"`
	CanonicalLedgerUnchanged bool   `json:"canonical_ledger_unchanged"`
}

func recoveryDecision(disposition, reason, outcome string) RecoveryDecision {
	return RecoveryDecision{disposition, reason, outcome, "data_only_untrusted", true}
}
func validRecoveryLabel(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" && len(value) <= 256 && !strings.ContainsFunc(value, unicode.IsControl)
}
func validateRecoveryBinding(value RecoveryBinding, path string) error {
	if validateObservation(value.Scope) != nil {
		return observationError("invalid_value", path+".scope")
	}
	if validateObservation(value.RunRef) != nil || value.RunRef.Collection != "crew_runs" {
		return observationError("invalid_value", path+".run_ref")
	}
	for _, label := range []string{value.NodeID, value.IntentID, value.EffectID, value.AdapterID, value.TargetID, value.PremiseID} {
		if !validRecoveryLabel(label) {
			return observationError("invalid_value", path)
		}
	}
	if value.NodeAttempt < 1 || value.NodeAttempt > 10000 || value.TargetVersion < 1 || value.PremiseVersion < 1 || !validContextSHA(value.ArgsSHA256) {
		return observationError("invalid_value", path)
	}
	return nil
}
func validateRecoveryEnums(input RecoveryInput) error {
	values := []struct {
		value, path string
		allowed     []string
	}{
		{input.LedgerKind, "ledger_kind", []string{"native_flow", "local_fixture"}},
		{input.LedgerState, "ledger_state", []string{"not_prepared", "awaiting_approval", "approved", "claimed", "succeeded", "unknown", "rejected"}},
		{input.CurrentUse, "current_use", []string{"confirmed", "unknown", "denied", "revoked", "deleted"}},
		{input.LeaseState, "lease_state", []string{"current", "expired", "foreign", "unknown"}},
		{input.RunState, "run_state", []string{"running", "cancelled", "stopped", "succeeded", "failed", "unknown"}},
	}
	for _, item := range values {
		if !observationContains(item.allowed, item.value) {
			return observationError("invalid_value", "$."+item.path)
		}
	}
	return nil
}
func recoveryObserved(input RecoveryInput) (string, error) {
	seen := input.AdapterObservation
	if seen == nil {
		return "not_observed", nil
	}
	if err := validateRecoveryBinding(seen.Binding, "$.adapter_observation.binding"); err != nil {
		return "unknown", err
	}
	if !observationContains([]string{"not_observed", "absent", "committed", "unknown", "conflict"}, seen.Outcome) {
		return "unknown", observationError("invalid_value", "$.adapter_observation.outcome")
	}
	if seen.Outcome == "committed" {
		if seen.ReceiptSHA256 == nil || !validContextSHA(*seen.ReceiptSHA256) {
			return "unknown", observationError("invalid_value", "$.adapter_observation.receipt_sha256")
		}
	} else if seen.ReceiptSHA256 != nil {
		return "unknown", observationError("invalid_value", "$.adapter_observation.receipt_sha256")
	}
	if seen.Binding != input.Intent {
		return "foreign", nil
	}
	return seen.Outcome, nil
}

// EvaluateEffectRecovery has no IO, callbacks or mutable engine state. It
// describes a conservative recovery plan for independently observed inputs.
// Native claimed/unknown effects remain unchanged even if a private adapter
// reports committed. Actual native human approval/claim guards remain required.
func EvaluateEffectRecovery(input RecoveryInput) (RecoveryDecision, error) {
	if err := validateRecoveryBinding(input.Intent, "$.intent"); err != nil {
		return RecoveryDecision{}, err
	}
	if err := validateRecoveryBinding(input.Current, "$.current"); err != nil {
		return RecoveryDecision{}, err
	}
	if err := validateRecoveryEnums(input); err != nil {
		return RecoveryDecision{}, err
	}
	outcome, err := recoveryObserved(input)
	if err != nil {
		return RecoveryDecision{}, err
	}
	if reason := recoveryGate(input, outcome); reason != "" {
		return recoveryDecision("block", reason, outcome), nil
	}
	if reason := recoveryContexts(input); reason != "" {
		return recoveryDecision("block", reason, outcome), nil
	}
	return recoveryDisposition(input, outcome), nil
}
func recoveryGate(input RecoveryInput, outcome string) string {
	if outcome == "foreign" || outcome == "conflict" {
		return "adapter_observation_conflict"
	}
	if input.Current != input.Intent {
		return "binding_changed"
	}
	if input.CurrentUse != "confirmed" {
		return "current_use_unproven"
	}
	if input.LeaseState != "current" {
		return "lease_not_current"
	}
	if input.RunState != "running" {
		return "run_not_running"
	}
	if input.Capabilities.AdapterID != "" && input.Capabilities.AdapterID != input.Intent.AdapterID {
		return "capability_binding_mismatch"
	}
	if input.Capabilities.AdapterID == "" && (input.Capabilities.StatusLookup || input.Capabilities.IdempotencyKey || input.Capabilities.AtomicTargetVersion || input.Capabilities.AtomicPremiseVersion) {
		return "capability_binding_mismatch"
	}
	if !recoveryApproved(input) {
		return "approval_not_current"
	}
	return ""
}
func recoveryApproved(input RecoveryInput) bool {
	approval := input.Approval
	return approval != nil && approval.Binding == input.Intent && validRecoveryLabel(approval.ActorID) && approval.ActorRole == "human" && approval.Verdict == "approved" && approval.AuthorizationState == "confirmed" && approval.DecisionAtNS != nil && *approval.DecisionAtNS >= 0
}
func recoveryContexts(input RecoveryInput) string {
	if input.Contexts == nil {
		return "context_provenance_unknown"
	}
	seen := map[ContextSourceKey]bool{}
	for _, value := range input.Contexts {
		if !validContextMode(value.Mode) {
			return "context_mode_invalid"
		}
		if value.Source.Scope != input.Intent.Scope {
			return "context_scope_mismatch"
		}
		if value.ExecutionRef != input.Intent.RunRef {
			return "context_run_mismatch"
		}
		if seen[value.Source] {
			return "context_source_ambiguous"
		}
		seen[value.Source] = true
		value.Mode = ContextReceiptEnforceCurrentUse
		decision := EvaluateContextUse(value)
		if decision.Action != "continue" || !decision.EvidenceComplete {
			return "context_not_current"
		}
	}
	return ""
}
func recoveryDisposition(input RecoveryInput, outcome string) RecoveryDecision {
	if input.LedgerState == "rejected" || input.LedgerState == "awaiting_approval" || input.LedgerState == "not_prepared" {
		return recoveryDecision("block", "ledger_not_dispatchable", outcome)
	}
	if input.LedgerState == "succeeded" {
		if outcome == "committed" {
			return recoveryDecision("reuse", "existing_success_receipt", outcome)
		}
		if outcome == "absent" {
			return recoveryDecision("block", "success_receipt_conflict", outcome)
		}
		return recoveryDecision("unknown", "success_receipt_unavailable", outcome)
	}
	if input.LedgerKind == "native_flow" {
		if outcome == "committed" && input.Capabilities.StatusLookup {
			return recoveryDecision("reconcile", "historical_adapter_evidence_only", outcome)
		}
		if input.LedgerState == "approved" && (outcome == "not_observed" || outcome == "absent") {
			return recoveryDecision("reconcile", "native_claim_required", outcome)
		}
		return recoveryDecision("unknown", "native_outcome_not_retriable", outcome)
	}
	return recoveryLocalDisposition(input, outcome)
}
func recoveryLocalDisposition(input RecoveryInput, outcome string) RecoveryDecision {
	caps := input.Capabilities
	if outcome == "committed" {
		if caps.StatusLookup {
			return recoveryDecision("reconcile", "exact_adapter_commit_observed", outcome)
		}
		return recoveryDecision("unknown", "adapter_commit_unqualified", outcome)
	}
	if input.LedgerState == "approved" && (outcome == "not_observed" || outcome == "absent") {
		return recoveryDecision("retry_same_effect", "approved_first_dispatch_nonatomic", outcome)
	}
	if caps.AdapterID == "" {
		return recoveryDecision("unknown", "adapter_capabilities_unknown", outcome)
	}
	if caps.IdempotencyKey && caps.AtomicTargetVersion && caps.AtomicPremiseVersion {
		return recoveryDecision("retry_same_effect", "local_atomic_idempotent_contract", outcome)
	}
	return recoveryDecision("unknown", "adapter_recovery_unsupported", outcome)
}
