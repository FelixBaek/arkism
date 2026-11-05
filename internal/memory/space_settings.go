package memory

import (
	"context"
	"errors"
	"os"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Space behaviour settings: the part of SpaceConfig other features read.
//
// An empty value inherits, first from the config of the server's default
// template (ARKISM_SPACE_DEFAULT_TEMPLATE), then from the server's built-in
// default. Defense rules add up instead: a space adds rules to the template's
// and never drops them. Only a person edits them (PUT/PATCH config).
//
// Defense rules run on every ingestion path before the privacy and secret
// checks: each claim of a capture, every file or pasted text before the model
// sees it, and every memory, candidate or source text a create stores. A
// redact rule replaces each match with spaceRedactMark; a block rule refuses
// the claim or request. Patterns are Go regular expressions (RE2: linear time).

const (
	spaceExtractConcise  = "concise"
	spaceExtractVerbose  = "verbose"
	spaceExtractCustom   = "custom"
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
	spaceExtractModes  = []string{"", spaceExtractConcise, spaceExtractVerbose, spaceExtractCustom}
	spaceToolPattern   = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	spaceDefenseAction = []string{"redact", "block"}
	// ErrDefenseBlocked: a space defense rule refused the text.
	ErrDefenseBlocked = errors.New("defense_blocked")
)

// SpaceLanes turns recall lanes on or off; nil inherits.
type SpaceLanes struct {
	FTS      *bool `json:"fts,omitempty" bson:"fts,omitempty"`
	Vector   *bool `json:"vector,omitempty" bson:"vector,omitempty"`
	Temporal *bool `json:"temporal,omitempty" bson:"temporal,omitempty"`
	Graph    *bool `json:"graph,omitempty" bson:"graph,omitempty"`
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
		len(c.DefenseRules) > spaceDefenseMax || len(c.MCPTools) > spaceMCPToolsMax {
		return ErrInvalid
	}
	for i, tool := range c.MCPTools {
		if !spaceToolPattern.MatchString(tool) || slices.Contains(c.MCPTools[:i], tool) {
			return ErrInvalid
		}
	}
	for _, r := range c.DefenseRules {
		if !validDefenseRule(r) {
			return ErrInvalid
		}
	}
	if containsSecret(c.ExtractionInstructions) {
		return ErrSecretDetected
	}
	return nil
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
	if in.Lanes != nil {
		if c.Lanes == nil {
			c.Lanes = &SpaceLanes{}
		}
		for _, f := range spaceLaneFields {
			if v := *f.get(in.Lanes); v != nil {
				*f.get(c.Lanes) = v
			}
		}
	}
	if in.Rerank != nil {
		c.Rerank = in.Rerank
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
}

var spaceLaneFields = []struct {
	name string
	get  func(*SpaceLanes) **bool
}{
	{"fts", func(l *SpaceLanes) **bool { return &l.FTS }},
	{"vector", func(l *SpaceLanes) **bool { return &l.Vector }},
	{"temporal", func(l *SpaceLanes) **bool { return &l.Temporal }},
	{"graph", func(l *SpaceLanes) **bool { return &l.Graph }},
}

type spaceLayer struct {
	name   string
	config SpaceConfig
}

// serverSpaceDefaults is the built-in layer every inherited field ends at.
func (s *Service) serverSpaceDefaults() SpaceConfig {
	on, graph, rerank := true, s.RecallGraph, s.Reranker != nil
	return SpaceConfig{ExtractionMode: spaceExtractConcise, Lanes: &SpaceLanes{FTS: &on, Vector: &on, Temporal: &on, Graph: &graph},
		Rerank: &rerank, RecallBudgetTokens: 2000, ReflectMaxTokens: reflectDefaultMaxTokens}
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
	inheritSetting(&out, layers, src, "recall_budget_tokens", func(c *SpaceConfig) *int { return &c.RecallBudgetTokens }, num)
	inheritSetting(&out, layers, src, "reflect_max_tokens", func(c *SpaceConfig) *int { return &c.ReflectMaxTokens }, num)
	inheritSetting(&out, layers, src, "mcp_tools", func(c *SpaceConfig) *[]string { return &c.MCPTools }, func(v []string) bool { return len(v) > 0 })
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

// spaceSettings is the resolved settings of the space scope belongs to (internal read, no authorization).
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
// Defense patterns are shown to people only; other callers see each rule's name and action.
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
		config.DefenseRules = slices.Clone(config.DefenseRules)
		for i := range config.DefenseRules {
			config.DefenseRules[i].Pattern = ""
		}
	}
	return SpaceEffectiveView{WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, Revision: view.Revision, Config: config, Sources: src}, nil
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

// defend applies the rules to text: the redacted text, or ErrDefenseBlocked.
func defend(rules []SpaceDefenseRule, text string) (string, error) {
	for _, r := range rules {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			continue // validated on write; an unreadable stored rule is skipped, not fatal
		}
		if !re.MatchString(text) {
			continue
		}
		if r.Action == "block" {
			return "", ErrDefenseBlocked
		}
		text = re.ReplaceAllLiteralString(text, spaceRedactMark)
	}
	return text, nil
}

// defendAll applies the rules to every text in place; it stops at the first block.
func defendAll(rules []SpaceDefenseRule, texts ...*string) error {
	if len(rules) == 0 {
		return nil
	}
	for _, t := range texts {
		v, err := defend(rules, *t)
		if err != nil {
			return err
		}
		*t = v
	}
	return nil
}

// defendCreate runs the space rules on the text a memory, candidate or source create stores.
func (s *Service) defendCreate(ctx context.Context, col string, in *CreateInput) error {
	if col != colMemories && col != "memory_candidates" && col != "sources" {
		return nil
	}
	cfg, err := s.spaceSettings(ctx, in.Scope)
	if err != nil {
		return err
	}
	return defendAll(cfg.DefenseRules, &in.Text, &in.Summary)
}

// spaceExtractionGuidance is the space's extraction mode and the person's own
// instructions, appended to the system prompt of server-side extraction.
func spaceExtractionGuidance(c SpaceConfig) string {
	var b strings.Builder
	if c.ExtractionMode == spaceExtractVerbose {
		b.WriteString(" This space keeps detail: also keep supporting context and specific one-off facts that may matter later, as more, smaller claims.")
	}
	b.WriteString(spaceOwnerGuidance(c))
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
