package memory

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

type ArtifactListInput struct {
	Scope  Scope
	Limit  int
	Cursor string
}
type SourceArtifacts struct {
	Source     Ref                  `json:"source"`
	Items      []FileArtifactResult `json:"items"`
	NextCursor string               `json:"next_cursor"`
	Authority  string               `json:"authority"`
}

func normalizeArtifactList(in ArtifactListInput) (ArtifactListInput, error) {
	if in.Limit == 0 {
		in.Limit = 20
	}
	if !validScope(in.Scope) || containsSecret(structuredText(in.Scope)) || in.Limit < 1 || in.Limit > 100 || in.Cursor != "" && (!strings.HasPrefix(in.Cursor, "artifact-") || !sourceHashPattern.MatchString(strings.TrimPrefix(in.Cursor, "artifact-"))) {
		return in, ErrInvalid
	}
	return in, nil
}
func parseArtifactListQuery(raw string) (ArtifactListInput, error) {
	var in ArtifactListInput
	q, e := url.ParseQuery(raw)
	if e != nil {
		return in, ErrInvalid
	}
	for key, values := range q {
		if len(values) != 1 || !slices.Contains([]string{"workspace_id", "project_id", "user_id", "agent_id", "limit", "cursor"}, key) {
			return in, ErrInvalid
		}
	}
	in = ArtifactListInput{Scope: Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}, Limit: 20, Cursor: q.Get("cursor")}
	if values, ok := q["limit"]; ok {
		in.Limit, e = strconv.Atoi(values[0])
		if e != nil || in.Limit < 1 {
			return in, ErrInvalid
		}
	}
	return normalizeArtifactList(in)
}

func (s *Service) ListSourceArtifacts(ctx context.Context, token, sourceID string, in ArtifactListInput) (SourceArtifacts, error) {
	in, e := normalizeArtifactList(in)
	if e != nil {
		return SourceArtifacts{}, e
	}
	if !validRunLabel(sourceID, 256) {
		return SourceArtifacts{}, ErrInvalid
	}
	actor, e := s.authorize(ctx, token, "read:sources", in.Scope, sourceID, "")
	if e != nil {
		return SourceArtifacts{}, e
	}
	if s.Repo == nil {
		return SourceArtifacts{}, ErrUnavailable
	}
	src, e := s.Repo.Get(ctx, "sources", in.Scope, sourceID)
	if e != nil {
		return SourceArtifacts{}, e
	}
	if !live(src) {
		return SourceArtifacts{}, ErrNotFound
	}
	rows, e := s.Repo.ListSourceArtifacts(ctx, sourceID, in)
	if e != nil {
		return SourceArtifacts{}, e
	}
	if len(rows) > in.Limit+1 {
		return SourceArtifacts{}, ErrUnavailable
	}
	if e = s.authorizeSourceArtifacts(ctx, token, in.Scope, sourceID, src, actor, rows); e != nil {
		return SourceArtifacts{}, e
	}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		current, e := tx.Get(ctx, "sources", in.Scope, sourceID)
		if e != nil {
			return e
		}
		if !live(current) {
			return ErrNotFound
		}
		if current.Revision != src.Revision || current.Sensitivity != src.Sensitivity {
			return ErrConflict
		}
		for _, r := range rows {
			now, e := tx.Get(ctx, colArtifacts, in.Scope, r.ID)
			if e != nil {
				return e
			}
			if !validArtifactRecord(now) || now.Scope != in.Scope || now.ArtifactGeneration != r.ArtifactGeneration || now.Revision != r.Revision || now.Kind != r.Kind || *now.Artifact != *r.Artifact || *now.Target != *r.Target || now.Sensitivity != current.Sensitivity {
				return ErrConflict
			}
		}
		if e = tx.FenceSource(ctx, in.Scope, sourceID); e != nil {
			return e
		}
		if e = audit(ctx, tx, in.Scope, actor.PrincipalID, "read:sources", sourceID); e != nil {
			return e
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "read:artifacts:list", sourceID)
	})
	if e != nil {
		return SourceArtifacts{}, e
	}
	out := SourceArtifacts{Source: Ref{Collection: "sources", ID: sourceID, Revision: src.Revision}, Items: []FileArtifactResult{}, Authority: AuthorityDataOnly}
	if len(rows) > in.Limit {
		rows = rows[:in.Limit]
		out.NextCursor = rows[len(rows)-1].ID
	}
	for _, r := range rows {
		out.Items = append(out.Items, FileArtifactResult{ID: r.ID, FileArtifactSpec: FileArtifactSpec{Kind: r.Kind, MIME: r.Artifact.MIME, Digest: r.Artifact.Digest}, Bytes: r.Artifact.Bytes})
	}
	return out, nil
}

// shortcut: at most 102 sequential policy calls; batch only with equivalent per-file decisions.
func (s *Service) authorizeSourceArtifacts(ctx context.Context, token string, scope Scope, sourceID string, src Record, actor Decision, rows []Record) error {
	for _, r := range rows {
		if !validArtifactRecord(r) || r.Scope != scope || r.Target.ID != sourceID || r.Sensitivity != src.Sensitivity {
			return ErrUnavailable
		}
		if r.Target.Revision != src.Revision {
			return ErrConflict
		}
		d, e := s.authorize(ctx, token, "read:artifacts", scope, r.ID, "")
		if e != nil {
			return e
		}
		if d.PrincipalID != actor.PrincipalID || d.Role != actor.Role {
			return ErrDenied
		}
	}
	return nil
}
