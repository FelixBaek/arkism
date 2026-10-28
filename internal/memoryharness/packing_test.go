package memoryharness

import (
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"testing"
)

func packFixture(ids []string, costs []int64) []RankedCandidate {
	ranked := []RankedCandidate{}
	for i, id := range ids {
		c := rrfFixture(id, "text_search")
		c.ContextCostUnits = costs[i]
		ranked = append(ranked, RankedCandidate{c, 1.0 / float64(61+i), []int64{0}})
	}
	return ranked
}
func rankFixture(lanes [][]Candidate) (CandidateSnapshot, Probe, Policy) {
	scope := Scope{"ws", "proj", "usr", "agt"}
	s := CandidateSnapshot{"snapshot", "stream", scope, 100, 3, lanes}
	p := Probe{"query", "stream", scope, 100, 3, "snapshot", 3, ContextBudget{"fixture_units_v0", 10}, "b0-rrf-synthetic-v1"}
	policy := Policy{"arkism-offline-b0-v1", "b0-rrf-synthetic-v1", 60, 2, 1, "fixture_units_v0", "validate-filter-preserve-ordinal-fuse-pack"}
	return s, p, policy
}
func TestPackGolden(t *testing.T) {
	tests := []struct {
		name         string
		ids          []string
		costs        []int64
		budget, k    int64
		want         []string
		used, unique int64
	}{
		{"GV-PACK-01", []string{"a", "b", "c"}, []int64{4, 2, 1}, 3, 2, []string{"b", "c"}, 3, 3},
		{"GV-PACK-02_budget", []string{"a", "b", "c"}, []int64{4, 2, 1}, 0, 2, []string{}, 0, 3},
		{"GV-PACK-02_k", []string{"a", "b", "c"}, []int64{4, 2, 1}, 3, 0, []string{}, 0, 3},
		{"GV-PACK-03", []string{"a", "a", "b"}, []int64{1, 1, 1}, 2, 2, []string{"a", "b"}, 2, 2},
		{"GV-PACK-04", []string{"a", "b"}, []int64{5, 6}, 4, 2, []string{}, 0, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PackContext(packFixture(tt.ids, tt.costs), tt.k, ContextBudget{"fixture_units_v0", tt.budget})
			if err != nil {
				t.Fatal(err)
			}
			if got.Selected == nil || !slices.Equal(rrfIDs(got.Selected), tt.want) || got.UsedContextUnits != tt.used || got.EligibleUniqueCount != tt.unique {
				t.Fatalf("got %+v", got)
			}
		})
	}
}
func TestPackKeepsInputOrder(t *testing.T) {
	ranked := packFixture([]string{"b", "a"}, []int64{1, 1})
	ranked[0].Score = .1
	ranked[1].Score = 100
	got, err := PackContext(ranked, 2, ContextBudget{"fixture_units_v0", 2})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rrfIDs(got.Selected), []string{"b", "a"}) {
		t.Fatal("packing reordered supplied ranking")
	}
}
func TestPackValidationBeforeShortcut(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*[]RankedCandidate, *int64, *ContextBudget)
		code, path string
	}{
		{"nil_ranked", func(r *[]RankedCandidate, k *int64, b *ContextBudget) { *r = nil }, "invalid_type", "$.ranked"},
		{"negative_k", func(r *[]RankedCandidate, k *int64, b *ContextBudget) { *k = -1 }, "invalid_value", "$.final_k"},
		{"large_k", func(r *[]RankedCandidate, k *int64, b *ContextBudget) { *k = 65 }, "invalid_value", "$.final_k"},
		{"budget_unit", func(r *[]RankedCandidate, k *int64, b *ContextBudget) { b.Unit = "tokens" }, "invalid_value", "$.budget.unit"},
		{"budget_negative", func(r *[]RankedCandidate, k *int64, b *ContextBudget) { b.Limit = -1 }, "invalid_value", "$.budget.limit"},
		{"nil_lanes", func(r *[]RankedCandidate, k *int64, b *ContextBudget) { (*r)[0].Lanes = nil }, "invalid_type", "$.ranked[*].lanes"},
		{"nonfinite_score", func(r *[]RankedCandidate, k *int64, b *ContextBudget) { (*r)[0].Score = math.Inf(1) }, "invalid_value", "$.ranked[*].score"},
		{"zero_cost", func(r *[]RankedCandidate, k *int64, b *ContextBudget) { (*r)[0].Candidate.ContextCostUnits = 0 }, "invalid_value", "$.ranked[*].candidate.context_cost_units"},
		{"samekey_revision", func(r *[]RankedCandidate, k *int64, b *ContextBudget) {
			v := (*r)[0]
			v.Candidate.Ref.Revision++
			*r = append(*r, v)
		}, "snapshot_conflict", "$.ranked"},
		{"samekey_cost", func(r *[]RankedCandidate, k *int64, b *ContextBudget) {
			v := (*r)[0]
			v.Candidate.ContextCostUnits++
			*r = append(*r, v)
		}, "snapshot_conflict", "$.ranked"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ranked := packFixture([]string{"a"}, []int64{1})
			k := int64(0)
			budget := ContextBudget{"fixture_units_v0", 0}
			tt.mutate(&ranked, &k, &budget)
			_, err := PackContext(ranked, k, budget)
			rrfWantError(t, err, tt.code, tt.path)
		})
	}
	got, err := PackContext([]RankedCandidate{}, 0, ContextBudget{"fixture_units_v0", 0})
	if err != nil || got.Selected == nil || got.EligibleUniqueCount != 0 {
		t.Fatalf("empty %+v %v", got, err)
	}
}
func TestPackNonmutation(t *testing.T) {
	ranked := packFixture([]string{"a", "b"}, []int64{1, 1})
	before, _ := json.Marshal(ranked)
	first, err := PackContext(ranked, 2, ContextBudget{"fixture_units_v0", 2})
	if err != nil {
		t.Fatal(err)
	}
	second, err := PackContext(ranked, 2, ContextBudget{"fixture_units_v0", 2})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(ranked)
	if !slices.Equal(before, after) || !reflect.DeepEqual(first, second) {
		t.Fatal("packing mutated input or unstable")
	}
	first.Selected[0].Candidate.Reasons[0] = "vector"
	first.Selected[0].Lanes[0] = 2
	if ranked[0].Candidate.Reasons[0] != "text_search" || ranked[0].Lanes[0] != 0 || second.Selected[0].Lanes[0] != 0 {
		t.Fatal("packing output aliases input/other result")
	}
}
func TestRankAndPackGolden(t *testing.T) {
	t.Run("GV-RRF-08", func(t *testing.T) {
		a := rrfFixture("a", "text_search")
		a.Approved = false
		b := rrfFixture("b", "text_search")
		s, p, policy := rankFixture(rrfLanes([]Candidate{a, b}))
		got, err := RankAndPack(s, p, policy)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(rrfIDs(got.Selected), []string{"b"}) || got.EligibleUniqueCount != 1 {
			t.Fatalf("got %+v", got)
		}
		rrfWantScore(t, got.Selected[0].Score, 1.0/62)
	})
	t.Run("GV-RRF-09", func(t *testing.T) {
		ranked := packFixture([]string{"a", "b", "c"}, []int64{4, 2, 1})
		s, p, policy := rankFixture(rrfLanes([]Candidate{ranked[0].Candidate, ranked[1].Candidate, ranked[2].Candidate}))
		p.FinalK = 2
		p.ContextBudget.Limit = 3
		got, err := RankAndPack(s, p, policy)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(rrfIDs(got.Selected), []string{"b", "c"}) || got.UsedContextUnits != 3 || got.EligibleUniqueCount != 3 {
			t.Fatalf("earlyk/skip bug %+v", got)
		}
	})
}
func TestRankAndPackEligibility(t *testing.T) {
	flags := []struct {
		name   string
		change func(*Candidate)
	}{
		{"GV-ELIG-07_approved", func(c *Candidate) { c.Approved = false }},
		{"GV-ELIG-07_active", func(c *Candidate) { c.Active = false }},
		{"GV-ELIG-07_deleted", func(c *Candidate) { c.Deleted = true }},
		{"GV-ELIG-07_source", func(c *Candidate) { c.SourceVerified = false }},
	}
	for _, tt := range flags {
		t.Run(tt.name, func(t *testing.T) {
			c := rrfFixture("a", "exact_keyword")
			tt.change(&c)
			s, p, policy := rankFixture(rrfLanes([]Candidate{c}))
			got, err := RankAndPack(s, p, policy)
			if err != nil {
				t.Fatal(err)
			}
			if got.Selected == nil || len(got.Selected) != 0 || got.EligibleUniqueCount != 0 || got.UsedContextUnits != 0 {
				t.Fatalf("ineligible hit contributed %+v", got)
			}
		})
	}
}
func TestRankAndPackInvalidSnapshots(t *testing.T) {
	changes := []struct {
		name, code string
		change     func(*CandidateSnapshot, *Probe, *Policy)
	}{
		{"GV-ELIG-01", "snapshot_conflict", func(s *CandidateSnapshot, p *Probe, policy *Policy) { s.Lanes[0][0].Scope.User = "other" }},
		{"GV-ELIG-02", "snapshot_conflict", func(s *CandidateSnapshot, p *Probe, policy *Policy) {
			c := s.Lanes[0][0]
			c.Ref.Revision++
			s.Lanes[1] = []Candidate{c}
		}},
		{"GV-ELIG-03", "snapshot_conflict", func(s *CandidateSnapshot, p *Probe, policy *Policy) {
			c := s.Lanes[0][0]
			c.Ref.ContentSHA256 = rrfFixture("other").Ref.ContentSHA256
			s.Lanes[1] = []Candidate{c}
		}},
		{"GV-ELIG-04", "snapshot_conflict", func(s *CandidateSnapshot, p *Probe, policy *Policy) { s.Lanes[0][0].ObservedSeq = 4 }},
		{"GV-ELIG-05", "snapshot_conflict", func(s *CandidateSnapshot, p *Probe, policy *Policy) { s.Lanes[0][0].EffectiveAtNS = 101 }},
		{"GV-ELIG-06_time", "snapshot_binding", func(s *CandidateSnapshot, p *Probe, policy *Policy) { s.AsOfNS = 99 }},
		{"GV-ELIG-06_seq", "snapshot_binding", func(s *CandidateSnapshot, p *Probe, policy *Policy) { s.AsOfSeq = 2 }},
		{"GV-ELIG-08", "snapshot_conflict", func(s *CandidateSnapshot, p *Probe, policy *Policy) {
			c := s.Lanes[0][0]
			c.Approved = false
			s.Lanes[1] = []Candidate{c}
		}},
		{"GV-ELIG-09_cost", "snapshot_conflict", func(s *CandidateSnapshot, p *Probe, policy *Policy) {
			c := s.Lanes[0][0]
			c.ContextCostUnits++
			s.Lanes[1] = []Candidate{c}
		}},
		{"GV-ELIG-09_seq", "snapshot_conflict", func(s *CandidateSnapshot, p *Probe, policy *Policy) {
			c := s.Lanes[0][0]
			c.ObservedSeq++
			s.Lanes[1] = []Candidate{c}
		}},
		{"GV-ELIG-09_effective", "snapshot_conflict", func(s *CandidateSnapshot, p *Probe, policy *Policy) {
			c := s.Lanes[0][0]
			c.EffectiveAtNS++
			s.Lanes[1] = []Candidate{c}
		}},
		{"future_updated", "snapshot_conflict", func(s *CandidateSnapshot, p *Probe, policy *Policy) { s.Lanes[0][0].UpdatedAtNS = 101 }},
		{"snapshot_reference", "schedule_mismatch", func(s *CandidateSnapshot, p *Probe, policy *Policy) { p.CandidateSnapshotID = "missing" }},
		{"stream_binding", "snapshot_binding", func(s *CandidateSnapshot, p *Probe, policy *Policy) { p.StreamID = "other" }},
		{"scope_binding", "snapshot_binding", func(s *CandidateSnapshot, p *Probe, policy *Policy) { p.Scope.Agent = "other" }},
		{"policy", "invalid_value", func(s *CandidateSnapshot, p *Probe, policy *Policy) { policy.OtherWeight = 2 }},
	}
	for _, tt := range changes {
		t.Run(tt.name, func(t *testing.T) {
			s, p, policy := rankFixture(rrfLanes([]Candidate{rrfFixture("a", "vector")}))
			p.FinalK = 0
			p.ContextBudget.Limit = 0
			tt.change(&s, &p, &policy)
			_, err := RankAndPack(s, p, policy)
			rrfWantError(t, err, tt.code, "")
		})
	}
}
func TestRankAndPackEmptyBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name      string
		k, budget int64
	}{{"k_zero", 0, 10}, {"budget_zero", 3, 0}, {"both_zero", 0, 0}} {
		t.Run(tt.name, func(t *testing.T) {
			s, p, policy := rankFixture(rrfLanes([]Candidate{rrfFixture("a", "vector")}))
			p.FinalK = tt.k
			p.ContextBudget.Limit = tt.budget
			got, err := RankAndPack(s, p, policy)
			if err != nil {
				t.Fatal(err)
			}
			if got.Selected == nil || len(got.Selected) != 0 || got.EligibleUniqueCount != 1 {
				t.Fatalf("got %+v", got)
			}
		})
	}
	s, p, policy := rankFixture(rrfLanes())
	got, err := RankAndPack(s, p, policy)
	if err != nil || got.Selected == nil || got.EligibleUniqueCount != 0 {
		t.Fatalf("empty %+v %v", got, err)
	}
}
func TestRankAndPackNonmutation(t *testing.T) {
	s, p, policy := rankFixture(rrfLanes([]Candidate{rrfFixture("a", "text_search")}, []Candidate{rrfFixture("a", "vector")}))
	before, _ := json.Marshal(s)
	got, err := RankAndPack(s, p, policy)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(s)
	if !slices.Equal(before, after) {
		t.Fatal("snapshot mutated")
	}
	got.Selected[0].Candidate.Reasons[0] = "exact_keyword"
	if s.Lanes[0][0].Reasons[0] != "text_search" {
		t.Fatal("selected aliases snapshot")
	}
}
