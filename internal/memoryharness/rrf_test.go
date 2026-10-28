package memoryharness

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"reflect"
	"slices"
	"testing"
)

func rrfFixture(id string, reasons ...string) Candidate {
	hash := sha256.Sum256([]byte(id))
	return Candidate{Scope: Scope{"ws", "proj", "usr", "agt"}, Ref: Ref{"memory", "memories", id, 1, hex.EncodeToString(hash[:])}, Reasons: append([]string{}, reasons...), Trust: .5, UpdatedAtNS: 10, ObservedSeq: 1, EffectiveAtNS: 10, ContextCostUnits: 1, Approved: true, Active: true, SourceVerified: true, Authority: "data_only_untrusted"}
}
func rrfLanes(lists ...[]Candidate) [][]Candidate {
	lanes := [][]Candidate{{}, {}, {}}
	for i, list := range lists {
		lanes[i] = append([]Candidate{}, list...)
	}
	return lanes
}
func rrfWantScore(t *testing.T, got, want float64) {
	t.Helper()
	if math.IsNaN(got) || math.Abs(got-want) > 1e-12+1e-10*math.Abs(want) {
		t.Fatalf("score want %.17g got %.17g", want, got)
	}
}
func rrfWantError(t *testing.T, err error, code, path string) {
	t.Helper()
	var ce *ContractError
	if !errors.As(err, &ce) || ce.Code != code || path != "" && ce.Path != path {
		t.Fatalf("want %s %s got %v", code, path, err)
	}
}
func rrfIDs(ranked []RankedCandidate) []string {
	ids := []string{}
	for _, r := range ranked {
		ids = append(ids, r.Candidate.Ref.ID)
	}
	return ids
}
func TestRRFGolden(t *testing.T) {
	a := rrfFixture("a", "text_search")
	b := rrfFixture("b", "text_search")
	c := rrfFixture("c", "text_search")
	exact := rrfFixture("a", "exact_keyword")
	vector := rrfFixture("a", "vector")
	alt := a
	alt.Ref.Origin = "fixture_alt"
	tests := []struct {
		name    string
		lanes   [][]Candidate
		ids     []string
		scores  []float64
		reasons []string
		laneIDs []int64
	}{
		{"GV-RRF-01", rrfLanes([]Candidate{exact}), []string{"a"}, []float64{2.0 / 61}, []string{"exact_keyword"}, []int64{0}},
		{"GV-RRF-02", rrfLanes([]Candidate{vector}), []string{"a"}, []float64{1.0 / 61}, []string{"vector"}, []int64{0}},
		{"GV-RRF-03", rrfLanes([]Candidate{exact}, []Candidate{a}), []string{"a"}, []float64{3.0 / 61}, []string{"exact_keyword", "text_search"}, []int64{0, 1}},
		{"GV-RRF-04", rrfLanes([]Candidate{a, exact, b}, []Candidate{vector, c}), []string{"a", "c", "b"}, []float64{2.0 / 61, 1.0 / 62, 1.0 / 63}, []string{"text_search", "vector"}, []int64{0, 1}},
		{"GV-RRF-05", rrfLanes([]Candidate{a}, []Candidate{alt}), []string{"a", "a"}, []float64{1.0 / 61, 1.0 / 61}, []string{"text_search"}, []int64{1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RRFReference(tt.lanes, 10)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(rrfIDs(got), tt.ids) {
				t.Fatalf("order %v want %v", rrfIDs(got), tt.ids)
			}
			for i, want := range tt.scores {
				rrfWantScore(t, got[i].Score, want)
			}
			if !slices.Equal(got[0].Candidate.Reasons, tt.reasons) || !slices.Equal(got[0].Lanes, tt.laneIDs) {
				t.Fatalf("reasons/lanes: %+v", got[0])
			}
			if tt.name == "GV-RRF-05" && got[0].Candidate.Ref.Origin != "fixture_alt" {
				t.Fatal("origin merged or tie key changed")
			}
		})
	}
}
func TestRRFTieGolden(t *testing.T) {
	tests := []struct {
		name               string
		aTrust, bTrust     float64
		aUpdated, bUpdated int64
		want               []string
	}{
		{"GV-RRF-06_trust", .4, .8, 99, 10, []string{"b", "a"}},
		{"GV-RRF-06_updated", .5, .5, 10, 20, []string{"b", "a"}},
		{"GV-RRF-06_key", .5, .5, 10, 10, []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := rrfFixture("a", "text_search")
			b := rrfFixture("b", "text_search")
			a.Trust = tt.aTrust
			b.Trust = tt.bTrust
			a.UpdatedAtNS = tt.aUpdated
			b.UpdatedAtNS = tt.bUpdated
			got, err := RRFReference(rrfLanes([]Candidate{a}, []Candidate{b}), 10)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(rrfIDs(got), tt.want) {
				t.Fatalf("order %v", rrfIDs(got))
			}
		})
	}
}
func TestRRFFirstMetadataGolden(t *testing.T) {
	a := rrfFixture("a", "text_search")
	a.Trust = .2
	later := rrfFixture("a", "vector")
	later.Trust = .99
	later.UpdatedAtNS = 99
	b := rrfFixture("b", "exact_keyword")
	got, err := RRFReference(rrfLanes([]Candidate{a}, []Candidate{later}, []Candidate{b}), 10)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rrfIDs(got), []string{"b", "a"}) {
		t.Fatalf("GV-RRF-07 order %v", rrfIDs(got))
	}
	if got[1].Candidate.Trust != .2 || got[1].Candidate.UpdatedAtNS != 10 || !slices.Equal(got[1].Candidate.Reasons, []string{"text_search", "vector"}) {
		t.Fatalf("first metadata changed %+v", got[1])
	}
	rrfWantScore(t, got[0].Score, 2.0/61)
	rrfWantScore(t, got[1].Score, 2.0/61)
}
func TestRRFWeightPerOccurrence(t *testing.T) {
	first := rrfFixture("a", "exact_keyword")
	second := rrfFixture("a", "text_search")
	got, err := RRFReference(rrfLanes([]Candidate{first}, []Candidate{second}), 10)
	if err != nil {
		t.Fatal(err)
	}
	rrfWantScore(t, got[0].Score, 3.0/61)
	// Exact weight comes from the hit reasons even in the vector lane.
	got, err = RRFReference(rrfLanes(nil, nil, []Candidate{first}), 10)
	if err != nil {
		t.Fatal(err)
	}
	rrfWantScore(t, got[0].Score, 2.0/61)
}
func TestRRFSameKeyConflicts(t *testing.T) {
	changes := []struct {
		name  string
		apply func(*Candidate)
	}{
		{"scope", func(c *Candidate) { c.Scope.User = "other" }},
		{"revision", func(c *Candidate) { c.Ref.Revision++ }},
		{"hash", func(c *Candidate) { c.Ref.ContentSHA256 = rrfFixture("other").Ref.ContentSHA256 }},
		{"approved", func(c *Candidate) { c.Approved = false }},
		{"active", func(c *Candidate) { c.Active = false }},
		{"deleted", func(c *Candidate) { c.Deleted = true }},
		{"source", func(c *Candidate) { c.SourceVerified = false }},
		{"cost", func(c *Candidate) { c.ContextCostUnits++ }},
		{"observed", func(c *Candidate) { c.ObservedSeq++ }},
		{"effective", func(c *Candidate) { c.EffectiveAtNS++ }},
	}
	for _, tt := range changes {
		t.Run(tt.name, func(t *testing.T) {
			a := rrfFixture("a", "vector")
			b := a
			tt.apply(&b)
			for _, lanes := range [][][]Candidate{rrfLanes([]Candidate{a, b}), rrfLanes([]Candidate{a}, []Candidate{b})} {
				_, err := RRFReference(lanes, 0)
				rrfWantError(t, err, "snapshot_conflict", "$")
			}
		})
	}
}
func TestRRFValidationBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		lanes      [][]Candidate
		code, path string
	}{
		{"nil_outer", nil, "invalid_value", "$.lanes"},
		{"two_lanes", [][]Candidate{{}, {}}, "invalid_value", "$.lanes"},
		{"four_lanes", [][]Candidate{{}, {}, {}, {}}, "invalid_value", "$.lanes"},
		{"nil_inner", [][]Candidate{{}, nil, {}}, "invalid_type", "$.lanes[*]"},
		{"malformed", rrfLanes([]Candidate{{}}), "invalid_value", "$.lanes[*][*].scope.workspace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { _, err := RRFReference(tt.lanes, -1); rrfWantError(t, err, tt.code, tt.path) })
	}
	for _, limit := range []int{-1, 0, 1} {
		got, err := RRFReference(rrfLanes(), limit)
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("empty result %+v %v", got, err)
		}
	}
	a := rrfFixture("a", "vector")
	a.Approved = false
	got, err := RRFReference(rrfLanes([]Candidate{a}), 10)
	if err != nil || len(got) != 1 {
		t.Fatal("pure reference filtered eligibility", err)
	}
}
func TestRRFNoEpsilonTies(t *testing.T) {
	// Two entries differing by one representable float are not a tie.
	a := &rrfEntry{ranked: RankedCandidate{Candidate: rrfFixture("a"), Score: 1}, key: "a"}
	b := &rrfEntry{ranked: RankedCandidate{Candidate: rrfFixture("b"), Score: math.Nextafter(1, 2)}, key: "b"}
	if rrfBefore(a, b) || !rrfBefore(b, a) {
		t.Fatal("score comparison used epsilon tie")
	}
}
func TestRRFNonmutation(t *testing.T) {
	a := rrfFixture("a", "text_search")
	a.Reasons = make([]string, 1, 4)
	a.Reasons[0] = "text_search"
	backing := a.Reasons[:4]
	backing[1] = "sentinel"
	lanes := rrfLanes([]Candidate{a}, []Candidate{rrfFixture("a", "vector")})
	before := append([]string{}, backing...)
	first, err := RRFReference(lanes, 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RRFReference(lanes, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || !slices.Equal(backing, before) {
		t.Fatal("input changed or repeated output unstable")
	}
	first[0].Candidate.Reasons[0] = "exact_keyword"
	first[0].Lanes[0] = 2
	if lanes[0][0].Reasons[0] != "text_search" || second[0].Candidate.Reasons[0] != "text_search" || second[0].Lanes[0] != 0 {
		t.Fatal("output aliases caller or earlier result")
	}
}
