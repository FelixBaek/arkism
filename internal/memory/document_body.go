package memory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// A document's own text: read it, edit it (the changed chunks are extracted again
// and become candidates for a person to review), and move documents between spaces
// as a zip of documents/<id>.json files, each one SpaceBundleDocument. Only a person
// does any of these.

const docZipMaxBytes = 8 << 20

// DocumentTextView is a document's live text, its chunks joined in order.
type DocumentTextView struct {
	DocumentID string `json:"document_id"`
	Label      string `json:"label"`
	Text       string `json:"text"`
	Chunks     int    `json:"chunks"`
}

// DocumentTextInput replaces a document's text (at most 512 KiB).
type DocumentTextInput struct {
	Scope Scope  `json:"scope"`
	Text  string `json:"text"`
}

// DocumentsExportInput picks the documents of one space to export (1..200).
type DocumentsExportInput struct {
	Scope       Scope    `json:"scope"`
	DocumentIDs []string `json:"document_ids"`
}

// DocumentsImportInput restores the documents of a zip ExportDocuments wrote.
type DocumentsImportInput struct {
	Scope     Scope  `json:"scope"`
	ZipBase64 string `json:"zip_base64"`
	// Conflict: replace (default), skip or new_id, as SpaceImportInput.DocumentConflict.
	Conflict string `json:"conflict,omitempty"`
}

// docUnavailable turns a spaceDocument skip reason into an error.
func docUnavailable(why string) error {
	if why == "private" {
		return ErrDenied
	}
	return ErrConflict // knowledge part, erased or not kept (store_document_text off)
}

// DocumentText reads a document's live text back from its evidence sources.
func (s *Service) DocumentText(ctx context.Context, token string, scope Scope, id string) (DocumentTextView, error) {
	d, err := s.wikiAuthorize(ctx, token, "read:memory_candidates", scope, id, "", roleHuman)
	if err != nil {
		return DocumentTextView{}, err
	}
	doc, err := s.extractDocument(ctx, token, scope, id)
	if err != nil {
		return DocumentTextView{}, err
	}
	got, why := s.spaceDocument(ctx, d, scope, doc)
	if why != "" {
		return DocumentTextView{}, docUnavailable(why)
	}
	parts := make([]string, 0, len(got.Chunks))
	for _, ch := range got.Chunks {
		parts = append(parts, ch.Text)
	}
	return DocumentTextView{DocumentID: id, Label: got.Label, Text: strings.Join(parts, "\n\n"), Chunks: len(parts)}, nil
}

// EditDocumentText replaces the text of a document its uploader added and extracts
// the chunks that changed; unchanged chunks keep their candidates. Human only.
func (s *Service) EditDocumentText(ctx context.Context, token, id string, in DocumentTextInput) (FileMemoriesResult, error) {
	if !validScope(in.Scope) || strings.TrimSpace(in.Text) == "" || len(in.Text) > fileExtractMaxText || !utf8.ValidString(in.Text) || strings.ContainsRune(in.Text, 0) {
		return FileMemoriesResult{}, ErrInvalid
	}
	d, settings, err := s.extractActor(ctx, token, "write:memory_candidates", in.Scope, roleHuman)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	doc, err := s.extractDocument(ctx, token, in.Scope, id)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	job, err := s.documentJob(ctx, in.Scope, doc)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	j := job.FileExtract
	switch {
	case j.RequestedBy != d.PrincipalID:
		return FileMemoriesResult{}, ErrDenied
	case job.Status != JobStatusDone || strings.HasPrefix(j.Ref, "knowledge:"):
		return FileMemoriesResult{}, ErrConflict // a running job, or a knowledge file edited in Knowledge
	}
	plan := FileExtractJob{Label: j.Label, Settings: j.Settings, Options: j.Options, Zone: j.Zone, Language: j.Language, DropText: !settings.keepsDocumentText()}
	text := in.Text
	if err = s.defendItem(ctx, in.Scope, "write:memory_candidates", &plan.Options, &text); err != nil {
		return FileMemoriesResult{}, err
	}
	chunks, redacted, err := extractFileChunks([]string{text}, false, plan.Settings)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	return s.extractStored(ctx, token, d, in.Scope, plan, j.Ref, "", chunks, FileMemoriesResult{Skipped: extractRedactionNote(redacted)}, fileExtractSyncChunks, false)
}

// ExportDocuments zips the picked documents; one the reader may not take is left out
// and named in the zip's skipped.json.
func (s *Service) ExportDocuments(ctx context.Context, token string, in DocumentsExportInput) (SkillZipExport, error) {
	if !validScope(in.Scope) || len(in.DocumentIDs) == 0 || len(in.DocumentIDs) > docPickMax || !validDocIDs(in.DocumentIDs) {
		return SkillZipExport{}, ErrInvalid
	}
	d, err := s.authorizeSpaceExport(ctx, token, in.Scope, "")
	if err != nil {
		return SkillZipExport{}, err
	}
	if d.Role != roleHuman {
		return SkillZipExport{}, ErrDenied
	}
	now, size, report := time.Now().UTC(), 0, SpaceReport{Skipped: []SpaceSkip{}}
	files := []archiveFile{}
	for _, id := range in.DocumentIDs {
		doc, err := s.Repo.Get(ctx, "jobs", in.Scope, id)
		if err != nil || doc.Kind != fileExtractDocKind {
			report.skip(spaceSectionDocuments, "not_found", 1)
			continue
		}
		got, why := s.spaceDocument(ctx, d, in.Scope, doc)
		if why != "" {
			report.skip(spaceSectionDocuments, why, 1)
			continue
		}
		b, err := json.Marshal(got)
		if err != nil {
			return SkillZipExport{}, ErrUnavailable
		}
		if size += len(b); size > docZipMaxBytes {
			return SkillZipExport{}, errBlobTooLarge
		}
		files = append(files, archiveFile{name: "documents/" + id + ".json", body: b, at: now})
	}
	if len(files) == 0 {
		return SkillZipExport{}, ErrNotFound
	}
	skipped, _ := json.Marshal(report.Skipped)
	data, err := zipArchive(append(files, archiveFile{name: "skipped.json", body: skipped, at: now}))
	if err != nil {
		return SkillZipExport{}, ErrUnavailable
	}
	return SkillZipExport{Filename: "documents-" + now.Format("20060102-150405") + ".zip", ContentBase64: base64.StdEncoding.EncodeToString(data)}, nil
}

// ImportDocuments restores every documents/*.json of the zip into the space, as the
// documents part of a space import does; the report counts what happened.
func (s *Service) ImportDocuments(ctx context.Context, token string, in DocumentsImportInput) (SpaceReport, error) {
	if !validScope(in.Scope) || !validDocConflict(in.Conflict) || in.ZipBase64 == "" || base64.StdEncoding.DecodedLen(len(in.ZipBase64)) > docZipMaxBytes {
		return SpaceReport{}, ErrInvalid
	}
	d, err := s.mergeHuman(ctx, token, "write:memory_candidates", in.Scope, "")
	if err != nil {
		return SpaceReport{}, err
	}
	docs, err := readDocumentZip(in.ZipBase64)
	if err != nil {
		return SpaceReport{}, err
	}
	imp := &spaceImporter{s: s, token: token, d: d, scope: in.Scope, conflict: in.Conflict, report: SpaceReport{Skipped: []SpaceSkip{}}}
	for _, doc := range docs {
		imp.addDocument(ctx, doc)
	}
	return imp.report, nil
}

// readDocumentZip reads the documents/*.json entries of a documents zip (at most 200,
// 8 MiB unpacked) in name order; any other entry is ignored and a malformed one refuses the zip.
func readDocumentZip(b64 string) ([]SpaceBundleDocument, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, ErrInvalid
	}
	parts, err := zipParts(raw, func(name string) bool {
		dir, base := path.Split(name)
		return dir == "documents/" && path.Ext(base) == ".json"
	}, docZipMaxBytes)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 || len(parts) > docPickMax {
		return nil, ErrInvalid
	}
	out := make([]SpaceBundleDocument, 0, len(parts))
	for _, name := range slices.Sorted(maps.Keys(parts)) {
		var doc SpaceBundleDocument
		if json.Unmarshal(parts[name], &doc) != nil {
			return nil, ErrInvalid
		}
		out = append(out, doc)
	}
	return out, nil
}
