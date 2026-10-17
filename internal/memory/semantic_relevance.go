package memory

import "math"

// VectorMatch describes one same-space cosine comparison under the repository's
// configured distance ceiling. It is retrieval evidence, never memory content
// or a cross-model score. RRF remains a separate rank-fusion signal.
type VectorMatch struct {
	Space       string  `json:"space"`
	Distance    float64 `json:"distance"`
	MaxDistance float64 `json:"max_distance"`
}

func verifiedSemanticMatch(r Record, query []float64, space string, limit float64) *VectorMatch {
	if limit <= 0 || limit >= 2 || math.IsNaN(limit) || space == "" || len(query) != VectorDimension ||
		r.Embedding == nil || r.Embedding.Space != space || r.Embedding.Dimension != VectorDimension ||
		len(r.Embedding.Vector) != VectorDimension || r.Embedding.ContentHash != ContentHash(recordContent(r)) {
		return nil
	}
	var dot, queryNorm, documentNorm float64
	for i, q := range query {
		d := r.Embedding.Vector[i]
		if math.IsNaN(q) || math.IsInf(q, 0) || math.IsNaN(d) || math.IsInf(d, 0) {
			return nil
		}
		dot += q * d
		queryNorm += q * q
		documentNorm += d * d
	}
	if queryNorm == 0 || documentNorm == 0 || math.IsInf(dot, 0) || math.IsInf(queryNorm, 0) || math.IsInf(documentNorm, 0) {
		return nil
	}
	normProduct := math.Sqrt(queryNorm) * math.Sqrt(documentNorm)
	if normProduct == 0 || math.IsInf(normProduct, 0) {
		return nil
	}
	distance := 1 - dot/normProduct
	if math.IsNaN(distance) || math.IsInf(distance, 0) {
		return nil
	}
	// Floating-point roundoff can put identical unit vectors just below zero.
	distance = max(0, min(2, distance))
	if distance > limit {
		return nil
	}
	return &VectorMatch{Space: space, Distance: distance, MaxDistance: limit}
}
