package memory

// Space settings for reflect, consolidation and mental models. Each block is
// inherited whole (space, then template, then server): the first layer that sets
// a block supplies all of it. Every block is empty by default, so an upgrade keeps
// today's behaviour and spends no extra model calls.

import (
	"cmp"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	consolidationStrategiesMax = 16
	consolidationRulesMax      = 8
	consolidationGlobsMax      = 16
	consolidationParallelMax   = 4
	consolidationScopeMax      = 8 // tags in one observation scope
	consolidationMissionMax    = 2000
	consolidationScopesMax     = 1000 // distinct scopes one run or preview builds (all_combinations grows fast)
)

var (
	reflectBudgets     = []string{"low", "mid", "high"}
	reflectFactTypes   = []string{"memory", "observation"}
	obsScopeModes      = []string{"shared", "combined", "per_tag", "all_combinations"}
	obsScopeTagMatches = []string{"all", "exact"}
)

// SpaceReflectDefaults are the defaults a reflect request takes for what it leaves unset.
type SpaceReflectDefaults struct {
	Budget              string   `json:"budget,omitempty" bson:"budget,omitempty"`
	FactTypes           []string `json:"fact_types,omitempty" bson:"fact_types,omitempty"`
	ExcludeMentalModels bool     `json:"exclude_mental_models,omitempty" bson:"exclude_mental_models,omitempty"`
	// TagScopedDirectives (opt-in): a tagged directive applies only to a reflect whose tags share one of its tags.
	TagScopedDirectives bool              `json:"tag_scoped_directives,omitempty" bson:"tag_scoped_directives,omitempty"`
	Tools               ReflectToolBudget `json:"tools" bson:"tools"`
}

// ReflectToolBudget caps, in approximate tokens, what one tool call adds to the prompt. 0 keeps the shipped default.
type ReflectToolBudget struct {
	RecallMaxTokens           int `json:"recall_max_tokens,omitempty" bson:"recall_max_tokens,omitempty"`
	RecallMaxChunkTokens      int `json:"recall_max_chunk_tokens,omitempty" bson:"recall_max_chunk_tokens,omitempty"`
	ObservationsMaxTokens     int `json:"observations_max_tokens,omitempty" bson:"observations_max_tokens,omitempty"`
	MentalModelsMaxTokens     int `json:"mental_models_max_tokens,omitempty" bson:"mental_models_max_tokens,omitempty"`
	ReadMentalModelsMaxTokens int `json:"read_mental_models_max_tokens,omitempty" bson:"read_mental_models_max_tokens,omitempty"`
	ExpandMaxTokens           int `json:"expand_max_tokens,omitempty" bson:"expand_max_tokens,omitempty"`
}

// SpaceRecallBudget maps a budget level (low, mid, high) to how many records a reflect or
// mental-model recall reads when the request sets no limit. fixed takes the level's count;
// adaptive takes round(recall tokens x the level's ratio), clamped to [min, max]. Unset
// (the default) keeps the shipped mapping; an unset level keeps its shipped count.
type SpaceRecallBudget struct {
	Function     string  `json:"function,omitempty" bson:"function,omitempty"` // fixed (default) | adaptive
	FixedLow     int     `json:"fixed_low,omitempty" bson:"fixed_low,omitempty"`
	FixedMid     int     `json:"fixed_mid,omitempty" bson:"fixed_mid,omitempty"`
	FixedHigh    int     `json:"fixed_high,omitempty" bson:"fixed_high,omitempty"`
	AdaptiveLow  float64 `json:"adaptive_low,omitempty" bson:"adaptive_low,omitempty"`
	AdaptiveMid  float64 `json:"adaptive_mid,omitempty" bson:"adaptive_mid,omitempty"`
	AdaptiveHigh float64 `json:"adaptive_high,omitempty" bson:"adaptive_high,omitempty"`
	Min          int     `json:"min,omitempty" bson:"min,omitempty"`
	Max          int     `json:"max,omitempty" bson:"max,omitempty"`
}

// recallBudgetMax is the search limit ceiling a budget can reach.
const recallBudgetMax = 50

var recallBudgetFunctions = []string{"", "fixed", "adaptive"}

func (b *SpaceRecallBudget) valid() bool {
	if b == nil {
		return true
	}
	counts := []int{b.FixedLow, b.FixedMid, b.FixedHigh, b.Min, b.Max}
	ratios := []float64{b.AdaptiveLow, b.AdaptiveMid, b.AdaptiveHigh}
	return slices.Contains(recallBudgetFunctions, b.Function) && (b.Min == 0 || b.Max == 0 || b.Min <= b.Max) &&
		!slices.ContainsFunc(counts, func(n int) bool { return n < 0 || n > recallBudgetMax }) &&
		!slices.ContainsFunc(ratios, func(r float64) bool { return r < 0 || r > 1 })
}

// limit is the records a recall at level reads, or shipped when the space sets no budget
// (shipped is the caller's own count for the level; 0 means the search default).
// tokens is the answer's max_tokens, the base of the adaptive ratio (round(tokens × ratio),
// clamped to [min, max]).
func (b *SpaceRecallBudget) limit(level string, tokens, shipped int) int {
	if b == nil {
		return shipped
	}
	idx := slices.Index(reflectBudgets, cmp.Or(level, "mid"))
	if idx < 0 {
		return shipped
	}
	if b.Function != "adaptive" {
		return cmp.Or([]int{b.FixedLow, b.FixedMid, b.FixedHigh}[idx], shipped)
	}
	ratio := []float64{b.AdaptiveLow, b.AdaptiveMid, b.AdaptiveHigh}[idx]
	if ratio == 0 {
		return shipped
	}
	n := int(math.Round(float64(tokens) * ratio))
	return max(cmp.Or(b.Min, 1), min(cmp.Or(b.Max, recallBudgetMax), n))
}

// SpaceMentalModelDefaults is the trigger a mental model created without a refresh mode takes,
// and the space-wide min_interval every model's triggers wait for when it sets none.
type SpaceMentalModelDefaults struct {
	Trigger     *MentalModelRefresh `json:"trigger,omitempty" bson:"trigger,omitempty"`
	MinInterval string              `json:"min_interval,omitempty" bson:"min_interval,omitempty"`
}

// SpaceConsolidation shapes observation consolidation: which tag scopes memories are
// consolidated in, the mission, strategies per tag scope, per-scope limits and run size.
type SpaceConsolidation struct {
	ObservationsMission string `json:"observations_mission,omitempty" bson:"observations_mission,omitempty"`
	// DefaultScopeMode: shared (one untagged scope, the default), combined (a memory's full tag set),
	// per_tag (one scope per tag) or all_combinations (every non-empty subset, up to 8 tags).
	DefaultScopeMode string `json:"default_scope_mode,omitempty" bson:"default_scope_mode,omitempty"`
	// DefaultScopes, when set, replaces the mode: each memory joins every listed glob scope its tags cover.
	DefaultScopes           [][]string              `json:"default_scopes,omitempty" bson:"default_scopes,omitempty"`
	Strategies              []ConsolidationStrategy `json:"strategies,omitempty" bson:"strategies,omitempty"`
	ScopeLimits             []ObservationScopeLimit `json:"scope_limits,omitempty" bson:"scope_limits,omitempty"`
	MaxObservationsPerScope *int                    `json:"max_observations_per_scope,omitempty" bson:"max_observations_per_scope,omitempty"`
	BatchSize               int                     `json:"batch_size,omitempty" bson:"batch_size,omitempty"`
	Parallel                int                     `json:"parallel,omitempty" bson:"parallel,omitempty"`
}

// ConsolidationStrategy claims the observation scopes one of its rules matches; the first claiming strategy wins whole.
type ConsolidationStrategy struct {
	Name                          string               `json:"name" bson:"name"`
	Scopes                        []ObservationTagRule `json:"scopes" bson:"scopes"`
	ObservationsMission           string               `json:"observations_mission,omitempty" bson:"observations_mission,omitempty"`
	MaxObservationsPerScope       *int                 `json:"max_observations_per_scope,omitempty" bson:"max_observations_per_scope,omitempty"`
	SourceMaxTokens               int                  `json:"source_max_tokens,omitempty" bson:"source_max_tokens,omitempty"`
	SourceMaxTokensPerObservation int                  `json:"source_max_tokens_per_observation,omitempty" bson:"source_max_tokens_per_observation,omitempty"`
}

// ObservationTagRule matches a scope's tags with globs: "all" when every glob matches some tag, "exact" when the globs cover the tags one to one.
type ObservationTagRule struct {
	Tags      []string `json:"tags" bson:"tags"`
	TagsMatch string   `json:"tags_match,omitempty" bson:"tags_match,omitempty"`
}

// ObservationScopeLimit caps the observations of scopes its globs cover exactly: -1 unlimited, 0 none new.
type ObservationScopeLimit struct {
	Scope []string `json:"scope" bson:"scope"`
	Limit int      `json:"limit" bson:"limit"`
}

func validReflectModelSettings(c SpaceConfig) error {
	if !c.ReflectDefaults.valid() || !c.MentalModelDefaults.valid() || !c.Consolidation.valid() || !c.RecallQuality.valid() || !c.RecallBudget.valid() {
		return ErrInvalid
	}
	if c.Consolidation != nil && containsSecret(consolidationMissions(c.Consolidation)) {
		return ErrSecretDetected
	}
	return nil
}

func (d *SpaceReflectDefaults) valid() bool {
	if d == nil {
		return true
	}
	t := d.Tools
	return (d.Budget == "" || slices.Contains(reflectBudgets, d.Budget)) && validFactTypes(d.FactTypes) &&
		!slices.ContainsFunc([]int{t.RecallMaxTokens, t.ObservationsMaxTokens, t.MentalModelsMaxTokens, t.ReadMentalModelsMaxTokens, t.ExpandMaxTokens},
			func(n int) bool { return n != 0 && (n < 64 || n > reflectMaxPromptTokens) }) &&
		(t.RecallMaxChunkTokens == 0 || t.RecallMaxChunkTokens >= 16 && t.RecallMaxChunkTokens <= reflectMaxPromptTokens)
}

func validFactTypes(types []string) bool {
	return len(types) <= len(reflectFactTypes) && !slices.ContainsFunc(types, func(t string) bool { return !slices.Contains(reflectFactTypes, t) })
}

func (d *SpaceMentalModelDefaults) valid() bool {
	if d == nil {
		return true
	}
	if d.MinInterval != "" {
		if gap, err := time.ParseDuration(d.MinInterval); err != nil || gap <= 0 || gap > mmIntervalMax {
			return false
		}
	}
	return d.Trigger == nil || validMentalModelRefresh(*d.Trigger)
}

func (c *SpaceConsolidation) valid() bool {
	if c == nil {
		return true
	}
	if utf8.RuneCountInString(c.ObservationsMission) > consolidationMissionMax || !utf8.ValidString(c.ObservationsMission) ||
		c.DefaultScopeMode != "" && !slices.Contains(obsScopeModes, c.DefaultScopeMode) ||
		len(c.DefaultScopes) > consolidationRulesMax || len(c.Strategies) > consolidationStrategiesMax || len(c.ScopeLimits) > consolidationRulesMax ||
		!validObsLimit(c.MaxObservationsPerScope) || c.BatchSize < 0 || c.BatchSize > obsGroupsMax || c.Parallel < 0 || c.Parallel > consolidationParallelMax {
		return false
	}
	return !slices.ContainsFunc(c.DefaultScopes, func(g []string) bool { return !validGlobs(g) }) &&
		!slices.ContainsFunc(c.ScopeLimits, func(l ObservationScopeLimit) bool { return !validGlobs(l.Scope) || l.Limit < -1 }) &&
		!slices.ContainsFunc(c.Strategies, func(st ConsolidationStrategy) bool { return !st.valid() })
}

func (st ConsolidationStrategy) valid() bool {
	return validRunLabel(st.Name, 64) && len(st.Scopes) > 0 && len(st.Scopes) <= consolidationRulesMax &&
		utf8.RuneCountInString(st.ObservationsMission) <= consolidationMissionMax && utf8.ValidString(st.ObservationsMission) &&
		validObsLimit(st.MaxObservationsPerScope) && st.SourceMaxTokens >= 0 && st.SourceMaxTokens <= obsRunTokens &&
		st.SourceMaxTokensPerObservation >= 0 && st.SourceMaxTokensPerObservation <= obsRunTokens &&
		!slices.ContainsFunc(st.Scopes, func(r ObservationTagRule) bool {
			return !validGlobs(r.Tags) || r.TagsMatch != "" && !slices.Contains(obsScopeTagMatches, r.TagsMatch)
		})
}

func validObsLimit(n *int) bool { return n == nil || *n >= -1 }

func validGlobs(globs []string) bool {
	return len(globs) > 0 && len(globs) <= consolidationGlobsMax && !slices.ContainsFunc(globs, func(g string) bool {
		return g == "" || len(g) > 128 || !utf8.ValidString(g)
	})
}

func consolidationMissions(c *SpaceConsolidation) string {
	parts := []string{c.ObservationsMission}
	for _, st := range c.Strategies {
		parts = append(parts, st.ObservationsMission)
	}
	return strings.Join(parts, "\n")
}

// resolveReflectModelBlocks fills the blocks from the first layer that sets each.
func resolveReflectModelBlocks(out *SpaceConfig, layers []spaceLayer, src map[string]string) {
	inheritSetting(out, layers, src, "reflect_default_options", func(c *SpaceConfig) **SpaceReflectDefaults { return &c.ReflectDefaults }, func(v *SpaceReflectDefaults) bool { return v != nil })
	inheritSetting(out, layers, src, "mental_model_defaults", func(c *SpaceConfig) **SpaceMentalModelDefaults { return &c.MentalModelDefaults }, func(v *SpaceMentalModelDefaults) bool { return v != nil })
	inheritSetting(out, layers, src, "consolidation", func(c *SpaceConfig) **SpaceConsolidation { return &c.Consolidation }, func(v *SpaceConsolidation) bool { return v != nil })
	inheritSetting(out, layers, src, "recall_quality", func(c *SpaceConfig) **SpaceRecallQuality { return &c.RecallQuality }, func(v *SpaceRecallQuality) bool { return v != nil })
	inheritSetting(out, layers, src, "recall_budget", func(c *SpaceConfig) **SpaceRecallBudget { return &c.RecallBudget }, func(v *SpaceRecallBudget) bool { return v != nil })
}

// applyReflectModelBlocks copies the blocks a PUT or PATCH sent; an empty block clears it (back to inherited).
func applyReflectModelBlocks(c *SpaceConfig, in SpaceConfigInput) {
	c.ReflectDefaults = replaceBlock(c.ReflectDefaults, in.ReflectDefaults)
	c.MentalModelDefaults = replaceBlock(c.MentalModelDefaults, in.MentalModelDefaults)
	c.Consolidation = replaceBlock(c.Consolidation, in.Consolidation)
	c.RecallQuality = replaceBlock(c.RecallQuality, in.RecallQuality)
	c.RecallBudget = replaceBlock(c.RecallBudget, in.RecallBudget)
}

func replaceBlock[T any](cur, in *T) *T {
	if in == nil {
		return cur
	}
	if reflect.ValueOf(*in).IsZero() {
		return nil
	}
	return in
}
