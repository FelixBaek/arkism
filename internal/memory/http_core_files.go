package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// FileBatchInput is the leading metadata part of a batch upload. Files gives
// optional per-file settings in the order the file parts follow.
type FileBatchInput struct {
	Scope   Scope           `json:"scope"`
	DryRun  bool            `json:"dry_run"`
	Parsers []string        `json:"parsers,omitempty"`
	Files   []FileBatchItem `json:"files,omitempty"`
	// Context, Timestamp, Tags and Strategy apply to every file, as on POST /v1/files/memories.
	Context   string     `json:"context,omitempty"`
	Timestamp *time.Time `json:"timestamp,omitempty"`
	Tags      []string   `json:"tags,omitempty"`
	Strategy  string     `json:"strategy,omitempty"`
}

type FileBatchItem struct {
	MIME    string   `json:"mime,omitempty"`
	Parsers []string `json:"parsers,omitempty"`
}

// FileBatchResult is one file's outcome; a file that failed names its error code.
type FileBatchResult struct {
	Filename string              `json:"filename"`
	Result   *FileMemoriesResult `json:"result,omitempty"`
	Error    string              `json:"error,omitempty"`
}

type FileBatchOutput struct {
	Files []FileBatchResult `json:"files"`
}

func registerFilesCoreHTTP(mux routeRegistrar, s *Service, _ *requestMetrics) {
	mux.HandleFunc("POST /v1/files/memories/batch", s.extractBatchHTTP)
	mux.HandleFunc("GET /v1/files/memories/documents", func(w http.ResponseWriter, r *http.Request) {
		scope, page, e := parseDocumentQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.ListExtractDocuments(r.Context(), bearer(r), scope, page)
		respond(w, out, e)
	})
	mux.HandleFunc("PATCH /v1/files/memories/documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in DocumentTagsInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.EditDocumentTags(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("DELETE /v1/files/memories/documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in WikiScopeInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.DeleteExtractDocument(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/files/memories/documents/{id}/reprocess", func(w http.ResponseWriter, r *http.Request) {
		var in WikiScopeInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ReprocessDocument(r.Context(), bearer(r), r.PathValue("id"), in)
		s.startExtractJob(r, in.Scope, out, e)
		respond(w, out, e)
	})
}

// batchRequestBytes bounds a whole batch request: the files plus the metadata
// part and the multipart framing.
func (s *Service) batchRequestBytes() int64 {
	return s.Files.batchBytes() + maxFileMetadataBytes + fileRequestJSONOverhead
}

// extractBatchHTTP reads a multipart batch: the metadata part first, the
// person is authorized before any file byte is read, then each "file" part is
// read (at most the per-file limit) and extracted before the next one, so the
// server holds one file at a time.
func (s *Service) extractBatchHTTP(w http.ResponseWriter, r *http.Request) {
	limit := s.batchRequestBytes()
	if r.ContentLength > limit {
		respond(w, nil, errBlobTooLarge)
		return
	}
	if r.Header.Get("Content-Encoding") != "" {
		respond(w, nil, ErrInvalid)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	m, err := r.MultipartReader()
	if err != nil {
		respond(w, nil, ErrInvalid)
		return
	}
	in, err := fileBatchMetadata(m)
	if err != nil {
		respond(w, nil, err)
		return
	}
	out, err := s.ExtractFileBatch(r.Context(), bearer(r), in, multipartFiles(m, s.Files.maxBytes()))
	for _, f := range out.Files {
		if f.Result != nil {
			s.startExtractJob(r, in.Scope, *f.Result, nil)
		}
	}
	respond(w, out, err)
}

func fileBatchMetadata(m *multipart.Reader) (FileBatchInput, error) {
	part, err := m.NextRawPart()
	if err != nil || part.FormName() != "metadata" || part.Header.Get("Content-Transfer-Encoding") != "" {
		return FileBatchInput{}, fileReadError(err)
	}
	raw, err := filePartBytes(part, maxFileMetadataBytes)
	if err != nil {
		return FileBatchInput{}, err
	}
	var in FileBatchInput
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || !validScope(in.Scope) {
		return FileBatchInput{}, ErrInvalid
	}
	return in, nil
}

// batchFile is one file of a batch; next returns io.EOF after the last one.
type batchFile struct {
	Name string
	MIME string
	Raw  []byte
}

func multipartFiles(m *multipart.Reader, perFile int64) func() (batchFile, error) {
	return func() (batchFile, error) {
		part, err := m.NextRawPart()
		if err == io.EOF {
			return batchFile{}, io.EOF
		}
		if err != nil || part.FormName() != "file" || part.Header.Get("Content-Transfer-Encoding") != "" {
			return batchFile{}, fileReadError(err)
		}
		raw, err := filePartBytes(part, perFile)
		if err != nil {
			return batchFile{}, err
		}
		return batchFile{Name: part.FileName(), Raw: raw}, nil
	}
}

// ExtractFileBatch extracts up to the batch file limit one after another. A
// file that fails is reported and the rest go on; going over the batch byte
// or file limit, or a refusal, stops the batch.
func (s *Service) ExtractFileBatch(ctx context.Context, token string, in FileBatchInput, next func() (batchFile, error)) (FileBatchOutput, error) {
	out := FileBatchOutput{Files: []FileBatchResult{}}
	opts := ExtractOptions{Context: in.Context, Timestamp: in.Timestamp, Tags: in.Tags, Strategy: in.Strategy}
	if !validScope(in.Scope) || len(in.Files) > s.Files.batchFiles() || !validExtractOptions(opts, time.Now()) {
		return out, ErrInvalid
	}
	d, settings, err := s.extractActor(ctx, token, "write:memory_candidates", in.Scope, roleHuman, roleAgent)
	if err != nil {
		return out, err
	}
	plan, err := s.extractPlan(settings, opts, nil)
	if err != nil {
		return out, err
	}
	var total int64
	for i := 0; ; i++ {
		file, err := next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, err
		}
		if total += int64(len(file.Raw)); i >= s.Files.batchFiles() || total > s.Files.batchBytes() {
			return out, errBlobTooLarge
		}
		result, err := s.extractBatchFile(ctx, token, d, plan, in, in.item(i), file)
		if errors.Is(err, ErrDenied) || errors.Is(err, ErrModelBudget) {
			return out, err
		}
		out.Files = append(out.Files, batchResult(file.Name, result, err))
	}
	if len(out.Files) == 0 {
		return out, ErrInvalid
	}
	return out, nil
}

// item is the i-th file's MIME type and parsers; a file without its own parsers uses the batch's.
func (in FileBatchInput) item(i int) FileBatchItem {
	item := FileBatchItem{Parsers: in.Parsers}
	if i < len(in.Files) {
		item.MIME = in.Files[i].MIME
		if len(in.Files[i].Parsers) > 0 {
			item.Parsers = in.Files[i].Parsers
		}
	}
	return item
}

func (s *Service) extractBatchFile(ctx context.Context, token string, d Decision, plan FileExtractJob, in FileBatchInput, item FileBatchItem, file batchFile) (FileMemoriesResult, error) {
	if !validUploadName(file.Name) || len(file.Raw) == 0 {
		return FileMemoriesResult{}, ErrInvalid
	}
	mime, err := s.uploadMIME(file.Name, item.MIME)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	return s.extractUploaded(ctx, token, d, plan, uploadedFile{Scope: in.Scope, Name: file.Name, MIME: mime, Raw: file.Raw, Parsers: item.Parsers, DryRun: in.DryRun})
}

func batchResult(name string, result FileMemoriesResult, err error) FileBatchResult {
	if err != nil {
		code, _ := errorResponse(err)
		return FileBatchResult{Filename: name, Error: code}
	}
	return FileBatchResult{Filename: name, Result: &result}
}
