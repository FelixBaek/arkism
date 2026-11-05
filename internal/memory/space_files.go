package memory

import (
	"context"
	"encoding/base64"
	"path"
	"slices"
	"strings"
	"time"
)

// Knowledge files and source attachments in a space bundle (parts "knowledge"
// and "attachments"). They live in the space's user and agent sub-scopes, so
// these two parts are the only ones that read past the project-level scope;
// each line names its sub-scope and an import or clone restores it there.
// Only a person exports or restores them. Knowledge is private to the person
// who supplied it, so only that person's files leave; an attachment marked
// private leaves only for its creator and a restricted one never does. A
// restored knowledge file arrives as a draft source, an attachment as a source
// with its original bytes, through the same ingest paths a person uses.

const (
	spaceSectionKnowledge   = "knowledge"
	spaceSectionAttachments = "attachments"
	// spaceSubScopeMax bounds the sub-scopes one space may have for these parts;
	// more is refused rather than cut, so a bundle never silently misses files.
	spaceSubScopeMax = 1000
)

var spaceFiles = []string{spaceSectionKnowledge, spaceSectionAttachments}

// SpaceBundleFile is one knowledge file or one attachment of a space bundle.
type SpaceBundleFile struct {
	UserID  string `json:"user_id,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
	// ID is the original knowledge source or source id; a restore keys on it, so importing twice stores one copy.
	ID       string `json:"id"`
	Filename string `json:"filename,omitempty"`
	MIME     string `json:"mime"`
	// Content is a knowledge file's text; ContentBase64 an attachment's bytes.
	Content       string `json:"content,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
	// Source fields of an attachment.
	Kind          string     `json:"kind,omitempty"`
	ArtifactKind  string     `json:"artifact_kind,omitempty"`
	Text          string     `json:"text,omitempty"`
	Summary       string     `json:"summary,omitempty"`
	Keywords      []string   `json:"keywords,omitempty"`
	Tags          []string   `json:"tags,omitempty"`
	Sensitivity   string     `json:"sensitivity,omitempty"`
	OriginProduct string     `json:"origin_product,omitempty"`
	SourceRef     string     `json:"source_ref,omitempty"`
	CollectedAt   *time.Time `json:"collected_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// spaceSubScopes lists the scopes of one space that hold rows of collection.
func (s *Service) spaceSubScopes(ctx context.Context, scope Scope, collection string) ([]Scope, error) {
	store, ok := s.Repo.(spaceStore)
	if !ok {
		return nil, ErrUnavailable
	}
	subs, err := store.SpaceSubScopes(ctx, scope, []string{collection}, spaceSubScopeMax+1)
	if err == nil && len(subs) > spaceSubScopeMax {
		err = ErrUnavailable
	}
	return subs, err
}

func (s *Service) emitSpaceFiles(ctx context.Context, token string, d Decision, scope Scope, name string, emit func(spaceBundleLine) error, report *SpaceReport) error {
	col, each := colKnowledgeSources, s.spaceKnowledgeFile
	if name == spaceSectionAttachments {
		col, each = colArtifacts, s.spaceAttachment
	}
	subs, err := s.spaceSubScopes(ctx, scope, col)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		err = eachCatalogRecord(ctx, s.Repo, sub, col, func(r Record) error {
			file, why := each(ctx, token, d, r)
			if ctx.Err() != nil {
				return ErrUnavailable
			}
			if why != "" {
				report.skip(name, why, 1)
				return nil
			}
			file.UserID, file.AgentID, file.CreatedAt = sub.UserID, sub.AgentID, r.CreatedAt
			return emit(spaceBundleLine{Section: name, File: &file})
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// spaceKnowledgeFile reads the current document text of one knowledge source the reader supplied.
func (s *Service) spaceKnowledgeFile(ctx context.Context, token string, d Decision, r Record) (SpaceBundleFile, string) {
	switch {
	case r.Knowledge == nil || r.Knowledge.DocumentID == "" || r.PrivacyQuarantined || r.ContentErased || r.Status == "deleted" || r.Status == "quarantined" || r.Status == "failed":
		return SpaceBundleFile{}, "not_live"
	case r.CreatedBy != d.PrincipalID:
		return SpaceBundleFile{}, "private"
	}
	doc, err := s.Repo.Get(ctx, colKnowledgeDocuments, r.Scope, r.Knowledge.DocumentID)
	if err != nil || doc.Knowledge == nil {
		return SpaceBundleFile{}, "not_live"
	}
	text, err := s.ReadArtifact(ctx, token, r.Scope, r.SourceEventID, doc.Knowledge.StorageRef)
	if err != nil {
		return SpaceBundleFile{}, captureErrorCode(err)
	}
	name, mime := doc.Knowledge.OriginalFilename, doc.Knowledge.MIMEType
	// The store keeps a converted file's text, not its bytes; that text reads back as Markdown.
	if f := documentFormats[strings.ToLower(path.Ext(name))]; f.read != nil || f.mime == "" {
		name, mime = name+".md", documentFormats[".md"].mime
	}
	return SpaceBundleFile{ID: r.ID, Filename: name, MIME: mime, Content: string(text)}, ""
}

// spaceAttachment reads one text attachment of a source with the source's fields.
func (s *Service) spaceAttachment(ctx context.Context, token string, d Decision, a Record) (SpaceBundleFile, string) {
	if !validArtifactRecord(a) {
		return SpaceBundleFile{}, "not_live"
	}
	src, err := s.Repo.Get(ctx, "sources", a.Scope, a.Target.ID)
	switch {
	case err != nil || !live(src) || src.ContentErased:
		return SpaceBundleFile{}, "not_live"
	case src.OriginProduct == "synthetic-knowledge":
		return SpaceBundleFile{}, "knowledge_part"
	case src.Sensitivity == "restricted" || src.Sensitivity == "private" && src.CreatedBy != d.PrincipalID:
		return SpaceBundleFile{}, "private"
	case a.Artifact.Bytes > maxTextArtifactBytes || a.Artifact.MIME != "text/plain" && a.Artifact.MIME != "application/json":
		return SpaceBundleFile{}, "unsupported_file"
	}
	content, err := s.ReadArtifact(ctx, token, a.Scope, src.ID, a.ID)
	if err != nil {
		return SpaceBundleFile{}, captureErrorCode(err)
	}
	return SpaceBundleFile{ID: src.ID, MIME: a.Artifact.MIME, ContentBase64: base64.StdEncoding.EncodeToString(content),
		Kind: src.Kind, ArtifactKind: a.Kind, Text: src.Text, Summary: src.Summary, Keywords: src.Keywords, Tags: src.Tags,
		Sensitivity: src.Sensitivity, OriginProduct: src.OriginProduct, SourceRef: src.SourceRef, CollectedAt: src.CollectedAt}, ""
}

// addFile restores one knowledge file or attachment into the same sub-scope of the target space.
func (imp *spaceImporter) addFile(ctx context.Context, section string, f SpaceBundleFile) {
	if imp.d.Role != roleHuman {
		imp.report.skip(section, "not_human", 1)
		return
	}
	scope := Scope{WorkspaceID: imp.scope.WorkspaceID, ProjectID: imp.scope.ProjectID, UserID: f.UserID, AgentID: f.AgentID}
	var duplicate bool
	var err error
	if section == spaceSectionKnowledge {
		var out KnowledgeIngestResult
		out, err = imp.s.IngestKnowledge(ctx, imp.token, KnowledgeIngestInput{Scope: scope, SourceKey: f.ID, Filename: f.Filename, MIMEType: f.MIME, Content: f.Content})
		duplicate = out.Duplicate
	} else {
		err = imp.s.restoreSpaceAttachment(ctx, imp.token, scope, f)
	}
	switch {
	case err != nil:
		imp.report.skip(section, captureErrorCode(err), 1)
	case duplicate:
		imp.report.Merged++
	default:
		imp.report.Imported++
	}
}

// restoreSpaceAttachment stores the attachment through IngestFile. The
// idempotency key comes from the original source id and digest, so a second
// import of the same file returns the first copy.
func (s *Service) restoreSpaceAttachment(ctx context.Context, token string, scope Scope, f SpaceBundleFile) error {
	content, err := base64.StdEncoding.DecodeString(f.ContentBase64)
	if err != nil || len(content) == 0 {
		return ErrInvalid
	}
	digest := ContentHash(string(content))
	in := FileIngestInput{
		Source: CreateInput{Scope: scope, Kind: f.Kind, Text: f.Text, Summary: f.Summary, Keywords: slices.Clone(f.Keywords), Tags: slices.Clone(f.Tags),
			Sensitivity: f.Sensitivity, OriginProduct: f.OriginProduct, SourceRef: f.SourceRef, CollectedAt: f.CollectedAt,
			IdempotencyKey: "space-file-" + ContentHash(f.ID + "\n" + digest)[:48]},
		Artifact: FileArtifactSpec{Kind: f.ArtifactKind, MIME: f.MIME, Digest: digest},
	}
	_, err = s.IngestFile(ctx, token, in, content)
	return err
}
