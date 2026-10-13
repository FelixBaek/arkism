package memory

const maxBackupFiles = 100000
const maxBackupManifest = 8 << 20

type backupFile struct {
	Digest string `json:"digest"`
	Bytes  int64  `json:"bytes"`
}
type blobBackupManifest struct {
	Version        int          `json:"version"`
	SourceIdentity string       `json:"source_identity"`
	SourceRootID   string       `json:"source_root_id"`
	Database       backupFile   `json:"database"`
	Files          []backupFile `json:"files"`
}

// A stopped-writer restore check, not a live cross-collection snapshot.

func countRestoreArtifact(r Record, files, counts map[string]int64) error {
	if !validArtifactRecord(r) || files[r.Artifact.Digest] != r.Artifact.Bytes {
		return ErrUnavailable
	}
	counts[r.Artifact.Digest]++
	return nil
}
func checkRestoreBlob(r Record, files, counts map[string]int64) error {
	if r.Kind == "blob_root_binding" {
		return nil
	}
	if r.Kind != "blob" || r.Scope != blobScope() || !sourceHashPattern.MatchString(r.ID) || r.Blob == nil || r.Blob.Bytes < 0 || r.Blob.Bytes > 200<<20 || r.Blob.References != counts[r.ID] || r.Revision < 1 {
		return ErrUnavailable
	}
	if r.Status != statusActive && r.Status != JobStatusPending {
		return ErrUnavailable
	}
	if r.Blob.References > 0 && (r.Status != statusActive || files[r.ID] != r.Blob.Bytes || r.Blob.Bytes < 1) {
		return ErrUnavailable
	}
	delete(counts, r.ID)
	return nil
}
