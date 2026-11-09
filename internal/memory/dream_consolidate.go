package memory

// Merging paraphrases: memories that say the same thing in other words, or in another language, become
// one memory. Each memory changed since the last pass goes into a batch with its nearest neighbours
// (overlapping wording, and the memory search when it finds any); the model returns the groups in a
// batch that state the same thing and one text that keeps every detail of each. The merged memory
// cites every source of the group and supersedes its members, so each member keeps its own history.
// Members are only merged with memories of their own kind and their own authority: what the user
// stated is never merged into an inference.

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	dreamBatchMax      = 24  // memories per model call
	dreamGroupMax      = 6   // memories one merge may replace
	dreamNeighbourDice = 0.3 // wording overlap that makes a memory a neighbour worth showing
	dreamModelMinConf  = 0.8 // the model's own confidence floor for a merge or an insight
	dreamFaithfulDice  = 0.3 // wording a merged text must share with each member it replaces
	dreamFaithfulCos   = 0.7 // else its meaning must be this close (another language), when an embedder is set
)

const consolidateSystem = `You tidy a person's long-term memory. Each line is: id, kind, text.
Find groups of two or more lines that state the same fact, preference or rule, also when they are worded differently or written in different languages. Lines that are related but say different things are not a group; a newer detail that changes a fact is not a group.
For each group write one memory that keeps every detail any member states, in the language most members use (the first member's language on a tie), without adding anything.
Answer with only JSON: {"groups":[{"ids":["id",...],"text":"merged memory","confidence":0.0-1.0}]}. Answer {"groups":[]} when there is none.`

type dreamGroup struct {
	IDs        []string `json:"ids"`
	Text       string   `json:"text"`
	Confidence float64  `json:"confidence"`
}

// consolidate merges paraphrase groups among the memories changed since the last pass.
func (d *dreamer) consolidate(ctx context.Context) error {
	if !d.modelReady() {
		return nil
	}
	pool, err := d.dreamPool(ctx)
	if err != nil {
		return err
	}
	batches := d.paraphraseBatches(ctx, pool)
	if len(batches) == 0 || !d.allowed(ctx, "consolidate", "write:memory_candidates") {
		return nil
	}
	used := map[string]bool{}
	for _, batch := range batches {
		if d.stop() {
			return nil
		}
		var answer struct {
			Groups []dreamGroup `json:"groups"`
		}
		ok, stop := d.ask(ctx, "consolidate", consolidateSystem, promptMemories(batch), &answer)
		if stop {
			return nil
		}
		if !ok {
			d.skip("stage/consolidate", int64(d.run.ModelCalls), "model_answer_unusable", 0)
			continue
		}
		for _, g := range answer.Groups {
			if err = d.mergeGroup(ctx, batch, g, used); err != nil {
				return err
			}
		}
		for _, r := range batch {
			d.remember("compared/"+r.ID, r.Revision)
		}
	}
	return nil
}

// paraphraseBatches: each memory changed since the last pass with its neighbours, as few batches as fit.
// shortcut: a memory is compared when it or a neighbour changes; an old pair nobody touches stays apart.
func (d *dreamer) paraphraseBatches(ctx context.Context, pool []Record) [][]Record {
	byID := map[string]Record{}
	for _, r := range pool {
		byID[r.ID] = r
	}
	batches, cur, in, curKey := [][]Record{}, []Record{}, map[string]bool{}, ""
	byKind := slices.Clone(pool) // one kind and authority share batches, never two
	slices.SortStableFunc(byKind, func(a, b Record) int { return strings.Compare(batchKey(a), batchKey(b)) })
	for _, r := range byKind {
		if len(batches) >= d.set.modelCalls-d.run.ModelCalls {
			break // no calls left for more: the rest waits for the next run
		}
		if !d.touched(r) || in[r.ID] || d.skipped("compared/"+r.ID, r.Revision) {
			continue
		}
		group := append([]Record{r}, d.neighbours(ctx, r, pool, byID)...)
		if len(cur) > 0 && (curKey != batchKey(r) || len(cur)+len(group) > dreamBatchMax) {
			batches, cur = withBatch(batches, cur), []Record{}
			clear(in)
		}
		curKey = batchKey(r)
		for _, g := range group {
			if !in[g.ID] && len(cur) < dreamBatchMax {
				in[g.ID] = true
				cur = append(cur, g)
			}
		}
	}
	return withBatch(batches, cur)
}

// withBatch adds cur when it holds something to compare.
func withBatch(batches [][]Record, cur []Record) [][]Record {
	if len(cur) > 1 {
		return append(batches, cur)
	}
	return batches
}

// batchKey: memories share a model batch only with their own kind and authority, so a line an inference
// carries can never steer how something the user stated is merged.
func batchKey(r Record) string { return r.Kind + "\x00" + strconv.FormatBool(userStated(r)) }

// neighbours: memories of the same kind and authority that share wording with r, or that the memory search
// finds nearest to it (the meaning lane matches a paraphrase in another language when an embedder is set).
func (d *dreamer) neighbours(ctx context.Context, r Record, pool []Record, byID map[string]Record) []Record {
	same := func(o Record) bool { return o.ID != r.ID && o.Kind == r.Kind && userStated(o) == userStated(r) }
	out := []Record{}
	grams := digestGrams(r.Text)
	for _, o := range pool {
		if same(o) && digestDice(grams, digestGrams(o.Text)) >= dreamNeighbourDice && len(out) < dreamGroupMax-1 {
			out = append(out, o)
		}
	}
	near, _, _, _, err := d.s.similarMemories(ctx, d.token, Decision{PrincipalID: d.principal}, d.scope, r, 3)
	if err != nil {
		return out
	}
	for _, n := range near {
		if o, ok := byID[n.ID]; ok && same(o) && !slices.ContainsFunc(out, func(x Record) bool { return x.ID == o.ID }) {
			out = append(out, o)
		}
	}
	return out
}

// validGroup checks one answer group against the batch: known, distinct, unmerged members of one kind
// and one authority, a usable text that stays close to every member, and enough confidence.
func (d *dreamer) validGroup(ctx context.Context, batch []Record, g dreamGroup, used map[string]bool) ([]Record, string) {
	if len(g.IDs) < 2 || len(g.IDs) > dreamGroupMax || strings.TrimSpace(g.Text) == "" || utf8.RuneCountInString(g.Text) > 2000 {
		return nil, "merge_invalid"
	}
	if g.Confidence < max(d.set.minConfidence, dreamModelMinConf) {
		return nil, "low_confidence"
	}
	members := []Record{}
	for _, id := range g.IDs {
		i := slices.IndexFunc(batch, func(r Record) bool { return r.ID == id })
		if i < 0 || used[id] || slices.ContainsFunc(members, func(r Record) bool { return r.ID == id }) {
			return nil, "merge_invalid"
		}
		members = append(members, batch[i])
	}
	for _, m := range members[1:] {
		if m.Kind != members[0].Kind || userStated(m) != userStated(members[0]) {
			return nil, "merge_mixed"
		}
	}
	if !d.faithful(ctx, members, g.Text) {
		return nil, "merge_unfaithful"
	}
	return members, ""
}

// faithful: the merged text says what each member says, by shared wording or, for a member in another
// language, by meaning when an embedder is set. A text a stored memory talked the model into writing
// shares neither with the members it would replace, so it never becomes a memory.
// shortcut: without an embedder a merge across languages is refused; set one to merge those.
func (d *dreamer) faithful(ctx context.Context, members []Record, text string) bool {
	grams, far := digestGrams(text), []string{}
	for _, m := range members {
		if digestDice(grams, digestGrams(m.Text)) < dreamFaithfulDice {
			far = append(far, m.Text)
		}
	}
	if len(far) == 0 {
		return true
	}
	if d.s.Embedder == nil {
		return false
	}
	vecs, err := d.s.embedReviewTexts(ctx, d.scope, append([]string{text}, far...))
	if err != nil || len(vecs) != len(far)+1 {
		return false
	}
	for _, v := range vecs[1:] {
		if cosine(vecs[0].Vector, v.Vector) < dreamFaithfulCos {
			return false
		}
	}
	return true
}

// groupTarget names a model group in the report by the batch ids it lists. Anything else the model wrote
// there is only hashed, so the report never carries model text.
func groupTarget(batch []Record, ids []string) string {
	if len(ids) == 0 || len(ids) > dreamGroupMax || slices.ContainsFunc(ids, func(id string) bool {
		return !slices.ContainsFunc(batch, func(r Record) bool { return r.ID == id })
	}) {
		return "memories/model-group-" + ContentHash(strings.Join(ids, "\x00"))[:12]
	}
	return "memories/" + strings.Join(ids, "+")
}

// mergeGroup writes the merged memory and supersedes the group with it.
func (d *dreamer) mergeGroup(ctx context.Context, batch []Record, g dreamGroup, used map[string]bool) error {
	members, reason := d.validGroup(ctx, batch, g, used)
	target := groupTarget(batch, g.IDs)
	if reason != "" {
		d.skip(target, 0, reason, g.Confidence)
		return nil
	}
	for _, m := range members {
		used[m.ID] = true
	}
	first := members[0]
	semantic, evidence := "fact", "inference"
	if first.MemoryContext != nil {
		semantic, evidence = first.MemoryContext.SemanticKind, first.MemoryContext.EvidenceKind
	}
	merged, err := d.propose(ctx, first.Kind, strings.TrimSpace(g.Text), members, dreamContext(members, semantic, evidence), "dream-merged")
	if errors.Is(err, ErrCandidateQuota) {
		d.skip(target, 0, "candidate_daily_limit", g.Confidence) // the scope's daily review entries are used up
		return nil
	} else if changed(err) || errors.Is(err, ErrSecretDetected) {
		d.skip(target, 0, "merge_refused", g.Confidence)
		return nil
	} else if err != nil {
		return err
	}
	if err = d.settle(ctx, merged, candidateJudgement{action: "approve", op: "merge", reason: "paraphrases " + strings.Join(g.IDs, ","), confidence: g.Confidence, replaces: g.IDs}); err != nil {
		return err
	}
	if m, err := d.s.Repo.Get(ctx, colMemories, d.scope, merged.ID); err == nil {
		d.remember("compared/"+m.ID, m.Revision) // its batch was just compared; a later paraphrase brings it back
	}
	return nil
}
