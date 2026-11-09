package memory

// The dream's model: the generative stages (merging paraphrases, deriving insights, drafting a skill)
// call Service.DreamModel, the server model's endpoint with MEMORY_DREAM_MODEL as its model. Every
// call is a metered provider call under a dream_* operation, capped per day (operationCap), and one
// run makes at most MEMORY_DREAM_MODEL_CALLS of them. Memory text leaves the server only with an
// explicit transfer decision (generate:dream). Without a model those stages do nothing.

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	dreamOperationPrefix = "dream_"
	dreamDailyCalls      = 200
	dreamMaxTokens       = 2000
	dreamPromptVersion   = "dream-v1"
	dreamGeneratedTag    = "dream-generated"
	// dreamCallGrace lets a call started inside the run's time finish before the job lease (2m) ends.
	dreamCallGrace = 30 * time.Second
)

// modelReady: a model is configured and this run may call it.
func (d *dreamer) modelReady() bool {
	return d.s.DreamModel.Configured() && d.set.modelCalls > 0
}

// ask makes one metered model call and decodes its JSON answer into v. stop says the run's or the
// day's call budget is spent or the memories may not go to the model (the stage ends, logged); ok is
// false for any other failure, which the caller logs as a skip.
func (d *dreamer) ask(ctx context.Context, operation, system, user string, v any) (ok, stop bool) {
	if d.run.ModelCalls >= d.set.modelCalls {
		d.hit("model_limit")
		return false, true
	}
	t, err := d.s.authorizeAt(ctx, d.token, "generate:dream", d.scope, "", system+"\n"+user, wikiDestination)
	if err != nil || !t.ExternalTransfer || !t.ContentSafe || !safeTransfer(user) {
		d.skip("stage/"+operation, 0, "transfer_denied", 0)
		return false, true
	}
	d.run.ModelCalls++
	callCtx, cancel := context.WithDeadline(ctx, d.deadline.Add(dreamCallGrace))
	defer cancel()
	out, _, err := d.s.modelCallWith(callCtx, &ChatWikiLLM{Config: d.s.DreamModel}, projectScope(d.scope), dreamOperationPrefix+operation, system, user, dreamMaxTokens)
	if errors.Is(err, ErrModelBudget) {
		d.hit("model_limit")
		return false, true
	}
	if err != nil {
		slog.Warn("dream: model call failed", "operation", operation, "error", err)
		return false, false
	}
	return decodeModelJSON(out, v), false
}

// dreamable: an active, unheld memory a person did not take back from a dream, whose text may go to
// the model. Restricted or private text never leaves for it.
func dreamable(r Record, now time.Time) bool {
	return r.Status == statusActive && r.ConflictID == "" && !slices.Contains(r.Tags, dreamUndoneTag) && (r.ExpiresAt == nil || r.ExpiresAt.After(now)) &&
		!r.ContentErased && !r.PrivacyQuarantined && r.Sensitivity != "restricted" && !hasPrivateMarker(r.Text) && utf8.RuneCountInString(r.Text) <= 2000
}

// dreamPool lists the active memories the generative stages may read, newest first.
func (d *dreamer) dreamPool(ctx context.Context) ([]Record, error) {
	rows, err := d.s.Repo.ListRecordsByStatus(ctx, d.scope, colMemories, statusActive, dreamScanMax)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return slices.DeleteFunc(rows, func(r Record) bool { return !dreamable(r, now) }), nil
}

// touched: changed since the previous run, so a stage looks at it again.
func (d *dreamer) touched(r Record) bool { return d.since == nil || r.UpdatedAt.After(*d.since) }

// allowed: the worker holds action in the scope. A stage without its grant logs that once and stops.
func (d *dreamer) allowed(ctx context.Context, stage string, actions ...string) bool {
	for _, action := range actions {
		if _, err := d.s.authorize(ctx, d.token, action, d.scope, "", ""); err != nil {
			d.skip("stage/"+stage, 0, "denied:"+action, 0)
			return false
		}
	}
	return true
}

// propose writes the run's own candidate (a merged or derived memory) and returns it at its revision.
func (d *dreamer) propose(ctx context.Context, kind, text string, members []Record, mc *MemoryContext, tag string) (Record, error) {
	in := CreateInput{Scope: d.scope, Kind: kind, Text: text, Summary: text, Sources: unionSources(members), MemoryContext: mc, Tags: []string{dreamGeneratedTag, tag}, Generated: true,
		Generator: &Generator{Model: d.s.DreamModel.Model, PromptVersion: dreamPromptVersion, PipelineVersion: dreamPromptVersion}, Sensitivity: strictestSensitivity(members)}
	return d.s.Create(ctx, d.token, "memory_candidates", in)
}

// strictestSensitivity: a memory made from others is as sensitive as the most sensitive of them.
func strictestSensitivity(rows []Record) string {
	out := ""
	for _, r := range rows {
		if out == "" || sensitivityRank(r.Sensitivity) > sensitivityRank(out) {
			out = r.Sensitivity
		}
	}
	return out
}

// dreamContext is the memory context of what the run writes from members: their domains, stated by
// the user only when every member was.
func dreamContext(members []Record, semantic, evidence string) *MemoryContext {
	mc := &MemoryContext{Domains: []string{}, EvidenceKind: evidence, SemanticKind: semantic}
	for _, m := range members {
		if m.MemoryContext != nil {
			for _, dom := range m.MemoryContext.Domains {
				if !slices.Contains(mc.Domains, dom) && len(mc.Domains) < 2 {
					mc.Domains = append(mc.Domains, dom)
				}
			}
		}
	}
	if len(mc.Domains) == 0 {
		mc.Domains = []string{"work"}
	}
	return mc
}

// promptMemories lists memories for a prompt as id and text lines.
func promptMemories(rows []Record) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.ID + "\t" + r.Kind + "\t" + strings.ReplaceAll(r.Text, "\n", " ") + "\n")
	}
	return b.String()
}
