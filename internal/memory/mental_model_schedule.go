package memory

// When a mental model is due, what its last refresh came to, and the routes that
// read or reset that state (cron preview, clear). The derive scheduler
// (derive_schedule.go) wakes at mentalModelNextAt; a trigger that fires inside
// min_interval is held, not dropped, so triggers in a burst fold into one refresh.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/crewrun"
)

const (
	mmRetryAfter      = time.Hour // a refresh that wrote nothing waits this long before a trigger may run it again
	mmCronPreviewMax  = 10
	mmOutcomeWritten  = "content_written"
	mmOutcomeSame     = "content_unchanged"
	mmOutcomeNoFacts  = "content_preserved_no_new_facts"
	mmOutcomeEmpty    = "refresh_failed_empty_candidate"
	mmOutcomeDelta    = "refresh_failed_delta_not_applied"
	mmOutcomeSchema   = "refresh_failed_structured_output"
	mmOutcomeError    = "refresh_failed_error"
	mmTriggerManual   = "manual"
	mmTriggerFullCron = "full_refresh_cron"
)

// errStructuredOutput: the summary is not JSON valid against the model's response_schema.
var errStructuredOutput = fmt.Errorf("%w: response_schema", errModelAnswer)

// MentalModelOutcome is how the last refresh attempt ended and what triggered it.
type MentalModelOutcome struct {
	Code    string    `json:"code" bson:"code"`
	Trigger string    `json:"trigger" bson:"trigger"`
	At      time.Time `json:"at" bson:"at"`
}

func (o *MentalModelOutcome) wroteNothing() bool {
	return o != nil && o.Code != mmOutcomeWritten
}

// mentalModelTrigger names the trigger that fired, or "" (min_interval aside).
func mentalModelTrigger(m *MentalModelData, latest, latestObservation, now time.Time) string {
	r := m.Refresh
	if m.LastRefreshAt == nil {
		if r.Mode != mmRefreshManual || r.Cron != "" || r.AfterConsolidation || r.FullRefreshCron != "" {
			return "initial"
		}
		return ""
	}
	last := *m.LastRefreshAt
	switch {
	case mentalModelFullDue(m, now):
		return mmTriggerFullCron
	case r.Mode == mmRefreshDaily && now.Sub(last) >= mmDailyInterval:
		return mmRefreshDaily
	case r.Mode == mmRefreshChange && latest.After(last):
		return mmRefreshChange
	case r.AfterConsolidation && latestObservation.After(last):
		return "after_consolidation"
	case r.Cron != "" && latest.After(last) && cronTickedSince(r.Cron, last, now):
		return "cron"
	}
	return ""
}

// mentalModelFullDue: full_refresh_cron ticked since the last full refresh, and the model was refreshed since then.
func mentalModelFullDue(m *MentalModelData, now time.Time) bool {
	spec := m.Refresh.FullRefreshCron
	if spec == "" || m.LastRefreshAt == nil {
		return false
	}
	if m.LastFullRefreshAt == nil {
		return cronTickedSince(spec, *m.LastRefreshAt, now)
	}
	return m.LastRefreshAt.After(*m.LastFullRefreshAt) && cronTickedSince(spec, *m.LastFullRefreshAt, now)
}

// mentalModelHold is the earliest time a fired trigger may run: min_interval (the model's,
// else the space's) after the last refresh, and mmRetryAfter after an attempt that wrote nothing.
func mentalModelHold(m *MentalModelData, spaceGap time.Duration) time.Time {
	hold := time.Time{}
	gap := spaceGap
	if d, err := time.ParseDuration(m.Refresh.MinInterval); err == nil {
		gap = d
	}
	if m.LastRefreshAt != nil && gap > 0 {
		hold = m.LastRefreshAt.Add(gap)
	}
	if o := m.LastOutcome; o.wroteNothing() && (m.LastRefreshAt == nil || o.At.After(*m.LastRefreshAt)) && o.At.Add(mmRetryAfter).After(hold) {
		hold = o.At.Add(mmRetryAfter)
	}
	return hold
}

// mentalModelDue: a trigger fired and the model's own min_interval has passed.
func mentalModelDue(m *MentalModelData, latest, latestObservation, now time.Time) bool {
	return mentalModelTrigger(m, latest, latestObservation, now) != "" && !now.Before(mentalModelHold(m, 0))
}

// mentalModelNextAt is when the model next runs: now (or its hold) when a trigger fired,
// else the next daily or cron time. on_change and after_consolidation have no clock: an
// approval or a consolidation run wakes the scheduler. A model awaiting review has none.
func mentalModelNextAt(m *MentalModelData, latest, latestObservation, now time.Time, spaceGap time.Duration) (time.Time, bool) {
	if m.Draft != nil {
		return time.Time{}, false
	}
	hold := mentalModelHold(m, spaceGap)
	later := func(t time.Time) time.Time {
		if hold.After(t) {
			return hold
		}
		return t
	}
	if mentalModelTrigger(m, latest, latestObservation, now) != "" {
		return later(now), true
	}
	if m.LastRefreshAt == nil {
		return time.Time{}, false
	}
	next, ok := time.Time{}, false
	pick := func(t time.Time, fires bool) {
		if fires && (!ok || t.Before(next)) {
			next, ok = t, true
		}
	}
	r := m.Refresh
	pick(m.LastRefreshAt.Add(mmDailyInterval), r.Mode == mmRefreshDaily)
	if r.Cron != "" && latest.After(*m.LastRefreshAt) {
		pick(cronNextAfter(r.Cron, now))
	}
	if r.FullRefreshCron != "" && (m.LastFullRefreshAt == nil || m.LastRefreshAt.After(*m.LastFullRefreshAt)) {
		pick(cronNextAfter(r.FullRefreshCron, now))
	}
	return later(next), ok
}

func cronNextAfter(spec string, after time.Time) (time.Time, bool) {
	c, err := crewrun.ParseCron(spec)
	if err != nil {
		return time.Time{}, false
	}
	return c.Next(after.UTC())
}

// cronTickedSince reports whether the schedule fired in (last, now], in UTC.
func cronTickedSince(spec string, last, now time.Time) bool {
	if now.Sub(last) > 366*24*time.Hour {
		return true // every schedule that ever fires does within a year
	}
	next, ok := cronNextAfter(spec, last)
	return ok && !next.After(now)
}

// refreshOutcome names how a refresh attempt ended; ok is false for errors that are not an outcome (denied, budget, conflict).
func refreshOutcome(err error, delta bool) (string, bool) {
	switch {
	case err == nil:
		return mmOutcomeWritten, true
	case errors.Is(err, ErrNotFound):
		return mmOutcomeNoFacts, true
	case errors.Is(err, errStructuredOutput):
		return mmOutcomeSchema, true
	case errors.Is(err, errModelAnswer) && delta:
		return mmOutcomeDelta, true
	case errors.Is(err, errModelAnswer):
		return mmOutcomeEmpty, true
	case errors.Is(err, ErrSecretDetected):
		return mmOutcomeError, true
	}
	return "", false
}

// noteRefreshOutcome stores an attempt that wrote no draft, so readers see why and triggers back off.
func (s *Service) noteRefreshOutcome(ctx context.Context, d Decision, r Record, trigger string, cause error) {
	code, ok := refreshOutcome(cause, r.MentalModel.Refresh.Strategy == mmStrategyDelta && r.MentalModel.Active != nil && trigger != mmTriggerFullCron)
	if !ok || cause == nil {
		return
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	_, _ = s.mentalModelWrite(ctx, d, r.Scope, r.ID, r.Revision, "refresh_outcome", func(r *Record) error {
		r.MentalModel.LastOutcome = &MentalModelOutcome{Code: code, Trigger: trigger, At: now}
		return nil
	})
}

// CronPreviewInput asks for the next times a five-field UTC cron fires.
type CronPreviewInput struct {
	Scope Scope      `json:"scope"`
	Cron  string     `json:"cron"`
	Count int        `json:"count,omitempty"`
	After *time.Time `json:"after,omitempty"`
}

type CronPreview struct {
	Cron string      `json:"cron"`
	Next []time.Time `json:"next"`
}

// PreviewCron lists up to 10 firing times (default 5) within a year; it reads nothing and calls no model.
func (s *Service) PreviewCron(ctx context.Context, token string, in CronPreviewInput) (CronPreview, error) {
	if in.Count == 0 {
		in.Count = 5
	}
	c, err := crewrun.ParseCron(in.Cron)
	if err != nil || in.Count < 1 || in.Count > mmCronPreviewMax || !validScope(in.Scope) {
		return CronPreview{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colMentalModels, in.Scope, "", ""); err != nil {
		return CronPreview{}, err
	}
	at := time.Now().UTC()
	if in.After != nil {
		at = in.After.UTC()
	}
	out := CronPreview{Cron: strings.Join(strings.Fields(in.Cron), " "), Next: []time.Time{}}
	for len(out.Next) < in.Count {
		next, ok := c.Next(at)
		if !ok {
			break
		}
		out.Next, at = append(out.Next, next), next
	}
	return out, nil
}

// ClearMentalModel empties the approved text and any draft but keeps the topic, its
// trigger and its history (a "cleared" entry records who cleared it). People only.
func (s *Service) ClearMentalModel(ctx context.Context, token, id string, in MentalModelReviewInput) (MentalModelView, error) {
	if !validRunLabel(id, 256) || in.ExpectedRevision < 1 || len(in.Reason) > 500 {
		return MentalModelView{}, ErrInvalid
	}
	d, err := s.derivedHuman(ctx, token, colMentalModels, in.Scope, id)
	if err != nil {
		return MentalModelView{}, err
	}
	r, err := s.mentalModelWrite(ctx, d, in.Scope, id, in.ExpectedRevision, "clear", func(r *Record) error {
		m := r.MentalModel
		if m.Active == nil && m.Draft == nil {
			return ErrConflict
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		m.History = appendRevision(m.History, DerivedRevision{Status: mmSuperseded, Action: "cleared", By: d.PrincipalID, At: now})
		m.Active, m.Draft, m.LastRefreshAt, m.LastFullRefreshAt, m.LastOutcome = nil, nil, nil, nil, nil
		r.Review = &ReviewDecision{Action: "clear", By: d.PrincipalID, ActorRole: d.Role, At: now, Reason: in.Reason}
		return nil
	})
	if err != nil {
		return MentalModelView{}, err
	}
	s.scheduleDerive(ctx, in.Scope)
	return s.mentalModelView(ctx, r), nil
}

// mentalModelSpaceGap is the space's default min_interval for mental-model triggers (0 when unset).
func mentalModelSpaceGap(c SpaceConfig) time.Duration {
	if c.MentalModelDefaults == nil {
		return 0
	}
	gap, _ := time.ParseDuration(c.MentalModelDefaults.MinInterval)
	return gap
}

var mmBudgetRecall = map[string]int{"low": 10, "mid": mmRecall, "high": 40}

func validMentalModelRecall(r MentalModelRefresh) bool {
	_, budget := mmBudgetRecall[r.Budget]
	ok := (r.Budget == "" || budget) && validFactTypes(r.FactTypes) && (r.TagsMatch == "" || slices.Contains(tagsMatchModes, r.TagsMatch)) &&
		validTagGroups(r.TagGroups) && r.RecallMaxTokens >= 0 && r.RecallMaxTokens <= 64000
	if r.FullRefreshCron != "" {
		_, err := crewrun.ParseCron(r.FullRefreshCron)
		ok = ok && err == nil
	}
	if schemaSet(r.ResponseSchema) {
		_, err := parseReflectSchema(r.ResponseSchema)
		ok = ok && err == nil && len(r.ResponseSchema) <= reflectSchemaMax
	}
	return ok
}

// schemaSet: a response_schema was given (JSON null, as a JSON round trip stores an unset one, is none).
func schemaSet(raw json.RawMessage) bool {
	return len(raw) > 0 && strings.TrimSpace(string(raw)) != "null"
}

// factTypeKept: no fact_types keeps both; "memory" keeps memories, "observation" observations.
func factTypeKept(types []string, collection string) bool {
	return len(types) == 0 || collection == colMemories && slices.Contains(types, "memory") || collection == colObservations && slices.Contains(types, "observation")
}

// summarySearch is the recall a cited summary runs; every hit goes to the server model.
func summarySearch(req summaryRequest) SearchInput {
	search := SearchInput{Scope: req.scope, Query: req.query, Limit: cmp.Or(req.limit, mmRecall), Destination: wikiDestination, Kinds: req.kinds}
	if len(req.tags) > 0 || len(req.tagGroups) > 0 {
		search.Filters = &SearchFilters{Tags: req.tags, TagsMatch: req.tagsMatch, TagGroups: req.tagGroups}
	}
	if req.recallTokens > 0 {
		search.BudgetTokens, search.TokenEncoding = req.recallTokens, "cl100k_base"
	}
	return search
}

func mentalModelSchemaRule(schema *jsonSchema) string {
	raw, _ := json.Marshal(schema.root)
	return "\nThe summary field must be one JSON value, written as a JSON string, that is valid against this JSON Schema. Cite ids in citations as usual.\n" + string(raw)
}

// structuredSummary checks the summary against the schema and returns it compact.
func structuredSummary(schema *jsonSchema, summary string) (string, error) {
	var v any
	if !decodeModelJSON(summary, &v) || !schemaAccepts(schema, v) {
		return "", errStructuredOutput
	}
	compact, err := json.Marshal(v)
	if err != nil || utf8.RuneCount(compact) > mmSummaryMax {
		return "", errStructuredOutput
	}
	return string(compact), nil
}

// summaryChunks adds the source excerpts of the recalled memories (include_chunks), as data.
// An excerpt that holds a secret is left out.
func (s *Service) summaryChunks(ctx context.Context, token string, scope Scope, hits []Hit) string {
	var b strings.Builder
	for _, h := range hits {
		if h.Collection != colMemories {
			continue
		}
		trace, err := s.traceRecordSources(ctx, token, scope, h.ID, colMemories, 3)
		if err != nil {
			continue
		}
		for _, src := range trace.Sources {
			if src.Excerpt != "" && !containsSecret(src.Excerpt) {
				b.WriteString("- memory: " + h.ID + "\n  excerpt: " + strings.ReplaceAll(src.Excerpt, "\n", " ") + "\n")
			}
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n\n" + wikiFence("SOURCE_CHUNKS", b.String())
}
