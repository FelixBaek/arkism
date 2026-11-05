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

// docPickMax bounds a document pick (an export subset, a documents zip).
const docPickMax = 200

// validDocumentPick: a pick names distinct documents and needs the documents part.
func validDocumentPick(in SpaceExportInput) bool {
	if len(in.DocumentIDs) == 0 {
		return true
	}
	return len(in.DocumentIDs) <= docPickMax && slices.Contains(spaceSections(in), spaceSectionDocuments) && validDocIDs(in.DocumentIDs)
}

func validDocIDs(ids []string) bool {
	for i, id := range ids {
		if !validRunLabel(id, 256) || slices.Contains(ids[:i], id) {
			return false
		}
	}
	return true
}

// emitSpaceDocuments emits the space's documents, or only those in pick when it is set.
func (s *Service) emitSpaceDocuments(ctx context.Context, d Decision, scope Scope, pick []string, emit func(spaceBundleLine) error, report *SpaceReport) error {
	return eachCatalogRecord(ctx, s.Repo, scope, "jobs", func(r Record) error {
		if r.Kind != fileExtractDocKind || len(pick) > 0 && !slices.Contains(pick, r.ID) {
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
	got, err := imp.s.restoreSpaceDocument(ctx, imp.token, imp.d, imp.scope, doc, imp.conflict)
	switch {
	case err != nil:
		imp.report.skip(spaceSectionDocuments, captureErrorCode(err), 1)
	case got == docRestoreSkipped:
		imp.report.skip(spaceSectionDocuments, "exists", 1)
	case got == docRestoreMerged:
		imp.report.Merged++
	default:
		imp.report.Imported++
	}
}

// Document import conflict policies: what a bundle document does when the importer
// already has a document of the same name. replace (the default) brings it up to the
// bundle's text (unchanged chunks are kept); skip leaves it; new_id stores a copy beside it.
const (
	docConflictReplace = "replace"
	docConflictSkip    = "skip"
	docConflictNewID   = "new_id"
	docRestoreImported = "imported"
	docRestoreMerged   = "merged"
	docRestoreSkipped  = "skipped"
)

func validDocConflict(v string) bool {
	return v == "" || v == docConflictReplace || v == docConflictSkip || v == docConflictNewID
}

// restoreSpaceDocument stores one bundle document under conflict (validDocConflict) and says
// whether it was imported, merged (nothing changed) or skipped. The text is kept as evidence
// unless the space's store_document_text is off.
func (s *Service) restoreSpaceDocument(ctx context.Context, token string, d Decision, scope Scope, doc SpaceBundleDocument, conflict string) (string, error) {
	if !validSpaceDocument(doc, time.Now().UTC()) || !validDocConflict(conflict) {
		return "", ErrInvalid
	}
	settings, err := s.spaceSettings(ctx, scope)
	if err != nil {
		return "", err
	}
	chunks := make([]FileExtractChunk, 0, len(doc.Chunks))
	for _, ch := range doc.Chunks {
		chunks = append(chunks, FileExtractChunk{Locator: ch.Locator, Text: ch.Text, Hash: ContentHash(extractSpaces(ch.Text))})
	}
	docID := extractDocID(scope, d.PrincipalID, doc.Ref)
	prev, err := s.extractDocState(ctx, scope, docID)
	if err != nil {
		return "", err
	}
	if len(prev) > 0 && conflict == docConflictSkip {
		return docRestoreSkipped, nil
	}
	if len(prev) > 0 && conflict == docConflictNewID {
		// The reference names the document, so a new reference makes a separate copy.
		doc.Ref = runeCutString(doc.Ref, 1000) + "#copy-" + ID()[:8]
		docID, prev = extractDocID(scope, d.PrincipalID, doc.Ref), nil
	}
	fresh, kept, delta := diffExtractChunks(prev, chunks)
	if len(fresh) == 0 && delta.ChunksRemoved == 0 {
		return docRestoreMerged, nil
	}
	if len(fresh) > 0 {
		if err = s.storeExtractSources(ctx, token, scope, doc.Label, fresh, !settings.keepsDocumentText()); err != nil {
			return "", err
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
	if _, err = s.createExtractJob(ctx, d, scope, plan); err != nil {
		return "", err
	}
	return docRestoreImported, nil
}
