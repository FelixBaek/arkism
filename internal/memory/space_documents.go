package memory

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// The "documents" part of a space bundle: each extracted document (an uploaded
// file or pasted text that went through file extraction) with its chunks' text,
// read back from the evidence sources the extraction stored. Only a person
// exports or restores it. A restore stores the chunks as new evidence sources
// and records the document as done without calling a model: its old candidates
// travel in the data part, and a person may reprocess the document later.

const (
	spaceSectionDocuments = "documents"
	spaceDocChunksMax     = 4096
)

// SpaceBundleDocument is one extracted document of a space bundle.
type SpaceBundleDocument struct {
	ID       string               `json:"id"`
	Ref      string               `json:"ref"`
	Label    string               `json:"label"`
	Tags     []string             `json:"tags,omitempty"`
	Settings ExtractStrategy      `json:"settings"`
	Options  ExtractOptions       `json:"options"`
	Zone     string               `json:"zone,omitempty"`
	Language string               `json:"language,omitempty"`
	Chunks   []SpaceDocumentChunk `json:"chunks"`
}

// SpaceDocumentChunk is one chunk's locator and text.
type SpaceDocumentChunk struct {
	Locator string `json:"locator"`
	Text    string `json:"text"`
}

func (s *Service) emitSpaceDocuments(ctx context.Context, d Decision, scope Scope, emit func(spaceBundleLine) error, report *SpaceReport) error {
	return eachCatalogRecord(ctx, s.Repo, scope, "jobs", func(r Record) error {
		if r.Kind != fileExtractDocKind {
			return nil
		}
		doc, why := s.spaceDocument(ctx, d, scope, r)
		if ctx.Err() != nil {
			return ErrUnavailable
		}
		if why != "" {
			report.skip(spaceSectionDocuments, why, 1)
			return nil
		}
		return emit(spaceBundleLine{Section: spaceSectionDocuments, Document: &doc})
	})
}

// spaceDocument reads the live chunks of one document back from its sources.
func (s *Service) spaceDocument(ctx context.Context, d Decision, scope Scope, doc Record) (SpaceBundleDocument, string) {
	if doc.Target == nil {
		return SpaceBundleDocument{}, "not_live"
	}
	last, err := s.extractJob(ctx, s.Repo, scope, doc.Target.ID)
	if err != nil {
		return SpaceBundleDocument{}, "not_live"
	}
	j := last.FileExtract
	if strings.HasPrefix(j.Ref, "knowledge:") {
		return SpaceBundleDocument{}, "knowledge_part"
	}
	chunks, err := s.extractDocState(ctx, scope, doc.ID)
	if err != nil || len(chunks) == 0 {
		return SpaceBundleDocument{}, "not_live"
	}
	for _, id := range extractSourceIDs(chunks) {
		src, err := s.Repo.Get(ctx, "sources", scope, id)
		switch {
		case err != nil || src.ContentErased || src.PrivacyQuarantined:
			return SpaceBundleDocument{}, "not_live"
		case src.Sensitivity == "restricted" || src.Sensitivity == "private" && src.CreatedBy != d.PrincipalID:
			return SpaceBundleDocument{}, "private"
		}
	}
	if chunks, err = s.storedChunks(ctx, scope, chunks); err != nil {
		return SpaceBundleDocument{}, "not_live"
	}
	out := SpaceBundleDocument{ID: doc.ID, Ref: j.Ref, Label: j.Label, Tags: doc.Tags, Settings: j.Settings, Options: j.Options, Zone: j.Zone, Language: j.Language,
		Chunks: make([]SpaceDocumentChunk, 0, len(chunks))}
	for _, ch := range chunks {
		out.Chunks = append(out.Chunks, SpaceDocumentChunk{Locator: ch.Locator, Text: ch.Text})
	}
	return out, ""
}

func validSpaceDocument(doc SpaceBundleDocument, now time.Time) bool {
	if !validRunLabel(doc.ID, 256) || doc.Ref == "" || len(doc.Ref) > 1024 || strings.HasPrefix(doc.Ref, "knowledge:") || doc.Label == "" || len(doc.Label) > 1024 ||
		containsSecret(doc.Ref+"\n"+doc.Label) || !validTags(doc.Tags) || validExtractStrategy(doc.Settings) != nil || !validExtractOptions(doc.Options, now) ||
		len(doc.Language) > 32 || len(doc.Chunks) == 0 || len(doc.Chunks) > spaceDocChunksMax {
		return false
	}
	if _, err := time.LoadLocation(doc.Zone); len(doc.Zone) > 64 || err != nil {
		return false
	}
	return validSpaceDocumentChunks(doc.Chunks)
}

func validSpaceDocumentChunks(chunks []SpaceDocumentChunk) bool {
	for i, ch := range chunks {
		if ch.Locator == "" || len(ch.Locator) > 256 || strings.ContainsAny(ch.Locator, "]\n") || strings.TrimSpace(ch.Text) == "" || !utf8.ValidString(ch.Text) ||
			slices.ContainsFunc(chunks[:i], func(o SpaceDocumentChunk) bool { return o.Locator == ch.Locator }) {
			return false
		}
	}
	return true
}

// addDocument restores one document under the importing person. Unchanged
// chunks of the same document are kept, so importing twice stores nothing new.
func (imp *spaceImporter) addDocument(ctx context.Context, doc SpaceBundleDocument) {
	if imp.d.Role != roleHuman {
		imp.report.skip(spaceSectionDocuments, "not_human", 1)
		return
	}
	merged, err := imp.s.restoreSpaceDocument(ctx, imp.token, imp.d, imp.scope, doc)
	switch {
	case err != nil:
		imp.report.skip(spaceSectionDocuments, captureErrorCode(err), 1)
	case merged:
		imp.report.Merged++
	default:
		imp.report.Imported++
	}
}

func (s *Service) restoreSpaceDocument(ctx context.Context, token string, d Decision, scope Scope, doc SpaceBundleDocument) (bool, error) {
	if !validSpaceDocument(doc, time.Now().UTC()) {
		return false, ErrInvalid
	}
	chunks := make([]FileExtractChunk, 0, len(doc.Chunks))
	for _, ch := range doc.Chunks {
		chunks = append(chunks, FileExtractChunk{Locator: ch.Locator, Text: ch.Text, Hash: ContentHash(extractSpaces(ch.Text))})
	}
	docID := extractDocID(scope, d.PrincipalID, doc.Ref)
	prev, err := s.extractDocState(ctx, scope, docID)
	if err != nil {
		return false, err
	}
	fresh, kept, delta := diffExtractChunks(prev, chunks)
	if len(fresh) == 0 && delta.ChunksRemoved == 0 {
		return true, nil
	}
	if len(fresh) > 0 {
		if err = s.storeExtractSources(ctx, token, scope, doc.Label, fresh); err != nil {
			return false, err
		}
	}
	stored := map[string]FileExtractChunk{}
	for _, ch := range append(kept, fresh...) {
		ch.Text = ""
		stored[ch.Hash] = ch
	}
	// Every chunk, in document order, is recorded as kept: the job is done at once and no model is called.
	all := make([]FileExtractChunk, 0, len(chunks))
	for _, ch := range chunks {
		k := stored[ch.Hash]
		k.Locator = ch.Locator
		all = append(all, k)
	}
	plan := FileExtractJob{Label: doc.Label, Document: docID, Ref: doc.Ref, Chunks: []FileExtractChunk{}, Kept: all, Delta: delta,
		Settings: doc.Settings, Options: doc.Options, Zone: doc.Zone, Language: doc.Language, Tags: slices.Clone(doc.Tags), Skipped: []ExtractSkip{}}
	_, err = s.createExtractJob(ctx, d, scope, plan)
	return false, err
}
