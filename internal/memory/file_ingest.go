package memory

import (
	"bytes"
	"context"
)

// This public text limit leaves room for JSON escaping inside the default
// 8 MiB policy request. Large/binary ingestion still needs a full-file inspector.
const maxTextArtifactBytes = 512 << 10
const maxFileMetadataBytes = 256 << 10

type FileIngestInput struct {
	Source   CreateInput      `json:"source"`
	Artifact FileArtifactSpec `json:"artifact"`
}
type FileArtifactSpec struct {
	Kind   string `json:"kind"`
	MIME   string `json:"mime"`
	Digest string `json:"digest,omitempty"`
}
type FileArtifactResult struct {
	ID string `json:"id,omitempty"`
	FileArtifactSpec
	Bytes int64 `json:"bytes"`
}
type FileIngestResult struct {
	Source   Record             `json:"source"`
	Artifact FileArtifactResult `json:"artifact"`
}
type textArtifact struct {
	metadata sourceArtifact
	content  []byte
}

func (f textArtifact) descriptor() FileArtifactResult {
	return FileArtifactResult{FileArtifactSpec: FileArtifactSpec{Kind: f.metadata.Kind, MIME: f.metadata.Data.MIME, Digest: f.metadata.Data.Digest}, Bytes: f.metadata.Data.Bytes}
}

func (s *Service) IngestFile(ctx context.Context, token string, in FileIngestInput, content []byte) (FileIngestResult, error) {
	if len(content) > maxTextArtifactBytes || len(structuredText(in)) > maxFileMetadataBytes {
		return FileIngestResult{}, errBlobTooLarge
	}
	if len(content) == 0 || in.Artifact.Digest != "" && !sourceHashPattern.MatchString(in.Artifact.Digest) {
		return FileIngestResult{}, ErrInvalid
	}
	content = bytes.Clone(content)
	file := textArtifact{metadata: sourceArtifact{Kind: in.Artifact.Kind, Data: artifactMetadata{Digest: ContentHash(string(content)), MIME: in.Artifact.MIME, Bytes: int64(len(content))}}, content: content}
	if !validArtifact(file.metadata.Kind, file.metadata.Data) || (in.Artifact.MIME != "text/plain" && in.Artifact.MIME != "application/json") {
		return FileIngestResult{}, ErrInvalid
	}
	if in.Artifact.Digest != "" && in.Artifact.Digest != file.metadata.Data.Digest {
		return FileIngestResult{}, errBlobDigestMismatch
	}
	r, err := s.create(ctx, token, "sources", in.Source, &file)
	if err != nil {
		return FileIngestResult{}, err
	}
	artifact := file.descriptor()
	artifact.ID = artifactID(r.Scope, r.ID, artifact.Digest)
	return FileIngestResult{Source: r, Artifact: artifact}, nil
}
