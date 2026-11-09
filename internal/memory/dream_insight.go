package memory

// Insights: what several memories show together and none says alone, a preference, a recurring pattern
// or a lesson, becomes a memory of its own. The model reads the newest memories and returns insights
// with the memories that support each; one is kept only with at least three supporting memories from
// the batch and enough confidence, and never when a memory already says it. It is stored as an
// inference that cites every source of its evidence, and the run's report names the evidence ids.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	dreamInsightTag       = "dream-insight"
	dreamInsightMin       = 3    // supporting memories an insight needs
	dreamInsightBatch     = 40   // newest memories the model reads
	dreamInsightKnownDice = 0.55 // wording overlap at which a memory already says the insight
)

const insightSystem = `You read a person's long-term memories. Each line is: id, kind, text.
Find what at least three memories show together but none of them states on its own: a preference of the person, a pattern that keeps recurring, or a lesson learned. Do not restate a single memory, and do not guess beyond what the memories show.
Write each insight as one short sentence in the language most of its memories use.
Answer with only JSON: {"insights":[{"text":"...","type":"preference"|"pattern"|"lesson","evidence":["id",...],"confidence":0.0-1.0}]}. Answer {"insights":[]} when there is none.`

type dreamInsight struct {
	Text       string   `json:"text"`
	Type       string   `json:"type"`
	Evidence   []string `json:"evidence"`
	Confidence float64  `json:"confidence"`
}

// insightKinds maps an insight type to the memory kind and semantic kind it is stored as.
var insightKinds = map[string][2]string{"preference": {"user", "preference"}, "pattern": {"semantic", "fact"}, "lesson": {"lesson", "lesson"}}

// insights derives insights from the newest memories once some of them changed since the last pass.
func (d *dreamer) insights(ctx context.Context) error {
	if !d.modelReady() {
		return nil
	}
	pool, err := d.dreamPool(ctx)
	if err != nil {
		return err
	}
	// Insights a person took back count as known, so the next run does not derive them again.
	undone, err := d.s.Repo.ListRecordsByStatus(ctx, d.scope, colMemories, "rejected", dreamScanMax)
	if err != nil {
		return err
	}
	all := append(slices.Clone(pool), slices.DeleteFunc(undone, func(r Record) bool { return !slices.Contains(r.Tags, dreamInsightTag) })...)
	pool = slices.DeleteFunc(slices.Clone(pool), func(r Record) bool { return slices.Contains(r.Tags, dreamInsightTag) })
	pool = pool[:min(len(pool), dreamInsightBatch)]
	if len(pool) < dreamInsightMin || !slices.ContainsFunc(pool, d.touched) || d.stop() || !d.allowed(ctx, "insights", "write:memory_candidates") {
		return nil
	}
	var answer struct {
		Insights []dreamInsight `json:"insights"`
	}
	ok, stop := d.ask(ctx, "insight", insightSystem, promptMemories(pool), &answer)
	if stop {
		return nil
	}
	if !ok {
		d.skip("stage/insights", int64(d.run.ModelCalls), "model_answer_unusable", 0)
		return nil
	}
	for _, in := range answer.Insights {
		if d.stop() {
			return nil
		}
		if err = d.addInsight(ctx, pool, all, in); err != nil {
			return err
		}
	}
	return nil
}

// insightEvidence checks one insight against the batch and returns its supporting memories, or why not.
func (d *dreamer) insightEvidence(pool, all []Record, in dreamInsight) ([]Record, string) {
	kinds, ok := insightKinds[in.Type]
	text := strings.TrimSpace(in.Text)
	if !ok || text == "" || utf8.RuneCountInString(text) > 1000 {
		return nil, "insight_invalid"
	}
	if in.Confidence < max(d.set.minConfidence, dreamModelMinConf) {
		return nil, "low_confidence"
	}
	evidence := []Record{}
	for _, id := range in.Evidence {
		i := slices.IndexFunc(pool, func(r Record) bool { return r.ID == id })
		if i >= 0 && !slices.ContainsFunc(evidence, func(r Record) bool { return r.ID == id }) {
			evidence = append(evidence, pool[i])
		}
	}
	if len(evidence) < dreamInsightMin {
		return nil, "insight_thin"
	}
	grams := digestGrams(text)
	for _, r := range all {
		if r.Kind == kinds[0] && digestDice(grams, digestGrams(r.Text)) >= dreamInsightKnownDice {
			return nil, "insight_known"
		}
	}
	return evidence, ""
}

// addInsight writes one insight as an approved memory.
func (d *dreamer) addInsight(ctx context.Context, pool, all []Record, in dreamInsight) error {
	target := "insight/" + ContentHash(in.Text)[:12]
	evidence, reason := d.insightEvidence(pool, all, in)
	if reason != "" {
		d.skip(target, 0, reason, in.Confidence)
		return nil
	}
	kinds := insightKinds[in.Type]
	ids := make([]string, len(evidence))
	for i, r := range evidence {
		ids[i] = r.ID
	}
	made, err := d.propose(ctx, kinds[0], strings.TrimSpace(in.Text), evidence, dreamContext(evidence, kinds[1], "inference"), dreamInsightTag)
	if changed(err) || errors.Is(err, ErrSecretDetected) {
		d.skip(target, 0, "insight_refused", in.Confidence)
		return nil
	} else if err != nil {
		return err
	}
	return d.settle(ctx, made, candidateJudgement{action: "approve", op: "insight", reason: in.Type + " from " + strings.Join(ids, ","), confidence: in.Confidence})
}
