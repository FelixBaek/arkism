package memory

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Space behaviour settings: the part of SpaceConfig other features read.
//
// An empty value inherits, first from the config of the server's default
// template (ARKISM_SPACE_DEFAULT_TEMPLATE), then from the server's built-in
// default. Defense rules add up instead: a space adds rules to the template's
// and never drops them. Only a person edits them (PUT/PATCH config).
//
// Defense rules run on every ingestion path, through defendSpace, before the
// privacy and secret checks: each claim of a capture (session and bundle imports
// included), every file or pasted text before the model sees it, knowledge
// documents, uploaded text files, supersede and review edits, and every memory,
// candidate or source text a create stores. A
// redact rule replaces each match with spaceRedactMark; a block rule refuses
// the claim or request. Patterns are Go regular expressions (RE2: linear time).

const (
	spaceExtractConcise  = "concise"
	spaceExtractVerbose  = "verbose"
	spaceExtractCustom   = "custom"
	spaceExtractVerbatim = "verbatim"
	// chunks stores each chunk of the text as a candidate as it stands, with no model call.
	spaceExtractChunks   = "chunks"
	spaceChunkMin        = 500
	spaceChunkMax        = 12000
	spaceStrategiesMax   = 16
	spaceDefenseMaxText  = 8 << 20
	spaceInstructionsMax = 2000
	spaceDefenseMax      = 32
	spacePatternMax      = 512
	spaceMCPToolsMax     = 64
	spaceRedactMark      = "[redacted]"
	spaceSourceSpace     = "space"
	spaceSourceTemplate  = "template"
	spaceSourceServer    = "server"
	spaceTemplateMax     = 1 << 20
)

var (
	spaceExtractModes  = []string{"", spaceExtractConcise, spaceExtractVerbose, spaceExtractCustom, spaceExtractVerbatim, spaceExtractChunks}
	spaceStrategyName  = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	spaceToolPattern   = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	spaceDefenseAction = []string{"redact", "block"}
	// ErrDefenseBlocked: a space defense rule refused the text.
	ErrDefenseBlocked = errors.New("defense_blocked")
	// ErrExtractionDisabled: the space's extraction_enabled is off.
	ErrExtractionDisabled = errors.New("extraction_disabled")
)

// SpaceLanes turns recall lanes on or off; nil inherits.
type SpaceLanes struct {
	FTS      *bool `json:"fts,omitempty" bson:"fts,omitempty"`
	Vector   *bool `json:"vector,omitempty" bson:"vector,omitempty"`
	Temporal *bool `json:"temporal,omitempty" bson:"temporal,omitempty"`
	Graph    *bool `json:"graph,omitempty" bson:"graph,omitempty"`
	// Semantic links each top hit to its nearest stored embeddings (off by default; Postgres only).
	Semantic *bool `json:"semantic,omitempty" bson:"semantic,omitempty"`
}

// SpaceAutoDerive tunes automatic derivation (auto_derive.go); empty fields inherit.
// Interval is a Go duration from 1m to 720h.
type SpaceAutoDerive struct {
	Enabled   *bool  `json:"enabled,omitempty" bson:"enabled,omitempty"`
	Threshold int    `json:"threshold,omitempty" bson:"threshold,omitempty"`
	Interval  string `json:"interval,omitempty" bson:"interval,omitempty"`
}

// ExtractStrategy is a set of extraction settings: a space's named strategy an item
// picks by name, or a dry run's overrides. Empty fields keep the space's setting.
type ExtractStrategy struct {
	Mode         string `json:"extraction_mode,omitempty" bson:"extraction_mode,omitempty"`
	Instructions string `json:"extraction_instructions,omitempty" bson:"extraction_instructions,omitempty"`
	Mission      string `json:"extraction_mission,omitempty" bson:"extraction_mission,omitempty"`
	ChunkSize    int    `json:"extraction_chunk_size,omitempty" bson:"extraction_chunk_size,omitempty"`
	Causal       *bool  `json:"extract_causal,omitempty" bson:"extract_causal,omitempty"`
}

// over lays the fields st sets over base.
func (st ExtractStrategy) over(base ExtractStrategy) ExtractStrategy {
	base.Mode, base.Instructions, base.Mission = cmp.Or(st.Mode, base.Mode), cmp.Or(st.Instructions, base.Instructions), cmp.Or(st.Mission, base.Mission)
	base.ChunkSize = cmp.Or(st.ChunkSize, base.ChunkSize)
	if st.Causal != nil {
		base.Causal = st.Causal
	}
	return base
}

// validExtractStrategy checks a strategy as stored; a resolved one must also name a mode.
func validExtractStrategy(st ExtractStrategy) error {
	if !slices.Contains(spaceExtractModes, st.Mode) || st.ChunkSize != 0 && (st.ChunkSize < spaceChunkMin || st.ChunkSize > spaceChunkMax) {
		return ErrInvalid
	}
	for _, text := range []string{st.Instructions, st.Mission} {
		if !utf8.ValidString(text) || utf8.RuneCountInString(text) > spaceInstructionsMax || strings.ContainsRune(text, 0) {
			return ErrInvalid
		}
		if containsSecret(text) {
			return ErrSecretDetected
		}
	}
	return nil
}

// causal reports whether causal candidates are asked for (off unless set).
func (st ExtractStrategy) causal() bool { return st.Causal != nil && *st.Causal }

// validExtractionSettings checks the extraction fields of a space config.
func validExtractionSettings(c SpaceConfig) error {
	if err := validExtractStrategy(c.extractBase()); err != nil {
		return err
	}
	if len(c.ExtractionStrategies) > spaceStrategiesMax || c.ExtractionDefaultStrategy != "" && !spaceStrategyName.MatchString(c.ExtractionDefaultStrategy) {
		return ErrInvalid
	}
	if c.DefaultTimezone != "" {
		if _, err := time.LoadLocation(c.DefaultTimezone); err != nil || c.DefaultTimezone == "Local" {
			return ErrInvalid
		}
	}
	for name, st := range c.ExtractionStrategies {
		if !spaceStrategyName.MatchString(name) {
			return ErrInvalid
		}
		if err := validExtractStrategy(st); err != nil {
			return err
		}
	}
	return nil
}

// extractBase is the space's own extraction settings as a strategy.
func (c SpaceConfig) extractBase() ExtractStrategy {
	return ExtractStrategy{Mode: c.ExtractionMode, Instructions: c.ExtractionInstructions, Mission: c.ExtractionMission, ChunkSize: c.ExtractionChunkSize, Causal: c.ExtractCausal}
}

// extractStrategy is the resolved space's extraction settings with the named strategy
// (or the space's default one) laid over them, then a dry run's overrides.
func (c SpaceConfig) extractStrategy(name string, override *ExtractStrategy) (ExtractStrategy, error) {
	out := c.extractBase()
	if name = cmp.Or(name, c.ExtractionDefaultStrategy); name != "" {
		st, ok := c.ExtractionStrategies[name]
		if !ok {
			return ExtractStrategy{}, ErrInvalid
		}
		out = st.over(out)
	}
	if override != nil {
		out = override.over(out)
	}
	if err := validExtractStrategy(out); err != nil {
		return ExtractStrategy{}, err
	}
	if out.Mode == spaceExtractCustom && strings.TrimSpace(out.Instructions) == "" {
		return ExtractStrategy{}, ErrInvalid
	}
	out.Mode, out.ChunkSize = cmp.Or(out.Mode, spaceExtractConcise), cmp.Or(out.ChunkSize, fileExtractChunkRunes)
	return out, nil
}

// zone is the space's time zone for reading dates; the server's when unset.
func (c SpaceConfig) zone() *time.Location {
	if z, err := time.LoadLocation(c.DefaultTimezone); c.DefaultTimezone != "" && err == nil {
		return z
	}
	return usageZone()
}

type SpaceDefenseRule struct {
	Name    string `json:"name,omitempty" bson:"name,omitempty"`
	Pattern string `json:"pattern,omitempty" bson:"pattern"`
	Action  string `json:"action" bson:"action"` // redact | block
}

// SpaceEffectiveView is a space's resolved settings and, per inherited field, the layer it came from.
type SpaceEffectiveView struct {
	WorkspaceID string            `json:"workspace_id"`
	ProjectID   string            `json:"project_id"`
	Revision    int64             `json:"revision"`
	Config      SpaceConfig       `json:"config"`
	Sources     map[string]string `json:"sources"`
}

// SpaceTemplate is the server's default template bundle (config, directives and mental model definitions).
type SpaceTemplate struct {
	Bundle string
	Config SpaceConfig
	// Auto applies the template to a space the first time a person gives it settings
	// or a memory (space_template.go); off unless ARKISM_SPACE_TEMPLATE_AUTO=1.
	Auto    bool
	applied sync.Map // scopes this process already applied the template to
}

// LoadSpaceTemplate reads a template bundle exported with template=true; "" means none.
func LoadSpaceTemplate(path string) (*SpaceTemplate, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > spaceTemplateMax {
		return nil, ErrInvalid
	}
	m, lines, err := parseSpaceBundle(string(raw))
	if err != nil || !m.Template {
		return nil, ErrInvalid
	}
	t := &SpaceTemplate{Bundle: string(raw)}
	for _, line := range lines {
		if line.Section == spaceSectionConfig && line.Config != nil {
			t.Config = *line.Config
		}
	}
	t.Config.Aliases = []string{}
	if validSpaceConfig(t.Config) != nil {
		return nil, ErrInvalid
	}
	return t, nil
}

func validSpaceSettings(c SpaceConfig) error {
	if !slices.Contains(spaceExtractModes, c.ExtractionMode) || utf8.RuneCountInString(c.ExtractionInstructions) > spaceInstructionsMax ||
		c.ExtractionMode == spaceExtractCustom && strings.TrimSpace(c.ExtractionInstructions) == "" ||
		c.RecallBudgetTokens < 0 || c.RecallBudgetTokens > 64000 || c.ReflectMaxTokens != 0 && (c.ReflectMaxTokens < 64 || c.ReflectMaxTokens > reflectMaxTokens) ||
		len(c.DefenseRules) > spaceDefenseMax || len(c.MCPTools) > spaceMCPToolsMax || !validLaneWeights(c.RecallLaneWeights) {
		return ErrInvalid
	}
	for i, tool := range c.MCPTools {
		if !spaceToolPattern.MatchString(tool) || slices.Contains(c.MCPTools[:i], tool) {
			return ErrInvalid
		}
	}
	if slices.ContainsFunc(c.DefenseRules, func(r SpaceDefenseRule) bool { return !validDefenseRule(r) }) ||
		!c.AutoDerive.valid() || !c.Audit.valid() || !slices.Contains(entityResolveModes, c.EntityResolution) || !validEntityLabels(c.EntityLabels) {
		return ErrInvalid
	}
	return validExtractionSettings(c)
}

func (a *SpaceAutoDerive) valid() bool {
	if a == nil {
		return true
	}
	_, ok := autoDeriveInterval(a.Interval)
	return (a.Threshold == 0 || a.Threshold >= 1 && a.Threshold <= 1000) && (a.Interval == "" || ok)
}

// validDefenseRule: a pattern that matches nothing-at-all or the redaction mark
// would rewrite every text, or its own output, again on each pass.
func validDefenseRule(r SpaceDefenseRule) bool {
	if !slices.Contains(spaceDefenseAction, r.Action) || r.Pattern == "" || len(r.Pattern) > spacePatternMax ||
		r.Name != "" && !validRunLabel(r.Name, 64) {
		return false
	}
	re, err := regexp.Compile(r.Pattern)
	return err == nil && !re.MatchString("") && !re.MatchString(spaceRedactMark)
}

// applySpaceSettingsInput copies the behaviour fields a PUT or PATCH sent.
func applySpaceSettingsInput(c *SpaceConfig, in SpaceConfigInput) {
	if in.ExtractionMode != nil {
		c.ExtractionMode = strings.TrimSpace(*in.ExtractionMode)
	}
	if in.ExtractionInstructions != nil {
		c.ExtractionInstructions = strings.TrimSpace(*in.ExtractionInstructions)
	}
	applySpaceLanes(c, in)
	if in.Rerank != nil || in.resets.rerank {
		c.Rerank = in.Rerank
	}
	if in.RerankModel != nil || in.resets.rerankModel {
		c.RerankModel = in.RerankModel
	}
	if in.DefenseRules != nil {
		c.DefenseRules = *in.DefenseRules
	}
	if in.MCPTools != nil {
		c.MCPTools = *in.MCPTools
	}
	if in.RecallBudgetTokens != nil {
		c.RecallBudgetTokens = *in.RecallBudgetTokens
	}
	if in.ReflectMaxTokens != nil {
		c.ReflectMaxTokens = *in.ReflectMaxTokens
	}
	if in.AutoDerive != nil {
		c.AutoDerive = mergeAutoDerive(c.AutoDerive, *in.AutoDerive)
	}
	if in.Audit != nil {
		c.Audit = in.Audit
		if c.Audit.Enabled == nil && len(c.Audit.Actions) == 0 {
			c.Audit = nil
		}
	}
	if in.EntityResolution != nil {
		c.EntityResolution = strings.TrimSpace(*in.EntityResolution)
	}
	if in.EntitiesAllowFreeForm != nil || in.resets.entitiesFreeForm {
		c.EntitiesAllowFreeForm = in.EntitiesAllowFreeForm
	}
	if in.EntityFreeTypes != nil || in.resets.entityFreeTypes {
		c.EntityFreeTypes = in.EntityFreeTypes
	}
	if in.EntityLabels != nil {
		c.EntityLabels = *in.EntityLabels
	}
	if in.RecallLaneWeights != nil {
		c.RecallLaneWeights = *in.RecallLaneWeights // {} hands the weights back
	}
	applyExtractionInput(c, in)
}

// applyExtractionInput copies the extraction fields a PUT or PATCH sent.
func applyExtractionInput(c *SpaceConfig, in SpaceConfigInput) {
	if in.ExtractionMission != nil {
		c.ExtractionMission = strings.TrimSpace(*in.ExtractionMission)
	}
	if in.ExtractionChunkSize != nil {
		c.ExtractionChunkSize = *in.ExtractionChunkSize
	}
	if in.ExtractCausal != nil || in.resets.extractCausal {
		c.ExtractCausal = in.ExtractCausal
	}
	if in.ExtractionStrategies != nil {
		c.ExtractionStrategies = *in.ExtractionStrategies
	}
	if in.ExtractionDefaultStrategy != nil {
		c.ExtractionDefaultStrategy = strings.TrimSpace(*in.ExtractionDefaultStrategy)
	}
	if in.DefaultTimezone != nil {
		c.DefaultTimezone = strings.TrimSpace(*in.DefaultTimezone)
	}
}

// spaceResets are the boolean settings a PATCH hands back to the template or
// server default by sending null: booleans have no empty value like "" or 0.
type spaceResets struct {
	lanes, rerank, rerankModel, extractCausal bool
	entitiesFreeForm, entityFreeTypes         bool
	lane                                      []string // lanes.<name>: null
}

// UnmarshalJSON decodes the input strictly and notes which boolean settings were sent as null.
func (in *SpaceConfigInput) UnmarshalJSON(b []byte) error {
	type plain SpaceConfigInput
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode((*plain)(in)); err != nil {
		return err
	}
	var raw struct {
		Lanes       json.RawMessage `json:"lanes"`
		Rerank      json.RawMessage `json:"rerank"`
		RerankModel json.RawMessage `json:"rerank_model"`
		FreeForm    json.RawMessage `json:"entities_allow_free_form"`
		FreeTypes   json.RawMessage `json:"entity_free_types"`
		Causal      json.RawMessage `json:"extract_causal"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	isNull := func(m json.RawMessage) bool { return string(m) == "null" }
	in.resets = spaceResets{lanes: isNull(raw.Lanes), rerank: isNull(raw.Rerank), rerankModel: isNull(raw.RerankModel),
		entitiesFreeForm: isNull(raw.FreeForm), entityFreeTypes: isNull(raw.FreeTypes), extractCausal: isNull(raw.Causal)}
	var lanes map[string]json.RawMessage
	if in.Lanes != nil && json.Unmarshal(raw.Lanes, &lanes) == nil {
		for _, f := range spaceLaneFields {
			if isNull(lanes[f.name]) {
				in.resets.lane = append(in.resets.lane, f.name)
			}
		}
	}
	return nil
}

// applySpaceLanes sets the lanes a PUT or PATCH sent; null or {} hands them all
// back, a lane sent as null hands back that lane.
func applySpaceLanes(c *SpaceConfig, in SpaceConfigInput) {
	if in.resets.lanes || in.Lanes != nil && *in.Lanes == (SpaceLanes{}) && len(in.resets.lane) == 0 {
		c.Lanes = nil
		return
	}
	if in.Lanes == nil {
		return
	}
	if c.Lanes == nil {
		c.Lanes = &SpaceLanes{}
	}
	for _, f := range spaceLaneFields {
		if v := *f.get(in.Lanes); v != nil || slices.Contains(in.resets.lane, f.name) {
			*f.get(c.Lanes) = v
		}
	}
	if *c.Lanes == (SpaceLanes{}) {
		c.Lanes = nil
	}
}

// mergeAutoDerive lays the fields a PATCH sent over the stored block; an all-empty block clears it.
func mergeAutoDerive(cur *SpaceAutoDerive, in SpaceAutoDerive) *SpaceAutoDerive {
	if in == (SpaceAutoDerive{}) {
		return nil
	}
	out := SpaceAutoDerive{}
	if cur != nil {
		out = *cur
	}
	if in.Enabled != nil {
		out.Enabled = in.Enabled
	}
	if in.Threshold != 0 {
		out.Threshold = in.Threshold
	}
	if in.Interval != "" {
		out.Interval = strings.TrimSpace(in.Interval)
	}
	return &out
}

var spaceLaneFields = []struct {
	name string
	get  func(*SpaceLanes) **bool
}{
	{"fts", func(l *SpaceLanes) **bool { return &l.FTS }},
	{"vector", func(l *SpaceLanes) **bool { return &l.Vector }},
	{"temporal", func(l *SpaceLanes) **bool { return &l.Temporal }},
	{"graph", func(l *SpaceLanes) **bool { return &l.Graph }},
	{"semantic", func(l *SpaceLanes) **bool { return &l.Semantic }},
}

type spaceLayer struct {
	name   string
	config SpaceConfig
}

// serverSpaceDefaults is the built-in layer every inherited field ends at.
func (s *Service) serverSpaceDefaults() SpaceConfig {
	on, off, graph, rerank, model := true, false, s.RecallGraph, s.Reranker != nil, false
	return SpaceConfig{ExtractionMode: spaceExtractConcise, Lanes: &SpaceLanes{FTS: &on, Vector: &on, Temporal: &on, Graph: &graph, Semantic: &off},
		Rerank: &rerank, RerankModel: &model, RecallBudgetTokens: 2000, ReflectMaxTokens: reflectDefaultMaxTokens, AutoDerive: serverAutoDerive(),
		RecallLaneWeights: serverLaneWeights(), EntityResolution: entityResolvePropose, EntitiesAllowFreeForm: &on, EntityFreeTypes: &off,
		ExtractionChunkSize: fileExtractChunkRunes, ExtractCausal: &model, DefaultTimezone: usageZone().String()}
}

func inheritSetting[T any](out *SpaceConfig, layers []spaceLayer, src map[string]string, name string, field func(*SpaceConfig) *T, set func(T) bool) {
	for _, l := range layers {
		if v := *field(&l.config); set(v) {
			*field(out), src[name] = v, l.name
			return
		}
	}
}

// resolveSpaceSettings fills every behaviour field of space from the first layer that sets it.
func (s *Service) resolveSpaceSettings(space SpaceConfig) (SpaceConfig, map[string]string) {
	layers := []spaceLayer{{spaceSourceSpace, space}}
	if s.SpaceTemplate != nil {
		layers = append(layers, spaceLayer{spaceSourceTemplate, s.SpaceTemplate.Config})
	}
	layers = append(layers, spaceLayer{spaceSourceServer, s.serverSpaceDefaults()})
	out, src := space, map[string]string{}
	str := func(v string) bool { return v != "" }
	num := func(v int) bool { return v != 0 }
	ptr := func(v *bool) bool { return v != nil }
	inheritSetting(&out, layers, src, "extraction_mode", func(c *SpaceConfig) *string { return &c.ExtractionMode }, str)
	inheritSetting(&out, layers, src, "extraction_instructions", func(c *SpaceConfig) *string { return &c.ExtractionInstructions }, str)
	inheritSetting(&out, layers, src, "rerank", func(c *SpaceConfig) **bool { return &c.Rerank }, ptr)
	inheritSetting(&out, layers, src, "rerank_model", func(c *SpaceConfig) **bool { return &c.RerankModel }, ptr)
	inheritSetting(&out, layers, src, "recall_budget_tokens", func(c *SpaceConfig) *int { return &c.RecallBudgetTokens }, num)
	inheritSetting(&out, layers, src, "reflect_max_tokens", func(c *SpaceConfig) *int { return &c.ReflectMaxTokens }, num)
	inheritSetting(&out, layers, src, "recall_lane_weights", func(c *SpaceConfig) *map[string]float64 { return &c.RecallLaneWeights }, func(v map[string]float64) bool { return len(v) > 0 })
	inheritSetting(&out, layers, src, "mcp_tools", func(c *SpaceConfig) *[]string { return &c.MCPTools }, func(v []string) bool { return len(v) > 0 })
	inheritSetting(&out, layers, src, "reflect_mission", func(c *SpaceConfig) *string { return &c.ReflectMission }, str)
	inheritSetting(&out, layers, src, "reflect_disposition", func(c *SpaceConfig) **ReflectDisposition { return &c.ReflectDisposition }, func(v *ReflectDisposition) bool { return v != nil })
	inheritSetting(&out, layers, src, "entity_resolution", func(c *SpaceConfig) *string { return &c.EntityResolution }, str)
	inheritSetting(&out, layers, src, "entities_allow_free_form", func(c *SpaceConfig) **bool { return &c.EntitiesAllowFreeForm }, ptr)
	inheritSetting(&out, layers, src, "entity_free_types", func(c *SpaceConfig) **bool { return &c.EntityFreeTypes }, ptr)
	inheritSetting(&out, layers, src, "entity_labels", func(c *SpaceConfig) *[]EntityLabelGroup { return &c.EntityLabels }, func(v []EntityLabelGroup) bool { return len(v) > 0 })
	resolveAutoDerive(&out, layers, src)
	resolveExtraction(&out, layers, src)
	out.Lanes = &SpaceLanes{}
	for _, f := range spaceLaneFields {
		inheritSetting(&out, layers, src, "lanes."+f.name, func(c *SpaceConfig) **bool {
			if c.Lanes == nil {
				c.Lanes = &SpaceLanes{}
			}
			return f.get(c.Lanes)
		}, ptr)
	}
	out.DefenseRules, src["defense_rules"] = []SpaceDefenseRule{}, spaceSourceServer
	from := []string{}
	for _, l := range slices.Backward(layers) {
		if len(l.config.DefenseRules) > 0 {
			out.DefenseRules, from = append(out.DefenseRules, l.config.DefenseRules...), append(from, l.name)
		}
	}
	if len(from) > 0 {
		src["defense_rules"] = strings.Join(from, "+")
	}
	return out, src
}

// resolveExtraction fills each extraction field past mode and instructions from the first layer that sets it.
func resolveExtraction(out *SpaceConfig, layers []spaceLayer, src map[string]string) {
	str := func(v string) bool { return v != "" }
	inheritSetting(out, layers, src, "extraction_mission", func(c *SpaceConfig) *string { return &c.ExtractionMission }, str)
	inheritSetting(out, layers, src, "extraction_chunk_size", func(c *SpaceConfig) *int { return &c.ExtractionChunkSize }, func(v int) bool { return v != 0 })
	inheritSetting(out, layers, src, "extract_causal", func(c *SpaceConfig) **bool { return &c.ExtractCausal }, func(v *bool) bool { return v != nil })
	inheritSetting(out, layers, src, "extraction_strategies", func(c *SpaceConfig) *map[string]ExtractStrategy { return &c.ExtractionStrategies },
		func(v map[string]ExtractStrategy) bool { return len(v) > 0 })
	inheritSetting(out, layers, src, "extraction_default_strategy", func(c *SpaceConfig) *string { return &c.ExtractionDefaultStrategy }, str)
	inheritSetting(out, layers, src, "default_timezone", func(c *SpaceConfig) *string { return &c.DefaultTimezone }, str)
}

// resolveAutoDerive fills each auto_derive field from the first layer that sets it.
func resolveAutoDerive(out *SpaceConfig, layers []spaceLayer, src map[string]string) {
	block := func(c *SpaceConfig) *SpaceAutoDerive {
		if c.AutoDerive == nil {
			c.AutoDerive = &SpaceAutoDerive{}
		}
		return c.AutoDerive
	}
	out.AutoDerive = &SpaceAutoDerive{}
	inheritSetting(out, layers, src, "auto_derive.enabled", func(c *SpaceConfig) **bool { return &block(c).Enabled }, func(v *bool) bool { return v != nil })
	inheritSetting(out, layers, src, "auto_derive.threshold", func(c *SpaceConfig) *int { return &block(c).Threshold }, func(v int) bool { return v != 0 })
	inheritSetting(out, layers, src, "auto_derive.interval", func(c *SpaceConfig) *string { return &block(c).Interval }, func(v string) bool { return v != "" })
}

// spaceSettings is the resolved settings of the space scope belongs to (internal read, no authorization).
// It is the accessor other features read their per-space settings through.
func (s *Service) spaceSettings(ctx context.Context, scope Scope) (SpaceConfig, error) {
	rec, err := s.Repo.Get(ctx, colSpaceConfigs, spaceScope(scope), spaceConfigID)
	view, err := spaceView(spaceScope(scope), rec, err)
	if err != nil {
		return SpaceConfig{}, err
	}
	out, _ := s.resolveSpaceSettings(view.Config)
	return out, nil
}

// searchSpaceSettings reads the settings only after the caller passes the memory-search check,
// so a refused request reads nothing of the space.
func (s *Service) searchSpaceSettings(ctx context.Context, token string, scope Scope) (SpaceConfig, error) {
	if _, err := s.authorize(ctx, token, "search:memories", scope, "", ""); err != nil {
		return SpaceConfig{}, err
	}
	return s.spaceSettings(ctx, scope)
}

// EffectiveSpaceConfig shows a space's resolved settings and where each came from.
// Defense rules are shown to people only (shownSpaceConfig); sources still says where rules come from.
func (s *Service) EffectiveSpaceConfig(ctx context.Context, token string, scope Scope) (SpaceEffectiveView, error) {
	scope, err := s.resolveSpace(ctx, scope)
	if err != nil {
		return SpaceEffectiveView{}, err
	}
	// search:memories: the agents whose recall these settings shape may read them.
	d, err := s.authorize(ctx, token, "search:memories", scope, "", "")
	if err != nil {
		return SpaceEffectiveView{}, err
	}
	rec, err := s.Repo.Get(ctx, colSpaceConfigs, scope, spaceConfigID)
	view, err := spaceView(scope, rec, err)
	if err != nil {
		return SpaceEffectiveView{}, err
	}
	config, src := s.resolveSpaceSettings(view.Config)
	if d.Role != roleHuman {
		// search:memories is wider than read:space_configs: the owner's notes stay with the config route.
		config.Notes = ""
	}
	config = shownSpaceConfig(config, d.Role)
	return SpaceEffectiveView{WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, Revision: view.Revision, Config: config, Sources: src}, nil
}

// shownSpaceConfig is c as role may see it. Defense rules, names and patterns alike,
// are for people only: a rule's name or pattern tells an agent what to word around.
// Other settings shape the agent's own work, so it may read them.
func shownSpaceConfig(c SpaceConfig, role string) SpaceConfig {
	if role != roleHuman {
		c.DefenseRules = nil
	}
	return c
}

// lane reports whether a resolved recall lane is on; a missing value is on.
func (c SpaceConfig) lane(name string) bool {
	if c.Lanes == nil {
		return true
	}
	for _, f := range spaceLaneFields {
		if f.name == name {
			v := *f.get(c.Lanes)
			return v == nil || *v
		}
	}
	return true
}

// defendSpace is the one entry every ingestion path calls (capture, create, file
// and pasted-text extraction, knowledge documents, uploaded text, supersede and
// review edits): it applies the space's defense rules to texts in place before
// the privacy and secret checks. Callers already past their authorization pass
// a zero guard; a path that defends ahead of it passes its check as guard.
func (s *Service) defendSpace(ctx context.Context, token string, guard defenseGuard, scope Scope, texts ...*string) error {
	cfg, err := s.spaceSettings(ctx, scope)
	if err != nil {
		return err
	}
	return s.defendWith(ctx, token, guard, scope, cfg.DefenseRules, texts...)
}

// defenseGuard is the authorization a block is reported under. With a token
// given, a block is shown (and audited) only to a caller that check allows (and,
// with human set, only to a person); anyone else gets the check's own error, so
// unauthenticated or unauthorized callers cannot probe the rules.
type defenseGuard struct {
	action, id string
	human      bool
}

// defendWith is defendSpace with rules the caller already read.
func (s *Service) defendWith(ctx context.Context, token string, guard defenseGuard, scope Scope, rules []SpaceDefenseRule, texts ...*string) error {
	err := defendAll(rules, texts...)
	if !errors.Is(err, ErrDefenseBlocked) {
		return err
	}
	if token != "" {
		d, authErr := s.authorize(ctx, token, guard.action, scope, guard.id, "")
		if authErr == nil && guard.human && d.Role != roleHuman {
			authErr = ErrDenied
		}
		if authErr != nil {
			return authErr
		}
	}
	s.auditDeniedInput(ctx, guard.action+":defense_blocked", scope, "")
	return err
}

// defendAll applies the rules to every text in place; it stops at the first block.
// Work is bounded: at most two layers of spaceDefenseMax linear-time patterns over
// texts of at most spaceDefenseMaxText bytes each.
func defendAll(rules []SpaceDefenseRule, texts ...*string) error {
	if len(rules) == 0 {
		return nil
	}
	compiled := make([]*regexp.Regexp, len(rules))
	for i, r := range rules {
		compiled[i], _ = regexp.Compile(r.Pattern) // validated on write; an unreadable stored rule is skipped, not fatal
	}
	for _, t := range texts {
		if len(*t) > spaceDefenseMaxText {
			return errBlobTooLarge
		}
		for i, re := range compiled {
			if re == nil || !re.MatchString(*t) {
				continue
			}
			if rules[i].Action == "block" {
				return ErrDefenseBlocked
			}
			*t = re.ReplaceAllLiteralString(*t, spaceRedactMark)
		}
	}
	return nil
}

// defendCreate runs the space rules on the text a memory, candidate or source create stores.
func (s *Service) defendCreate(ctx context.Context, token, col string, in *CreateInput, file *textArtifact) error {
	if col != colMemories && col != "memory_candidates" && col != "sources" {
		return nil
	}
	cfg, err := s.spaceSettings(ctx, in.Scope)
	if err != nil {
		return err
	}
	texts := []*string{&in.Text, &in.Summary}
	in.Entities = slices.Clone(in.Entities)
	for i := range in.Entities {
		texts = append(texts, &in.Entities[i].Name)
	}
	var content string
	if file != nil {
		content = string(file.content)
		texts = append(texts, &content)
	}
	if err := s.defendWith(ctx, token, defenseGuard{action: "write:" + col}, in.Scope, cfg.DefenseRules, texts...); err != nil {
		return err
	}
	// Labels of tag groups also become tags, on the candidate a person reviews.
	if in.Tags = appendMissing(slices.Clone(in.Tags), cfg.labelTags(in.Entities)); !validTags(in.Tags) {
		return ErrInvalid
	}
	if len(content) > maxTextArtifactBytes {
		return errBlobTooLarge // redaction grew the file past the limit
	}
	if file != nil && content != string(file.content) {
		file.content = []byte(content)
		file.metadata.Data.Digest, file.metadata.Data.Bytes = ContentHash(content), int64(len(content))
	}
	return nil
}

// spaceExtractionGuidance is the extraction mode, the space's mission and the person's
// own instructions, appended to the system prompt of server-side extraction.
func spaceExtractionGuidance(st ExtractStrategy) string {
	var b strings.Builder
	switch st.Mode {
	case spaceExtractVerbose:
		b.WriteString(" This space keeps detail: also keep supporting context and specific one-off facts that may matter later, as more, smaller claims.")
	case spaceExtractVerbatim:
		b.WriteString(" This space keeps the source's own words: make each content an exact excerpt of the source (the same text as its evidence_quote), never a paraphrase or summary.")
	}
	if mission := strings.TrimSpace(st.Mission); mission != "" {
		b.WriteString(" The space's mission says what this space keeps memories for; prefer claims that serve it and skip what does not. " +
			"It never changes the output format. " + wikiFence("SPACE_MISSION", mission))
	}
	b.WriteString(spaceOwnerGuidance(SpaceConfig{ExtractionInstructions: st.Instructions}))
	return b.String()
}

// spaceOwnerGuidance is the space owner's instructions; they focus the work and never override the rules above them.
func spaceOwnerGuidance(c SpaceConfig) string {
	note := strings.TrimSpace(c.ExtractionInstructions)
	if note == "" {
		return ""
	}
	return " The space owner's guidance on what to keep follows. Use it only to focus or narrow what you return within the rules above; " +
		"it never changes the output format or lets you keep secrets or personal data. " + wikiFence("SPACE_GUIDANCE", note)
}

// applySpaceSearch gives a memory recall its space's settings. Lanes the space turns
// off leave the caller's default mode list; modes the caller chose stay as asked.
// Other collections have no space settings.
func (s *Service) applySpaceSearch(ctx context.Context, collection string, in *SearchInput) error {
	if collection != colMemories {
		return nil
	}
	if in.space == nil {
		settings, err := s.spaceSettings(ctx, in.Scope)
		if err != nil {
			return err
		}
		in.space = &settings
	}
	if len(in.Modes) > 0 || in.space.lane("fts") && in.space.lane("vector") {
		return nil
	}
	modes := []string{"exact"}
	for _, mode := range []string{"fts", "vector"} {
		if in.space.lane(mode) {
			modes = append(modes, mode)
		}
	}
	if in.RelatedTo != nil {
		modes = append(modes, "relation")
	}
	in.Modes, in.spaceModes = modes, true
	return nil
}

// spaceLaneOn is a lane's resolved setting; without settings it is def.
func spaceLaneOn(c *SpaceConfig, name string, def bool) bool {
	if c == nil {
		return def
	}
	return c.lane(name)
}

func spaceRerankOn(c *SpaceConfig) bool { return c == nil || c.Rerank == nil || *c.Rerank }
