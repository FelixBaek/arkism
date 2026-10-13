package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
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
	part, err := m.NextRawPart()
	if err != nil || part.FormName() != "metadata" || part.Header.Get("Content-Transfer-Encoding") != "" {
		respond(w, nil, fileReadError(err))
		return
	}
	metadata, err := filePartBytes(part, maxFileMetadataBytes)
	if err != nil {
		respond(w, nil, err)
		return
	}
	var in FileIngestInput
	d := json.NewDecoder(bytes.NewReader(metadata))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		respond(w, nil, ErrInvalid)
		return
	}
	if !validScope(in.Source.Scope) {
		respond(w, nil, ErrInvalid)
		return
	}
	if containsSecret(structuredText(in.Source.Scope)) {
		respond(w, nil, ErrSecretDetected)
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
	part, err = m.NextRawPart()
	if err != nil || part.FormName() != "file" || part.Header.Get("Content-Transfer-Encoding") != "" {
		respond(w, nil, fileReadError(err))
		return
	}
	if value := part.Header.Get("Content-Type"); value != "" {
		mediaType, params, err := mime.ParseMediaType(value)
		if err != nil || mediaType != in.Artifact.MIME || len(params) > 1 || len(params) == 1 && params["charset"] != "utf-8" {
			respond(w, nil, ErrInvalid)
			return
		}
	}
	content, err := filePartBytes(part, maxTextArtifactBytes)
	if err != nil {
		respond(w, nil, err)
		return
	}
	if _, err = m.NextRawPart(); err != io.EOF {
		respond(w, nil, fileReadError(err))
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
