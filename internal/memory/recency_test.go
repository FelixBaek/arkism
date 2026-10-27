package memory

import (
	"math"
	"testing"
	"time"
)

func TestRecencyFactor(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	ago := func(days int) time.Time { return now.Add(-time.Duration(days) * 24 * time.Hour) }
	for _, c := range []struct {
		kind string
		at   time.Time
		want float64
	}{
		{"episodic", ago(14), 0.75},
		{"decision", ago(60), 0.75},
		{"lesson", ago(0), 1},
		{"episodic", ago(3650), 0.5},
		{"procedural", ago(3650), 1},
		{"user", ago(3650), 1},
		{"lesson", time.Time{}, 1},
	} {
		if got := recencyFactor(Hit{Kind: c.kind, UpdatedAt: c.at}, now); got < c.want-1e-9 || got > c.want+1e-9 {
			t.Errorf("%s %v: got %v, want %v", c.kind, now.Sub(c.at), got, c.want)
		}
	}
	effective := ago(0)
	if got := recencyFactor(Hit{Kind: "episodic", UpdatedAt: ago(3650), EffectiveAt: &effective}, now); got != 1 {
		t.Errorf("effective_at should win over updated_at, got %v", got)
	}
	created := ago(3650)
	if got := recencyFactor(Hit{Kind: "episodic", UpdatedAt: ago(0), CreatedAt: &created}, now); got > 0.5+1e-9 {
		t.Errorf("a metadata edit must not make an old memory fresh, got %v", got)
	}
}

func TestDecayByRecencyReordersAndLimits(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	stale := ScoredHit{Hit: Hit{ID: "stale", Kind: "episodic", UpdatedAt: now.Add(-90 * 24 * time.Hour)}, Score: 1.0}
	fresh := ScoredHit{Hit: Hit{ID: "fresh", Kind: "episodic", UpdatedAt: now}, Score: 0.8}
	rule := ScoredHit{Hit: Hit{ID: "rule", Kind: "procedural", UpdatedAt: now.Add(-900 * 24 * time.Hour)}, Score: 0.9}
	got := decayByRecency([]ScoredHit{stale, fresh, rule}, now, nil, 2)
	if len(got) != 2 || got[0].ID != "rule" || got[1].ID != "fresh" {
		t.Fatalf("got %v", []string{got[0].ID, got[1].ID})
	}
}

func TestDecayByRecencyAppliesOutcomeWeights(t *testing.T) {
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	a := ScoredHit{Hit: Hit{ID: "a", Kind: "procedural", UpdatedAt: now}, Score: 1.0}
	b := ScoredHit{Hit: Hit{ID: "b", Kind: "procedural", UpdatedAt: now}, Score: 0.9}
	got := decayByRecency([]ScoredHit{a, b}, now, map[string]float64{"a": 0.1}, 2)
	if got[0].ID != "b" || got[1].ID != "a" || math.Abs(got[1].Score-0.1) > 1e-9 {
		t.Fatalf("got %+v", got)
	}
}

func TestOutcomeFactor(t *testing.T) {
	if outcomeFactor(nil) != 1 {
		t.Fatal("a missing signal must weigh 1")
	}
	for _, c := range []struct {
		samples, passed int
		want            float64
	}{{0, 0, 1}, {1, 1, 1.05}, {3, 3, 1.15}, {7, 7, 1.15}, {2, 1, 0.105}, {1, 0, 0.1}} {
		if got := outcomeFactor(&MemorySignal{Samples: c.samples, Passed: c.passed}); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%d/%d: got %v, want %v", c.passed, c.samples, got, c.want)
		}
	}
}

func TestOutcomeWeightsReadStoredSignals(t *testing.T) {
	f := newFixture(t)
	scope := Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u"}
	for id, sig := range map[string]MemorySignal{"good": {Samples: 2, Passed: 2}, "bad": {Samples: 2, Passed: 1}, "unjudged": {}} {
		sig.Memory = Ref{Collection: colMemories, ID: id, Revision: 1}
		if e := f.repo.Insert(testCtx, colJobs, Record{ID: memorySignalID(scope, id), Scope: scope, Kind: memorySignalKind, Status: JobStatusDone, Revision: 1, MemorySignal: &sig}); e != nil {
			t.Fatal(e)
		}
	}
	rows := []ScoredHit{{Hit: Hit{ID: "good"}}, {Hit: Hit{ID: "bad"}}, {Hit: Hit{ID: "unjudged"}}, {Hit: Hit{ID: "none"}}}
	got := f.svc.outcomeWeights(testCtx, scope, rows)
	if len(got) != 2 || math.Abs(got["good"]-1.1) > 1e-9 || math.Abs(got["bad"]-0.105) > 1e-9 {
		t.Fatalf("weights = %v", got)
	}
	f.svc.SignalRankingOff = true
	if got := f.svc.outcomeWeights(testCtx, scope, rows); got != nil {
		t.Fatalf("off switch still weighs: %v", got)
	}
}
