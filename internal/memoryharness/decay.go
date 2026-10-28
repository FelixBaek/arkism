package memoryharness

import "math"

// Decay applies exact exponential half-life decay to a normalized scalar.
// dtSeconds and halfLifeSeconds use seconds; tau = halfLifeSeconds / ln(2).
// Inputs must be finite, value must be in [0,1], dtSeconds must be nonnegative,
// and halfLifeSeconds must be positive. Invalid inputs are never clamped.
// This isolated calculation does not change B0 ranking or product state.
func Decay(value, dtSeconds, halfLifeSeconds float64) (float64, error) {
	// Reject all nonfinite inputs before range checks or zero shortcuts.
	for _, input := range []struct {
		value      float64
		code, path string
	}{
		{value, "invalid_state", "$.value"},
		{dtSeconds, "invalid_time", "$.dtSeconds"},
		{halfLifeSeconds, "invalid_half_life", "$.halfLifeSeconds"},
	} {
		if math.IsNaN(input.value) || math.IsInf(input.value, 0) {
			return 0, contractError(input.code, input.path)
		}
	}
	if value < 0 || value > 1 {
		return 0, contractError("invalid_state", "$.value")
	}
	if dtSeconds < 0 {
		return 0, contractError("invalid_time", "$.dtSeconds")
	}
	if halfLifeSeconds <= 0 {
		return 0, contractError("invalid_half_life", "$.halfLifeSeconds")
	}
	// Divide first: multiplication before division changes subnormal inputs.
	ratio := dtSeconds / halfLifeSeconds
	if math.IsInf(ratio, 1) {
		return 0, nil
	}
	result := value * math.Exp(-math.Ln2*ratio)
	// Valid large positive elapsed times may underflow. Normalize signed zero.
	if result == 0 {
		return 0, nil
	}
	return result, nil
}
