package memory

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"mime"
	"net/http"
	"strconv"
	"time"
)

// archiveFile is one named file of a download archive (skill zip, wiki export).
type archiveFile struct {
	name string
	body []byte
	at   time.Time
}

// zipArchive packs files in order with deflate.
func zipArchive(files []archiveFile) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: f.at})
		if err != nil {
			return nil, err
		}
		if _, err = fw.Write(f.body); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// tarArchive packs files in order as regular PAX entries.
func tarArchive(files []archiveFile) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), ModTime: f.at, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeAttachment answers a binary download.
func writeAttachment(w http.ResponseWriter, contentType, name string, data []byte) {
	w.Header().Set(headerContentType, contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
