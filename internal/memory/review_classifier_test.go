package memory

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Rejections sit on one side of a direction in embedding space; training must find it.
func separableExamples(n int) []reviewExample {
	rng := rand.New(rand.NewSource(7))
	var ex []reviewExample
	for i := range n {
		rejected := i%8 == 0
		vec := make([]float64, 6)
		for j := range vec {
			vec[j] = rng.NormFloat64() * 0.3
		}
		if rejected {
			vec[0] += 1
		} else {
			vec[0] -= 1
		}
		ex = append(ex, reviewExample{vec: vec, kind: "lesson", rejected: rejected})
	}
	return ex
}

func TestTrainReviewClassifierLearnsRareRejects(t *testing.T) {
	ex := separableExamples(160)
	rep := crossValidate(ex)
	if rep.Rejected != 20 || !rep.MinimumReached || rep.AUC < 0.95 || rep.RejectsFound < 18 || rep.FalseRejects > 10 {
		t.Fatalf("weak classifier on separable data: %+v", rep)
	}
	c := trainReviewClassifier(ex, "fake")
	if c.RejectScore([]float64{1, 0, 0, 0, 0, 0}, "lesson") < 0.5 || c.RejectScore([]float64{-1, 0, 0, 0, 0, 0}, "lesson") > 0.5 {
		t.Fatal("classifier does not separate the two sides")
	}
	if one := trainReviewClassifier(ex[1:8], "fake"); one.Rejected != 0 || len(one.Weights) != 6 {
		t.Fatal("no rejections must leave an untrained, neutral model")
	}
}

func TestAUC(t *testing.T) {
	if got := auc([]float64{0.9, 0.8}, []float64{0.1, 0.2}); got != 1 {
		t.Fatal(got)
	}
	if got := auc([]float64{0.5}, []float64{0.5}); got != 0.5 {
		t.Fatal(got)
	}
}

func TestLoadReviewClassifier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.json")
	data, _ := json.Marshal(ReviewClassifier{Scope: scopeA, Space: "s", Weights: []float64{1}, Kinds: map[string]float64{}})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := LoadReviewClassifier(path); err != nil || c.Space != "s" {
		t.Fatal(c, err)
	}
	if err := os.WriteFile(path, []byte(`{"space":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReviewClassifier(path); err != ErrInvalid {
		t.Fatal("empty model accepted", err)
	}
}

func TestLoadReviewClassifierRequiresScopeAndBoundsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.json")
	for _, data := range []string{`{"space":"s","weights":[1]}`, strings.Repeat(" ", (4<<20)+1)} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadReviewClassifier(path); err != ErrInvalid {
			t.Fatal("unscoped or oversized model accepted", err)
		}
	}
}

func TestReviewQueueOrdersLikelyRejectsFirst(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i, kind := range []string{"lesson", "episodic", "lesson"} {
		r := Record{ID: "rank-" + string(rune('a'+i)), Scope: scopeA, Kind: kind, Status: "candidate", Revision: 1, Text: "candidate " + kind, Trust: float64(3-i) / 4, CreatedAt: now}
		if err := f.svc.Repo.Insert(testCtx, "memory_candidates", r); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.svc.ListCandidates(testCtx, f.human, ReviewInput{Scope: scopeA})
	if err != nil || len(page.Items) != 3 || page.Items[0].ID != "rank-a" || page.Items[0].RejectScore != nil {
		t.Fatal("without a classifier the queue keeps its order and carries no score", err)
	}
	f.svc.ReviewClassifier = &ReviewClassifier{Scope: scopeA, Space: f.emb.Space(), Weights: make([]float64, VectorDimension), Kinds: map[string]float64{"episodic": 4, "lesson": -4}}
	page, err = f.svc.ListCandidates(testCtx, f.human, ReviewInput{Scope: scopeA})
	if err != nil || len(page.Items) != 3 || page.Items[0].ID != "rank-b" || page.Items[0].RejectScore == nil || *page.Items[0].RejectScore < 0.9 || page.Items[1].ID != "rank-a" {
		t.Fatal("likely reject not listed first", err)
	}
	f.svc.ReviewClassifier.Space = "other:space"
	page, err = f.svc.ListCandidates(testCtx, f.human, ReviewInput{Scope: scopeA})
	if err != nil || page.Items[0].ID != "rank-a" || page.Items[0].RejectScore != nil {
		t.Fatal("a model from another embedding space must not score", err)
	}
}
