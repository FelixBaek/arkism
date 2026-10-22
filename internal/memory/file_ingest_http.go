package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
)

const maxFileRequestBytes = maxFileMetadataBytes + maxTextArtifactBytes + (64 << 10)

func fileReadError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return errBlobTooLarge
	}
	return ErrInvalid
}

func filePartBytes(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if len(b) > int(limit) {
		return nil, errBlobTooLarge
	}
	if err != nil {
		return nil, fileReadError(err)
	}
	return b, nil
}

func (s *Service) ingestFileHTTP(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > maxFileRequestBytes {
		respond(w, nil, errBlobTooLarge)
		return
	}
	if s.BlobDirectory == "" {
		respond(w, nil, ErrUnavailable)
		return
	}
	if r.Header.Get("Content-Encoding") != "" {
		respond(w, nil, ErrInvalid)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFileRequestBytes)
	m, err := r.MultipartReader()
	if err != nil {
		respond(w, nil, ErrInvalid)
		return
	}
	in, err := fileIngestMetadata(m)
	if err != nil {
		respond(w, nil, err)
		return
	}
	// Refuse unauthorized scopes before reading file bytes. No uninspected body
	// or digest is sent to policy in this preflight.
	decision, err := s.authorize(r.Context(), bearer(r), "write:sources", in.Source.Scope, "", "")
	if err != nil {
		respond(w, nil, err)
		return
	}
	if !validArtifactActor(decision, in.Source.Scope) {
		respond(w, nil, ErrDenied)
		return
	}
	content, err := fileIngestContent(m, in.Artifact.MIME)
	if err != nil {
		respond(w, nil, err)
		return
	}
	// Consume the bounded epilogue too: chunked trailing bytes still count
	// against the whole-request limit even after the closing MIME boundary.
	if _, err = io.Copy(io.Discard, r.Body); err != nil {
		respond(w, nil, fileReadError(err))
		return
	}
	out, err := s.IngestFile(bindRequestActor(r.Context(), decision), bearer(r), in, content)
	if err != nil {
		respond(w, nil, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// fileIngestMetadata reads and validates the leading metadata part.
func fileIngestMetadata(m *multipart.Reader) (FileIngestInput, error) {
	part, err := m.NextRawPart()
	if err != nil || part.FormName() != "metadata" || part.Header.Get("Content-Transfer-Encoding") != "" {
		return FileIngestInput{}, fileReadError(err)
	}
	metadata, err := filePartBytes(part, maxFileMetadataBytes)
	if err != nil {
		return FileIngestInput{}, err
	}
	var in FileIngestInput
	d := json.NewDecoder(bytes.NewReader(metadata))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		return FileIngestInput{}, ErrInvalid
	}
	if !validScope(in.Source.Scope) {
		return FileIngestInput{}, ErrInvalid
	}
	if containsSecret(structuredText(in.Source.Scope)) {
		return FileIngestInput{}, ErrSecretDetected
	}
	return in, nil
}

// fileIngestContent reads the file part and requires it to be the last part.
func fileIngestContent(m *multipart.Reader, mimeType string) ([]byte, error) {
	part, err := m.NextRawPart()
	if err != nil || part.FormName() != "file" || part.Header.Get("Content-Transfer-Encoding") != "" {
		return nil, fileReadError(err)
	}
	if value := part.Header.Get("Content-Type"); value != "" {
		mediaType, params, err := mime.ParseMediaType(value)
		if err != nil || mediaType != mimeType || len(params) > 1 || len(params) == 1 && params["charset"] != "utf-8" {
			return nil, ErrInvalid
		}
	}
	content, err := filePartBytes(part, maxTextArtifactBytes)
	if err != nil {
		return nil, err
	}
	if _, err = m.NextRawPart(); err != io.EOF {
		return nil, fileReadError(err)
	}
	return content, nil
}
