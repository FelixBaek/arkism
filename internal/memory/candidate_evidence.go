package memory

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Evidence counts describe checked source messages, never the model's confidence
// or the number of retries. Missing/expired evidence remains explicitly unknown.
type CandidateEvidenceSummary struct {
	UserMentionKeys []string `json:"user_mention_keys"`
	SourceKeys      []string `json:"source_keys"`
	Complete        bool     `json:"complete"`
	Authority       string   `json:"authority"`
}

func (s *Service) CandidateEvidence(ctx context.Context, token, id string, scope Scope) (CandidateEvidenceSummary, error) {
	out := CandidateEvidenceSummary{UserMentionKeys: []string{}, SourceKeys: []string{}, Complete: true, Authority: AuthorityDataOnly}
	if !validScope(scope) || !validRunLabel(id, 256) {
		return out, ErrInvalid
	}
	actor, err := s.authorize(ctx, token, reviewAction, scope, id, "")
	if err != nil {
		return out, err
	}
	if actor.Role != roleHuman {
		return out, ErrDenied
	}
	ctx = bindRequestActor(ctx, actor)
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	candidate, err := s.Get(ctx, token, "memory_candidates", scope, id)
	if err != nil {
		return out, err
	}
	if candidate.Status != "candidate" && candidate.Status != "conflicted" {
		return out, ErrConflict
	}
	checked := 0
	userStatement := candidate.MemoryContext != nil && candidate.MemoryContext.EvidenceKind == "user_statement"
	for _, ref := range candidate.Sources {
		source, err := s.Get(ctx, token, "sources", scope, ref.SourceID)
		if err != nil {
			out.Complete = false
			continue
		}
		key := ContentHash(structuredText(scope) + "/source/" + source.ID)
		if !slices.Contains(out.SourceKeys, key) {
			out.SourceKeys = append(out.SourceKeys, key)
		}
		if len(source.ArchiveMessages) == 0 {
			out.Complete = false
		}
		if !userStatement {
			out.Complete = false
			continue
		}
		// A source can reference an entire conversation window. Only count a
		// turn when this candidate's verified literal citation occurs in it.
		// Merely being a user turn in the same window is not another mention.
		citation := ""
		if ref.Span != nil && validateSourceSpan(ref.Span, ContentHash(source.Text), utf8.RuneCountInString(source.Text)) == nil {
			citation = strings.TrimSpace(string([]rune(source.Text)[ref.Span.Start:ref.Span.End]))
		}
		if utf8.RuneCountInString(citation) < 5 {
			out.Complete = false
			continue
		}
		for _, message := range source.ArchiveMessages {
			if checked >= 64 || ctx.Err() != nil {
				out.Complete = false
				break
			}
			checked++
			view, err := s.ReadSessionArchiveMessage(ctx, token, ArchiveMessageInput{Scope: scope, Ref: message})
			if err != nil {
				out.Complete = false
				continue
			}
			if view.Role != "user" || !strings.Contains(view.Text, citation) {
				out.Complete = false
				continue
			}
			header, err := s.Repo.Get(ctx, colSessionArchives, scope, message.ArchiveID)
			if err != nil || !live(header) || header.SessionArchive == nil {
				out.Complete = false
				continue
			}
			a := header.SessionArchive
			// Reconstructed multi-part messages need a canonical parent identity;
			// do not turn projection parts into multiple supposed user requests.
			if a.Format == "reconstructed_zcode" || a.SessionID == "" {
				out.Complete = false
				continue
			}
			identity := view.Ref.SQLiteStep + "/" + view.Ref.RecordKey
			if identity == "/" {
				identity = strconv.Itoa(view.Ref.Chunk) + "/" + strconv.Itoa(view.Ref.Offset)
			}
			key = ContentHash(structuredText(scope) + "/utterance/" + a.Origin + "/" + a.SessionID + "/" + identity)
			if !slices.Contains(out.UserMentionKeys, key) {
				out.UserMentionKeys = append(out.UserMentionKeys, key)
			}
		}
	}
	if len(out.SourceKeys) == 0 {
		out.Complete = false
	}
	slices.Sort(out.SourceKeys)
	slices.Sort(out.UserMentionKeys)
	current, err := s.Get(ctx, token, "memory_candidates", scope, id)
	if err != nil {
		return CandidateEvidenceSummary{}, err
	}
	if current.Revision != candidate.Revision || current.Status != candidate.Status {
		return CandidateEvidenceSummary{}, ErrConflict
	}
	return out, nil
}

func candidateEvidenceRoutes(mux *http.ServeMux, s *Service) {
	mux.HandleFunc("GET /v1/memory_candidates/{id}/sources", func(w http.ResponseWriter, r *http.Request) {
		scope, err := parseSourceTraceQuery(r.URL.RawQuery)
		if err != nil {
			respond(w, nil, err)
			return
		}
		out, err := s.TraceCandidateSources(r.Context(), bearer(r), scope, r.PathValue("id"))
		respond(w, out, err)
	})
	mux.HandleFunc("GET /v1/memory_candidates/{id}/evidence-summary", func(w http.ResponseWriter, r *http.Request) {
		scope, err := parseSourceTraceQuery(r.URL.RawQuery)
		if err != nil {
			respond(w, nil, err)
			return
		}
		out, err := s.CandidateEvidence(r.Context(), bearer(r), r.PathValue("id"), scope)
		respond(w, out, err)
	})
}
