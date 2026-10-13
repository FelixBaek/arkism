package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"strconv"

	sqlite "github.com/ncruces/go-sqlite3"
)

// BLOB handles are used sequentially inside one request; no connection or handle
// crosses goroutines. Seeking lets the parser retain spans instead of payloads.
type archiveSQLiteBlobReader struct {
	ctx  context.Context
	blob *sqlite.Blob
}

func (r archiveSQLiteBlobReader) ReadAt(dst []byte, offset int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if _, err := r.blob.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	return io.ReadFull(r.blob, dst)
}
func archiveSQLiteLargeMessage(ctx context.Context, db *sqlite.Conn, reader *archiveSQLiteReader, ref ArchiveMessageRef, idx, kind, status, format, size int64, query string, offset int, direct bool) (view ArchiveMessageView, state string, err error) {
	// Preserve request/storage failures even when SQLite wraps them as a generic
	// interruption or a parser encounters a truncated buffer after that failure.
	requestCtx := ctx
	defer func() {
		if reader.failure != nil {
			err = reader.failure
		}
		if requestCtx.Err() != nil {
			err = requestCtx.Err()
		}
		if err != nil {
			view = ArchiveMessageView{}
			state = ""
		}
	}()
	if kind != 14 && kind != 15 || status != 3 || format != 0 {
		return view, "ignored", nil
	}
	// The current row may exceed a page. Restore the original counter afterwards
	// and charge all decrypted chunks, including cache misses on repeated passes.
	originalBudget := reader.budget
	rowBudget := math.MaxInt
	reader.budget = &rowBudget
	defer func() { *originalBudget -= math.MaxInt - rowBudget; reader.budget = originalBudget }()
	blob, err := db.OpenBlob("main", "steps", "step_payload", idx, false)
	if err != nil {
		return view, "", err
	}
	defer blob.Close()
	if size < 0 || blob.Size() != size {
		return view, "", ErrInvalid
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	input := archiveSQLiteBlobReader{ctx, blob}
	digest := sha256.New()
	_, _ = fmt.Fprintf(digest, "arkism-sqlite-step-v1\n%d\n%d\n%d\n%d\n", idx, kind, status, format)
	if _, err = io.Copy(digest, io.NewSectionReader(input, 0, size)); err != nil {
		return view, "", err
	}
	span, role, err := archiveAntigravityStream(input, size, kind, status, format)
	if err != nil {
		return view, "", err
	}
	if role == "" {
		return view, "ignored", nil
	}
	open := func() (io.ReadCloser, error) {
		return io.NopCloser(&archiveUTF8Reader{source: io.NewSectionReader(input, span.start, span.size)}), nil
	}
	observe, state, err := archiveInspectText(ctx, cancel, open, query, offset, direct)
	if err != nil || state != "found" {
		return view, state, err
	}
	hash := ContentHash("arkism-sqlite-message-v1\n" + role + "\n" + hex.EncodeToString(digest.Sum(nil)))
	if ref.MessageSHA256 != "" && ref.MessageSHA256 != hash {
		return view, "", ErrConflict
	}
	ref.SQLiteStep = strconv.FormatInt(idx, 10)
	ref.MessageSHA256 = hash
	view = ArchiveMessageView{Ref: ref, Role: role, Text: string(observe.output), TextOffset: observe.start, RawBytes: int(size), Authority: AuthorityDataOnly}
	end := observe.start + len(observe.output)
	if end < observe.total {
		view.NextOffset = &end
	}
	return view, "found", nil
}
