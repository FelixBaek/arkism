package memory

import (
	"slices"
	"testing"
)

// No budget keeps the caller's shipped count; fixed picks the level's count; adaptive
// scales max_tokens and stays inside min/max.
func TestSpaceRecallBudgetLimit(t *testing.T) {
	var none *SpaceRecallBudget
	fixed := &SpaceRecallBudget{FixedLow: 3, FixedHigh: 30}
	adaptive := &SpaceRecallBudget{Function: "adaptive", AdaptiveLow: 0.01, AdaptiveMid: 0.02, AdaptiveHigh: 0.5, Min: 4, Max: 25}
	for _, c := range []struct {
		b                     *SpaceRecallBudget
		level                 string
		tokens, shipped, want int
	}{
		{none, "low", 1000, 5, 5}, {fixed, "low", 1000, 5, 3}, {fixed, "mid", 1000, 0, 0}, {fixed, "high", 1000, 20, 30},
		{adaptive, "", 1000, 0, 20}, {adaptive, "low", 100, 5, 4}, {adaptive, "high", 1000, 20, 25}, {fixed, "huge", 1000, 7, 7},
	} {
		if got := c.b.limit(c.level, c.tokens, c.shipped); got != c.want {
			t.Fatalf("%+v %s: %d, want %d", c.b, c.level, got, c.want)
		}
	}
	for _, bad := range []SpaceRecallBudget{{Function: "linear"}, {FixedLow: 51}, {AdaptiveMid: 1.5}, {Min: 10, Max: 5}} {
		if bad.valid() {
			t.Fatalf("invalid budget accepted: %+v", bad)
		}
	}
	// Reflect without a space budget keeps its shipped shape.
	in := ReflectInput{Budget: "low", MaxTokens: 1000}
	applyReflectDefaults(&in, &reflectRun{}, SpaceConfig{}, false, false)
	if in.Limit != 5 || in.MaxIterations != 2 {
		t.Fatalf("shipped shape %+v", in)
	}
	in = ReflectInput{MaxTokens: 1000} // no budget is mid
	applyReflectDefaults(&in, &reflectRun{}, SpaceConfig{}, false, false)
	if in.Limit != 20 || in.MaxIterations != reflectDefaultIter {
		t.Fatalf("default shape %+v", in)
	}
	in = ReflectInput{Budget: "high", MaxTokens: 1000}
	applyReflectDefaults(&in, &reflectRun{}, SpaceConfig{RecallBudget: adaptive}, false, false)
	if in.Limit != 20 { // reflect reads at most 20
		t.Fatalf("adaptive reflect limit %d", in.Limit)
	}
}

func TestCapChunkImagesAndBatchEnd(t *testing.T) {
	piece := "intro\n\n[image 1] a\n\n[image 2] b\n\n[image 3] c"
	if got := capChunkImages([]string{piece}, 0); !slices.Equal(got, []string{piece}) {
		t.Fatalf("no cap %q", got)
	}
	if got := capChunkImages([]string{piece}, 2); !slices.Equal(got, []string{"intro\n\n[image 1] a\n\n[image 2] b", "[image 3] c"}) {
		t.Fatalf("cap 2 %q", got)
	}
	job := &FileExtractJob{Chunks: make([]FileExtractChunk, 10), Next: 4}
	if extractBatchEnd(job) != 10 {
		t.Fatal("no batch size sends the rest")
	}
	job.Settings.BatchSize = 3
	if extractBatchEnd(job) != 7 {
		t.Fatal("batch size")
	}
}
