package dynamicsv2

import "math"

// NativeNumericLanePrototype carries only already-derived numeric rank inputs.
// It does not represent a policy decision, registration, identity or authority.
type NativeNumericLanePrototype struct {
	Kind      string
	Weight    float64
	DenseRank int64
}

// NativeFusionScorePrototype reuses policyScore without exporting private DTOs.
// No Ref/Scope/source/clock/packing validity is asserted by this numeric API.
func NativeFusionScorePrototype(lanes []NativeNumericLanePrototype) (float64, error) {
	if len(lanes) < 1 || len(lanes) > 3 {
		return 0, contractError("invalid_value", "$.lanes")
	}
	row := RankRow{Auxiliary: []AuxRank{}}
	cfg := RankerConfigV2{PolicyID: "B1", Lanes: []RankLane{}}
	seen := map[string]bool{}
	positive := false
	for i, lane := range lanes {
		if !nativeNumericLanePrototypeValid(lane, i, seen) {
			return 0, contractError("invalid_value", "$.lanes")
		}
		seen[lane.Kind] = true
		positive = positive || lane.Weight > 0
		cfg.Lanes = append(cfg.Lanes, RankLane{Kind: lane.Kind, Weight: lane.Weight})
		if i == 0 {
			row.BaseDenseRank = lane.DenseRank
		} else {
			row.Auxiliary = append(row.Auxiliary, AuxRank{Kind: lane.Kind, DenseRank: lane.DenseRank})
		}
	}
	if !positive {
		return 0, contractError("invalid_value", "$.lanes")
	}
	if err := policyScore(&row, cfg); err != nil {
		return 0, err
	}
	return row.FusionScore, nil
}

func nativeNumericLanePrototypeValid(lane NativeNumericLanePrototype, index int, seen map[string]bool) bool {
	if math.IsNaN(lane.Weight) || math.IsInf(lane.Weight, 0) || lane.Weight < 0 || lane.Weight > 1 || lane.DenseRank < 1 || lane.DenseRank > 200 || seen[lane.Kind] {
		return false
	}
	if index == 0 {
		return lane.Kind == "relevance"
	}
	return lane.Kind == "state" || lane.Kind == "recency" || lane.Kind == "importance"
}

// NativeDecayPrototype is arithmetic only; caller must bind the real clocks.
func NativeDecayPrototype(value float64, elapsedNS, tauNS int64) (float64, error) {
	return scalarDecay(value, elapsedNS, tauNS)
}

// NativeAdvanceScalarNumeric is numeric arithmetic, not an event transition.
// Caller owns real clocks/config/qualification/dedup/currentness; no DTO escapes.
// decay_only explicitly has no observed strength; pass requires its actual value.
func NativeAdvanceScalarNumeric(value float64, previousNS, currentNS, tauNS int64, decayEnabled bool, operation string, etaPositive, unitStrength float64, strength *float64) (float64, error) {
	if operation != "decay_only" && operation != "pass" {
		return 0, contractError("invalid_value", "$.outcome")
	}
	if operation == "decay_only" && strength != nil {
		return 0, contractError("invalid_value", "$.strength")
	}
	if operation == "pass" && strength == nil {
		return 0, contractError("invalid_type", "$.strength")
	}
	if err := nativeScalarNumericOperands(value, etaPositive, unitStrength, strength); err != nil {
		return 0, err
	}
	if tauNS <= 0 {
		return 0, contractError("invalid_value", "$.tau_ns")
	}
	if currentNS < previousNS || (previousNS < 0 && currentNS > math.MaxInt64+previousNS) {
		return 0, contractError("invalid_time", "$.at_ns")
	}
	elapsed := currentNS - previousNS
	result := value
	if decayEnabled {
		var err error
		result, err = NativeDecayPrototype(value, elapsed, tauNS)
		if err != nil {
			return 0, err
		}
	}
	if operation == "pass" {
		// These partial structs are solely scalarJump operands. No marshal, Validate,
		// qualified(), Event admission, source/authority mapping or external return.
		eventOperand := EventV2{Outcome: "passed", Strength: strength}
		configOperand := DynamicsConfigV2{EtaPositive: etaPositive, UnitStrength: unitStrength}
		result = scalarJump(result, eventOperand, configOperand)
	}
	return result, nil
}

func nativeScalarNumericOperands(value, etaPositive, unitStrength float64, strength *float64) error {
	operands := []struct {
		value      float64
		code, path string
	}{{value, "invalid_state", "$.value"}, {etaPositive, "invalid_value", "$.eta_positive"}, {unitStrength, "invalid_value", "$.unit_strength"}}
	if strength != nil {
		operands = append(operands, struct {
			value      float64
			code, path string
		}{*strength, "invalid_value", "$.strength"})
	}
	// Preserve explicit validation even if decay is disabled, value/eta is zero,
	// or the arithmetic would otherwise have no effect. Never clamp invalid input.
	for _, operand := range operands {
		if math.IsNaN(operand.value) || math.IsInf(operand.value, 0) {
			return contractError(operand.code, operand.path)
		}
	}
	for _, operand := range operands {
		if operand.value < 0 || operand.value > 1 {
			return contractError(operand.code, operand.path)
		}
	}
	return nil
}
