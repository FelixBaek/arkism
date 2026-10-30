package memory

import (
	"context"
	_ "embed"
	"errors"
	"slices"
)

//go:embed native_search_stages_v2.json
var nativeSearchStagesContract []byte

const NativeSearchStagesProtocol = "sys-native-service-stages-v2"

type NativeSearchFinalRejection struct {
	UnderlyingCauseRefSHA *string `json:"underlying_cause_ref_sha256"`
	ScopedRefSHA          string  `json:"scoped_ref_sha256"`
	Stage                 string  `json:"stage"`
	Code                  string  `json:"code"`
}

// Stages describe server arithmetic and response stages, never reader delivery or use.
type NativeSearchShadowStages struct {
	ContractSHA                 string                       `json:"contract_sha256"`
	ArithmeticSHA               string                       `json:"arithmetic_result_sha256"`
	BaselineSHA                 string                       `json:"actual_baseline_response_sha256"`
	PublishedSHA                string                       `json:"published_read_sha256"`
	BaselinePreFinalCurrentRefs []string                     `json:"baseline_pre_final_current_scoped_ref_sha256"`
	BaselinePreFinalHistoryRefs []string                     `json:"baseline_pre_final_history_scoped_ref_sha256"`
	Protocol                    string                       `json:"protocol"`
	ScopeSHA                    string                       `json:"scope_sha256"`
	CandidateSHA                string                       `json:"candidate_sha256"`
	ContextViewSHA              string                       `json:"context_view_sha256,omitempty"`
	ContextIDHash               string                       `json:"baseline_context_id_sha256,omitempty"`
	FullRankedRefs              []string                     `json:"shadow_full_ranked_scoped_ref_sha256"`
	PrepackRefs                 []string                     `json:"shadow_prepack_scoped_ref_sha256"`
	EmittedCurrentRefs          []string                     `json:"shadow_emitted_current_scoped_ref_sha256"`
	EmittedHistoryRefs          []string                     `json:"shadow_emitted_history_scoped_ref_sha256"`
	BaselineCurrentRefs         []string                     `json:"actual_baseline_current_scoped_ref_sha256"`
	BaselineHistoryRefs         []string                     `json:"actual_baseline_history_scoped_ref_sha256"`
	BaselineReturned            bool                         `json:"actual_baseline_returned"`
	FinalEntered                bool                         `json:"final_transaction_entered"`
	FinalRejected               []NativeSearchFinalRejection `json:"final_rejected"`
	FinalErrorCode              string                       `json:"final_error_code"`
	Status                      string                       `json:"shadow_status"`
	Reason                      string                       `json:"shadow_reason"`
	ReaderAcknowledgement       string                       `json:"reader_acknowledgement"`
	ActualUse                   string                       `json:"actual_use"`
	TaskSuccess                 string                       `json:"task_success"`
	CausalAttribution           string                       `json:"causal_attribution"`
}

func cloneNativeSearchStages(v NativeSearchShadowStages) NativeSearchShadowStages {
	v.BaselinePreFinalCurrentRefs = slices.Clone(v.BaselinePreFinalCurrentRefs)
	v.BaselinePreFinalHistoryRefs = slices.Clone(v.BaselinePreFinalHistoryRefs)
	v.FullRankedRefs = slices.Clone(v.FullRankedRefs)
	v.PrepackRefs = slices.Clone(v.PrepackRefs)
	v.EmittedCurrentRefs = slices.Clone(v.EmittedCurrentRefs)
	v.EmittedHistoryRefs = slices.Clone(v.EmittedHistoryRefs)
	v.BaselineCurrentRefs = slices.Clone(v.BaselineCurrentRefs)
	v.BaselineHistoryRefs = slices.Clone(v.BaselineHistoryRefs)
	v.FinalRejected = slices.Clone(v.FinalRejected)
	return v
}
func (r *NativeSearchShadowRuntime) StagesV2() []NativeSearchShadowStages {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]NativeSearchShadowStages, len(r.stages))
	for i, v := range r.stages {
		out[i] = cloneNativeSearchStages(v)
	}
	return out
}
func nativeStageMaterialSHA(scope Scope, m nativeBridgeMaterial) string {
	return contextScopedRefSHA(scope, Ref{Collection: m.Collection, ID: m.ID, Revision: m.Revision})
}
func (a *nativeSearchAttempt) computedStages(scope Scope, computed nativeBridgeResult) {
	if computed.Status != "selected" {
		return
	}
	a.stages.FullRankedRefs = []string{}
	a.stages.PrepackRefs = []string{}
	a.stages.EmittedCurrentRefs = []string{}
	a.stages.EmittedHistoryRefs = []string{}
	for _, row := range computed.Ranked {
		a.stages.FullRankedRefs = append(a.stages.FullRankedRefs, nativeStageMaterialSHA(scope, row.Material))
	}
	for _, row := range computed.Prepack {
		a.stages.PrepackRefs = append(a.stages.PrepackRefs, nativeStageMaterialSHA(scope, row))
	}
	for _, row := range computed.Emitted {
		digest := nativeStageMaterialSHA(scope, row.Material)
		if row.History {
			a.stages.EmittedHistoryRefs = append(a.stages.EmittedHistoryRefs, digest)
		} else {
			a.stages.EmittedCurrentRefs = append(a.stages.EmittedCurrentRefs, digest)
		}
	}
}
func (a *nativeSearchAttempt) beginFinal() {
	if a == nil {
		return
	}
	a.stages.FinalEntered = true
	a.stages.FinalRejected = []NativeSearchFinalRejection{}
	a.stages.FinalErrorCode = "none"
}
func (a *nativeSearchAttempt) rejectFinal(h Hit, stage, code string) {
	if a == nil {
		return
	}
	a.stages.FinalRejected = append(a.stages.FinalRejected, NativeSearchFinalRejection{ScopedRefSHA: contextHitRefSHA(h), Stage: stage, Code: code})
}
func nativeFinalCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "caller_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, ErrConflict):
		return "conflict"
	case errors.Is(err, ErrDenied):
		return "access_denied"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrInvalid):
		return "invalid_request"
	default:
		return "transaction_abort"
	}
}
func (a *nativeSearchAttempt) failedFinal(err error) {
	if a != nil {
		a.stages.FinalErrorCode = nativeFinalCode(err)
	}
}
func observeSearchFinalRejection(in SearchInput, h Hit, stage, code string) {
	if in.finalRejected != nil {
		in.finalRejected(h, stage, code)
	}
}
