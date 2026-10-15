package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// Handoff transfer is an immediate egress check, not an approval or reusable
// capability. The caller must dispatch exactly this serialized prompt once.
type HandoffTransferSkill struct {
	SkillID     string `json:"skill_id"`
	VersionID   string `json:"version_id"`
	ContentHash string `json:"content_hash"`
}
type HandoffTransferInput struct {
	Scope       Scope                  `json:"scope"`
	Work        Ref                    `json:"work"`
	Destination string                 `json:"destination"`
	Content     string                 `json:"content"`
	Skills      []HandoffTransferSkill `json:"skills,omitempty"`
}

type HandoffTransferOutput struct {
	Authorized      bool                `json:"authorized"`
	ContentHash     string              `json:"content_hash"`
	SourceRefs      []Ref               `json:"source_refs"`
	ArchiveMessages []ArchiveMessageRef `json:"archive_messages"`
}

func (s *Service) authorizeHandoffContent(ctx context.Context, token string, in HandoffTransferInput, id, sensitivity, content string) (Decision, error) {
	if sensitivity == "" || !validSensitivity(sensitivity) || sensitivity == "restricted" || !safeTransfer(content) {
		return Decision{}, ErrDenied
	}
	d, e := s.authorizeAt(ctx, token, "transfer:model_input", in.Scope, id, structuredText(map[string]string{"destination": in.Destination, "sensitivity": sensitivity, "content": content}), in.Destination)
	if e != nil {
		return Decision{}, e
	}
	if !d.ExternalTransfer || !d.ContentSafe {
		return Decision{}, ErrDenied
	}
	return d, nil
}
func (s *Service) AuthorizeHandoffTransfer(ctx context.Context, token string, in HandoffTransferInput) (HandoffTransferOutput, error) {
	fail := HandoffTransferOutput{}
	if !validScope(in.Scope) || in.Work.Collection != colWorkCheckpoints || in.Work.Revision < 1 || !workCheckpointIDPattern.MatchString(in.Work.ID) || len(in.Skills) > 8 || (in.Destination != "external:openai" && in.Destination != "external:anthropic") || len(in.Content) > 100000 {
		return fail, ErrInvalid
	}
	var prompt map[string]string
	if json.Unmarshal([]byte(in.Content), &prompt) != nil || len(prompt) != 2 || strings.TrimSpace(prompt["task"]) == "" || strings.TrimSpace(prompt["instructions"]) == "" {
		return fail, ErrInvalid
	}
	actor, err := s.authorizeHandoffContent(ctx, token, in, "", "private", in.Content)
	if err != nil {
		return fail, err
	}
	if actor.Role != roleAgent && actor.Role != roleHuman && actor.Role != roleService {
		return fail, ErrDenied
	}
	ctx = bindRequestActor(ctx, actor)
	work, err := s.GetWorkCheckpoint(ctx, token, WorkCheckpointGetInput{Scope: in.Scope, ID: in.Work.ID})
	if err != nil {
		return fail, err
	}
	if work.Revision != in.Work.Revision {
		return fail, ErrConflict
	}
	// Always inspect the complete canonical parent, not caller-supplied provenance.
	if _, err = s.authorizeHandoffContent(ctx, token, in, work.ID, "private", structuredText(work.Package)); err != nil {
		return fail, err
	}
	evidence := append([]Ref(nil), work.Package.SourceRefs...)
	headers := []Record{}
	archiveRefs := []ArchiveMessageRef{}
	seenArchives := map[ArchiveMessageRef]bool{}
	addArchives := func(refs []ArchiveMessageRef) error {
		for i := 0; i < len(refs); i += 8 {
			more, normalized, e := s.validateArchiveMessageRefs(ctx, token, in.Scope, refs[i:min(i+8, len(refs))], "", "")
			if e != nil {
				return e
			}
			headers = append(headers, more...)
			for _, ref := range normalized {
				if !seenArchives[ref] {
					seenArchives[ref] = true
					archiveRefs = append(archiveRefs, ref)
				}
			}
			// The child checkpoint save accepts eight archive pointers per call.
			if len(archiveRefs) > 8 {
				return ErrInvalid
			}
		}
		return nil
	}
	if err = addArchives(work.ArchiveMessages); err != nil {
		return fail, err
	}
	resolved := []ResolvedSkill{}
	seenSkills := map[string]bool{}
	for _, skill := range in.Skills {
		if !validRunLabel(skill.SkillID, 256) || strings.Contains(skill.SkillID, "@") || !validRunLabel(skill.VersionID, 256) || !sourceHashPattern.MatchString(skill.ContentHash) || seenSkills[skill.SkillID] {
			return fail, ErrInvalid
		}
		seenSkills[skill.SkillID] = true
		page, e := s.ResolveSkills(ctx, token, SkillResolveInput{Scope: in.Scope, Refs: []string{skill.SkillID + "@latest-approved"}})
		if e != nil {
			return fail, e
		}
		if len(page.Items) != 1 {
			return fail, ErrConflict
		}
		item := page.Items[0]
		if item.Error != "" || !item.LatestApproved || item.SkillID != skill.SkillID || item.VersionID != skill.VersionID || item.ContentHash != skill.ContentHash || !strings.Contains(prompt["instructions"], item.Instructions) {
			return fail, ErrConflict
		}
		version, e := s.Get(ctx, token, colSkillVersions, in.Scope, item.VersionID)
		if e != nil {
			return fail, e
		}
		evidence = append(evidence, Ref{Collection: colSkillVersions, ID: version.ID, Revision: version.Revision})
		resolved = append(resolved, item)
	}
	seen := map[Ref]bool{}
	checked := []Ref{}
	for at := 0; at < len(evidence); at++ {
		if len(evidence) > 128 {
			return fail, ErrInvalid
		}
		ref := evidence[at]
		if seen[ref] {
			continue
		}
		seen[ref] = true
		r, e := s.Get(ctx, token, ref.Collection, in.Scope, ref.ID)
		if e != nil {
			return fail, e
		}
		if r.Revision != ref.Revision || !live(r) || r.ContentErased {
			return fail, ErrConflict
		}
		if _, e = s.authorizeHandoffContent(ctx, token, in, r.ID, r.Sensitivity, recordContent(r)); e != nil {
			return fail, e
		}
		if e = addArchives(r.ArchiveMessages); e != nil {
			return fail, e
		}
		for _, source := range r.Sources {
			parent, e := s.Get(ctx, token, "sources", in.Scope, source.SourceID)
			if e != nil {
				return fail, e
			}
			next := Ref{Collection: "sources", ID: parent.ID, Revision: parent.Revision}
			if !seen[next] {
				evidence = append(evidence, next)
			}
		}
		checked = append(checked, ref)
		if len(checked) > workCheckpointMaxArray {
			return fail, ErrInvalid
		}
	}
	for _, header := range headers {
		if header.Sensitivity == "restricted" {
			return fail, ErrDenied
		}
	}
	// Every policy call is above this transaction, including with a one-connection
	// authorizer pool. Fence the same revisions and archive retention afterward.
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if e := validateWorkCheckpointSourceRefs(ctx, tx, in.Scope, checked); e != nil {
			return e
		}
		if e := s.fenceSourceArchives(ctx, tx, headers); e != nil {
			return e
		}
		current, e := tx.Get(ctx, colWorkCheckpoints, in.Scope, in.Work.ID)
		if e != nil {
			return e
		}
		if !isWorkCheckpoint(current) || !live(current) || current.ContentErased || current.Revision != in.Work.Revision || structuredText(current.WorkCheckpoint.Package) != structuredText(work.Package) || structuredText(current.WorkCheckpoint.ArchiveMessages) != structuredText(work.ArchiveMessages) {
			return ErrConflict
		}
		if e = fenceWorkCheckpoint(ctx, tx, in.Scope, current.ID, current.Revision); e != nil {
			return e
		}
		for _, item := range resolved {
			fresh, e := resolveSkillRef(ctx, searchReadTx{Tx: tx}, in.Scope, item.Ref)
			if e != nil {
				return e
			}
			if structuredText(fresh) != structuredText(item) {
				return ErrConflict
			}
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "transfer:model_input", ContentHash(in.Content))
	})
	if err != nil {
		return fail, err
	}
	return HandoffTransferOutput{Authorized: true, ContentHash: ContentHash(in.Content), SourceRefs: checked, ArchiveMessages: archiveRefs}, nil
}
func handoffTransferRoutes(mux *http.ServeMux, s *Service) {
	mux.HandleFunc("POST /v1/work-checkpoints/authorize-handoff", func(w http.ResponseWriter, r *http.Request) {
		var in HandoffTransferInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.AuthorizeHandoffTransfer(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
}
