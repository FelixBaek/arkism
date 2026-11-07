package memory

import (
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Per-lane recall weights. Fusion (fuseWeighted) counts a hit at rank r in a lane of
// weight w as if it were at rank r/w: 2 lets the lane's rank 2 tie another lane's rank 1,
// 0.5 halves the lane's pull. The weights come from, per lane, the request's lane_weights,
// else the space's recall_lane_weights (inherited from the template, then the server's
// ARKISM_RECALL_LANE_WEIGHTS, e.g. "graph=2,temporal=4"); a lane named nowhere weighs 1.

// recallLaneNames are the recall lanes, in the fixed order hits list them.
var recallLaneNames = []string{"exact", "keyword", "vector", "relation", "temporal", "graph", "causal", "semantic"}

const (
	laneWeightMin = 0.25
	laneWeightMax = 8
)

func validLaneWeights(w map[string]float64) bool {
	if len(w) > len(recallLaneNames) {
		return false
	}
	for lane, v := range w {
		if !slices.Contains(recallLaneNames, lane) || math.IsNaN(v) || math.IsInf(v, 0) || v < laneWeightMin || v > laneWeightMax {
			return false
		}
	}
	return true
}

// serverLaneWeights reads ARKISM_RECALL_LANE_WEIGHTS ("lane=weight,..."); entries it cannot read are skipped.
func serverLaneWeights() map[string]float64 {
	out := map[string]float64{}
	for _, part := range strings.Split(os.Getenv("ARKISM_RECALL_LANE_WEIGHTS"), ",") {
		lane, raw, _ := strings.Cut(strings.TrimSpace(part), "=")
		if v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil && validLaneWeights(map[string]float64{strings.TrimSpace(lane): v}) {
			out[strings.TrimSpace(lane)] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// laneWeights is the space's weights with the request's laid over them; nil when all are 1.
func (in SearchInput) laneWeights() map[string]float64 {
	out := map[string]float64{}
	if in.space != nil {
		maps.Copy(out, in.space.RecallLaneWeights)
	}
	maps.Copy(out, in.LaneWeights)
	maps.DeleteFunc(out, func(_ string, v float64) bool { return v == 1 })
	if len(out) == 0 {
		return nil
	}
	return out
}

// laneWeight is the weight of the lane a pre-fusion hit came from (its first lane reason).
func laneWeight(weights map[string]float64, reasons []string) float64 {
	for _, r := range reasons {
		if lane, ok := laneByReason[r]; ok {
			if w, ok := weights[lane]; ok {
				return w
			}
			return 1
		}
	}
	return 1
}

func isNeighborReason(r string) bool {
	return r == reasonGraph || r == reasonCausal || r == reasonSemantic
}
