package dynamicsv2

import "github.com/FelixBaek/arkism/internal/memoryharness"

// RankPolicyP1 consumes an explicit importance sidecar; it never reads files,
// guesses missing values or creates state. Non-IR policies share the same fence.
func RankPolicyP1(snapshot memoryharness.CandidateSnapshot, probe ProbeV2, views []StateViewV2, features []ImportanceFeatureV1, cfg RankerConfigV2) (PolicyDecisionV2, error) {
	return rankPolicy(snapshot, probe, views, features, cfg, true, nil)
}

type importanceLineage struct {
	version, rule, config, producer string
}

func policyImportance(features []ImportanceFeatureV1, target RefV2, q ProbeV2) (float64, error) {
	var selected *ImportanceFeatureV1
	var lineage importanceLineage
	ambiguous := false
	for i := range features {
		feature := &features[i]
		// A valid future lineage is not evidence about this earlier query.
		if feature.AvailableAtNS > q.AtNS || feature.AvailableAcceptedSeq > q.CutoffAcceptedSeq {
			continue
		}
		if feature.Target != target {
			continue
		}
		if err := importanceContext(*feature, q); err != nil {
			return 0, err
		}
		current := importanceLineage{feature.FeatureVersion, feature.RuleSHA256, feature.GeneratorConfigSHA256, feature.ProducerVersion}
		if selected != nil && current != lineage {
			return 0, contractError("missing_feature", "$.features")
		}
		if selected == nil {
			selected, lineage = feature, current
			continue
		}
		order := importanceAvailability(*feature, *selected)
		if order == 0 {
			ambiguous = true
		}
		if order > 0 {
			selected = feature
			ambiguous = false
		}
	}
	if selected == nil || ambiguous || selected.Status != "present" || selected.Value == nil {
		return 0, contractError("missing_feature", "$.features")
	}
	return *selected.Value, nil
}

func importanceContext(feature ImportanceFeatureV1, q ProbeV2) error {
	if feature.Scope != q.Scope {
		return contractError("scope_mismatch", "$.features[*].scope")
	}
	if feature.ClockDomain != q.ClockDomain {
		return contractError("clock_domain_mismatch", "$.features[*].clock_domain")
	}
	if feature.ObservedAtNS != nil && *feature.ObservedAtNS > q.AtNS {
		return contractError("future_event", "$.features[*].observed_at_ns")
	}
	return nil
}

func importanceAvailability(a, b ImportanceFeatureV1) int {
	if a.AvailableAcceptedSeq > b.AvailableAcceptedSeq {
		return 1
	}
	if a.AvailableAcceptedSeq < b.AvailableAcceptedSeq {
		return -1
	}
	if a.AvailableAtNS > b.AvailableAtNS {
		return 1
	}
	if a.AvailableAtNS < b.AvailableAtNS {
		return -1
	}
	return 0
}

// policyFDView is called only with the explicitly selected FD policy context by
// the future runner. Flags do not identify a policy: NOOUT can have equal flags.
// Validate the real snapshot/read fence, then evaluate from its immutable anchor.
func policyFDView(snapshot SnapshotV2, atNS int64, target RefV2, cfg DynamicsConfigV2) (StateViewV2, error) {
	view, err := ReadAt(snapshot, atNS, target, cfg)
	if err != nil {
		return StateViewV2{}, err
	}
	if !cfg.DecayEnabled || cfg.OutcomeJumpEnabled {
		return StateViewV2{}, contractError("config_mismatch", "$.config_sha256")
	}
	value, err := scalarDecay(cfg.Initial, atNS-view.ActivatedAtNS, cfg.TauNS)
	if err != nil {
		return StateViewV2{}, policyError(err)
	}
	view.Value = value
	if err := ValidateTyped("StateViewV2", view); err != nil {
		return StateViewV2{}, err
	}
	return view, nil
}

// RunPoliciesP1 uses the explicit shared sidecar; it does not materialize EXP01
// semantic plans or acquire product approval/authority for native input records.
func RunPoliciesP1(events EventFileV2, probes ProbeFileV2, candidates memoryharness.CandidateFile, features P1FeatureFileV1, cfg DynamicsRunConfigV2) ([]PredictionRunV2, error) {
	if err := ValidateTyped("P1FeatureFileV1", features); err != nil {
		return nil, err
	}
	return runPolicies(events, probes, candidates, features.Features, cfg, true)
}
