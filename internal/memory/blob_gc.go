package memory

type BlobGCResult struct {
	ScannedFiles          int `json:"scanned_files"`
	ReferencedFiles       int `json:"referenced_files"`
	CollectedBlobs        int `json:"collected_blobs"`
	TemporaryFilesRemoved int `json:"temporary_files_removed"`
}
