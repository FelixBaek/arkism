package memoryharness

import (
	"errors"
	"math"
	"testing"
)

// Golden constants and bit patterns are independent, fixed GV-DECAY-01..10
// expectations. These tests validate scalar arithmetic, not product quality.
func TestDecayGolden(t *testing.T) {
	tests := []struct {
		name                  string
		value, dt, half, want float64
	}{
		{"GV-DECAY-01", .8, 10, 10, .4},
		{"GV-DECAY-02", .8, 20, 10, .2},
		{"GV-DECAY-03", .8, 0, 10, .8},
		{"GV-DECAY-04", .8, math.Float64frombits(1), math.Float64frombits(1), .4},
		{"GV-DECAY-05", .8, math.Float64frombits(0x7fefffffffffffff), math.Float64frombits(1), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Decay(tt.value, tt.dt, tt.half)
			if err != nil {
				t.Fatal(err)
			}
			decayClose(t, got, tt.want)
			if tt.want == 0 && got != 0 {
				t.Fatalf("expected exact zero, got %g", got)
			}
		})
	}
}
func TestDecayInvalid(t *testing.T) {
	tests := []struct {
		name            string
		value, dt, half float64
		code, path      string
	}{
		{"GV-DECAY-06", .8, -1, 10, "invalid_time", "$.dtSeconds"},
		{"GV-DECAY-07_low", -.01, 10, 10, "invalid_state", "$.value"},
		{"GV-DECAY-07_high", 1.01, 10, 10, "invalid_state", "$.value"},
		{"GV-DECAY-08_zero", .8, 10, 0, "invalid_half_life", "$.halfLifeSeconds"},
		{"GV-DECAY-08_negative", .8, 10, -1, "invalid_half_life", "$.halfLifeSeconds"},
		{"negative_zero_half", .8, 10, math.Copysign(0, -1), "invalid_half_life", "$.halfLifeSeconds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { decayReject(t, tt.value, tt.dt, tt.half, tt.code, tt.path) })
	}
}
func TestDecayNonfinite(t *testing.T) {
	// Fixed GV-DECAY-09 input bit patterns, independently exercised in all inputs.
	for _, fp := range []struct {
		name string
		bits uint64
	}{{"positive_inf", 0x7ff0000000000000}, {"negative_inf", 0xfff0000000000000}, {"nan", 0x7ff8000000000001}} {
		v := math.Float64frombits(fp.bits)
		t.Run(fp.name+"_value", func(t *testing.T) { decayReject(t, v, 10, 10, "invalid_state", "$.value") })
		t.Run(fp.name+"_dt", func(t *testing.T) { decayReject(t, .8, v, 10, "invalid_time", "$.dtSeconds") })
		t.Run(fp.name+"_half", func(t *testing.T) { decayReject(t, .8, 10, v, "invalid_half_life", "$.halfLifeSeconds") })
	}
}
func TestDecayBoundaries(t *testing.T) {
	min := math.SmallestNonzeroFloat64
	tests := []struct {
		name                  string
		value, dt, half, want float64
		exact                 bool
	}{
		{"zero_value", 0, 10, 10, 0, true},
		{"maximum_state", 1, 10, 10, .5, true},
		{"zero_elapsed", .7, 0, 10, .7, true},
		{"negative_zero_elapsed", .7, math.Copysign(0, -1), 10, .7, true},
		{"negative_zero_state", math.Copysign(0, -1), 10, 10, 0, true},
		{"long_finite_gap_underflow", .8, 20000, 1, 0, true},
		{"largest_equal_times", .8, math.MaxFloat64, math.MaxFloat64, .4, false},
		{"smallest_state_no_elapsed", min, 0, 1, min, true},
		{"smallest_state_half_underflows", min, 1, 1, 0, true},
		{"ratio_underflows", .8, min, math.MaxFloat64, .8, true},
		{"zero_value_overflow_ratio", 0, math.MaxFloat64, min, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Decay(tt.value, tt.dt, tt.half)
			if err != nil {
				t.Fatal(err)
			}
			if tt.exact && got != tt.want {
				t.Fatalf("want exact %g got %g", tt.want, got)
			}
			decayClose(t, got, tt.want)
			if got == 0 && math.Signbit(got) {
				t.Fatal("negative zero result")
			}
		})
	}
}
func TestDecayErrorPrecedence(t *testing.T) {
	tests := []struct {
		name            string
		value, dt, half float64
		code, path      string
	}{
		{"all_nonfinite", math.NaN(), math.Inf(1), math.Inf(-1), "invalid_state", "$.value"},
		{"dt_before_half_nonfinite", .8, math.NaN(), math.Inf(1), "invalid_time", "$.dtSeconds"},
		{"finiteness_before_state_range", -1, math.Inf(1), 10, "invalid_time", "$.dtSeconds"},
		{"finiteness_before_time_range", .8, -1, math.Inf(1), "invalid_half_life", "$.halfLifeSeconds"},
		{"state_before_time_range", -1, -1, 0, "invalid_state", "$.value"},
		{"time_before_half_range", .8, -1, 0, "invalid_time", "$.dtSeconds"},
		{"zero_does_not_skip_invalid_half", 0, 10, 0, "invalid_half_life", "$.halfLifeSeconds"},
		{"zero_time_does_not_skip_invalid_state", 2, 0, 10, "invalid_state", "$.value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { decayReject(t, tt.value, tt.dt, tt.half, tt.code, tt.path) })
	}
}
func TestDecaySplitTime(t *testing.T) {
	for _, tt := range []struct {
		name              string
		value, a, b, half float64
	}{{"two_halves", .8, 5, 5, 10}, {"uneven", .7, 3, 17, 10}, {"zero_first", 1, 0, 30, 15}, {"tiny_times", .8, math.SmallestNonzeroFloat64, math.SmallestNonzeroFloat64, math.SmallestNonzeroFloat64}} {
		t.Run(tt.name, func(t *testing.T) {
			first, err := Decay(tt.value, tt.a, tt.half)
			if err != nil {
				t.Fatal(err)
			}
			split, err := Decay(first, tt.b, tt.half)
			if err != nil {
				t.Fatal(err)
			}
			whole, err := Decay(tt.value, tt.a+tt.b, tt.half)
			if err != nil {
				t.Fatal(err)
			}
			decayClose(t, split, whole)
		})
	}
}
func TestDecayMonotonicRange(t *testing.T) {
	// Supplementary deterministic grid; independent fixed goldens remain above.
	for _, v := range []float64{0, .1, .5, .8, 1} {
		prior := v
		for _, dt := range []float64{0, .1, 1, 10, 100, 10000} {
			got, err := Decay(v, dt, 10)
			if err != nil {
				t.Fatal(err)
			}
			if math.IsNaN(got) || math.IsInf(got, 0) || got < 0 || got > v || got > prior {
				t.Fatalf("out of range/nonmonotone result %g for %g/%g", got, v, dt)
			}
			prior = got
		}
	}
}
func TestDecayNanosecondDifference(t *testing.T) {
	// GV-DECAY-10: subtract int64 timestamps before converting the duration.
	// This is test-only usage guidance; Decay exposes seconds, not a clock API.
	from, to := int64(9223372036854775806), int64(9223372036854775807)
	delta := to - from
	if delta != 1 {
		t.Fatal("integer difference lost")
	}
	seconds := float64(delta) / 1e9
	if seconds != 1e-9 {
		t.Fatalf("want 1ns, got %g", seconds)
	}
	got, err := Decay(.8, seconds, 1e-9)
	if err != nil {
		t.Fatal(err)
	}
	decayClose(t, got, .4)
}
func TestDecayTauRelation(t *testing.T) {
	// tau=half/ln2 is a supplemental identity, not an alternative time unit.
	tau := 10 / math.Ln2
	got, err := Decay(.8, tau, 10)
	if err != nil {
		t.Fatal(err)
	}
	decayClose(t, got, 0.29430355293715383) // .8/e, independently tabulated.
}
func decayClose(t *testing.T, got, want float64) {
	t.Helper()
	if math.IsNaN(got) || math.IsInf(got, 0) || math.Abs(got-want) > 1e-12+1e-10*math.Abs(want) {
		t.Fatalf("want %.17g got %.17g", want, got)
	}
}
func decayReject(t *testing.T, value, dt, half float64, code, path string) {
	t.Helper()
	got, err := Decay(value, dt, half)
	var ce *ContractError
	if !errors.As(err, &ce) || ce.Code != code || ce.Path != path {
		t.Fatalf("want %s %s got %v", code, path, err)
	}
	if got != 0 {
		t.Fatalf("error returned value %g", got)
	}
}
