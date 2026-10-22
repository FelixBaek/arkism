package memory

import (
	"context"
	"net/http"
	"strings"
)

// This is an immediate egress check, not a reusable export capability.
// The client must submit the exact data again before a later model attempt.
type ModelArchiveTransferInput struct {
	SourceRefs  []Ref               `json:"source_refs,omitempty"`
	Scope       Scope               `json:"scope"`
	Refs        []ArchiveMessageRef `json:"refs"`
	Destination string              `json:"destination"`
	Content     string              `json:"content"`
	Work        *Ref                `json:"work,omitempty"`
}
type ModelArchiveTransferOutput struct {
	Authorized  bool   `json:"authorized"`
	ContentHash string `json:"content_hash"`
}

func (s *Service) AuthorizeModelArchiveTransfer(ctx context.Context, token string, in ModelArchiveTransferInput) (ModelArchiveTransferOutput, error) {
	if !validModelArchiveTransferInput(in) {
		return ModelArchiveTransferOutput{}, ErrInvalid
	}
	// No API key, prompt body or excerpt is logged or stored in the receipt.
	actor, e := s.authorizeArchiveModelContent(ctx, token, in.Scope, "", "private", in.Content)
	if e != nil {
		return ModelArchiveTransferOutput{}, e
	}
	if actor.Role != roleService {
		return ModelArchiveTransferOutput{}, ErrDenied
	}
	ctx = bindRequestActor(ctx, actor)
	headers, _, e := s.validateArchiveMessageRefs(ctx, token, in.Scope, in.Refs, "", "")
	if e != nil {
		return ModelArchiveTransferOutput{}, e
	}
	evidence := append([]Ref(nil), in.SourceRefs...)
	for _, ref := range in.SourceRefs {
		if ref.Collection != "sources" || ref.Revision < 1 || !validRunLabel(ref.ID, 256) {
			return ModelArchiveTransferOutput{}, ErrInvalid
		}
	}

	if in.Work != nil {
		refs, more, err := s.archiveTransferWork(ctx, token, in)
		if err != nil {
			return ModelArchiveTransferOutput{}, err
		}
		evidence = append(evidence, refs...)
		headers = append(headers, more...)
	}
	// Follow canonical source provenance as well as direct work archive refs.
	// A still-present derived source cannot revive a removed raw archive.
	seenSources := map[string]bool{}
	for at := 0; at < len(evidence); at++ {
		if len(evidence) > 128 {
			return ModelArchiveTransferOutput{}, ErrInvalid
		}
		more, parents, err := s.archiveTransferEvidence(ctx, token, in, evidence[at], seenSources)
		if err != nil {
			return ModelArchiveTransferOutput{}, err
		}
		headers = append(headers, more...)
		evidence = append(evidence, parents...)
	}

	for _, h := range headers {
		if h.Sensitivity == "restricted" {
			return ModelArchiveTransferOutput{}, ErrDenied
		}
	}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if err := validateWorkCheckpointSourceRefs(ctx, tx, in.Scope, evidence); err != nil {
			return err
		}
		if err := s.fenceSourceArchives(ctx, tx, headers); err != nil {
			return err
		}
		if in.Work != nil {
			current, err := tx.Get(ctx, colWorkCheckpoints, in.Scope, in.Work.ID)
			if err != nil {
				return err
			}
			if !isWorkCheckpoint(current) || !live(current) || current.ContentErased || current.Revision != in.Work.Revision {
				return ErrConflict
			}
			if err = validateWorkCheckpointSourceRefs(ctx, tx, in.Scope, current.WorkCheckpoint.Package.SourceRefs); err != nil {
				return err
			}
			if err = fenceWorkCheckpoint(ctx, tx, in.Scope, current.ID, current.Revision); err != nil {
				return err
			}
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "transfer:model_input", ContentHash(in.Content))
	})
	if e != nil {
		return ModelArchiveTransferOutput{}, e
	}
	return ModelArchiveTransferOutput{Authorized: true, ContentHash: ContentHash(in.Content)}, nil
}

func validModelArchiveTransferInput(in ModelArchiveTransferInput) bool {
	if !validScope(in.Scope) || len(in.Refs)+len(in.SourceRefs) < 1 || len(in.Refs)+len(in.SourceRefs) > 8 || in.Destination != "external:anthropic" || strings.TrimSpace(in.Content) == "" || len(in.Content) > 100000 {
		return false
	}
	return in.Work == nil || in.Work.Collection == colWorkCheckpoints && in.Work.Revision >= 1 && workCheckpointIDPattern.MatchString(in.Work.ID)
}

// archiveTransferWork returns the work checkpoint's source provenance and the
// headers of its archive messages.
func (s *Service) archiveTransferWork(ctx context.Context, token string, in ModelArchiveTransferInput) ([]Ref, []Record, error) {
	work, e := s.GetWorkCheckpoint(ctx, token, WorkCheckpointGetInput{Scope: in.Scope, ID: in.Work.ID})
	if e != nil {
		return nil, nil, e
	}
	if work.Revision != in.Work.Revision {
		return nil, nil, ErrConflict
	}
	// The previous summary's complete provenance also participates, never just
	// today's new messages. Existing read fences verify exact source revisions.
	var headers []Record
	for start := 0; start < len(work.ArchiveMessages); start += 8 {
		end := min(start+8, len(work.ArchiveMessages))
		more, _, err := s.validateArchiveMessageRefs(ctx, token, in.Scope, work.ArchiveMessages[start:end], "", "")
		if err != nil {
			return nil, nil, err
		}
		headers = append(headers, more...)
	}
	return work.Package.SourceRefs, headers, nil
}

// archiveTransferEvidence authorizes one evidence record and returns its
// archive headers and the not yet seen parent sources to follow.
func (s *Service) archiveTransferEvidence(ctx context.Context, token string, in ModelArchiveTransferInput, ref Ref, seenSources map[string]bool) ([]Record, []Ref, error) {
	r, err := s.Get(ctx, token, ref.Collection, in.Scope, ref.ID)
	if err != nil {
		return nil, nil, err
	}
	if r.Revision != ref.Revision || !live(r) || r.ContentErased {
		return nil, nil, ErrConflict
	}
	if ref.Collection == "sources" {
		_, err = s.authorizeArchiveModelContent(ctx, token, in.Scope, r.ID, r.Sensitivity, recordContent(r))
	} else {
		_, err = s.authorizeTransfer(ctx, token, "transfer:model_input", in.Scope, r.ID, r.Sensitivity, in.Destination, recordContent(r), r.Sources)
	}
	if err != nil {
		return nil, nil, err
	}
	var headers []Record
	for start := 0; start < len(r.ArchiveMessages); start += 8 {
		more, _, err := s.validateArchiveMessageRefs(ctx, token, in.Scope, r.ArchiveMessages[start:min(start+8, len(r.ArchiveMessages))], "", "")
		if err != nil {
			return nil, nil, err
		}
		headers = append(headers, more...)
	}
	if ref.Collection == "sources" {
		seenSources[r.ID] = true
	}
	var parents []Ref
	for _, src := range r.Sources {
		if seenSources[src.SourceID] {
			continue
		}
		seenSources[src.SourceID] = true
		parent, err := s.Get(ctx, token, "sources", in.Scope, src.SourceID)
		if err != nil {
			return nil, nil, err
		}
		parents = append(parents, Ref{Collection: "sources", ID: parent.ID, Revision: parent.Revision})
	}
	return headers, parents, nil
}

func modelTransferRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/session-archives/authorize-transfer", func(w http.ResponseWriter, r *http.Request) {
		var in ModelArchiveTransferInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.AuthorizeModelArchiveTransfer(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}

// Raw archive messages have archive refs, not fabricated source documents.
// Their exact read authorization, privacy state and fences are checked by the
// caller. Derived source-backed records still use authorizeTransfer unchanged.
func (s *Service) authorizeArchiveModelContent(ctx context.Context, token string, scope Scope, id, sensitivity, content string) (Decision, error) {
	if sensitivity == "" || !validSensitivity(sensitivity) || sensitivity == "restricted" || !safeTransfer(content) {
		return Decision{}, ErrDenied
	}
	d, e := s.authorizeAt(ctx, token, "transfer:model_input", scope, id, structuredText(map[string]string{"destination": "external:anthropic", "sensitivity": sensitivity, "content": content}), "external:anthropic")
	if e != nil {
		return Decision{}, e
	}
	if !d.ExternalTransfer || !d.ContentSafe {
		return Decision{}, ErrDenied
	}
	return d, nil
}
