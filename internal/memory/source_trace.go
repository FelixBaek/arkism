package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"time"
	"unicode/utf8"
)

type SourceTrace struct {
	OriginAgentID   string              `json:"origin_agent_id,omitempty"`
	ArchiveMessages []ArchiveMessageRef `json:"archive_messages,omitempty"`

	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
	SourceID      string     `json:"source_id"`
	State         string     `json:"state"`
	Reference     *SourceRef `json:"reference,omitempty"`
	Source        *Ref       `json:"source,omitempty"`
	OriginProduct string     `json:"origin_product,omitempty"`
	SourceRef     string     `json:"source_ref,omitempty"`
	CollectedAt   *time.Time `json:"collected_at,omitempty"`
	Excerpt       string     `json:"excerpt,omitempty"`
}

type MemorySources struct {
	Memory     Ref           `json:"memory"`
	ApprovedBy string        `json:"approved_by,omitempty"`
	Sources    []SourceTrace `json:"sources"`
	Authority  string        `json:"authority"`
}

func parseSourceTraceQuery(raw string) (Scope, error) {
	q, e := url.ParseQuery(raw)
	if e != nil {
		return Scope{}, ErrInvalid
	}
	for key, values := range q {
		if len(values) != 1 {
			return Scope{}, ErrInvalid
		}
		switch key {
		case "workspace_id", "project_id", "user_id", "agent_id":
		default:
			return Scope{}, ErrInvalid
		}
	}
	scope := Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}
	if !validScope(scope) {
		return Scope{}, ErrInvalid
	}
	return scope, nil
}

func (s *Service) TraceMemorySources(ctx context.Context, token string, scope Scope, id string) (MemorySources, error) {
	return s.traceRecordSources(ctx, token, scope, id, colMemories)
}

func (s *Service) TraceCandidateSources(ctx context.Context, token string, scope Scope, id string) (MemorySources, error) {
	actor, err := s.authorize(ctx, token, reviewAction, scope, id, "")
	if err != nil {
		return MemorySources{}, err
	}
	if actor.Role != roleHuman {
		return MemorySources{}, ErrDenied
	}
	return s.traceRecordSources(bindRequestActor(ctx, actor), token, scope, id, "memory_candidates")
}

func (s *Service) traceRecordSources(ctx context.Context, token string, scope Scope, id, collection string) (MemorySources, error) {
	if len(id) == 0 || len(id) > 256 {
		return MemorySources{}, ErrInvalid
	}
	d, e := s.authorize(ctx, token, "read:"+collection, scope, id, "")
	if e != nil {
		return MemorySources{}, e
	}
	if s.Repo == nil {
		return MemorySources{}, ErrUnavailable
	}
	// shortcut: at most 64 policy calls in the transaction; preauthorize with a
	// revision fence if measured policy latency makes multi-source reads too slow.
	var out MemorySources
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out = MemorySources{}
		r, e := tx.Get(ctx, collection, scope, id)
		if e != nil {
			return e
		}
		if r.ContentErased || r.PrivacyQuarantined {
			return ErrNotFound
		}
		if len(r.Sources) == 0 || len(r.Sources) > 64 {
			return ErrInvalid
		}
		out = MemorySources{Memory: Ref{Collection: collection, ID: r.ID, Revision: r.Revision}, ApprovedBy: r.ApprovedBy, Sources: []SourceTrace{}, Authority: "data_only_untrusted"}
		// Cache only within this transaction attempt; duplicate locations keep their own proofs.
		rows := map[string]Record{}
		states := map[string]string{}
		deleted := map[string]time.Time{}
		for _, ref := range r.Sources {
			if ref.SourceID == "" || len(ref.SourceID) > 256 {
				return ErrInvalid
			}
			state, seen := states[ref.SourceID]
			if !seen {
				var err error
				state, err = s.traceSourceState(ctx, tx, token, scope, ref.SourceID, d, rows, deleted)
				if err != nil {
					return err
				}
				states[ref.SourceID] = state
			}
			item := SourceTrace{SourceID: ref.SourceID, State: state}
			if state == "deleted" {
				at := deleted[ref.SourceID]
				item.DeletedAt = &at
			}
			if state == "available" {
				fillAvailableSourceTrace(&item, ref, rows[ref.SourceID])
			}
			out.Sources = append(out.Sources, item)
		}
		action := "read:memory_sources"
		if collection == "memory_candidates" {
			action = "read:candidate_sources"
		}
		return audit(ctx, tx, scope, d.PrincipalID, action, id)
	})
	if e != nil {
		return MemorySources{}, e
	}
	return out, nil
}

func (s *Service) traceSourceState(ctx context.Context, tx Tx, token string, scope Scope, sourceID string, d Decision, rows map[string]Record, deleted map[string]time.Time) (string, error) {
	var state string
	sd, err := s.authorize(ctx, token, "read:sources", scope, sourceID, "")
	if errors.Is(err, ErrDenied) {
		state = "hidden"
	} else if err != nil {
		return "", err
	} else if sd.PrincipalID != d.PrincipalID || sd.Role != d.Role {
		return "", ErrDenied
	} else {
		source, err := tx.Get(ctx, "sources", scope, sourceID)
		if errors.Is(err, ErrNotFound) {
			state = "unavailable"
			at, proofErr := sourceDeletion(ctx, tx, scope, sourceID)
			if proofErr == nil {
				if at.IsZero() || at.After(time.Now()) {
					return "", ErrUnavailable
				}
				state = "deleted"
				deleted[sourceID] = at
			} else if !errors.Is(proofErr, ErrNotFound) {
				return "", proofErr
			}
		} else if err != nil {
			return "", err
		} else if !live(source) {
			state = "inactive"
		} else {
			state = "available"
			rows[sourceID] = source
		}
	}
	return state, nil
}

func fillAvailableSourceTrace(item *SourceTrace, ref SourceRef, source Record) {
	err := validateSourceSpan(ref.Span, ContentHash(source.Text), utf8.RuneCountInString(source.Text))
	if ref.Locator == "" {
		err = ErrInvalid
	}
	if errors.Is(err, ErrConflict) {
		item.State = "changed"
	} else if err != nil {
		item.State = "invalid"
	} else {
		item.ArchiveMessages = append([]ArchiveMessageRef(nil), source.ArchiveMessages...)
		refCopy := ref
		item.Reference = &refCopy
		item.Source = &Ref{Collection: "sources", ID: source.ID, Revision: source.Revision}
		item.OriginProduct, item.SourceRef, item.CollectedAt = source.OriginProduct, source.SourceRef, source.CollectedAt
		if source.Kind == "note" && source.SourceEventID != "" && source.TraceID != "" {
			var provenance struct {
				OriginAgentID string `json:"origin_agent_id"`
			}
			if json.Unmarshal([]byte(source.Text), &provenance) == nil && validRunLabel(provenance.OriginAgentID, 256) {
				item.OriginAgentID = provenance.OriginAgentID
			}
		}
		if ref.Span != nil {
			item.Excerpt = string([]rune(source.Text)[ref.Span.Start:ref.Span.End])
		}
	}
}
