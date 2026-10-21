package memory

import (
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
	got := decayByRecency([]ScoredHit{stale, fresh, rule}, now, 2)
	if len(got) != 2 || got[0].ID != "rule" || got[1].ID != "fresh" {
		t.Fatalf("got %v", []string{got[0].ID, got[1].ID})
	}
}
