package memory

import (
	"context"
	"net/http"
	"time"
)

func memoryApprovalOrigin(r Record) string {
	if r.Review != nil && r.Review.ReceiptID != "" && r.Review.ActorRole == roleHuman && r.Review.Channel == "explicit_review_api" && r.Review.By == r.ApprovedBy && (r.Review.Action == "approve" || r.Review.Action == "edit_approve") {
		return "explicit_human_role_review"
	}
	return "legacy_human_confirmation_unproven"
}

type MemoryManagementStatus struct {
	ApprovalOrigin       string `json:"approval_origin"`
	ConflictPending      bool   `json:"conflict_pending"`
	ReviewOverdue        bool   `json:"review_overdue"`
	SourcesChecked       int    `json:"sources_checked"`
	OriginalsAvailable   int    `json:"originals_available"`
	OriginalsExpired     int    `json:"originals_expired"`
	OriginalsUnknown     int    `json:"originals_unknown"`
	Complete             bool   `json:"complete"`
	ActualUseStatus      string `json:"actual_use_status"`
	SemanticCompleteness string `json:"semantic_completeness"`
	Authority            string `json:"authority"`
}

func (s *Service) MemoryManagement(ctx context.Context, token, id string, scope Scope) (MemoryManagementStatus, error) {
	out := MemoryManagementStatus{Complete: true, ActualUseStatus: "not_measured", SemanticCompleteness: "not_measured", Authority: AuthorityDataOnly}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	actor, err := s.authorize(ctx, token, "read:memories", scope, id, "")
	if err != nil {
		return out, err
	}
	if actor.Role != roleHuman {
		return out, ErrDenied
	}
	ctx = bindRequestActor(ctx, actor)
	r, err := s.Get(ctx, token, colMemories, scope, id)
	if err != nil {
		return out, err
	}
	out.ApprovalOrigin = memoryApprovalOrigin(r)
	out.ConflictPending = r.ConflictID != ""
	out.ReviewOverdue = r.ReviewDueAt != nil && !r.ReviewDueAt.After(time.Now())
	seen := map[string]bool{}
	for _, ref := range r.Sources {
		if ctx.Err() != nil {
			out.Complete = false
			break
		}
		source, err := s.Get(ctx, token, "sources", scope, ref.SourceID)
		if err != nil {
			out.Complete = false
			out.OriginalsUnknown++
			continue
		}
		out.SourcesChecked++
		if len(source.ArchiveMessages) == 0 {
			out.OriginalsUnknown++
			out.Complete = false
		}
		for _, original := range source.ArchiveMessages {
			if seen[original.ArchiveID] {
				continue
			}
			seen[original.ArchiveID] = true
			if len(seen) > 32 {
				out.OriginalsUnknown++
				out.Complete = false
				continue
			}
			header, err := s.GetSessionArchive(ctx, token, SessionArchiveGetInput{Scope: scope, ID: original.ArchiveID})
			if err != nil {
				out.OriginalsUnknown++
				out.Complete = false
				continue
			}
			switch header.State {
			case sessionArchiveStateActive:
				out.OriginalsAvailable++
			case sessionArchiveStateExpired:
				out.OriginalsExpired++
			default:
				out.OriginalsUnknown++
				out.Complete = false
			}
		}
	}
	if len(r.Sources) == 0 {
		out.Complete = false
	}
	current, err := s.Get(ctx, token, colMemories, scope, id)
	if err != nil {
		return MemoryManagementStatus{}, err
	}
	if current.Revision != r.Revision || current.Status != r.Status {
		return MemoryManagementStatus{}, ErrConflict
	}
	return out, nil
}

// The optional audit is strictly bounded and read-only. A timeout is unknown,
// never a clean result. It does not attest external backups or client caches.
type ArchiveStorageAudit struct {
	State            string `json:"state"`
	OrphanCandidates *int   `json:"orphan_candidates,omitempty"`
	Limit            int    `json:"limit"`
	ExternalCopies   string `json:"external_copies"`
	Authority        string `json:"authority"`
}

func (s *Service) AuditArchiveStorage(ctx context.Context, token string, scope Scope) (ArchiveStorageAudit, error) {
	out := ArchiveStorageAudit{State: "not_verified", Limit: 25, ExternalCopies: "not_verified", Authority: AuthorityDataOnly}
	actor, err := s.authorize(ctx, token, "read:"+colSessionArchives, scope, "", "")
	if err != nil {
		return out, err
	}
	if actor.Role != roleHuman {
		return out, ErrDenied
	}
	ctx = bindRequestActor(ctx, actor)
	store, ok := s.Repo.(SessionArchiveStore)
	if !ok {
		return out, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := store.SessionArchiveOrphanRows(ctx, scope, s.sessionArchiveNow(), out.Limit)
	if err != nil || ctx.Err() != nil {
		out.State = "incomplete"
		return out, nil
	}
	if len(rows) > out.Limit {
		return out, ErrUnavailable
	}
	for _, r := range rows {
		if r.Scope != scope {
			return out, ErrDenied
		}
		who, err := s.authorize(ctx, token, "read:"+colSessionArchives, scope, r.ID, "")
		if err != nil {
			return out, err
		}
		if who.PrincipalID != actor.PrincipalID || who.Role != actor.Role {
			return out, ErrDenied
		}
	}
	n := len(rows)
	out.OrphanCandidates = &n
	if n >= out.Limit {
		out.State = "partial"
	} else {
		out.State = "checked_scope"
	}
	return out, nil
}

func memoryManagementRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/memories/{id}/management-status", func(w http.ResponseWriter, r *http.Request) {
		scope, err := parseSourceTraceQuery(r.URL.RawQuery)
		if err != nil {
			respond(w, nil, err)
			return
		}
		out, err := s.MemoryManagement(r.Context(), bearer(r), r.PathValue("id"), scope)
		respond(w, out, err)
	})
	mux.HandleFunc("GET /v1/session-archives/storage-audit", func(w http.ResponseWriter, r *http.Request) {
		scope, err := parseSourceTraceQuery(r.URL.RawQuery)
		if err != nil {
			respond(w, nil, err)
			return
		}
		out, err := s.AuditArchiveStorage(r.Context(), bearer(r), scope)
		respond(w, out, err)
	})
}
