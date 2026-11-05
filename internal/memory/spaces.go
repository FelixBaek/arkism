package memory

import (
	"context"
	"errors"
	"io"
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
// Bundle: JSON lines (space_stream.go). Line 1 is the manifest, each later
// line one config, record or history row, the last a trailer with the counts and
// the sha256 of every line between. Downloads stream fresh from the store, so
// nothing erased since the export leaves, and no bundle rests on the job row.
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
	spaceBundleVersion    = 2
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
	// Collections at runtime are skipped and reported, never guessed.
	spaceExportable = []string{colMemories, "memory_candidates", colMentalModels, colDirectives, "entities", colObservations}
	// spaceData is the "data" part (the default with config). Observations are their own
	// part: they arrive as text candidates, since the memory ids they cite stay home.
	spaceData         = []string{colMemories, "memory_candidates", colMentalModels, colDirectives, "entities"}
	spaceCaptured     = []string{colMemories, "memory_candidates", colObservations}
	spaceHistory      = []string{"audit_events", colLLMRequests}
	spaceParts        = []string{spaceSectionConfig, "data", colObservations, "history", spaceSectionKnowledge, spaceSectionAttachments, spaceSectionDocuments}
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
	ExtractionBatchSize       int                        `json:"extraction_batch_size,omitempty" bson:"extraction_batch_size,omitempty"`
	ExtractionImagesPerChunk  int                        `json:"extraction_images_per_chunk,omitempty" bson:"extraction_images_per_chunk,omitempty"`
	ExtractCausal             *bool                      `json:"extract_causal,omitempty" bson:"extract_causal,omitempty"`
	ExtractionStrategies      map[string]ExtractStrategy `json:"extraction_strategies,omitempty" bson:"extraction_strategies,omitempty"`
	ExtractionDefaultStrategy string                     `json:"extraction_default_strategy,omitempty" bson:"extraction_default_strategy,omitempty"`
	DefaultTimezone           string                     `json:"default_timezone,omitempty" bson:"default_timezone,omitempty"`
	// MemoryLanguage is the language captured memories are written in: "auto" (the server
	// default) keeps the language of the person's own words, or a language tag such as "ko".
	MemoryLanguage string `json:"memory_language,omitempty" bson:"memory_language,omitempty"`
	// WorkStateCandidates (opt-in) turns a saved work checkpoint or handoff into decision
	// candidates for "where the work stands"; RecallSessionArchives (opt-in) adds a session-archive lane to search.
	WorkStateCandidates   *bool `json:"work_state_candidates,omitempty" bson:"work_state_candidates,omitempty"`
	RecallSessionArchives *bool `json:"recall_session_archives,omitempty" bson:"recall_session_archives,omitempty"`
	// CaptureMode is whose words the capture hook extracts: user (the default) or assistant_results
	// (opt-in: also results the agent confirmed and other speakers' statements, as assistant_result
	// candidates). CaptureDensity (durable, balanced, the default, or dense) is how much it keeps.
	CaptureMode    string `json:"capture_mode,omitempty" bson:"capture_mode,omitempty"`
	CaptureDensity string `json:"capture_density,omitempty" bson:"capture_density,omitempty"`
	// DecisionModels picks per task the decision engine that settles easy cases before the server
	// model is asked: off (the default) or a server engine (decision_models.go).
	DecisionModels map[string]string `json:"decision_models,omitempty" bson:"decision_models,omitempty"`
	// Reflect, mental-model and consolidation blocks (reflect_models_settings.go).
	ReflectDefaults     *SpaceReflectDefaults     `json:"reflect_default_options,omitempty" bson:"reflect_default_options,omitempty"`
	MentalModelDefaults *SpaceMentalModelDefaults `json:"mental_model_defaults,omitempty" bson:"mental_model_defaults,omitempty"`
	Consolidation       *SpaceConsolidation       `json:"consolidation,omitempty" bson:"consolidation,omitempty"`
	// RecallQuality: relevance gate, pending corrections, provisional candidates, closing corrected facts (recall_quality.go).
	RecallQuality *SpaceRecallQuality `json:"recall_quality,omitempty" bson:"recall_quality,omitempty"`
	// RecallBudget maps reflect and mental-model budget levels to recall sizes (reflect_models_settings.go).
	RecallBudget *SpaceRecallBudget `json:"recall_budget,omitempty" bson:"recall_budget,omitempty"`
	// StoreDocumentText (default on, null inherits): off keeps no extracted document text as
	// evidence once its chunks are extracted, so a document cannot be reprocessed, edited or exported.
	StoreDocumentText *bool `json:"store_document_text,omitempty" bson:"store_document_text,omitempty"`
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
	ExtractionBatchSize       *int                        `json:"extraction_batch_size,omitempty"`
	ExtractionImagesPerChunk  *int                        `json:"extraction_images_per_chunk,omitempty"`
	ExtractCausal             *bool                       `json:"extract_causal,omitempty"`
	ExtractionStrategies      *map[string]ExtractStrategy `json:"extraction_strategies,omitempty"`
	ExtractionDefaultStrategy *string                     `json:"extraction_default_strategy,omitempty"`
	DefaultTimezone           *string                     `json:"default_timezone,omitempty"`
	MemoryLanguage            *string                     `json:"memory_language,omitempty"`
	WorkStateCandidates       *bool                       `json:"work_state_candidates,omitempty"`
	RecallSessionArchives     *bool                       `json:"recall_session_archives,omitempty"`
	CaptureMode               *string                     `json:"capture_mode,omitempty"`
	CaptureDensity            *string                     `json:"capture_density,omitempty"`
	// DecisionModels replaces the stored map ({} hands every task back).
	DecisionModels      *map[string]string        `json:"decision_models,omitempty"`
	ReflectDefaults     *SpaceReflectDefaults     `json:"reflect_default_options,omitempty"`
	MentalModelDefaults *SpaceMentalModelDefaults `json:"mental_model_defaults,omitempty"`
	Consolidation       *SpaceConsolidation       `json:"consolidation,omitempty"`
	RecallQuality       *SpaceRecallQuality       `json:"recall_quality,omitempty"`
	RecallBudget        *SpaceRecallBudget        `json:"recall_budget,omitempty"`
	StoreDocumentText   *bool                     `json:"store_document_text,omitempty"`

	resets spaceResets // keys a PATCH sent as null (UnmarshalJSON)
}

type SpaceView struct {
	WorkspaceID string           `json:"workspace_id"`
	ProjectID   string           `json:"project_id"`
	Revision    int64            `json:"revision"`
	Config      SpaceConfig      `json:"config"`
	Counts      map[string]int64 `json:"counts,omitempty"`
	// Created is the oldest record's creation and LastActivity the newest change, over the counted collections.
	Created      *time.Time `json:"created,omitempty"`
	LastActivity *time.Time `json:"last_activity,omitempty"`
}

type SpacePage struct {
	Items []SpaceView `json:"items"`
	Next  string      `json:"next,omitempty"`
}

type SpaceCount struct {
	ProjectID  string
	Collection string
	Count      int64
	First      time.Time // oldest created_at of the rows
	Last       time.Time // newest updated_at of the rows
}

// spaceStore is the cross-project read a space list and alias lookup need.
// Row-level security still limits both to the caller's permitted projects.
type spaceStore interface {
	SpaceCounts(ctx context.Context, workspace, after string, collections []string, limit int) ([]SpaceCount, error)
	SpaceAliasOwners(ctx context.Context, workspace, alias string) ([]Record, error)
	// SpaceSubScopes lists the scopes (project level and user or agent sub-scopes)
	// of one space that hold rows of the collections, at most limit.
	SpaceSubScopes(ctx context.Context, space Scope, collections []string, limit int) ([]Scope, error)
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
	Manifest   *SpaceManifest       `json:"manifest,omitempty"`
	Section    string               `json:"section,omitempty"`
	Config     *SpaceConfig         `json:"config,omitempty"`
	Record     *SpaceBundleRecord   `json:"record,omitempty"`
	Audit      *AuditEventView      `json:"audit,omitempty"`
	LLMRequest *LLMRequestView      `json:"llm_request,omitempty"`
	File       *SpaceBundleFile     `json:"file,omitempty"`
	Document   *SpaceBundleDocument `json:"document,omitempty"`
	End        *SpaceBundleEnd      `json:"end,omitempty"`
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
	Kind     string   `json:"kind" bson:"kind"`
	State    string   `json:"state" bson:"state"` // queued | done | failed
	Include  []string `json:"include,omitempty" bson:"include,omitempty"`
	Parts    []string `json:"parts,omitempty" bson:"parts,omitempty"`
	Template bool     `json:"template" bson:"template"`
	// DocumentIDs narrows the documents part to these documents.
	DocumentIDs []string       `json:"document_ids,omitempty" bson:"document_ids,omitempty"`
	ImportAs    string         `json:"import_as,omitempty" bson:"import_as,omitempty"`
	Target      *Scope         `json:"target,omitempty" bson:"target,omitempty"`
	BundleID    string         `json:"bundle_id,omitempty" bson:"bundle_id,omitempty"`
	Manifest    *SpaceManifest `json:"manifest,omitempty" bson:"manifest,omitempty"`
	Report      *SpaceReport   `json:"report,omitempty" bson:"report,omitempty"`
	Error       string         `json:"error,omitempty" bson:"error,omitempty"`
	FinishedAt  *time.Time     `json:"finished_at,omitempty" bson:"finished_at,omitempty"`
	// Bundle is the text older exports kept on the row; a download clears it.
	Bundle string `json:"-" bson:"bundle,omitempty"`
}

type SpaceJobView struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	SpaceJob
}

// SpaceExportInput picks sections by Include (collection names) or by Parts
// (config, data, observations, history, knowledge, attachments; default config
// and data), not both. Knowledge and attachments read the space's sub-scopes too.
type SpaceExportInput struct {
	Include  []string `json:"include,omitempty"`
	Parts    []string `json:"parts,omitempty"`
	Template bool     `json:"template,omitempty"`
	// DocumentIDs exports only these documents in the documents part (at most 200).
	DocumentIDs []string `json:"document_ids,omitempty"`
}

type SpaceImportInput struct {
	Bundle   string `json:"bundle"`
	ImportAs string `json:"import_as,omitempty"`
	// DefaultTemplate imports the server's default template (ARKISM_SPACE_DEFAULT_TEMPLATE) instead of Bundle.
	DefaultTemplate bool `json:"default_template,omitempty"`
	// AsTemplate takes only the settings and definitions of any bundle, under the template rule (a space without settings).
	AsTemplate bool `json:"as_template,omitempty"`
	// DocumentConflict says what a bundle document does when one of the same name exists:
	// replace (default), skip or new_id (keep both).
	DocumentConflict string `json:"document_conflict,omitempty"`
}

type SpaceCloneInput struct {
	Target   Scope    `json:"target"`
	Include  []string `json:"include,omitempty"`
	Parts    []string `json:"parts,omitempty"`
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
	counts, order, spans := map[string]map[string]int64{}, []string{}, map[string][2]time.Time{}
	for _, row := range rows {
		if counts[row.ProjectID] == nil {
			counts[row.ProjectID] = map[string]int64{}
			order = append(order, row.ProjectID)
		}
		spans[row.ProjectID] = widenSpan(spans[row.ProjectID], row.First, row.Last)
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
		if span := spans[project]; !span[0].IsZero() {
			view.Created, view.LastActivity = &span[0], &span[1]
		}
		page.Items = append(page.Items, view)
	}
	return page, nil
}

// widenSpan grows [first, last] to take in another row's times; zero times are left out.
func widenSpan(span [2]time.Time, first, last time.Time) [2]time.Time {
	if !first.IsZero() && (span[0].IsZero() || first.Before(span[0])) {
		span[0] = first
	}
	if last.After(span[1]) {
		span[1] = last
	}
	return span
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
	switch {
	case in.Template:
		return append([]string{spaceSectionConfig}, spaceDefinitions...)
	case len(in.Include) > 0:
		return in.Include
	}
	parts := in.Parts
	if len(parts) == 0 {
		parts = []string{spaceSectionConfig, "data"}
	}
	out := []string{}
	for _, p := range spaceParts { // fixed order whatever the request's order
		if !slices.Contains(parts, p) {
			continue
		}
		switch p {
		case "data":
			out = append(out, spaceData...)
		case "history":
			out = append(out, spaceHistory...)
		default:
			out = append(out, p)
		}
	}
	return out
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

func validSpaceExport(in SpaceExportInput) bool {
	if !validSpaceInclude(in.Include) || len(in.Parts) > 0 && (len(in.Include) > 0 || in.Template) || !validDocumentPick(in) {
		return false
	}
	for i, p := range in.Parts {
		if !slices.Contains(spaceParts, p) || slices.Contains(in.Parts[:i], p) {
			return false
		}
	}
	return true
}

// ExportSpace starts an export job of one space.
func (s *Service) ExportSpace(ctx context.Context, token string, scope Scope, in SpaceExportInput) (SpaceJobView, error) {
	scope, err := s.resolveSpace(ctx, scope)
	if err != nil {
		return SpaceJobView{}, err
	}
	if !validSpaceExport(in) {
		return SpaceJobView{}, ErrInvalid
	}
	d, err := s.authorizeSpaceExport(ctx, token, scope, "")
	if err != nil {
		return SpaceJobView{}, err
	}
	job := SpaceJob{Kind: spaceJobExport, Include: in.Include, Parts: in.Parts, Template: in.Template, DocumentIDs: in.DocumentIDs, BundleID: "sb_" + ID()}
	// The job counts what the bundle holds now; the download streams it fresh.
	return s.startSpaceJob(ctx, scope, d, job, func(ctx context.Context, job *SpaceJob) error {
		manifest, err := s.writeSpaceBundle(ctx, token, d, scope, in, job.BundleID, time.Now().UTC().Truncate(time.Millisecond), io.Discard)
		job.Manifest = &manifest
		return err
	})
}

// ImportSpace verifies a bundle sent as a string and starts its import into a space; human principals only.
func (s *Service) ImportSpace(ctx context.Context, token string, scope Scope, in SpaceImportInput) (SpaceJobView, error) {
	return s.importSpace(ctx, token, scope, in, nil)
}

// ImportSpaceStream is ImportSpace for a bundle of any size sent as the raw body:
// it is spooled to a temp file and verified before any line is applied.
func (s *Service) ImportSpaceStream(ctx context.Context, token string, scope Scope, in SpaceImportInput, body io.Reader) (SpaceJobView, error) {
	if in.Bundle != "" || in.DefaultTemplate || body == nil {
		return SpaceJobView{}, ErrInvalid
	}
	return s.importSpace(ctx, token, scope, in, body)
}

func (s *Service) importSpace(ctx context.Context, token string, scope Scope, in SpaceImportInput, body io.Reader) (SpaceJobView, error) {
	scope, err := s.resolveSpace(ctx, scope)
	if err != nil {
		return SpaceJobView{}, err
	}
	if !validImportAs(in.ImportAs) || !validDocConflict(in.DocumentConflict) {
		return SpaceJobView{}, ErrInvalid
	}
	d, err := s.mergeHuman(ctx, token, "write:memory_candidates", scope, "")
	if err != nil {
		return SpaceJobView{}, err
	}
	src, err := s.importSource(in, body)
	if err != nil {
		return SpaceJobView{}, err
	}
	manifest, err := s.checkSpaceImport(ctx, scope, in, src)
	if err != nil {
		src.done()
		return SpaceJobView{}, err
	}
	template := manifest.Template || in.AsTemplate
	job := SpaceJob{Kind: spaceJobImport, ImportAs: in.ImportAs, Template: template, BundleID: manifest.BundleID, Manifest: &manifest}
	view, err := s.startSpaceJob(ctx, scope, d, job, func(ctx context.Context, job *SpaceJob) error {
		defer src.done()
		imp := &spaceImporter{s: s, token: token, d: d, scope: scope, m: manifest, approved: in.ImportAs == "approved", template: in.AsTemplate, conflict: in.DocumentConflict}
		report, err := s.runSpaceImport(ctx, imp, src.lines())
		job.Report = &report
		return err
	})
	if err != nil {
		src.done()
	}
	return view, err
}

func (s *Service) importSource(in SpaceImportInput, body io.Reader) (spaceSource, error) {
	switch {
	case body != nil:
		return spoolSpaceBundle(body)
	case !in.DefaultTemplate:
		return stringSpaceSource(in.Bundle), nil
	case s.SpaceTemplate == nil:
		return spaceSource{}, ErrNotFound
	case in.Bundle != "":
		return spaceSource{}, ErrInvalid
	}
	return stringSpaceSource(s.SpaceTemplate.Bundle), nil
}

// checkSpaceImport verifies the whole bundle before the job starts. A template
// (or a bundle taken as one) sets up a new space: it goes only into a space
// without stored settings, so applying it twice cannot duplicate its definitions.
func (s *Service) checkSpaceImport(ctx context.Context, scope Scope, in SpaceImportInput, src spaceSource) (SpaceManifest, error) {
	manifest, err := src.verify()
	if err != nil || !manifest.Template && !in.AsTemplate {
		return manifest, err
	}
	_, err = s.Repo.Get(ctx, colSpaceConfigs, scope, spaceConfigID)
	if err == nil {
		return SpaceManifest{}, ErrConflict
	}
	if !errors.Is(err, ErrNotFound) {
		return SpaceManifest{}, err
	}
	return manifest, nil
}

// CloneSpace copies one space into another (export then import, inside the server).
// Copies arrive as candidates; approved stays approved only for a human caller who asks.
func (s *Service) CloneSpace(ctx context.Context, token string, scope Scope, in SpaceCloneInput) (SpaceJobView, error) {
	scope, err := s.resolveSpace(ctx, scope)
	if err != nil {
		return SpaceJobView{}, err
	}
	export := SpaceExportInput{Include: in.Include, Parts: in.Parts}
	if !validScope(in.Target) || !validSpaceExport(export) || !validImportAs(in.ImportAs) {
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
	history := slices.Contains(spaceSections(export), colLLMRequests) || slices.Contains(spaceSections(export), "audit_events")
	if (approved || history) && (d.Role != roleHuman || td.Role != roleHuman) {
		return SpaceJobView{}, ErrDenied
	}
	job := SpaceJob{Kind: spaceJobClone, Include: in.Include, Parts: in.Parts, ImportAs: in.ImportAs, Target: &target, BundleID: "sb_" + ID()}
	// Lines go straight from the source pages into the target: no bundle is built or held.
	return s.startSpaceJob(ctx, scope, d, job, func(ctx context.Context, job *SpaceJob) error {
		now := time.Now().UTC().Truncate(time.Millisecond)
		manifest := SpaceManifest{Format: spaceBundleFormat, FormatVersion: spaceBundleVersion, BundleID: job.BundleID, Source: scope, CreatedAt: now}
		imp := &spaceImporter{s: s, token: token, d: td, scope: target, m: manifest, approved: approved, history: true}
		report, err := s.runSpaceImport(ctx, imp, func(yield func(spaceBundleLine) error) error {
			var err error
			manifest.Sections, manifest.Skipped, err = s.eachSpaceLine(ctx, token, d, scope, spaceSections(export), nil, now, yield)
			return err
		})
		job.Manifest, job.Report = &manifest, &report
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

// DownloadSpaceExport checks a finished export and returns the writer that
// streams its bundle, read fresh from the store with the caller's role: a record
// erased, made private or retired since the export is simply not in it.
func (s *Service) DownloadSpaceExport(ctx context.Context, token string, scope Scope, id string) (func(io.Writer) error, error) {
	rec, err := s.spaceJob(ctx, token, scope, id)
	if err != nil {
		return nil, err
	}
	job := rec.SpaceJob
	if rec.Kind != spaceJobExport || job.State != "done" || job.Manifest == nil {
		return nil, ErrNotFound
	}
	if job.Bundle != "" {
		s.dropSpaceBundle(ctx, rec)
	}
	if time.Since(rec.CreatedAt) > spaceExportTTL {
		return nil, ErrNotFound
	}
	d, err := s.authorizeSpaceExport(ctx, token, rec.Scope, id)
	if err != nil {
		return nil, err
	}
	in := SpaceExportInput{Include: job.Include, Parts: job.Parts, Template: job.Template, DocumentIDs: job.DocumentIDs}
	return func(w io.Writer) error {
		_, err := s.writeSpaceBundle(ctx, token, d, rec.Scope, in, job.BundleID, job.Manifest.CreatedAt, w)
		return err
	}, nil
}

// dropSpaceBundle clears the bundle an older export kept on its job row, so
// erased or retired text does not stay at rest there. Best effort: a caller who
// may only read cannot write.
// Bundles nobody downloads again are cleared by RepairSpace (memoryd --space-repair).
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

func (s *Service) importSpaceChunk(ctx context.Context, token string, scope Scope, m SpaceManifest, n int, claims []CaptureClaim, origin []bool, approved bool, report *SpaceReport) error {
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
			if approved && origin[i] && item.Reason == "review_required" && s.approveImported(ctx, token, scope, item.CandidateID) == nil {
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
		report.skip(line.Section, captureErrorCode(err), 1)
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
	return &MentalModelInput{Title: r.MentalModel.Title, Query: r.MentalModel.Query, Refresh: r.MentalModel.Refresh, Folder: r.MentalModel.Folder}
}

// SpaceRepairReport is what RepairSpace found (preview) or fixed (execute) in one space.
type SpaceRepairReport struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	Execute     bool   `json:"execute"`
	// StaleObservations: active or proposed observations whose cited memories are gone or retired.
	StaleObservations int `json:"stale_observations"`
	// LegacyBundles: export job rows still holding bundle text at rest from before streamed bundles.
	LegacyBundles int `json:"legacy_bundles"`
}

// RepairSpace is the offline administrator repair of one space's derived state
// (memoryd --space-repair). Preview counts; execute marks orphan observations
// stale through the ordinary sweep and clears bundle text left on job rows.
// It runs with the server's database credentials, not a principal's token.
func (s *Service) RepairSpace(ctx context.Context, scope Scope, execute bool) (SpaceRepairReport, error) {
	scope = spaceScope(scope)
	out := SpaceRepairReport{WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, Execute: execute}
	if !validScope(scope) {
		return out, ErrInvalid
	}
	var err error
	if out.StaleObservations, err = s.sweepObservationsAt(ctx, scope, "space-repair", execute); err != nil {
		return out, err
	}
	err = eachCatalogRecord(ctx, s.Repo, scope, colJobs, func(r Record) error {
		if r.SpaceJob == nil || r.SpaceJob.Bundle == "" {
			return nil
		}
		out.LegacyBundles++
		if execute {
			s.dropSpaceBundle(ctx, r)
		}
		return nil
	})
	return out, err
}
