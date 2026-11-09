package memory

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// Space deletion: a person removes one space (or several) with everything in
// it, or clears only its memories. Each space goes in one transaction under the
// person's own database permissions, so a failure leaves the space as it was.
// Attachments give back their stored-file reference first (the worker's blob
// collection then removes bytes nobody references); queued jobs of the space
// go with it. A space with a running job or a privacy deletion on legal hold is
// refused (409). The space keeps one audit row naming who deleted it.
// Agents never delete: they propose space_delete or memories_clear, and a
// person accepting that proposal runs the same path (maintenance.go).

const (
	spaceDeleteBatchMax = 50
	spaceArtifactPage   = 200
)

// SpaceDeleteReport counts the rows a delete or clear removed, per collection or table.
type SpaceDeleteReport struct {
	WorkspaceID string           `json:"workspace_id"`
	ProjectID   string           `json:"project_id"`
	Deleted     map[string]int64 `json:"deleted"`
	Attachments int              `json:"attachments"`
}

// SpaceDeleteItem is one space of a bulk delete; Confirm repeats its project id.
type SpaceDeleteItem struct {
	ProjectID string `json:"project_id"`
	Confirm   string `json:"confirm"`
}

type SpaceDeleteBatchInput struct {
	Spaces []SpaceDeleteItem `json:"spaces"`
}

// SpaceDeleteResult is one space's outcome in a bulk delete: a report or an error code.
type SpaceDeleteResult struct {
	ProjectID string             `json:"project_id"`
	Report    *SpaceDeleteReport `json:"report,omitempty"`
	Error     string             `json:"error,omitempty"`
}

type SpaceDeleteBatch struct {
	Results []SpaceDeleteResult `json:"results"`
}

// spaceEraser is the transaction part that removes a space's rows.
type spaceEraser interface {
	// SpaceArtifacts returns up to limit attachment rows of the space (every sub-scope).
	SpaceArtifacts(ctx context.Context, space Scope, limit int) ([]Record, error)
	// EraseSpace deletes every row of the space; with clear set, only the
	// memory collections (and their queued embedding jobs) of that kind.
	EraseSpace(ctx context.Context, space Scope, only *spaceClear) (map[string]int64, error)
}

// spaceClear picks the rows a memories clear removes: Collections, of Kind when set,
// and the causal links that name a removed memory.
type spaceClear struct {
	Collections []string
	Kind        string
}

var spaceClearCollections = []string{colMemories, "memory_candidates", colObservations, colMemoryRelations}

// clearFor maps the kind filter: "" every memory row, "observation" only observations,
// a memory kind that kind's memories and candidates.
func clearFor(kind string) (*spaceClear, bool) {
	switch {
	case kind == "":
		return &spaceClear{Collections: spaceClearCollections}, true
	case kind == "observation":
		return &spaceClear{Collections: []string{colObservations}}, true
	case slices.Contains(captureKinds, kind):
		return &spaceClear{Collections: []string{colMemories, "memory_candidates"}, Kind: kind}, true
	}
	return nil, false
}

// literalSpace is a project-level space named exactly, never by alias: a destructive call names what it removes.
func literalSpace(scope Scope, confirm string) (Scope, bool) {
	scope = spaceScope(scope)
	return scope, validScope(scope) && !strings.HasPrefix(scope.ProjectID, "@") && confirm == scope.ProjectID
}

// DeleteSpace removes one space and everything in it. confirm must repeat the project id.
func (s *Service) DeleteSpace(ctx context.Context, token string, scope Scope, confirm string) (SpaceDeleteReport, error) {
	scope, ok := literalSpace(scope, confirm)
	if !ok {
		return SpaceDeleteReport{}, ErrInvalid
	}
	d, err := s.opsAuthorize(ctx, token, "delete:spaces", scope, "", roleHuman)
	if err != nil {
		return SpaceDeleteReport{}, err
	}
	return s.eraseSpace(ctx, d, scope, nil)
}

// DeleteSpaces removes several spaces of the caller's workspace, each on its own;
// one that fails is reported with its error code and the others still go.
func (s *Service) DeleteSpaces(ctx context.Context, token string, workspace string, in SpaceDeleteBatchInput) (SpaceDeleteBatch, error) {
	if len(in.Spaces) == 0 || len(in.Spaces) > spaceDeleteBatchMax || !scopeName(workspace) {
		return SpaceDeleteBatch{}, ErrInvalid
	}
	out := SpaceDeleteBatch{Results: make([]SpaceDeleteResult, 0, len(in.Spaces))}
	seen := map[string]bool{}
	for _, item := range in.Spaces {
		if seen[item.ProjectID] {
			return SpaceDeleteBatch{}, ErrInvalid
		}
		seen[item.ProjectID] = true
	}
	for _, item := range in.Spaces {
		res := SpaceDeleteResult{ProjectID: item.ProjectID}
		report, err := s.DeleteSpace(ctx, token, Scope{WorkspaceID: workspace, ProjectID: item.ProjectID}, item.Confirm)
		if err != nil {
			res.Error = captureErrorCode(err)
		} else {
			res.Report = &report
		}
		out.Results = append(out.Results, res)
	}
	return out, nil
}

// ClearSpaceMemories removes the space's memories, candidates, observations and
// causal links (or one kind of them) and keeps everything else: settings,
// directives, mental models, documents, wiki and attachments.
func (s *Service) ClearSpaceMemories(ctx context.Context, token string, scope Scope, confirm, kind string) (SpaceDeleteReport, error) {
	scope, ok := literalSpace(scope, confirm)
	only, known := clearFor(kind)
	if !ok || !known {
		return SpaceDeleteReport{}, ErrInvalid
	}
	d, err := s.opsAuthorize(ctx, token, "delete:memories", scope, "", roleHuman)
	if err != nil {
		return SpaceDeleteReport{}, err
	}
	return s.eraseSpace(ctx, d, scope, only)
}

func (s *Service) eraseSpace(ctx context.Context, d Decision, scope Scope, only *spaceClear) (SpaceDeleteReport, error) {
	report := SpaceDeleteReport{WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID}
	action := "delete:space"
	if only != nil {
		action = "clear:memories"
	}
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		eraser, ok := tx.(spaceEraser)
		if !ok {
			return ErrUnavailable
		}
		if only == nil {
			n, err := releaseSpaceArtifacts(ctx, tx, eraser, scope, d.PrincipalID)
			if err != nil {
				return err
			}
			report.Attachments = n
		}
		counts, err := eraser.EraseSpace(ctx, scope, only)
		if err != nil {
			return err
		}
		report.Deleted = counts
		return audit(ctx, tx, scope, d.PrincipalID, action, scope.ProjectID)
	})
	if err != nil {
		return SpaceDeleteReport{}, err
	}
	if only == nil && s.SpaceTemplate != nil {
		s.SpaceTemplate.applied.Delete(scope) // a space made again under this name gets the template again
	}
	return report, nil
}

// releaseSpaceArtifacts detaches every attachment of the space the way source
// erasure does, so each stored file's reference count stays exact.
func releaseSpaceArtifacts(ctx context.Context, tx Tx, eraser spaceEraser, scope Scope, actor string) (int, error) {
	n := 0
	for {
		rows, err := eraser.SpaceArtifacts(ctx, scope, spaceArtifactPage)
		if err != nil {
			return n, err
		}
		for _, r := range rows {
			if err = detachArtifactMetadata(ctx, tx, r, actor); err != nil {
				return n, err
			}
			n++
		}
		if len(rows) < spaceArtifactPage {
			return n, nil
		}
	}
}

// acceptDestructiveChange runs an accepted space_delete or memories_clear
// proposal with the person's typed confirm, then closes it. Nothing is closed
// when the delete fails. A deleted space takes its proposals with it, so that
// accept answers with the proposal as decided rather than a stored row.
func (s *Service) acceptDestructiveChange(ctx context.Context, token string, in MaintenanceDecision, seen Record, status, principal string) (Record, error) {
	var err error
	if seen.Maintenance.Change.Action == "space_delete" {
		_, err = s.DeleteSpace(ctx, token, in.Scope, in.Confirm)
	} else {
		_, err = s.ClearSpaceMemories(ctx, token, in.Scope, in.Confirm, "")
	}
	if err != nil {
		return Record{}, err
	}
	out, err := s.closeMaintenance(ctx, in.Scope, seen, status, in.Decision, principal)
	if errors.Is(err, ErrNotFound) {
		p := *seen.Maintenance
		p.Decision, p.DecidedBy = in.Decision, principal
		seen.Maintenance, seen.Status = &p, status
		return seen, nil
	}
	return out, err
}

// destructiveChange says whether accepting the proposal deletes data, so the decision must carry the typed confirm.
func destructiveChange(p MaintenanceProposal) bool {
	return p.Change != nil && (p.Change.Action == "space_delete" || p.Change.Action == "memories_clear")
}

var errSpaceRenameStore = errors.New("space rename needs the database store")

// spaceRenameStore is the store that moves a space to a new project id (space_rename_postgres.go).
type spaceRenameStore interface {
	RenameSpace(ctx context.Context, from, to Scope, execute bool) (SpaceRenameReport, error)
}

// SpaceRenameInput names the new project id; Execute false only counts what would move.
type SpaceRenameInput struct {
	ProjectID string `json:"project_id"`
	Execute   bool   `json:"execute"`
}

// RenameSpace is the person's route to the administrator rename (memoryd
// --space-rename): the same transaction, under the person's database permissions
// on both names.
func (s *Service) RenameSpace(ctx context.Context, token string, scope Scope, in SpaceRenameInput) (SpaceRenameReport, error) {
	from, ok := literalSpace(scope, scope.ProjectID)
	to := Scope{WorkspaceID: from.WorkspaceID, ProjectID: in.ProjectID}
	if !ok || !validScope(to) || strings.HasPrefix(to.ProjectID, "@") || to == from {
		return SpaceRenameReport{}, ErrInvalid
	}
	d, err := s.opsAuthorize(ctx, token, "write:space_configs", from, "", roleHuman)
	if err != nil {
		return SpaceRenameReport{}, err
	}
	td, err := s.opsAuthorize(ctx, token, "write:space_configs", to, "", roleHuman)
	if err != nil {
		return SpaceRenameReport{}, err
	}
	if td.PrincipalID != d.PrincipalID {
		return SpaceRenameReport{}, ErrDenied
	}
	store, ok := s.Repo.(spaceRenameStore)
	if !ok {
		return SpaceRenameReport{}, errors.Join(ErrUnavailable, errSpaceRenameStore)
	}
	report, err := store.RenameSpace(ctx, from, to, in.Execute)
	if err != nil || !in.Execute {
		return report, err
	}
	if s.SpaceTemplate != nil {
		s.SpaceTemplate.applied.Delete(from)
	}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return audit(ctx, tx, to, d.PrincipalID, "rename:space", from.ProjectID)
	})
	return report, err
}
