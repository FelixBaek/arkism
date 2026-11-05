package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Memory spaces: one workspace+project scope seen as a unit a person can name,
// configure, export, import and clone. A space is the project-level scope
// (user_id and agent_id empty); user and agent sub-scopes are not part of it.
//
// Settings live in space_configs, a private collection only these routes
// touch. Export, import and clone are rows in jobs (kinds the job worker never
// claims) run in the background; an export keeps its bundle on the job row.
//
// Bundle: JSON lines. Line 1 is the manifest (format, version, counts and the
// sha256 of every following byte); each later line is one config or record.
// Imported memories go through the ordinary capture path, so privacy and secret
// checks, dedupe and per-event idempotency apply, and they arrive as
// candidates. Only a person may import as approved, and only records the
// bundle marks approved.

const (
	colSpaceConfigs       = "space_configs"
	spaceConfigID         = "space-config"
	spaceJobExport        = "space_export"
	spaceJobImport        = "space_import"
	spaceJobClone         = "space_clone"
	spaceBundleFormat     = "arkism.space.bundle"
	spaceBundleVersion    = 1
	spaceBundleMaxBytes   = 8 << 20
	spaceBundleMaxRecords = 5000
	spaceAliasMax         = 16
	spaceListMax          = 200
	spaceImportChunk      = 32
	spaceJobTimeout       = 10 * time.Minute
	spaceExportTTL        = 24 * time.Hour
	spaceSectionConfig    = "config"
	// spaceExportDestination: a bundle leaves the server, so the export needs
	// the principal's external:transfer grant, as every other transfer does.
	spaceExportDestination = "external:space_bundle"
)

var (
	// spaceExportable: collections a bundle may carry. Names missing from
	// Collections at runtime are skipped and reported, never guessed. Observations
	// stay home: they cite memory ids that do not exist in another space.
	spaceExportable   = []string{colMemories, "memory_candidates", colMentalModels, colDirectives, "entities"}
	spaceDefinitions  = []string{colDirectives, colMentalModels}
	spaceAliasPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
	spaceSchedules    = []string{"off", "daily", "weekly"}
	spaceGenerator    = Generator{Model: "none", PromptVersion: "space-bundle-v1", PipelineVersion: "space-import-v1"}
)

type SpaceConfig struct {
	DisplayName       string   `json:"display_name" bson:"display_name"`
	Aliases           []string `json:"aliases" bson:"aliases"`
	DefaultReach      string   `json:"default_reach" bson:"default_reach"`
	ExtractionEnabled bool     `json:"extraction_enabled" bson:"extraction_enabled"`
	// RerankEnabled is the old advisory switch nothing read; kept so older bundles still import. Rerank replaces it.
	RerankEnabled         bool   `json:"rerank_enabled,omitempty" bson:"rerank_enabled,omitempty"`
	ConsolidationSchedule string `json:"consolidation_schedule" bson:"consolidation_schedule"`
	Notes                 string `json:"notes" bson:"notes"`

	// Behaviour settings (space_settings.go): an empty value inherits the server default.
	ExtractionMode         string             `json:"extraction_mode,omitempty" bson:"extraction_mode,omitempty"`
	ExtractionInstructions string             `json:"extraction_instructions,omitempty" bson:"extraction_instructions,omitempty"`
	Lanes                  *SpaceLanes        `json:"lanes,omitempty" bson:"lanes,omitempty"`
	Rerank                 *bool              `json:"rerank,omitempty" bson:"rerank,omitempty"`
	RerankModel            *bool              `json:"rerank_model,omitempty" bson:"rerank_model,omitempty"`
	DefenseRules           []SpaceDefenseRule `json:"defense_rules,omitempty" bson:"defense_rules,omitempty"`
	MCPTools               []string           `json:"mcp_tools,omitempty" bson:"mcp_tools,omitempty"`
	RecallBudgetTokens     int                `json:"recall_budget_tokens,omitempty" bson:"recall_budget_tokens,omitempty"`
	ReflectMaxTokens       int                `json:"reflect_max_tokens,omitempty" bson:"reflect_max_tokens,omitempty"`
	AutoDerive             *SpaceAutoDerive   `json:"auto_derive,omitempty" bson:"auto_derive,omitempty"`
	// RecallLaneWeights scales each recall lane in fusion (recall_weights.go).
	RecallLaneWeights map[string]float64 `json:"recall_lane_weights,omitempty" bson:"recall_lane_weights,omitempty"`
	// ReflectMission and ReflectDisposition guide reflect answers in this space (reflect.go).
	ReflectMission     string              `json:"reflect_mission" bson:"reflect_mission"`
	ReflectDisposition *ReflectDisposition `json:"reflect_disposition,omitempty" bson:"reflect_disposition,omitempty"`
	// Audit narrows the optional audit events of this space (audit_policy.go); nil records all.
	Audit *SpaceAudit `json:"audit,omitempty" bson:"audit,omitempty"`
	// Entity settings (entities_similar.go, entity_labels.go): resolution exact|propose|similar,
	// free-form names next to labels, types beyond the built-in list, and the label schema.
	EntityResolution      string             `json:"entity_resolution,omitempty" bson:"entity_resolution,omitempty"`
	EntitiesAllowFreeForm *bool              `json:"entities_allow_free_form,omitempty" bson:"entities_allow_free_form,omitempty"`
	EntityFreeTypes       *bool              `json:"entity_free_types,omitempty" bson:"entity_free_types,omitempty"`
	EntityLabels          []EntityLabelGroup `json:"entity_labels,omitempty" bson:"entity_labels,omitempty"`
	// Extraction settings past mode and instructions (space_settings.go): the mission, chunk size,
	// causal candidates, named strategies an item may pick, and the zone dates are read in.
	ExtractionMission         string                     `json:"extraction_mission,omitempty" bson:"extraction_mission,omitempty"`
	ExtractionChunkSize       int                        `json:"extraction_chunk_size,omitempty" bson:"extraction_chunk_size,omitempty"`
	ExtractCausal             *bool                      `json:"extract_causal,omitempty" bson:"extract_causal,omitempty"`
	ExtractionStrategies      map[string]ExtractStrategy `json:"extraction_strategies,omitempty" bson:"extraction_strategies,omitempty"`
	ExtractionDefaultStrategy string                     `json:"extraction_default_strategy,omitempty" bson:"extraction_default_strategy,omitempty"`
	DefaultTimezone           string                     `json:"default_timezone,omitempty" bson:"default_timezone,omitempty"`
	// Reflect, mental-model and consolidation blocks (reflect_models_settings.go).
	ReflectDefaults     *SpaceReflectDefaults     `json:"reflect_default_options,omitempty" bson:"reflect_default_options,omitempty"`
	MentalModelDefaults *SpaceMentalModelDefaults `json:"mental_model_defaults,omitempty" bson:"mental_model_defaults,omitempty"`
	Consolidation       *SpaceConsolidation       `json:"consolidation,omitempty" bson:"consolidation,omitempty"`
}

func defaultSpaceConfig() SpaceConfig {
	return SpaceConfig{Aliases: []string{}, ExtractionEnabled: true, ConsolidationSchedule: "off"}
}

// SpaceConfigInput is PUT (unset fields take defaults) or PATCH (unset fields stay).
type SpaceConfigInput struct {
	ExpectedRevision      int64   `json:"expected_revision"`
	DisplayName           *string `json:"display_name,omitempty"`
	DefaultReach          *string `json:"default_reach,omitempty"`
	ExtractionEnabled     *bool   `json:"extraction_enabled,omitempty"`
	ConsolidationSchedule *string `json:"consolidation_schedule,omitempty"`
	Notes                 *string `json:"notes,omitempty"`
	ReflectMission        *string `json:"reflect_mission,omitempty"`
	// ReflectDisposition: an all-zero value clears it.
	ReflectDisposition *ReflectDisposition `json:"reflect_disposition,omitempty"`

	// Behaviour settings (space_settings.go).
	ExtractionMode         *string             `json:"extraction_mode,omitempty"`
	ExtractionInstructions *string             `json:"extraction_instructions,omitempty"`
	Lanes                  *SpaceLanes         `json:"lanes,omitempty"`
	Rerank                 *bool               `json:"rerank,omitempty"`
	RerankModel            *bool               `json:"rerank_model,omitempty"`
	DefenseRules           *[]SpaceDefenseRule `json:"defense_rules,omitempty"`
	MCPTools               *[]string           `json:"mcp_tools,omitempty"`
	RecallBudgetTokens     *int                `json:"recall_budget_tokens,omitempty"`
	ReflectMaxTokens       *int                `json:"reflect_max_tokens,omitempty"`
	AutoDerive             *SpaceAutoDerive    `json:"auto_derive,omitempty"`
	// Audit replaces the whole block; {} hands it back to recording everything.
	Audit                 *SpaceAudit `json:"audit,omitempty"`
	EntityResolution      *string     `json:"entity_resolution,omitempty"`
	EntitiesAllowFreeForm *bool       `json:"entities_allow_free_form,omitempty"`
	EntityFreeTypes       *bool       `json:"entity_free_types,omitempty"`
	// EntityLabels: [] hands the schema back to the template or server default.
	EntityLabels      *[]EntityLabelGroup `json:"entity_labels,omitempty"`
	RecallLaneWeights *map[string]float64 `json:"recall_lane_weights,omitempty"`
	// Extraction settings; an extraction_strategies map replaces the stored one ({} clears it).
	ExtractionMission         *string                     `json:"extraction_mission,omitempty"`
	ExtractionChunkSize       *int                        `json:"extraction_chunk_size,omitempty"`
	ExtractCausal             *bool                       `json:"extract_causal,omitempty"`
	ExtractionStrategies      *map[string]ExtractStrategy `json:"extraction_strategies,omitempty"`
	ExtractionDefaultStrategy *string                     `json:"extraction_default_strategy,omitempty"`
	DefaultTimezone           *string                     `json:"default_timezone,omitempty"`
	ReflectDefaults           *SpaceReflectDefaults       `json:"reflect_default_options,omitempty"`
	MentalModelDefaults       *SpaceMentalModelDefaults   `json:"mental_model_defaults,omitempty"`
	Consolidation             *SpaceConsolidation         `json:"consolidation,omitempty"`

	resets spaceResets // keys a PATCH sent as null (UnmarshalJSON)
}

type SpaceView struct {
	WorkspaceID string           `json:"workspace_id"`
	ProjectID   string           `json:"project_id"`
	Revision    int64            `json:"revision"`
	Config      SpaceConfig      `json:"config"`
	Counts      map[string]int64 `json:"counts,omitempty"`
}

type SpacePage struct {
	Items []SpaceView `json:"items"`
	Next  string      `json:"next,omitempty"`
}

type SpaceCount struct {
	ProjectID  string
	Collection string
	Count      int64
}

// spaceStore is the cross-project read a space list and alias lookup need.
// Row-level security still limits both to the caller's permitted projects.
type spaceStore interface {
	SpaceCounts(ctx context.Context, workspace, after string, collections []string, limit int) ([]SpaceCount, error)
	SpaceAliasOwners(ctx context.Context, workspace, alias string) ([]Record, error)
}

type SpaceSection struct {
	Name  string `json:"name" bson:"name"`
	Count int    `json:"count" bson:"count"`
}

type SpaceSkip struct {
	Section string `json:"section" bson:"section"`
	Reason  string `json:"reason" bson:"reason"`
	Count   int    `json:"count" bson:"count"`
}

type SpaceManifest struct {
	Format        string         `json:"format" bson:"format"`
	FormatVersion int            `json:"format_version" bson:"format_version"`
	BundleID      string         `json:"bundle_id" bson:"bundle_id"`
	Template      bool           `json:"template" bson:"template"`
	Source        Scope          `json:"source" bson:"source"`
	CreatedAt     time.Time      `json:"created_at" bson:"created_at"`
	Sections      []SpaceSection `json:"sections" bson:"sections"`
	Skipped       []SpaceSkip    `json:"skipped" bson:"skipped"`
	SHA256        string         `json:"sha256" bson:"sha256"`
}

// SpaceBundleRecord is the portable shape of one exported record.
type SpaceBundleRecord struct {
	ID            string         `json:"id"`
	Kind          string         `json:"kind"`
	Status        string         `json:"status"`
	Approved      bool           `json:"approved"`
	Text          string         `json:"text"`
	Summary       string         `json:"summary,omitempty"`
	Keywords      []string       `json:"keywords,omitempty"`
	Tags          []string       `json:"tags,omitempty"`
	MemoryContext *MemoryContext `json:"memory_context,omitempty"`
	Directive     *Directive     `json:"directive,omitempty"`
	// MentalModel carries only the definition; a person refreshes and approves the text in the new space.
	MentalModel *MentalModelInput `json:"mental_model,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
}

type spaceBundleLine struct {
	Manifest *SpaceManifest     `json:"manifest,omitempty"`
	Section  string             `json:"section,omitempty"`
	Config   *SpaceConfig       `json:"config,omitempty"`
	Record   *SpaceBundleRecord `json:"record,omitempty"`
}

// SpaceReport is what an import or clone did, per section.
type SpaceReport struct {
	Imported int         `json:"imported" bson:"imported"`
	Approved int         `json:"approved" bson:"approved"`
	Merged   int         `json:"merged" bson:"merged"`
	Rejected int         `json:"rejected" bson:"rejected"`
	Config   string      `json:"config,omitempty" bson:"config,omitempty"`
	Skipped  []SpaceSkip `json:"skipped" bson:"skipped"`
}

type SpaceJob struct {
	Kind       string         `json:"kind" bson:"kind"`
	State      string         `json:"state" bson:"state"` // queued | done | failed
	Include    []string       `json:"include,omitempty" bson:"include,omitempty"`
	Template   bool           `json:"template" bson:"template"`
	ImportAs   string         `json:"import_as,omitempty" bson:"import_as,omitempty"`
	Target     *Scope         `json:"target,omitempty" bson:"target,omitempty"`
	BundleID   string         `json:"bundle_id,omitempty" bson:"bundle_id,omitempty"`
	Manifest   *SpaceManifest `json:"manifest,omitempty" bson:"manifest,omitempty"`
	Report     *SpaceReport   `json:"report,omitempty" bson:"report,omitempty"`
	Error      string         `json:"error,omitempty" bson:"error,omitempty"`
	FinishedAt *time.Time     `json:"finished_at,omitempty" bson:"finished_at,omitempty"`
	Bundle     string         `json:"-" bson:"bundle,omitempty"`
}

type SpaceJobView struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	SpaceJob
}

type SpaceExportInput struct {
	Include  []string `json:"include,omitempty"`
	Template bool     `json:"template,omitempty"`
}

type SpaceImportInput struct {
	Bundle   string `json:"bundle"`
	ImportAs string `json:"import_as,omitempty"`
	// DefaultTemplate imports the server's default template (ARKISM_SPACE_DEFAULT_TEMPLATE) instead of Bundle.
	DefaultTemplate bool `json:"default_template,omitempty"`
}

type SpaceCloneInput struct {
	Target   Scope    `json:"target"`
	Include  []string `json:"include,omitempty"`
	ImportAs string   `json:"import_as,omitempty"`
}

func validImportAs(v string) bool { return v == "" || v == "candidate" || v == "approved" }

// spaceScope is the project-level scope of a space.
func spaceScope(s Scope) Scope { return Scope{WorkspaceID: s.WorkspaceID, ProjectID: s.ProjectID} }

// resolveSpace maps "@alias" in the project position to the space that owns
// the alias; any other project name is taken literally.
func (s *Service) resolveSpace(ctx context.Context, scope Scope) (Scope, error) {
	scope = spaceScope(scope)
	alias, ok := strings.CutPrefix(scope.ProjectID, "@")
	if !ok {
		return scope, nil
	}
	if !spaceAliasPattern.MatchString(alias) {
		return Scope{}, ErrInvalid
	}
	store, ok := s.Repo.(spaceStore)
	if !ok {
		return Scope{}, ErrUnavailable
	}
	owners, err := store.SpaceAliasOwners(ctx, scope.WorkspaceID, alias)
	switch {
	case err != nil:
		return Scope{}, err
	case len(owners) == 0:
		return Scope{}, ErrNotFound
	case len(owners) > 1:
		return Scope{}, ErrConflict
	}
	return spaceScope(owners[0].Scope), nil
}

// ListSpaces pages the spaces of the caller's workspace the caller may read, by project id.
func (s *Service) ListSpaces(ctx context.Context, token string, scope Scope, after string, limit int) (SpacePage, error) {
	if limit < 1 || limit > spaceListMax {
		limit = spaceListMax
	}
	if _, err := s.authorize(ctx, token, "read:memories", spaceScope(scope), "", ""); err != nil {
		return SpacePage{}, err
	}
	store, ok := s.Repo.(spaceStore)
	if !ok {
		return SpacePage{}, ErrUnavailable
	}
	rows, err := store.SpaceCounts(ctx, scope.WorkspaceID, after, append(slices.Clone(spaceExportable), colSpaceConfigs), limit+1)
	if err != nil {
		return SpacePage{}, err
	}
	counts, order := map[string]map[string]int64{}, []string{}
	for _, row := range rows {
		if counts[row.ProjectID] == nil {
			counts[row.ProjectID] = map[string]int64{}
			order = append(order, row.ProjectID)
		}
		if row.Collection != colSpaceConfigs {
			counts[row.ProjectID][row.Collection] = row.Count
		}
	}
	page := SpacePage{Items: []SpaceView{}}
	if len(order) > limit {
		order, page.Next = order[:limit], order[limit-1]
	}
	for _, project := range order {
		// Listed ids are literal projects: a project named "@x" is not the alias x.
		view, err := s.getSpaceConfig(ctx, token, Scope{WorkspaceID: scope.WorkspaceID, ProjectID: project})
		if errors.Is(err, ErrDenied) {
			continue
		}
		if err != nil {
			return SpacePage{}, err
		}
		view.Counts = counts[project]
		page.Items = append(page.Items, view)
	}
	return page, nil
}

func (s *Service) GetSpaceConfig(ctx context.Context, token string, scope Scope) (SpaceView, error) {
	scope, err := s.resolveSpace(ctx, scope)
	if err != nil {
		return SpaceView{}, err
	}
	return s.getSpaceConfig(ctx, token, scope)
}

func (s *Service) getSpaceConfig(ctx context.Context, token string, scope Scope) (SpaceView, error) {
	d, err := s.authorize(ctx, token, "read:"+colSpaceConfigs, scope, spaceConfigID, "")
	if err != nil {
		return SpaceView{}, err
	}
	rec, err := s.Repo.Get(ctx, colSpaceConfigs, scope, spaceConfigID)
	view, err := spaceView(scope, rec, err)
	view.Config = shownSpaceConfig(view.Config, d.Role)
	return view, err
}

func spaceView(scope Scope, rec Record, err error) (SpaceView, error) {
	view := SpaceView{WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, Config: defaultSpaceConfig()}
	if errors.Is(err, ErrNotFound) {
		return view, nil
	}
	if err != nil {
		return SpaceView{}, err
	}
	if rec.SpaceConfig == nil {
		return SpaceView{}, ErrUnavailable
	}
	view.Revision, view.Config = rec.Revision, *rec.SpaceConfig
	return view, nil
}

// PutSpaceConfig replaces (replace) or patches the settings; human principals only.
func (s *Service) PutSpaceConfig(ctx context.Context, token string, scope Scope, in SpaceConfigInput, replace bool) (SpaceView, error) {
	scope, err := s.resolveSpace(ctx, scope)
	if err != nil {
		return SpaceView{}, err
	}
	d, err := s.mergeHuman(ctx, token, "write:"+colSpaceConfigs, scope, spaceConfigID)
	if err != nil {
		return SpaceView{}, err
	}
	out, err := s.changeSpaceConfig(ctx, scope, d, in.ExpectedRevision, func(c *SpaceConfig) error { return applySpaceConfig(c, in, replace) })
	if err == nil && out.Revision == 1 {
		s.autoApplySpaceTemplate(ctx, token, d, scope, true)
	}
	if err == nil {
		s.scheduleDerive(ctx, scope) // the consolidation schedule or a mental-model default may have changed
	}
	return out, err
}

func applySpaceConfig(c *SpaceConfig, in SpaceConfigInput, replace bool) error {
	if replace {
		aliases := c.Aliases
		*c = defaultSpaceConfig()
		c.Aliases = aliases
	}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	set(&c.DisplayName, in.DisplayName)
	set(&c.DefaultReach, in.DefaultReach)
	set(&c.ConsolidationSchedule, in.ConsolidationSchedule)
	set(&c.Notes, in.Notes)
	set(&c.ReflectMission, in.ReflectMission)
	if in.ReflectDisposition != nil {
		c.ReflectDisposition = in.ReflectDisposition
		if *in.ReflectDisposition == (ReflectDisposition{}) {
			c.ReflectDisposition = nil
		}
	}
	if in.ExtractionEnabled != nil {
		c.ExtractionEnabled = *in.ExtractionEnabled
	}
	c.RerankEnabled = false
	applySpaceSettingsInput(c, in)
	return validSpaceConfig(*c)
}

func validSpaceConfig(c SpaceConfig) error {
	if len(c.DisplayName) > 256 || len(c.Notes) > 4000 || len(c.Aliases) > spaceAliasMax ||
		!slices.Contains([]string{"", ReachPersonal, ReachGeneral, ReachProject}, c.DefaultReach) || !slices.Contains(spaceSchedules, c.ConsolidationSchedule) ||
		utf8.RuneCountInString(c.ReflectMission) > reflectMissionMax || !utf8.ValidString(c.ReflectMission) || !c.ReflectDisposition.valid() {
		return ErrInvalid
	}
	if containsSecret(c.DisplayName) || containsSecret(c.Notes) || containsSecret(c.ReflectMission) {
		return ErrSecretDetected
	}
	return validSpaceSettings(c)
}

// changeSpaceConfig applies change to the stored (or default) config under a revision check.
func (s *Service) changeSpaceConfig(ctx context.Context, scope Scope, d Decision, expected int64, change func(*SpaceConfig) error) (SpaceView, error) {
	var out Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		rec, err := tx.Get(ctx, colSpaceConfigs, scope, spaceConfigID)
		fresh := errors.Is(err, ErrNotFound)
		if err != nil && !fresh {
			return err
		}
		if expected >= 0 && rec.Revision != expected {
			return ErrConflict
		}
		now := time.Now().UTC()
		config := defaultSpaceConfig()
		if !fresh && rec.SpaceConfig != nil {
			config = *rec.SpaceConfig
			config.Aliases = slices.Clone(config.Aliases)
		}
		if err = change(&config); err != nil {
			return err
		}
		if fresh {
			rec = Record{ID: spaceConfigID, Scope: scope, Kind: "space_config", Status: statusActive, CreatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role}
		}
		rec.SpaceConfig, rec.UpdatedAt, rec.Revision = &config, now, rec.Revision+1
		if fresh {
			err = tx.Insert(ctx, colSpaceConfigs, rec)
		} else {
			err = tx.Replace(ctx, colSpaceConfigs, rec, rec.Revision-1)
		}
		if err != nil {
			return err
		}
		out = rec
		return audit(ctx, tx, scope, d.PrincipalID, "write:"+colSpaceConfigs, spaceConfigID)
	})
	return spaceView(scope, out, err)
}

// SetSpaceAlias adds (add) or removes an alias; an alias names one space per workspace.
func (s *Service) SetSpaceAlias(ctx context.Context, token string, scope Scope, alias string, add bool) (SpaceView, error) {
	scope, err := s.resolveSpace(ctx, scope)
	if err != nil {
		return SpaceView{}, err
	}
	if !spaceAliasPattern.MatchString(alias) {
		return SpaceView{}, ErrInvalid
	}
	d, err := s.mergeHuman(ctx, token, "write:"+colSpaceConfigs, scope, spaceConfigID)
	if err != nil {
		return SpaceView{}, err
	}
	if add {
		owner, err := s.resolveSpace(ctx, Scope{WorkspaceID: scope.WorkspaceID, ProjectID: "@" + alias})
		if err == nil && owner != scope || errors.Is(err, ErrConflict) {
			return SpaceView{}, ErrHandleTaken
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			return SpaceView{}, err
		}
	}
	return s.changeSpaceConfig(ctx, scope, d, -1, func(c *SpaceConfig) error {
		c.Aliases = slices.DeleteFunc(c.Aliases, func(a string) bool { return a == alias })
		if add {
			c.Aliases = append(c.Aliases, alias)
		}
		return validSpaceConfig(*c)
	})
}

// spaceRecordInactive: expired, or an agent's proposal, which is not a definition a person made.
func spaceRecordInactive(col string, r Record, now time.Time) bool {
	return r.ExpiresAt != nil && !r.ExpiresAt.After(now) || spaceDefinitionCollection(col) && r.Status == obsProposed
}

// spaceRecordSkip says why a stored record stays out of a bundle ("" = it goes in).
func spaceRecordSkip(col string, r Record, now time.Time) string {
	if r.ContentErased || r.PrivacyQuarantined || r.PrivacyDeletion != nil || r.Removal != nil {
		return "erased"
	}
	transferable := transferableContent(r.Sensitivity, recordContent(r))
	if spaceDefinitionCollection(col) && r.Sensitivity == "" {
		// Definitions carry no sensitivity label; a person wrote them and secrets were refused then.
		transferable = (r.Directive != nil || r.MentalModel != nil) && safeTransfer(structuredText([]any{r.Text, r.Directive, mentalModelDefinition(r)}))
	}
	if r.Sensitivity == "private" || !transferable {
		return "privacy_excluded"
	}
	if spaceRecordInactive(col, r, now) {
		return "inactive"
	}
	switch col {
	case colMemories:
		if r.Status != "approved" && r.Status != statusActive {
			return "inactive"
		}
	case "memory_candidates":
		if r.Status != "candidate" {
			return "inactive"
		}
	default:
		if slices.Contains([]string{"rejected", "expired", "superseded"}, r.Status) {
			return "inactive"
		}
	}
	return ""
}

func spaceSections(in SpaceExportInput) []string {
	if in.Template {
		return append([]string{spaceSectionConfig}, spaceDefinitions...)
	}
	if len(in.Include) == 0 {
		return append([]string{spaceSectionConfig}, spaceExportable...)
	}
	return in.Include
}

func validSpaceInclude(include []string) bool {
	if len(include) > 32 {
		return false
	}
	for i, name := range include {
		if !validRunLabel(name, 64) || slices.Contains(include[:i], name) {
			return false
		}
	}
	return true
}

// buildSpaceBundle writes the bundle of one space; it reads only that exact scope.
// role is the exporter's: the settings it carries are the ones that role may see (shownSpaceConfig).
func buildSpaceBundle(ctx context.Context, repo Repository, scope Scope, in SpaceExportInput, role, bundleID string, now time.Time) (string, SpaceManifest, error) {
	manifest := SpaceManifest{Format: spaceBundleFormat, FormatVersion: spaceBundleVersion, BundleID: bundleID, Template: in.Template, Source: scope, CreatedAt: now, Sections: []SpaceSection{}, Skipped: []SpaceSkip{}}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	total := 0
	for _, name := range spaceSections(in) {
		lines, skipped, err := spaceSectionLines(ctx, repo, scope, name, role, now)
		if err != nil {
			return "", SpaceManifest{}, err
		}
		manifest.Skipped = append(manifest.Skipped, skipped...)
		if total+len(lines) > spaceBundleMaxRecords {
			return "", SpaceManifest{}, errBlobTooLarge
		}
		total += len(lines)
		for _, line := range lines {
			if err = enc.Encode(line); err != nil {
				return "", SpaceManifest{}, ErrUnavailable
			}
		}
		manifest.Sections = append(manifest.Sections, SpaceSection{Name: name, Count: len(lines)})
	}
	manifest.SHA256 = ContentHash(body.String())
	head, _ := json.Marshal(spaceBundleLine{Manifest: &manifest})
	if len(head)+1+body.Len() > spaceBundleMaxBytes {
		return "", SpaceManifest{}, errBlobTooLarge
	}
	return string(head) + "\n" + body.String(), manifest, nil
}

func spaceSectionLines(ctx context.Context, repo Repository, scope Scope, name, role string, now time.Time) ([]spaceBundleLine, []SpaceSkip, error) {
	if name == spaceSectionConfig {
		rec, err := repo.Get(ctx, colSpaceConfigs, scope, spaceConfigID)
		if errors.Is(err, ErrNotFound) || err == nil && rec.SpaceConfig == nil {
			return nil, nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		config := shownSpaceConfig(*rec.SpaceConfig, role)
		return []spaceBundleLine{{Section: name, Config: &config}}, nil, nil
	}
	if !slices.Contains(spaceExportable, name) {
		return nil, []SpaceSkip{{Section: name, Reason: "not_exportable", Count: 1}}, nil
	}
	if !slices.Contains(Collections, name) {
		return nil, []SpaceSkip{{Section: name, Reason: "unknown_collection", Count: 1}}, nil
	}
	rows, err := collectCatalogRecords(ctx, repo, scope, name)
	if err != nil {
		return nil, nil, err
	}
	lines, reasons := []spaceBundleLine{}, map[string]int{}
	for _, r := range rows {
		if why := spaceRecordSkip(name, r, now); why != "" {
			reasons[why]++
			continue
		}
		lines = append(lines, spaceBundleLine{Section: name, Record: &SpaceBundleRecord{ID: r.ID, Kind: r.Kind, Status: r.Status,
			Approved: name == colMemories, Text: r.Text, Summary: r.Summary, Keywords: r.Keywords, Tags: r.Tags,
			MemoryContext: copyMemoryContext(r.MemoryContext), Directive: r.Directive, MentalModel: mentalModelDefinition(r), CreatedAt: r.CreatedAt}})
	}
	skipped := []SpaceSkip{}
	for _, why := range []string{"erased", "privacy_excluded", "inactive"} {
		if reasons[why] > 0 {
			skipped = append(skipped, SpaceSkip{Section: name, Reason: why, Count: reasons[why]})
		}
	}
	return lines, skipped, nil
}

// parseSpaceBundle checks the manifest, format version and sha256 before any line is used.
func parseSpaceBundle(bundle string) (SpaceManifest, []spaceBundleLine, error) {
	head, body, ok := strings.Cut(bundle, "\n")
	var first spaceBundleLine
	if !ok || len(bundle) > spaceBundleMaxBytes || json.Unmarshal([]byte(head), &first) != nil || first.Manifest == nil {
		return SpaceManifest{}, nil, ErrInvalid
	}
	m := *first.Manifest
	if m.Format != spaceBundleFormat || m.FormatVersion != spaceBundleVersion || !validRunLabel(m.BundleID, 64) || m.SHA256 != ContentHash(body) {
		return SpaceManifest{}, nil, ErrInvalid
	}
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	lines := []spaceBundleLine{}
	for dec.More() {
		var line spaceBundleLine
		if dec.Decode(&line) != nil || line.Manifest != nil || len(lines) >= spaceBundleMaxRecords {
			return SpaceManifest{}, nil, ErrInvalid
		}
		lines = append(lines, line)
	}
	return m, lines, nil
}

// ExportSpace starts an export job of one space.
func (s *Service) ExportSpace(ctx context.Context, token string, scope Scope, in SpaceExportInput) (SpaceJobView, error) {
	scope, err := s.resolveSpace(ctx, scope)
	if err != nil {
		return SpaceJobView{}, err
	}
	if !validSpaceInclude(in.Include) {
		return SpaceJobView{}, ErrInvalid
	}
	d, err := s.authorizeSpaceExport(ctx, token, scope, "")
	if err != nil {
		return SpaceJobView{}, err
	}
	job := SpaceJob{Kind: spaceJobExport, Include: in.Include, Template: in.Template, BundleID: "sb_" + ID()}
	return s.startSpaceJob(ctx, scope, d, job, func(ctx context.Context, job *SpaceJob) error {
		bundle, manifest, err := buildSpaceBundle(ctx, s.Repo, scope, in, d.Role, job.BundleID, time.Now().UTC().Truncate(time.Millisecond))
		job.Bundle, job.Manifest = bundle, &manifest
		return err
	})
}

// ImportSpace verifies a bundle and starts its import into a space; human principals only.
func (s *Service) ImportSpace(ctx context.Context, token string, scope Scope, in SpaceImportInput) (SpaceJobView, error) {
	scope, err := s.resolveSpace(ctx, scope)
	if err != nil {
		return SpaceJobView{}, err
	}
	if !validImportAs(in.ImportAs) {
		return SpaceJobView{}, ErrInvalid
	}
	d, err := s.mergeHuman(ctx, token, "write:memory_candidates", scope, "")
	if err != nil {
		return SpaceJobView{}, err
	}
	manifest, lines, err := s.importBundle(ctx, scope, in)
	if err != nil {
		return SpaceJobView{}, err
	}
	job := SpaceJob{Kind: spaceJobImport, ImportAs: in.ImportAs, Template: manifest.Template, BundleID: manifest.BundleID, Manifest: &manifest}
	return s.startSpaceJob(ctx, scope, d, job, func(ctx context.Context, job *SpaceJob) error {
		report, err := s.importSpaceLines(ctx, token, d, scope, manifest, lines, in.ImportAs == "approved")
		job.Report = &report
		return err
	})
}

// importBundle parses the bundle to import. A template sets up a new space: it
// goes only into a space without stored settings, so applying it twice cannot
// duplicate its directives and mental models.
func (s *Service) importBundle(ctx context.Context, scope Scope, in SpaceImportInput) (SpaceManifest, []spaceBundleLine, error) {
	bundle := in.Bundle
	if in.DefaultTemplate {
		if s.SpaceTemplate == nil {
			return SpaceManifest{}, nil, ErrNotFound
		}
		if bundle != "" {
			return SpaceManifest{}, nil, ErrInvalid
		}
		bundle = s.SpaceTemplate.Bundle
	}
	manifest, lines, err := parseSpaceBundle(bundle)
	if err != nil || !manifest.Template {
		return manifest, lines, err
	}
	_, err = s.Repo.Get(ctx, colSpaceConfigs, scope, spaceConfigID)
	if err == nil {
		return SpaceManifest{}, nil, ErrConflict
	}
	if !errors.Is(err, ErrNotFound) {
		return SpaceManifest{}, nil, err
	}
	return manifest, lines, nil
}

// CloneSpace copies one space into another (export then import, inside the server).
// Copies arrive as candidates; approved stays approved only for a human caller who asks.
func (s *Service) CloneSpace(ctx context.Context, token string, scope Scope, in SpaceCloneInput) (SpaceJobView, error) {
	scope, err := s.resolveSpace(ctx, scope)
	if err != nil {
		return SpaceJobView{}, err
	}
	if !validScope(in.Target) || !validSpaceInclude(in.Include) || !validImportAs(in.ImportAs) {
		return SpaceJobView{}, ErrInvalid
	}
	target, err := s.resolveSpace(ctx, in.Target)
	if err != nil {
		return SpaceJobView{}, err
	}
	if target == scope {
		return SpaceJobView{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "read:memories", scope, "", "")
	if err != nil {
		return SpaceJobView{}, err
	}
	td, err := s.authorize(ctx, token, "write:memory_candidates", target, "", "")
	if err != nil {
		return SpaceJobView{}, err
	}
	approved := in.ImportAs == "approved"
	if approved && (d.Role != roleHuman || td.Role != roleHuman) {
		return SpaceJobView{}, ErrDenied
	}
	job := SpaceJob{Kind: spaceJobClone, Include: in.Include, ImportAs: in.ImportAs, Target: &target, BundleID: "sb_" + ID()}
	return s.startSpaceJob(ctx, scope, d, job, func(ctx context.Context, job *SpaceJob) error {
		bundle, manifest, err := buildSpaceBundle(ctx, s.Repo, scope, SpaceExportInput{Include: in.Include}, d.Role, job.BundleID, time.Now().UTC().Truncate(time.Millisecond))
		if err != nil {
			return err
		}
		job.Manifest = &manifest
		_, lines, err := parseSpaceBundle(bundle)
		if err != nil {
			return err
		}
		report, err := s.importSpaceLines(ctx, token, td, target, manifest, lines, approved)
		job.Report = &report
		return err
	})
}

// startSpaceJob stores a queued job and runs it in the background with the caller's request values.
func (s *Service) startSpaceJob(ctx context.Context, scope Scope, d Decision, job SpaceJob, run func(context.Context, *SpaceJob) error) (SpaceJobView, error) {
	now := time.Now().UTC()
	job.State = "queued"
	rec := Record{ID: "spj_" + ID(), Scope: scope, Kind: job.Kind, Status: JobStatusDone, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role, SpaceJob: &job}
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.Insert(ctx, colJobs, rec); err != nil {
			return err
		}
		return audit(ctx, tx, scope, d.PrincipalID, "start:"+job.Kind, rec.ID)
	})
	if err != nil {
		return SpaceJobView{}, err
	}
	go func() {
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), spaceJobTimeout)
		defer cancel()
		result := job
		err := run(runCtx, &result)
		finished := time.Now().UTC()
		result.State, result.FinishedAt = "done", &finished
		if err != nil {
			result.State, result.Error, result.Bundle = "failed", captureErrorCode(err), ""
		}
		_ = s.Repo.Transaction(runCtx, func(ctx context.Context, tx Tx) error {
			cur, err := tx.Get(ctx, colJobs, scope, rec.ID)
			if err != nil {
				return err
			}
			cur.SpaceJob, cur.UpdatedAt, cur.Revision = &result, finished, cur.Revision+1
			return tx.Replace(ctx, colJobs, cur, cur.Revision-1)
		})
	}()
	return SpaceJobView{ID: rec.ID, WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, SpaceJob: job}, nil
}

// authorizeSpaceExport is the read of a space whose content leaves the server.
func (s *Service) authorizeSpaceExport(ctx context.Context, token string, scope Scope, id string) (Decision, error) {
	d, err := s.authorizeAt(ctx, token, "read:memories", scope, id, "", spaceExportDestination)
	if err != nil {
		return Decision{}, err
	}
	if !d.ExternalTransfer || !d.ContentSafe {
		return Decision{}, ErrDenied
	}
	return d, nil
}

func (s *Service) spaceJob(ctx context.Context, token string, scope Scope, id string) (Record, error) {
	scope, err := s.resolveSpace(ctx, scope)
	if err != nil {
		return Record{}, err
	}
	if _, err = s.authorize(ctx, token, "read:memories", scope, id, ""); err != nil {
		return Record{}, err
	}
	rec, err := s.Repo.Get(ctx, colJobs, scope, id)
	if err != nil {
		return Record{}, err
	}
	if rec.SpaceJob == nil || !strings.HasPrefix(rec.Kind, "space_") {
		return Record{}, ErrNotFound
	}
	return rec, nil
}

func (s *Service) GetSpaceJob(ctx context.Context, token string, scope Scope, id string) (SpaceJobView, error) {
	rec, err := s.spaceJob(ctx, token, scope, id)
	if err != nil {
		return SpaceJobView{}, err
	}
	view := SpaceJobView{ID: rec.ID, WorkspaceID: rec.Scope.WorkspaceID, ProjectID: rec.Scope.ProjectID, SpaceJob: *rec.SpaceJob}
	// Jobs run in-process: one still queued past its timeout died with a restart.
	if view.State == "queued" && time.Since(rec.CreatedAt) > spaceJobTimeout+time.Minute {
		view.State, view.Error = "failed", "interrupted"
	}
	return view, nil
}

// DownloadSpaceExport returns a finished export bundle. A record erased, made
// private or retired since the export makes the bundle stale: export again.
func (s *Service) DownloadSpaceExport(ctx context.Context, token string, scope Scope, id string) (string, error) {
	rec, err := s.spaceJob(ctx, token, scope, id)
	if err != nil {
		return "", err
	}
	job := rec.SpaceJob
	if rec.Kind != spaceJobExport || job.State != "done" || job.Bundle == "" {
		return "", ErrNotFound
	}
	if time.Since(rec.CreatedAt) > spaceExportTTL {
		s.dropSpaceBundle(ctx, rec)
		return "", ErrNotFound
	}
	if _, err = s.authorizeSpaceExport(ctx, token, rec.Scope, id); err != nil {
		return "", err
	}
	_, lines, err := parseSpaceBundle(job.Bundle)
	if err != nil {
		return "", ErrUnavailable
	}
	if err = s.checkSpaceBundleCurrent(ctx, rec.Scope, lines); errors.Is(err, ErrConflict) {
		s.dropSpaceBundle(ctx, rec)
	}
	if err != nil {
		return "", err
	}
	return job.Bundle, nil
}

func (s *Service) checkSpaceBundleCurrent(ctx context.Context, scope Scope, lines []spaceBundleLine) error {
	now := time.Now().UTC()
	for _, line := range lines {
		if line.Record == nil {
			continue
		}
		cur, err := s.Repo.Get(ctx, line.Section, scope, line.Record.ID)
		if errors.Is(err, ErrNotFound) || err == nil && spaceRecordSkip(line.Section, cur, now) != "" {
			return ErrConflict
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// dropSpaceBundle clears an expired or stale bundle from its job row so erased
// or retired text does not stay at rest there. Best effort: a caller who may
// only read cannot write, and the bundle is refused either way.
// shortcut: bundles nobody asks for again stay until a sweep exists; add one to maintenance when jobs get retention.
func (s *Service) dropSpaceBundle(ctx context.Context, rec Record) {
	_ = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		cur, err := tx.Get(ctx, colJobs, rec.Scope, rec.ID)
		if err != nil || cur.SpaceJob == nil {
			return err
		}
		job := *cur.SpaceJob
		job.Bundle = ""
		cur.SpaceJob, cur.UpdatedAt, cur.Revision = &job, time.Now().UTC(), cur.Revision+1
		return tx.Replace(ctx, colJobs, cur, cur.Revision-1)
	})
}

// importSpaceLines brings bundle lines into a space. Memories go through Capture
// in chunks of one event each ("space-import:<bundle>:<n>"), so a re-import of
// the same bundle is a duplicate event and stores nothing twice.
func (s *Service) importSpaceLines(ctx context.Context, token string, d Decision, scope Scope, m SpaceManifest, lines []spaceBundleLine, approved bool) (SpaceReport, error) {
	report := SpaceReport{Skipped: []SpaceSkip{}}
	var claims []CaptureClaim
	var origin []*SpaceBundleRecord
	for _, line := range lines {
		switch {
		case line.Section == spaceSectionConfig && line.Config != nil:
			report.Config = s.importSpaceConfig(ctx, d, scope, *line.Config)
		case line.Record != nil && (line.Section == colMemories || line.Section == "memory_candidates"):
			claims, origin = append(claims, spaceClaim(m.BundleID, line.Section, *line.Record)), append(origin, line.Record)
		case line.Record != nil && slices.Contains(spaceDefinitions, line.Section) && slices.Contains(Collections, line.Section):
			s.importSpaceDefinition(ctx, token, d.Role, scope, line, &report)
		default:
			report.Skipped = append(report.Skipped, SpaceSkip{Section: line.Section, Reason: "unknown_section", Count: 1})
		}
	}
	for start := 0; start < len(claims); start += spaceImportChunk {
		end := min(start+spaceImportChunk, len(claims))
		if err := s.importSpaceChunk(ctx, token, scope, m, start/spaceImportChunk, claims[start:end], origin[start:end], approved, &report); err != nil {
			return report, err
		}
	}
	return report, nil
}

// spaceClaim keeps the provenance: the bundle id rides on the capture event and
// source, the original collection and id in the candidate's summary.
func spaceClaim(bundleID, section string, r SpaceBundleRecord) CaptureClaim {
	kind := r.Kind
	if !slices.Contains(captureKinds, kind) {
		kind = "semantic"
	}
	subject := "import"
	if len(r.Keywords) > 0 && validRunLabel(r.Keywords[0], 128) {
		subject = r.Keywords[0]
	}
	c := CaptureClaim{Subject: subject, Text: r.Text, Kind: kind, Basis: "inferred", MemoryContext: copyMemoryContext(r.MemoryContext),
		Reason: cutUTF8("space bundle "+bundleID+" original "+section+"/"+r.ID, 500)}
	if !validCaptureClaim(c) {
		c.MemoryContext = nil
	}
	return c
}

func (s *Service) importSpaceChunk(ctx context.Context, token string, scope Scope, m SpaceManifest, n int, claims []CaptureClaim, origin []*SpaceBundleRecord, approved bool, report *SpaceReport) error {
	at := m.CreatedAt.UTC().Truncate(time.Millisecond)
	if at.After(time.Now()) || at.IsZero() {
		at = time.Now().UTC().Truncate(time.Millisecond)
	}
	gen := spaceGenerator
	out, err := s.Capture(ctx, token, CaptureInput{Scope: scope, Claims: claims, Generator: &gen,
		Event: CaptureEvent{ID: "space-import:" + m.BundleID + ":" + strconv.Itoa(n), Type: "backfill", OriginProduct: "space_import", SessionID: m.BundleID, OccurredAt: &at}})
	if err != nil {
		return err
	}
	for i, item := range out.Items {
		switch item.Status {
		case "candidate":
			report.Imported++
			if approved && origin[i].Approved && item.Reason == "review_required" && s.approveImported(ctx, token, scope, item.CandidateID) == nil {
				report.Approved++
			}
		case "merged":
			report.Merged++
		default:
			report.Rejected++
		}
	}
	return nil
}

// approveImported is the person's own approval, through the candidate review path.
func (s *Service) approveImported(ctx context.Context, token string, scope Scope, id string) error {
	cand, err := s.Repo.Get(ctx, "memory_candidates", scope, id)
	if err != nil {
		return err
	}
	_, err = s.ReviewCandidate(ctx, token, id, CandidateReviewInput{Scope: scope, ExpectedRevision: cand.Revision, Action: "approve"})
	return err
}

// importSpaceConfig sets a bundle's settings only on a space that has none; aliases stay with their source space.
func (s *Service) importSpaceConfig(ctx context.Context, d Decision, scope Scope, c SpaceConfig) string {
	if d.Role != roleHuman {
		return "skipped_not_human"
	}
	c.Aliases = []string{}
	if validSpaceConfig(c) != nil {
		return "invalid"
	}
	if _, err := s.changeSpaceConfig(ctx, scope, d, 0, func(dst *SpaceConfig) error { *dst = c; return nil }); err != nil {
		return "kept_existing"
	}
	return "applied"
}

// importSpaceDefinition hands a definition to its own collection's create path and rules.
func (s *Service) importSpaceDefinition(ctx context.Context, token, role string, scope Scope, line spaceBundleLine, report *SpaceReport) {
	r := line.Record
	// Both are human-written: an agent importer would only turn them into its own proposals.
	err := ErrInvalid
	switch {
	case role != roleHuman:
		err = ErrDenied
	case line.Section == colDirectives && r.Directive != nil:
		active := r.Status == statusActive
		_, err = s.CreateDirective(ctx, token, DirectiveInput{Scope: scope, Title: r.Directive.Title, Rule: r.Text,
			Priority: r.Directive.Priority, AppliesTo: r.Directive.AppliesTo, Active: &active, Tags: r.Tags})
	case line.Section == colMentalModels && r.MentalModel != nil:
		in := *r.MentalModel
		in.Scope, in.ExpectedRevision = scope, 0
		_, err = s.CreateMentalModel(ctx, token, in)
	}
	if err != nil {
		report.Skipped = append(report.Skipped, SpaceSkip{Section: line.Section, Reason: captureErrorCode(err), Count: 1})
		return
	}
	report.Imported++
}

func spaceDefinitionCollection(col string) bool { return slices.Contains(spaceDefinitions, col) }

// mentalModelDefinition is the part of a mental model a bundle carries: no model-written text.
func mentalModelDefinition(r Record) *MentalModelInput {
	if r.MentalModel == nil {
		return nil
	}
	return &MentalModelInput{Title: r.MentalModel.Title, Query: r.MentalModel.Query, Refresh: r.MentalModel.Refresh}
}
