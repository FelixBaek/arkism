package memory

// Contradictions: the dream decides which of two contradicting memories is current, by the source's
// authority first (what the user stated beats what was inferred or observed for them) and then by
// time (the one that took effect, was observed or was written later, at least a minute apart). The
// other is superseded, or rejected when one side is still a review entry. A pair neither rule
// separates stays for a person.

import (
	"context"
	"slices"
	"time"
)

const contradictionMinGap = time.Minute

// dreamWhen is when a memory became true: when it takes effect, else when it was observed, else written.
func dreamWhen(r Record) time.Time {
	switch {
	case r.EffectiveAt != nil:
		return *r.EffectiveAt
	case r.MemoryContext != nil && r.MemoryContext.ObservedAt != nil:
		return *r.MemoryContext.ObservedAt
	}
	return r.CreatedAt
}

// currentOf picks the current one of two contradicting memories: its index, the rule and how sure the
// rule is; -1 when neither rule separates them.
func currentOf(a, b Record) (int, string, float64) {
	if ua, ub := userStated(a), userStated(b); ua != ub {
		return map[bool]int{true: 0, false: 1}[ua], "user_stated", 0.9
	}
	ta, tb := dreamWhen(a), dreamWhen(b)
	switch {
	case ta.Sub(tb) >= contradictionMinGap:
		return 0, "newer", 0.8
	case tb.Sub(ta) >= contradictionMinGap:
		return 1, "newer", 0.8
	}
	return -1, "", 0
}

// judgeContradiction decides a review entry the classifier found contradicting a memory: a current
// entry is approved and supersedes the memory, an outdated one is rejected.
func (d *dreamer) judgeContradiction(ctx context.Context, c Record, j candidateJudgement) candidateJudgement {
	old, err := d.s.Repo.Get(ctx, colMemories, d.scope, c.Classification.DupHint)
	if err != nil || old.Status != statusActive {
		return candidateJudgement{action: "skip", reason: "contradicted_memory_gone"}
	}
	w, rule, confidence := currentOf(c, old)
	switch {
	case w < 0:
		return candidateJudgement{action: "skip", reason: "contradiction_undecidable"}
	case confidence < d.set.minConfidence:
		return candidateJudgement{action: "skip", reason: "low_confidence", confidence: confidence}
	case w == 0:
		return candidateJudgement{action: "supersede", reason: "contradiction:" + rule + " replaces " + old.ID, confidence: confidence, replaces: []string{old.ID}}
	}
	return candidateJudgement{action: "reject", reason: "contradiction:" + rule + " kept " + old.ID, confidence: confidence}
}

// contradictions resolves open conflicts between two memories (or a memory and a held review entry).
func (d *dreamer) contradictions(ctx context.Context) error {
	rels, err := d.s.Repo.ListRecordsByStatus(ctx, d.scope, "memory_relations", "active", dreamScanMax)
	if err != nil {
		return err
	}
	for _, rel := range rels {
		target := "memory_relations/" + rel.ID
		if rel.Kind != "conflicts_with" || rel.Conflict == nil || len(rel.Relations) != 2 || rel.ContentErased || slices.Contains(rel.Tags, dreamUndoneTag) || d.skipped(target, rel.Revision) {
			continue
		}
		if d.stop() {
			return nil
		}
		if err = d.resolveConflict(ctx, rel); err != nil {
			return err
		}
	}
	return nil
}

func (d *dreamer) resolveConflict(ctx context.Context, rel Record) error {
	target := "memory_relations/" + rel.ID
	rows, recs, err := d.snapshot(ctx, rel.Relations[0], rel.Relations[1], Ref{Collection: "memory_relations", ID: rel.ID})
	if err != nil {
		return err
	}
	if recs[0] == nil || recs[1] == nil {
		d.skip(target, rel.Revision, "member_missing", 0)
		return nil
	}
	w, rule, confidence := currentOf(*recs[0], *recs[1])
	if w < 0 || confidence < d.set.minConfidence {
		d.skip(target, rel.Revision, "contradiction_undecidable", confidence)
		return nil
	}
	resolution := []string{"keep_first", "keep_second"}[w]
	if rel.Relations[0].Collection == colMemories && rel.Relations[1].Collection == colMemories {
		resolution = []string{"first_supersedes_second", "second_supersedes_first"}[w]
	}
	rows[0].BeforeConflict, rows[1].BeforeConflict = rel.ID, rel.ID
	in := ConflictInput{Scope: d.scope, Memories: rel.Relations, Action: "resolve", Reason: "dream: " + rule, ConflictID: rel.ID, ExpectedRevision: rel.Revision, Resolution: resolution}
	err = d.s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := applyConflictAs(ctx, tx, in, d.principal, roleService, resolutionWinner[resolution]); err != nil {
			return err
		}
		return afterRevisions(ctx, tx, d.scope, rows)
	})
	if changed(err) {
		d.skip(target, rel.Revision, "changed", confidence)
		return nil
	} else if err != nil {
		return err
	}
	loser := rel.Relations[1-w]
	return d.record(ctx, DreamChange{Op: "resolve", Target: loser.Collection + "/" + loser.ID, Reason: "contradiction:" + rule + " kept " + rel.Relations[w].ID, Confidence: confidence, Before: rows[1-w].BeforeStatus, After: map[bool]string{true: "superseded", false: "rejected"}[resolutionWinner[resolution] >= 0]}, rows)
}
