package memory

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Space rename (memoryd --space-rename): every row of one project moves to a
// new project id of the same workspace in one transaction (space_rename_postgres.go).
// A project id lives in the scope columns that row-level security reads, in
// each stored document's scope objects, and inside ids the service derives from
// the scope. Those ids are recomputed when their formula is known and the row
// proves it (wiki pages, folders and links, artifacts, memory signals), or
// when the id carries the scope hash itself (per-space settings rows and
// queues, privacy deletion requests with their request hash and report
// reference); every reference to a remapped id in any document of the space,
// and the evidence id of a graph relation, is rewritten with it. Ids and
// request hashes hashed from the scope with inputs the row does not keep keep
// their old value: knowledge sources and bindings (source and binding key),
// source receipts and consultations (idempotency key), and the request hash of
// every other idempotent write (the whole request body). They still read and
// resolve by id; only a repeat of the original request under the new name
// would not replay them. Stored files are keyed by digest, so nothing moves in
// the file store.

// SpaceRenameReport counts what a rename moves; a preview counts the same rows and changes nothing.
type SpaceRenameReport struct {
	From        Scope            `json:"from"`
	To          Scope            `json:"to"`
	Executed    bool             `json:"executed"`
	SubScopes   int              `json:"sub_scopes"`
	Records     map[string]int64 `json:"records"`
	Tables      map[string]int64 `json:"tables"`
	IDsRemapped int              `json:"ids_remapped"`
}

type spaceRenamer struct {
	ws, from, to string
	ids          map[string]string
	// texts maps stored strings that are not row ids (request hashes, report references).
	texts map[string]string
	// hashes holds the old and new scope hash of each sub-scope.
	hashes [][2]string
}

func newSpaceRenamer(from, to Scope, subs []Scope) *spaceRenamer {
	m := &spaceRenamer{ws: from.WorkspaceID, from: from.ProjectID, to: to.ProjectID, ids: map[string]string{}, texts: map[string]string{}}
	for _, s := range subs {
		m.hashes = append(m.hashes, [2]string{ContentHash(structuredText(s)), ContentHash(structuredText(m.moved(s)))})
	}
	return m
}

func (m *spaceRenamer) moved(s Scope) Scope {
	if s.WorkspaceID == m.ws && s.ProjectID == m.from {
		s.ProjectID = m.to
	}
	return s
}

func (m *spaceRenamer) id(old string) string {
	if v, ok := m.ids[old]; ok {
		return v
	}
	if v, ok := m.texts[old]; ok {
		return v
	}
	return old
}

// scopeHashID maps an id that carries a sub-scope hash: whole (singletons,
// queue entries) or its first 32 or 16 characters as the suffix.
func (m *spaceRenamer) scopeHashID(id string) string {
	for _, h := range m.hashes {
		if strings.Contains(id, h[0]) {
			return strings.ReplaceAll(id, h[0], h[1])
		}
		for _, n := range []int{32, 16} {
			if base, ok := strings.CutSuffix(id, h[0][:n]); ok {
				return base + h[1][:n]
			}
		}
	}
	return id
}

// plan maps the row's own id. Wiki links name page ids in their formula, so
// they are planned after every page (planLink).
func (m *spaceRenamer) plan(col string, r Record) {
	old, scope := r.Scope, m.moved(r.Scope)
	id := m.scopeHashID(r.ID)
	switch {
	case col == colWikiPages && r.Wiki != nil && r.Wiki.Page != nil && r.ID == wikiPageID(old, r.Wiki.Page.Slug):
		id = wikiPageID(scope, r.Wiki.Page.Slug)
	case col == colWikiPages && r.Wiki != nil && r.Wiki.Folder != nil && r.ID == wikiFolderID(old, r.Wiki.Folder.Path):
		id = wikiFolderID(scope, r.Wiki.Folder.Path)
	case col == colArtifacts && validArtifactRecord(r):
		id = artifactID(scope, r.Target.ID, r.Artifact.Digest)
	case col == colJobs && r.MemorySignal != nil && r.ID == memorySignalID(old, r.MemorySignal.Memory.ID):
		id = memorySignalID(scope, r.MemorySignal.Memory.ID)
	case col == colJobs && r.Kind == privacyDeletionKind && r.PrivacyDeletion != nil && r.ID == privacyJobID(old, r.PrivacyDeletion.RequesterID, r.PrivacyDeletion.IdempotencyKey):
		id = privacyJobID(scope, r.PrivacyDeletion.RequesterID, r.PrivacyDeletion.IdempotencyKey)
		m.planPrivacy(r, scope, id)
	}
	if id != r.ID {
		m.ids[r.ID] = id
	}
}

// planPrivacy maps the report reference and, when the row's own fields
// reproduce its request hash, the hash under the new scope; a request that
// carried an expected revision the row does not keep keeps its old hash.
func (m *spaceRenamer) planPrivacy(r Record, scope Scope, id string) {
	pd := r.PrivacyDeletion
	m.texts["jobs/"+r.ID+"#privacy_deletion.report"] = "jobs/" + id + "#privacy_deletion.report"
	for _, hold := range []string{"", privacyHoldNone} {
		req := PrivacyDeletionRequest{Scope: r.Scope, IdempotencyKey: pd.IdempotencyKey, TargetType: pd.TargetType, TargetIDs: pd.TargetIDs, Reason: pd.Reason, RetentionPolicyRef: pd.RetentionPolicyRef, LegalHoldStatus: hold}
		if ContentHash(structuredText(req)) == pd.RequestHash {
			req.Scope = scope
			m.texts[pd.RequestHash] = ContentHash(structuredText(req))
			return
		}
	}
}

func (m *spaceRenamer) planLink(r Record) {
	if l := r.Wiki.Link; r.ID == wikiLinkID(r.Scope, l.FromID, l.ToID) {
		m.ids[r.ID] = wikiLinkID(m.moved(r.Scope), m.id(l.FromID), m.id(l.ToID))
	}
}

// rewrite moves the scope objects of a stored document and replaces every
// remapped id, as a value or as a map key. Numbers keep their text.
func (m *spaceRenamer) rewrite(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, ErrInvalid
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m.walk(v)); err != nil {
		return nil, ErrInvalid
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

func (m *spaceRenamer) walk(v any) any {
	switch x := v.(type) {
	case string:
		return m.id(x)
	case []any:
		for i := range x {
			x[i] = m.walk(x[i])
		}
	case map[string]any:
		if x["workspace_id"] == m.ws && x["project_id"] == m.from {
			x["project_id"] = m.to
		}
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[m.id(k)] = m.walk(val)
		}
		return out
	}
	return v
}
