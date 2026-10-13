package memory

// Skill hub: runtime-neutral resolution of skill_refs and validation of skill
// usage evidence. Arkism owns identity, immutable versions, provenance and
// metadata; the runtime owns execution and permission. Requirements returned
// here describe what a skill needs, never what a run may do.

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
)

const (
	skillUsageEventKind   = "skill.usage"
	skillRefLatest        = "latest-approved"
	skillResolveRefMax    = 32
	skillRequirementsNote = "requirements_only"
)

// SkillOrigin is the import provenance of one version. Vendor formats are
// labels here; the core never parses them.
type SkillOrigin struct {
	SourceType      string `json:"source_type" bson:"source_type"`
	Repository      string `json:"repository,omitempty" bson:"repository,omitempty"`
	Commit          string `json:"commit,omitempty" bson:"commit,omitempty"`
	OriginalFormat  string `json:"original_format,omitempty" bson:"original_format,omitempty"`
	OriginalRuntime string `json:"original_runtime,omitempty" bson:"original_runtime,omitempty"`
	DerivedFrom     string `json:"derived_from,omitempty" bson:"derived_from,omitempty"`
}

var skillSourceTypes = []string{"import", "manual", "derived"}

const skillStatusDeprecated = "deprecated"

var skillCompatibilityStates = []string{"supported", "unsupported", "untested", "requires_adapter"}

func validSkillOrigin(o *SkillOrigin) bool {
	if o == nil {
		return true
	}
	if !slices.Contains(skillSourceTypes, o.SourceType) || (o.SourceType == "derived") != (o.DerivedFrom != "") {
		return false
	}
	for _, v := range []string{o.Repository, o.Commit, o.OriginalFormat, o.OriginalRuntime, o.DerivedFrom} {
		if v != "" && (!validRunLabel(v, 512) || containsSecret(v)) {
			return false
		}
	}
	return o.DerivedFrom == "" || parseSkillRefOK(o.DerivedFrom)
}

func validSkillCompatibility(c map[string]string) bool {
	if len(c) > 16 {
		return false
	}
	for runtime, state := range c {
		if !validRunLabel(runtime, 64) || !slices.Contains(skillCompatibilityStates, state) {
			return false
		}
	}
	return true
}

// effectiveCompatibility overlays the owner's skill-level labels (set with
// SetSkillAvailability) on the importer's labels of one version.
func effectiveCompatibility(version, parent Record) map[string]string {
	out := map[string]string{}
	for k, v := range version.SkillCompatibility {
		out[k] = v
	}
	for k, v := range parent.SkillCompatibility {
		out[k] = v
	}
	return out
}

// SkillAvailabilityInput is the owner's per-skill switch: status deprecated
// retires the skill (resolve says skill_deprecated, routing and listing skip
// it, pinned versions still replay) and compatibility marks runtimes it must
// not be routed to. Both are replaced as a whole on every call.
type SkillAvailabilityInput struct {
	Scope            Scope             `json:"scope"`
	ExpectedRevision int64             `json:"expected_revision"`
	Status           string            `json:"status"`
	Compatibility    map[string]string `json:"compatibility"`
}

func (s *Service) SetSkillAvailability(ctx context.Context, token, id string, in SkillAvailabilityInput) (Record, error) {
	if !validScope(in.Scope) || (in.Status != statusActive && in.Status != skillStatusDeprecated) || !validSkillCompatibility(in.Compatibility) {
		return Record{}, ErrInvalid
	}
	// Same authority as apply: both change what a skill ref serves.
	d, e := s.authorize(ctx, token, "apply:skills", in.Scope, id, "")
	if e != nil {
		return Record{}, e
	}
	if d.Role != roleHuman {
		return Record{}, ErrDenied
	}
	var result Record
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		skill, e := tx.Get(ctx, colSkills, in.Scope, id)
		if e != nil {
			return e
		}
		if skill.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		// Only toggles active<->deprecated; an erased or quarantined skill stays so.
		if skill.PrivacyQuarantined || skill.Status != statusActive && skill.Status != skillStatusDeprecated {
			return ErrConflict
		}
		prev := skill.Revision
		skill.Status, skill.SkillCompatibility = in.Status, in.Compatibility
		skill.Revision++
		skill.UpdatedAt = time.Now().UTC()
		if e = tx.Replace(ctx, colSkills, skill, prev); e != nil {
			return e
		}
		result = skill
		return audit(ctx, tx, in.Scope, d.PrincipalID, "availability:skills", id)
	})
	return result, e
}

// parseSkillRef splits "<skill_id>@<n>" or "<skill_id>@latest-approved".
// Version 0 means latest-approved.
func parseSkillRef(ref string) (string, int, bool) {
	at := strings.LastIndex(ref, "@")
	if at <= 0 || !validRunLabel(ref[:at], 256) {
		return "", 0, false
	}
	if ref[at+1:] == skillRefLatest {
		return ref[:at], 0, true
	}
	n, err := strconv.Atoi(ref[at+1:])
	if err != nil || n < 1 || strconv.Itoa(n) != ref[at+1:] {
		return "", 0, false
	}
	return ref[:at], n, true
}

func parseSkillRefOK(ref string) bool { _, _, ok := parseSkillRef(ref); return ok }

// skillHistory keeps legacy parents (imported before version numbers) usable:
// their only version is the active one.
func skillHistory(parent Record) []string {
	if len(parent.VersionHistory) == 0 && parent.ActiveVersion != "" {
		return []string{parent.ActiveVersion}
	}
	return parent.VersionHistory
}

type SkillResolveInput struct {
	TrialID string   `json:"trial_id,omitempty"`
	Runtime string   `json:"runtime,omitempty"`
	Scope   Scope    `json:"scope"`
	Refs    []string `json:"refs"`
}

type SkillRequirements struct {
	Authority           string   `json:"authority"`
	Tools               []string `json:"tools"`
	APIs                []string `json:"apis"`
	Permissions         []string `json:"permissions"`
	SecretNames         []string `json:"secret_names"`
	IrreversibleActions []string `json:"irreversible_actions"`
	Environments        []string `json:"environments"`
	Dependencies        []string `json:"dependencies"`
	Reads               bool     `json:"reads"`
	Writes              bool     `json:"writes"`
	ExternalTransfer    bool     `json:"external_transfer"`
}

type SkillProvenanceSummary struct {
	SourceID    string       `json:"source_id"`
	SourcePath  string       `json:"source_path"`
	ContentHash string       `json:"content_hash"`
	ImportedBy  string       `json:"imported_by"`
	ImportedAt  time.Time    `json:"imported_at"`
	Origin      *SkillOrigin `json:"origin,omitempty"`
}

type ResolvedSkill struct {
	Bundle           *SkillBundle            `json:"bundle,omitempty"`
	TrialID          string                  `json:"trial_id,omitempty"`
	TrialStatus      string                  `json:"trial_status,omitempty"`
	Ref              string                  `json:"ref"`
	Error            string                  `json:"error,omitempty"`
	SkillID          string                  `json:"skill_id,omitempty"`
	Version          int                     `json:"resolved_version,omitempty"`
	VersionID        string                  `json:"version_id,omitempty"`
	ContentHash      string                  `json:"content_hash,omitempty"`
	Name             string                  `json:"name,omitempty"`
	Description      string                  `json:"description,omitempty"`
	Instructions     string                  `json:"instructions,omitempty"`
	Requirements     *SkillRequirements      `json:"requirements,omitempty"`
	Compatibility    map[string]string       `json:"compatibility,omitempty"`
	ValidationStatus string                  `json:"validation_status,omitempty"`
	LatestApproved   bool                    `json:"latest_approved,omitempty"`
	Deprecated       bool                    `json:"deprecated,omitempty"`
	Provenance       *SkillProvenanceSummary `json:"provenance_summary,omitempty"`
}

type SkillResolveResult struct {
	Authority string          `json:"authority"`
	Items     []ResolvedSkill `json:"items"`
}

// ResolveSkills answers each ref independently so one missing skill never
// hides the others. Failures are fixed codes: invalid_ref, skill_not_found,
// version_not_found, no_approved_version, skill_deprecated, skill_unavailable.
func (s *Service) ResolveSkills(ctx context.Context, token string, in SkillResolveInput) (SkillResolveResult, error) {
	out := SkillResolveResult{Authority: "data_only_untrusted", Items: []ResolvedSkill{}}
	if (in.TrialID != "" && (!validRunLabel(in.TrialID, 128) || !trialRuntime(in.Runtime) || len(in.Refs) != 1)) || (in.TrialID == "" && in.Runtime != "") || !validScope(in.Scope) || len(in.Refs) == 0 || len(in.Refs) > skillResolveRefMax {
		return out, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:skill_versions", in.Scope, "", ""); err != nil {
		return out, err
	}
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		for _, ref := range in.Refs {
			var item ResolvedSkill
			var err error
			if in.TrialID != "" {
				item, err = resolveSkillTrial(ctx, tx, in, ref)
			} else {
				item, err = resolveSkillRef(ctx, tx, in.Scope, ref)
			}
			if err != nil {
				return err
			}
			out.Items = append(out.Items, item)
		}
		return nil
	})
	return out, err
}

func resolveSkillRef(ctx context.Context, tx Tx, scope Scope, ref string) (ResolvedSkill, error) {
	item := ResolvedSkill{Ref: ref}
	id, number, ok := parseSkillRef(ref)
	if !ok {
		item.Error = "invalid_ref"
		return item, nil
	}
	parent, err := tx.Get(ctx, colSkills, scope, id)
	if errors.Is(err, ErrNotFound) {
		item.Error = "skill_not_found"
		return item, nil
	} else if err != nil {
		return item, err
	}
	history := skillHistory(parent)
	deprecated := !live(parent)
	versionID := ""
	latest := number == 0
	if latest {
		if deprecated {
			item.Error = "skill_deprecated"
			return item, nil
		}
		if parent.ActiveVersion == "" {
			item.Error = "no_approved_version"
			return item, nil
		}
		versionID = parent.ActiveVersion
		number = slices.Index(history, versionID) + 1
	} else if number > len(history) {
		item.Error = "version_not_found"
		return item, nil
	} else {
		versionID = history[number-1]
	}
	version, err := tx.Get(ctx, colSkillVersions, scope, versionID)
	if errors.Is(err, ErrNotFound) {
		item.Error = "version_not_found"
		return item, nil
	} else if err != nil {
		return item, err
	}
	// Exact pins resolve superseded and candidate versions for replay; the
	// runtime decides from validation_status whether to run them.
	if version.PrivacyQuarantined || version.ContentErased || version.Analysis == nil || version.SkillID != id || !slices.Contains([]string{"candidate", "approved", "active", "superseded"}, version.Status) {
		item.Error = "skill_unavailable"
		return item, nil
	}
	if len(version.Sources) == 0 || version.Kind != proposedSkillKind && len(version.Sources) != 1 {
		item.Error = "skill_unavailable"
		return item, nil
	}
	source, err := tx.Get(ctx, "sources", scope, version.Sources[0].SourceID)
	if err != nil || !live(source) || source.ContentErased || source.ContentHash != version.Analysis.ContentHash {
		if err != nil && !errors.Is(err, ErrNotFound) {
			return item, err
		}
		item.Error = "skill_unavailable"
		return item, nil
	}
	if !validSkillBundle(source.SkillBundle) || (source.SkillBundle == nil && version.SkillBundleSHA256 != "") || (source.SkillBundle != nil && source.SkillBundle.SHA256 != version.SkillBundleSHA256) {
		item.Error = "skill_unavailable"
		return item, nil
	}
	if version.Kind == proposedSkillKind {
		if err = checkRelations(ctx, tx, version); err != nil {
			if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrDenied) && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrInvalid) {
				return item, err
			}
			item.Error = "skill_unavailable"
			return item, nil
		}
	}
	if latest {
		if err = checkActiveSkill(ctx, tx, version); err != nil {
			// A failed statement aborts the transaction; only domain refusals
			// become a per-ref answer, store errors fail (and retry) the batch.
			if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrDenied) && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrInvalid) {
				return item, err
			}
			item.Error = "skill_unavailable"
			return item, nil
		}
	}
	a := version.Analysis
	item.SkillID, item.Version, item.VersionID, item.ContentHash = id, number, version.ID, a.ContentHash
	item.Name, item.Description, item.Instructions = a.Name, a.Description, source.Text
	item.Bundle = source.SkillBundle
	item.Requirements = &SkillRequirements{Authority: skillRequirementsNote, Tools: nonNil(a.Tools), APIs: nonNil(a.APIs), Permissions: nonNil(a.Permissions), SecretNames: nonNil(a.SecretNames),
		IrreversibleActions: nonNil(a.IrreversibleActions), Environments: nonNil(a.Environments), Dependencies: nonNil(a.Dependencies), Reads: a.Reads, Writes: a.Writes, ExternalTransfer: a.ExternalTransfer}
	item.Compatibility = effectiveCompatibility(version, parent)
	item.ValidationStatus, item.LatestApproved, item.Deprecated = version.Status, version.ID == parent.ActiveVersion, deprecated
	item.Provenance = &SkillProvenanceSummary{SourceID: source.ID, SourcePath: source.SourceRef, ContentHash: source.ContentHash, ImportedBy: source.CreatedBy, ImportedAt: source.CreatedAt, Origin: version.SkillOrigin}
	return item, nil
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// SkillUsage is the payload of a "skill.usage" trace event: IDs, hashes,
// codes and counts only, never user content.
type SkillUsage struct {
	TrialID       string    `json:"trial_id,omitempty"`
	SkillID       string    `json:"skill_id"`
	Version       int       `json:"version"`
	VersionID     string    `json:"version_id"`
	ContentHash   string    `json:"content_hash"`
	Runtime       string    `json:"runtime"`
	ProfileID     string    `json:"profile_id,omitempty"`
	Status        string    `json:"status"`
	StartedAt     time.Time `json:"started_at"`
	CompletedAt   time.Time `json:"completed_at"`
	RetryCount    int       `json:"retry_count"`
	ToolFailures  int       `json:"tool_failures"`
	RuntimeErrors int       `json:"runtime_errors"`
	OfflineCache  bool      `json:"offline_cache,omitempty"`
	// ErrorCode is a fixed snake_case code for a non-success run, never message text.
	ErrorCode string `json:"error_code,omitempty"`
}

var skillUsageErrorCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

var skillUsageStatuses = []string{"success", "failure", "cancelled", "validation_failed"}

// checkSkillUsageEvents binds every skill.usage event to an existing version
// with the same number and content hash, so usage evidence cannot name a
// skill the hub never served.
func checkSkillUsageEvents(ctx context.Context, tx Tx, scope Scope, events []TraceEventInput, canonical Scope) error {
	for _, event := range events {
		if event.Kind != skillUsageEventKind {
			continue
		}
		var u SkillUsage
		decoder := json.NewDecoder(bytes.NewReader(event.Payload))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&u) != nil || !validRunLabel(u.SkillID, 256) || u.Version < 1 || !validRunLabel(u.VersionID, 256) || len(u.ContentHash) != 64 ||
			!validRunLabel(u.Runtime, 64) || u.ProfileID != "" && !validRunLabel(u.ProfileID, 256) || !slices.Contains(skillUsageStatuses, u.Status) ||
			u.StartedAt.IsZero() || u.CompletedAt.Before(u.StartedAt) || u.RetryCount < 0 || u.ToolFailures < 0 || u.RuntimeErrors < 0 ||
			u.ErrorCode != "" && (u.Status == "success" || !skillUsageErrorCode.MatchString(u.ErrorCode)) {
			return ErrInvalid
		}
		// Runs are recorded under an agent; skills usually live in the same
		// user's agent-less scope. Only that one parent scope is consulted.
		skillScope := scope
		parent, err := tx.Get(ctx, colSkills, skillScope, u.SkillID)
		if errors.Is(err, ErrNotFound) && scope.AgentID != "" {
			skillScope.AgentID = ""
			parent, err = tx.Get(ctx, colSkills, skillScope, u.SkillID)
		}
		// A runtime that consumes the operator's canonical skill scope records
		// usage in its own scope; the skill lives there.
		if errors.Is(err, ErrNotFound) && canonical.WorkspaceID != "" && canonical != skillScope {
			skillScope = canonical
			parent, err = tx.Get(ctx, colSkills, skillScope, u.SkillID)
		}
		if err != nil {
			return usageLookupError(err)
		}
		history := skillHistory(parent)
		if u.Version > len(history) || history[u.Version-1] != u.VersionID {
			return ErrConflict
		}
		version, err := tx.Get(ctx, colSkillVersions, skillScope, u.VersionID)
		if err != nil {
			return usageLookupError(err)
		}
		if version.Analysis == nil || version.SkillID != u.SkillID || version.Analysis.ContentHash != u.ContentHash {
			return ErrConflict
		}
		if u.TrialID != "" {
			trial, e := tx.Get(ctx, colSkillTrials, skillScope, u.TrialID)
			if e != nil {
				return usageLookupError(e)
			}
			bound := trial.SkillTrial
			if bound == nil || trial.ContentErased || trial.PrivacyQuarantined || trial.SkillID != u.SkillID || bound.Candidate.ID != u.VersionID || bound.Runtime != u.Runtime || u.OfflineCache || u.StartedAt.Before(trial.CreatedAt) || !u.StartedAt.Before(bound.EndsAt) || bound.StoppedAt != nil && !u.StartedAt.Before(*bound.StoppedAt) {
				return ErrConflict
			}
		}
	}
	return nil
}

func usageLookupError(err error) error {
	if errors.Is(err, ErrNotFound) {
		return ErrConflict
	}
	return err
}
