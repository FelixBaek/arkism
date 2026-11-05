package memory

import (
	"cmp"
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// A change proposal is an agent asking a person for a change it may not make
// itself: a knowledge folder or page, deleting a page, document or directive,
// refreshing, clearing or deleting a mental model, changing the space settings,
// clearing the space's memories, creating a space or deleting this one. It names its target (checked to exist, at
// the revision the agent saw) and the change's parameters. Deciding it changes
// nothing: accept records that a person agrees, who then makes the change
// through the ordinary route; dismiss records that they do not.
const changeType = "change"

const changeParamsMax = 16 << 10

// ChangeRequest is the change a "change" proposal asks for.
type ChangeRequest struct {
	Action   string          `json:"action"`
	Target   string          `json:"target,omitempty"`
	Revision int64           `json:"revision,omitempty"`
	Params   json.RawMessage `json:"params,omitempty"`
}

type changeSpec struct {
	collection string // the target's collection; "" means the change has no target
	read       string // the permission that shows the target to the proposer
	revision   bool   // the target revision the agent saw is required
	params     func(json.RawMessage) bool
}

var changeActions = map[string]changeSpec{
	"knowledge_folder_create": {params: validFolderParams},
	"knowledge_page_create":   {params: func(raw json.RawMessage) bool { return validPageParams(raw, true) }},
	"knowledge_page_edit":     {collection: colWikiPages, read: "read:" + colWikiPages, revision: true, params: func(raw json.RawMessage) bool { return validPageParams(raw, false) }},
	"knowledge_node_delete":   {collection: colWikiPages, read: "read:" + colWikiPages, revision: true},
	"document_delete":         {collection: colJobs, read: "read:memory_candidates"},
	"mental_model_refresh":    {collection: colMentalModels, read: "read:" + colMentalModels},
	"mental_model_clear":      {collection: colMentalModels, read: "read:" + colMentalModels},
	"mental_model_delete":     {collection: colMentalModels, read: "read:" + colMentalModels},
	"directive_delete":        {collection: colDirectives, read: "read:" + colDirectives, revision: true},
	"space_config_change":     {params: validSpaceConfigParams},
	"memories_clear":          {},
	// A new space (its first settings make it) or this whole space removed; a person does either through the space routes.
	"space_create": {params: validSpaceCreateParams},
	"space_delete": {},
}

func isChangeProposal(p MaintenanceProposal) bool { return p.Change != nil }

// validChange checks a change proposal's shape; the target is checked against the store later.
func validChange(p MaintenanceProposal) bool {
	c := p.Change
	spec, ok := changeActions[c.Action]
	if !ok || p.Type != changeType || p.Reason != c.Action || p.Memory != nil || p.Evidence != nil || p.Keep != (Ref{}) || p.Retire != (Ref{}) {
		return false
	}
	if (spec.collection != "") != (c.Target != "") || c.Target != "" && !validRunLabel(c.Target, 256) || spec.revision != (c.Revision > 0) || c.Revision < 0 {
		return false
	}
	if spec.params == nil || len(c.Params) == 0 {
		return spec.params == nil && len(c.Params) == 0
	}
	return len(c.Params) <= changeParamsMax && utf8.Valid(c.Params) && spec.params(c.Params)
}

func validFolderParams(raw json.RawMessage) bool {
	var v struct {
		Path string `json:"path"`
	}
	path, ok := "", decodeStrict(raw, &v) == nil
	if ok {
		path, ok = normalizeWikiFolder(v.Path)
	}
	return ok && path != ""
}

func validPageParams(raw json.RawMessage, create bool) bool {
	var v struct {
		Title  string  `json:"title"`
		Text   string  `json:"text"`
		Folder *string `json:"folder"`
	}
	if decodeStrict(raw, &v) != nil || utf8.RuneCountInString(v.Title) > 200 {
		return false
	}
	if v.Folder != nil {
		if _, ok := normalizeWikiFolder(*v.Folder); !ok {
			return false
		}
	}
	if create {
		return strings.TrimSpace(v.Title) != ""
	}
	return v.Title != "" || v.Text != "" || v.Folder != nil
}

// validSpaceConfigParams: the patch applies cleanly to the default settings.
func validSpaceConfigParams(raw json.RawMessage) bool {
	var in SpaceConfigInput
	c := defaultSpaceConfig()
	return decodeStrict(raw, &in) == nil && in.ExpectedRevision == 0 && applySpaceConfig(&c, in, false) == nil
}

// validSpaceCreateParams: {project_id, workspace_id?, config?}, a valid space and settings that apply cleanly.
func validSpaceCreateParams(raw json.RawMessage) bool {
	var v struct {
		WorkspaceID string            `json:"workspace_id"`
		ProjectID   string            `json:"project_id"`
		Config      *SpaceConfigInput `json:"config"`
	}
	if decodeStrict(raw, &v) != nil || v.ProjectID == "" || !validScope(Scope{WorkspaceID: cmp.Or(v.WorkspaceID, "workspace"), ProjectID: v.ProjectID}) {
		return false
	}
	c := defaultSpaceConfig()
	return v.Config == nil || v.Config.ExpectedRevision == 0 && applySpaceConfig(&c, *v.Config, false) == nil
}

// checkChangeText refuses secrets and private spans in what the proposal stores.
func checkChangeText(c *ChangeRequest) error {
	text := c.Target + string(c.Params)
	if containsSecret(text) {
		return ErrSecretDetected
	}
	if hasPrivateMarker(text) {
		return ErrInvalid
	}
	return nil
}

// authorizeChangeTarget: the proposer may read the target's collection.
func (s *Service) authorizeChangeTarget(ctx context.Context, token string, scope Scope, c *ChangeRequest) error {
	spec := changeActions[c.Action]
	if spec.collection == "" {
		return nil
	}
	_, err := s.authorize(ctx, token, spec.read, scope, c.Target, "")
	return err
}

// checkChangeTarget: the target exists and, where asked, is at the revision the agent saw.
func checkChangeTarget(ctx context.Context, tx Tx, scope Scope, c *ChangeRequest) error {
	spec := changeActions[c.Action]
	if spec.collection == "" {
		return nil
	}
	r, err := tx.Get(ctx, spec.collection, scope, c.Target)
	if err == nil && spec.collection == colJobs && r.Kind != fileExtractDocKind {
		err = ErrNotFound
	}
	if err == nil && spec.revision && r.Revision != c.Revision {
		err = ErrConflict
	}
	return err
}

// changeProposalID is one record per action, target and parameters.
func changeProposalID(scope Scope, c *ChangeRequest) string {
	return pairID(scope, "\x00change:"+c.Action, c.Target+"\x00"+ContentHash(string(c.Params)))
}
