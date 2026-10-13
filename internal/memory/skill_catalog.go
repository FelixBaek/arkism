package memory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const importedSkillBaselineKind = "imported_skill_baseline"

type ImportSkillInput struct {
	Bundle         *SkillBundle         `json:"bundle,omitempty"`
	ComparisonPlan *SkillComparisonPlan `json:"comparison_plan,omitempty"`
	Scope          Scope                `json:"scope"`
	SkillID        string               `json:"skill_id"`
	Path           string               `json:"path"`
	Content        string               `json:"content"`
	Analysis       *SkillAnalysis       `json:"analysis"`
	RedactSecrets  bool                 `json:"redact_secrets,omitempty"`
	// Origin and Compatibility are importer-supplied labels; the core only
	// validates their shape and fixed state codes.
	Origin        *SkillOrigin      `json:"origin,omitempty"`
	Compatibility map[string]string `json:"compatibility,omitempty"`
}

type ImportSkillResult struct {
	ComparisonPlanSHA256 string `json:"comparison_plan_sha256,omitempty"`
	Skill                Record `json:"skill"`
	Record               Record `json:"record"`
	Reused               bool   `json:"reused"`
	// Version is the public number (skill_id@version).
	Version int `json:"version"`
}

type SkillListItem struct {
	ID          string    `json:"id"`
	VersionID   string    `json:"version_id"`
	Version     string    `json:"version"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	SourceID    string    `json:"source_id"`
	SourcePath  string    `json:"source_path"`
	UpdatedAt   time.Time `json:"updated_at"`
	// Which agent runtimes can use this version (supported | requires_adapter | ...); absent until analysed.
	SkillCompatibility map[string]string `json:"skill_compatibility,omitempty"`
}

type SkillPage struct {
	Items      []SkillListItem `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
	Authority  string          `json:"authority"`
	// Withheld lists skills on this page whose active version cannot be served
	// (resolve answers skill_unavailable); one bad record must not hide the rest.
	Withheld []string `json:"withheld,omitempty"`
}

type skillRecordLister interface {
	ListSkillRecords(context.Context, Scope, int, string) ([]Record, error)
}

type trashedSkillLister interface {
	ListDeprecatedSkillRecords(context.Context, Scope, int, string) ([]Record, error)
}

const actionReadSkills = "read:skills"

// trashedVersion is the skill's last version when nothing about it is erased or held for erasure.
func (s *Service) trashedVersion(ctx context.Context, scope Scope, skill Record) (Record, bool) {
	if skill.PrivacyQuarantined || skill.ContentErased {
		return Record{}, false
	}
	version, err := s.Repo.Get(ctx, colSkillVersions, scope, skill.ActiveVersion)
	if err != nil || version.PrivacyQuarantined || version.ContentErased || version.Analysis == nil {
		return Record{}, false
	}
	for _, ref := range version.Sources {
		source, err := s.Repo.Get(ctx, "sources", scope, ref.SourceID)
		if err != nil || source.PrivacyQuarantined || source.ContentErased {
			return Record{}, false
		}
	}
	return version, true
}

// ListTrashedSkills is 스킬 › 휴지통: retired skills with the name and description of their last version, so
// any device can show and restore them.
func (s *Service) ListTrashedSkills(ctx context.Context, token string, scope Scope, limit int, after string) (SkillPage, error) {
	out := SkillPage{Items: []SkillListItem{}, Authority: AuthorityDataOnly}
	actor, err := s.authorize(ctx, token, actionReadSkills, scope, "", "")
	if err != nil {
		return out, err
	}
	lister, ok := s.Repo.(trashedSkillLister)
	if !ok {
		return out, ErrUnavailable
	}
	rows, err := lister.ListDeprecatedSkillRecords(ctx, scope, limit, after)
	if err != nil {
		return out, err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		out.NextCursor = encodeSkillCursor(scope, rows[len(rows)-1].ID)
	}
	for _, skill := range rows {
		// Same per-skill check as the live list; a skill this principal may not read is left out.
		decision, err := s.authorize(ctx, token, actionReadSkills, scope, skill.ID, "")
		if err != nil || decision.PrincipalID != actor.PrincipalID || decision.Role != actor.Role {
			continue
		}
		version, ok := s.trashedVersion(ctx, scope, skill)
		if !ok {
			continue
		}
		out.Items = append(out.Items, SkillListItem{ID: skill.ID, VersionID: version.ID, Version: version.Analysis.Version,
			Name: version.Analysis.Name, Description: version.Analysis.Description, SourcePath: version.Analysis.SourcePath,
			UpdatedAt: skill.UpdatedAt, SkillCompatibility: listedCompatibility(version, skill)})
	}
	return out, nil
}

type skillCursor struct{ ID, Binding string }

func skillListBinding(scope Scope) string { return ContentHash(structuredText(scope)) }

func decodeSkillCursor(scope Scope, cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if len(cursor) > 2048 {
		return "", ErrInvalid
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	var value skillCursor
	if err != nil || json.Unmarshal(raw, &value) != nil || structuredText(value) != string(raw) || value.Binding != skillListBinding(scope) || !validRunLabel(value.ID, 256) || containsSecret(value.ID) {
		return "", ErrInvalid
	}
	return value.ID, nil
}

func encodeSkillCursor(scope Scope, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(structuredText(skillCursor{id, skillListBinding(scope)})))
}

func enqueueImportedSkill(ctx context.Context, tx Tx, r Record) error {
	id := "imported-skill-embedding-" + ContentHash(structuredText(r.Scope) + "\n" + r.ID + "\n" + strconv.FormatInt(r.Revision, 10))[:32]
	if existing, err := tx.Get(ctx, "jobs", r.Scope, id); err == nil {
		if existing.Kind != "embedding" || existing.Target == nil || existing.Target.Collection != colSkillVersions || existing.Target.ID != r.ID || existing.Target.Revision != r.Revision {
			return ErrConflict
		}
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	now := time.Now().UTC()
	return tx.Insert(ctx, "jobs", Record{ID: id, Scope: r.Scope, Kind: "embedding", Status: JobStatusPending, Revision: 1, CreatedAt: now, UpdatedAt: now, Target: &Ref{Collection: colSkillVersions, ID: r.ID, Revision: r.Revision}})
}

func parseSkillListQuery(raw string) (Scope, int, string, error) {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return Scope{}, 0, "", ErrInvalid
	}
	for key, values := range q {
		if len(values) != 1 || !slices.Contains([]string{"workspace_id", "project_id", "user_id", "agent_id", "limit", "cursor"}, key) {
			return Scope{}, 0, "", ErrInvalid
		}
	}
	scope := Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}
	limit := 100
	if rawLimit := q.Get("limit"); rawLimit != "" {
		limit, err = strconv.Atoi(rawLimit)
	}
	if !validScope(scope) || err != nil || limit < 1 || limit > 500 {
		return Scope{}, 0, "", ErrInvalid
	}
	after, err := decodeSkillCursor(scope, q.Get("cursor"))
	if err != nil {
		return Scope{}, 0, "", err
	}
	return scope, limit, after, nil
}

func (s *Service) ListSkills(ctx context.Context, token string, scope Scope, limit int, after string) (SkillPage, error) {
	out := SkillPage{Items: []SkillListItem{}, Authority: AuthorityDataOnly}
	actor, err := s.authorize(ctx, token, actionReadSkills, scope, "", "")
	if err != nil {
		return out, err
	}
	lister, ok := s.Repo.(skillRecordLister)
	if !ok {
		return out, ErrUnavailable
	}
	rows, err := lister.ListSkillRecords(ctx, scope, limit, after)
	if err != nil {
		return out, err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		out.NextCursor = encodeSkillCursor(scope, rows[len(rows)-1].ID)
	}
	for _, skill := range rows {
		decision, err := s.authorize(ctx, token, actionReadSkills, scope, skill.ID, "")
		if err != nil {
			return out, err
		}
		if decision.PrincipalID != actor.PrincipalID || decision.Role != actor.Role {
			return out, ErrDenied
		}
	}
	// One read transaction for every row's checks: each standalone read otherwise paid its own
	// begin/token/commit round trips (about 620 per page of 100).
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out.Items, out.Withheld = out.Items[:0], nil
		for _, skill := range rows {
			version, err := tx.Get(ctx, colSkillVersions, scope, skill.ActiveVersion)
			if err == nil {
				err = checkActiveSkill(ctx, tx, version)
			}
			if err == nil && !validAnalysis(version.Analysis) {
				err = ErrConflict
			}
			if err != nil {
				if !trialDomainError(err) {
					return err
				}
				out.Withheld = append(out.Withheld, skill.ID)
				continue
			}
			sourceID := ""
			if len(version.Sources) > 0 {
				sourceID = version.Sources[0].SourceID
			}
			out.Items = append(out.Items, SkillListItem{
				ID: skill.ID, VersionID: version.ID, Version: version.Analysis.Version,
				Name: version.Analysis.Name, Description: version.Analysis.Description,
				SourceID: sourceID, SourcePath: version.Analysis.SourcePath, UpdatedAt: version.UpdatedAt,
				SkillCompatibility: listedCompatibility(version, skill),
			})
		}
		return nil
	})
	if err != nil {
		return SkillPage{Items: []SkillListItem{}, Authority: AuthorityDataOnly}, err
	}
	return out, nil
}

// listedCompatibility is what routing uses (the owner's labels over the
// importer's), so a list and the router never disagree; nil when unlabelled.
func listedCompatibility(version, skill Record) map[string]string {
	if c := effectiveCompatibility(version, skill); len(c) > 0 {
		return c
	}
	return nil
}

func (s *Service) ImportSkill(ctx context.Context, token string, in ImportSkillInput) (ImportSkillResult, error) {
	return s.importSkill(ctx, token, in, nil)
}

func (s *Service) importSkill(ctx context.Context, token string, in ImportSkillInput, proposal *ProposeSkillInput) (ImportSkillResult, error) {
	var out ImportSkillResult
	if !validSkillBundle(in.Bundle) || !validComparisonPlan(in.ComparisonPlan) || !validScope(in.Scope) || !validRunLabel(in.SkillID, 256) || in.Path == "" || len(in.Path) > 4096 || strings.TrimSpace(in.Path) != in.Path || strings.IndexFunc(in.Path, unicode.IsControl) >= 0 || !strings.HasSuffix(in.Path, "SKILL.md") || strings.TrimSpace(in.Content) == "" || len(in.Content) > 64000 || in.Analysis == nil || !validSkillOrigin(in.Origin) || !validSkillCompatibility(in.Compatibility) {
		return out, ErrInvalid
	}
	if in.ComparisonPlan != nil {
		plan := *in.ComparisonPlan
		in.ComparisonPlan = &plan
	}
	onSecret := "block"
	if in.RedactSecrets {
		onSecret = "redact"
	}
	clean, inspection, err := inspectCreate(CreateInput{Scope: in.Scope, Text: in.Content, Sensitivity: "internal", OnSecret: onSecret})
	if err != nil {
		return out, err
	}
	in.Content = clean.Text
	if in.Bundle != nil {
		// Snapshot caller-owned slices; secrets are withheld (hash only), never redacted in place.
		copy := *in.Bundle
		copy.Files = slices.Clone(in.Bundle.Files)
		in.Bundle = &copy
		for i, file := range copy.Files {
			// A companion whose text trips the secret scan is pinned by hash only: its bytes are
			// never stored or served, and the bundle hash (paths + file hashes) is unchanged.
			text := file.Path + "\n" + file.Content
			if containsSecret(text) {
				if file.Omitted != "" || file.Content == "" || containsSecret(file.Path) {
					return out, ErrSecretDetected
				}
				copy.Files[i] = SkillBundleFile{Path: file.Path, SHA256: file.SHA256, Size: int64(len(file.Content)), Omitted: "secret"}
				continue
			}
			if personalPattern.MatchString(text) {
				inspection.PersonalData = true
				if clean.Sensitivity != "restricted" {
					clean.Sensitivity = "private"
				}
			}
		}
	}

	decision, err := s.authorize(ctx, token, "write:sources", in.Scope, in.SkillID, "")
	if err != nil {
		return out, err
	}
	if proposal == nil && decision.Role != roleHuman || proposal != nil && !slices.Contains([]string{roleHuman, "agent", "service", "pipeline"}, decision.Role) {
		return out, ErrDenied
	}
	for _, action := range []string{"write:skill_versions", "write:skills"} {
		allowed, e := s.authorize(ctx, token, action, in.Scope, in.SkillID, "")
		if e != nil {
			return out, e
		}
		if allowed.PrincipalID != decision.PrincipalID || allowed.Role != decision.Role {
			return out, ErrDenied
		}
	}
	ctx = bindRequestActor(ctx, decision)
	var evidence skillLessonEvidence
	if proposal != nil {
		copy := *proposal
		copy.Skill = in
		proposal = &copy
		// Authorize before reading any lesson; then authorize every transitive record.
		read := func(ref Ref) error {
			d, e := s.authorize(ctx, token, "read:"+ref.Collection, in.Scope, ref.ID, "")
			if e != nil {
				return e
			}
			if d.PrincipalID != decision.PrincipalID || d.Role != decision.Role {
				return ErrDenied
			}
			return nil
		}
		for _, ref := range []Ref{{Collection: colSkills, ID: in.SkillID}, {Collection: colSkillVersions, ID: in.ComparisonPlan.BaselineVersion}} {
			if err = read(ref); err != nil {
				return out, err
			}
		}
		for _, ref := range proposal.Lessons {
			if err = read(ref); err != nil {
				return out, err
			}
		}
		evidence, err = proposalEvidence(ctx, s.Repo, in.Scope, proposal)
		if err != nil {
			return out, err
		}
		for _, ref := range evidence.Relations {
			if err = read(ref); err != nil {
				return out, err
			}
		}
		for _, ref := range evidence.Sources {
			if err = read(Ref{Collection: "sources", ID: ref.SourceID}); err != nil {
				return out, err
			}
		}
		for _, action := range []string{"write:sources", "write:skill_versions", "write:skills"} {
			d, e := s.authorize(ctx, token, action, in.Scope, in.SkillID, structuredText(proposal))
			if e != nil {
				return out, e
			}
			if d.PrincipalID != decision.PrincipalID || d.Role != decision.Role || !d.ContentSafe {
				return out, ErrDenied
			}
		}
	}
	contentHash := ContentHash(in.Content)
	versionID := "skill-" + ContentHash(structuredText(in.Scope)+"\n"+in.SkillID+"\n"+contentHash)
	if in.Bundle != nil {
		versionID = "skill-" + ContentHash(versionID+"\n"+in.Bundle.SHA256)
	}
	if in.ComparisonPlan != nil {
		versionID = "skill-" + ContentHash(versionID+"\n"+structuredText(in.ComparisonPlan))
	}
	if proposal != nil {
		versionID = "skill-" + ContentHash(versionID+"\n"+structuredText(proposal)+"\n"+decision.PrincipalID+"\n"+decision.Role)
		if evidence.Basis != nil {
			versionID = "skill-" + ContentHash(versionID+"\n"+structuredText(evidence.Basis))
		}
	}
	sourceID := "source-" + versionID
	analysis := *in.Analysis
	analysis.SkillID, analysis.Version, analysis.ContentHash = in.SkillID, contentHash[:16], contentHash
	analysis.SourcePath, analysis.SourceFile = in.Path, "SKILL.md"
	analysis.EvaluationStats = map[string]float64{"runs": 0, "passed": 0, "failed": 0}
	if !validAnalysis(&analysis) {
		return out, ErrInvalid
	}
	now := time.Now().UTC()
	source := Record{OriginProduct: "arkism", SourceRef: in.Path, CollectedAt: &now, TrustLevel: "user_authored", Sensitivity: clean.Sensitivity, Inspection: &inspection, ID: sourceID, Scope: in.Scope, Kind: "skill_file", Status: statusActive, Revision: 1, Text: in.Content, ContentHash: contentHash, CreatedAt: now, UpdatedAt: now, CreatedBy: decision.PrincipalID, CreatedByType: decision.Role}
	source.SkillBundle = in.Bundle
	source.Sources = []SourceRef{sourceReference(source, "original")}
	version := Record{ComparisonPlan: in.ComparisonPlan, SkillOrigin: in.Origin, SkillCompatibility: in.Compatibility, Sensitivity: clean.Sensitivity, Inspection: &inspection, ID: versionID, Scope: in.Scope, Kind: importedSkillBaselineKind, Status: statusActive, Revision: 1, Text: structuredText(analysis), Summary: analysis.Description, Analysis: &analysis, SkillID: in.SkillID, Sources: source.Sources, CreatedAt: now, UpdatedAt: now, CreatedBy: decision.PrincipalID, CreatedByType: decision.Role, ApprovedBy: decision.PrincipalID}
	if in.Bundle != nil {
		version.SkillBundleSHA256 = in.Bundle.SHA256
	}
	if proposal != nil {
		source.SkillBasis, version.SkillBasis = evidence.Basis, evidence.Basis
		if evidence.Basis != nil {
			source.SkillID = in.SkillID
		}
		source.TrustLevel, source.Generator, source.Sensitivity = "agent_proposed", proposal.Generator, evidence.Sensitivity
		version.Kind, version.Generator, version.Sensitivity = proposedSkillKind, proposal.Generator, evidence.Sensitivity
		if clean.Sensitivity == "private" && evidence.Sensitivity != "restricted" {
			source.Sensitivity, version.Sensitivity = "private", "private"
		}
		version.Sources = append(version.Sources, evidence.Sources...)
		version.Relations = evidence.Relations
		source.Kind = proposedSkillSourceKind
		source.Relations = evidence.Relations
		source.Sources = append(source.Sources, evidence.Sources...)
	}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if proposal != nil {
			for _, ref := range proposal.Lessons {
				if e := tx.FenceRelationMember(ctx, in.Scope, ref.Collection, ref.ID); e != nil {
					return e
				}
			}
			current, e := proposalEvidence(ctx, tx, in.Scope, proposal)
			if e != nil {
				return e
			}
			if structuredText(current) != structuredText(evidence) {
				return ErrConflict
			}
		}
		if existing, e := tx.Get(ctx, colSkillVersions, in.Scope, versionID); e == nil {
			if existing.Analysis == nil || existing.Analysis.ContentHash != contentHash || (proposal == nil || existing.Status != "approved") && existing.Status != "candidate" && checkActiveSkill(ctx, tx, existing) != nil {
				return ErrConflict
			}
			if proposal != nil {
				if existing.Status == "approved" {
					if e = validation(ctx, tx, existing, existing.ValidationID, false); e != nil {
						return e
					}
				}
				if e = checkSources(ctx, tx, existing); e != nil {
					return e
				}
				if e = checkRelations(ctx, tx, existing); e != nil {
					return e
				}
			}
			if existing.Status == statusActive {
				if e = enqueueImportedSkill(ctx, tx, existing); e != nil {
					return e
				}
			}
			parent, e := tx.Get(ctx, colSkills, in.Scope, in.SkillID)
			if e != nil {
				return e
			}
			out = ImportSkillResult{ComparisonPlanSHA256: comparisonPlanHash(existing.ComparisonPlan), Skill: parent, Record: existing, Reused: true, Version: slices.Index(skillHistory(parent), existing.ID) + 1}
			return saveSkillGenerationReceipt(ctx, tx, proposal, existing)
		} else if !errors.Is(e, ErrNotFound) {
			return e
		}
		if e := tx.Insert(ctx, "sources", source); e != nil {
			return e
		}
		parent, e := tx.Get(ctx, colSkills, in.Scope, in.SkillID)
		if in.ComparisonPlan != nil {
			if e != nil && !errors.Is(e, ErrNotFound) {
				return e
			}
			if parent.ActiveVersion != in.ComparisonPlan.BaselineVersion {
				return ErrConflict
			}
			if parent.ActiveVersion != "" {
				baseline, err := tx.Get(ctx, colSkillVersions, in.Scope, parent.ActiveVersion)
				if err != nil {
					return err
				}
				if err = checkActiveSkill(ctx, tx, baseline); err != nil {
					return err
				}
			}
			version.Status, version.ApprovedBy, version.BaseVersion = "candidate", "", parent.ActiveVersion
		}
		if errors.Is(e, ErrNotFound) {
			parent = Record{ID: in.SkillID, Scope: in.Scope, Kind: "skill", Status: statusActive, Revision: 1, ActiveVersion: versionID, VersionHistory: []string{versionID}, Sources: source.Sources, CreatedAt: now, UpdatedAt: now, CreatedBy: decision.PrincipalID, CreatedByType: decision.Role}
			if in.ComparisonPlan != nil {
				parent.ActiveVersion = ""
			}
			if e = insertSourceRecord(ctx, tx, colSkills, parent); e != nil {
				return e
			}
		} else if e != nil {
			return e
		} else if in.ComparisonPlan != nil || parent.ActiveVersion != "" && parent.ActiveVersion != versionID {
			// Changed content never overwrites: it becomes the next immutable
			// version as a candidate based on the active one. Approval and
			// apply stay the existing human + independent-evaluation path.
			version.Status, version.ApprovedBy, version.BaseVersion = "candidate", "", parent.ActiveVersion
			previous := parent.Revision
			parent.VersionHistory, parent.UpdatedAt, parent.Revision = append(skillHistory(parent), versionID), now, previous+1
			if e = tx.Replace(ctx, colSkills, parent, previous); e != nil {
				return e
			}
		} else {
			previous := parent.Revision
			parent.ActiveVersion, parent.Sources, parent.UpdatedAt, parent.Revision = versionID, source.Sources, now, previous+1
			if !slices.Contains(parent.VersionHistory, versionID) {
				parent.VersionHistory = append(parent.VersionHistory, versionID)
			}
			if e = tx.Replace(ctx, colSkills, parent, previous); e != nil {
				return e
			}
		}
		if e = insertSourceRecord(ctx, tx, colSkillVersions, version); e != nil {
			return e
		}
		if version.Status == statusActive {
			if e = enqueueImportedSkill(ctx, tx, version); e != nil {
				return e
			}
		}
		action := "import:skills"
		if proposal != nil {
			action = "propose:skills"
		}
		if e = audit(ctx, tx, in.Scope, decision.PrincipalID, action, in.SkillID); e != nil {
			return e
		}
		out = ImportSkillResult{ComparisonPlanSHA256: comparisonPlanHash(version.ComparisonPlan), Skill: parent, Record: version, Version: len(skillHistory(parent))}
		return saveSkillGenerationReceipt(ctx, tx, proposal, version)
	})
	return out, err
}
