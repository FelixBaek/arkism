package capture

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqlite "github.com/ncruces/go-sqlite3"
)

var errArchivePrivate = errors.New("archive_private")

const archiveMaxBytes int64 = 2048 * archiveChunk
const archiveSQLiteValueBytes = 64 << 20

// Inspect the bytes before writing a chunk into the private staging file. An
// encoded marker can cross any number of read boundaries without buffering it.
type archivePrivacyWriter struct {
	file   *os.File
	marker privateMarker
}

func (w *archivePrivacyWriter) Write(p []byte) (int, error) {
	w.marker.write(p)
	if w.marker.found {
		return 0, errArchivePrivate
	}
	return w.file.Write(p)
}

// archiveSnapshot is the exact file later hashed, projected and transmitted.
// Source changes cannot introduce new, uninspected bytes after this operation.
func archiveSnapshot(path, format string) (target string, cleanup func(), err error) {
	cleanup = func() {}
	info, err := os.Lstat(path)
	if err != nil {
		return "", cleanup, err
	}
	if !info.Mode().IsRegular() {
		return "", cleanup, valueErr("archive_source_not_regular")
	}
	if info.Size() > archiveMaxBytes {
		return "", cleanup, valueErr("too_large")
	}
	in, err := os.Open(path)
	if err != nil {
		return "", cleanup, err
	}
	defer in.Close()
	opened, err := in.Stat()
	if err != nil {
		return "", cleanup, err
	}
	if !os.SameFile(info, opened) {
		return "", cleanup, valueErr("archive_source_changed")
	}
	dir, err := os.MkdirTemp("", "arkism-archive-inspect-")
	if err != nil {
		return "", cleanup, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	target = filepath.Join(dir, "session"+filepath.Ext(path))
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", cleanup, err
	}
	w := &archivePrivacyWriter{file: out}
	n, copyErr := io.CopyBuffer(w, io.LimitReader(in, archiveMaxBytes+1), make([]byte, 64<<10))
	closeErr := out.Close()
	if copyErr != nil {
		return "", cleanup, copyErr
	}
	if closeErr != nil {
		return "", cleanup, closeErr
	}
	if w.marker.marked() {
		return "", cleanup, errArchivePrivate
	}
	if n > archiveMaxBytes {
		return "", cleanup, valueErr("too_large")
	}
	if format == "sqlite_snapshot" {
		if err = inspectPrivateSQLite(target); err != nil {
			return "", cleanup, err
		}
	}
	return target, cleanup, nil
}

// SQLite may split a TEXT value over overflow pages. Inspect logical values as
// well as the raw snapshot, without changing rows, schema or the archived bytes.
func inspectPrivateSQLite(path string) error { return inspectSQLiteArchive(path, nil) }

func inspectSQLiteArchive(path string, policy *archivePolicy) error {
	db, err := openRO(path)
	if err != nil {
		return valueErr("archive_sqlite_inspection_failed")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db.SetInterrupt(ctx)
	if err := db.Exec("PRAGMA query_only=ON; PRAGMA trusted_schema=OFF"); err != nil {
		return valueErr("archive_sqlite_inspection_failed")
	}
	// Bound a decoded value while inspecting a local snapshot.
	db.Limit(sqlite.LIMIT_LENGTH, archiveSQLiteValueBytes)
	tables, err := archiveSQLiteTables(db)
	if err != nil {
		return err
	}
	rows := 0
	for _, table := range tables {
		query := `SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`
		err = eachRow(db, query, nil, func(cols []string, values []any) error {
			rows++
			if rows > 1000000 {
				return valueErr("archive_sqlite_inspection_failed")
			}
			return inspectArchiveSQLiteRow(policy, cols, values)
		})
		if errors.Is(err, errArchivePrivate) {
			return err
		}
		if err != nil {
			return valueErr("archive_sqlite_inspection_failed")
		}
	}
	return nil
}

func archiveSQLiteTables(db *sqlite.Conn) ([]string, error) {
	// Include SQLite's own schema/statistics tables: archive bytes include these
	// values as well, and an overflow page may interrupt a raw marker there.
	tables := []string{"sqlite_schema"}
	err := eachRow(db, `SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name`, nil, func(_ []string, values []any) error {
		if len(values) != 1 {
			return valueErr("archive_sqlite_inspection_failed")
		}
		name, ok := values[0].(string)
		if !ok {
			return valueErr("archive_sqlite_inspection_failed")
		}
		tables = append(tables, name)
		if len(tables) > 1024 {
			return valueErr("archive_sqlite_inspection_failed")
		}
		return nil
	})
	if err != nil {
		return nil, valueErr("archive_sqlite_inspection_failed")
	}
	return tables, nil
}

func inspectArchiveSQLiteRow(policy *archivePolicy, cols []string, values []any) error {
	for i, value := range values {
		data := archiveSQLiteBytes(value)
		var marker privateMarker
		marker.write(data)
		if marker.marked() {
			return errArchivePrivate
		}
		if policy != nil {
			if err := inspectArchiveSQLiteMetadata(policy, cols[i], data); err != nil {
				return err
			}
		}
	}
	return nil
}

func archiveSQLiteBytes(value any) []byte {
	switch v := value.(type) {
	case string:
		return []byte(v)
	case []byte:
		return v
	default:
		return nil
	}
}

func inspectArchiveSQLiteMetadata(policy *archivePolicy, column string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if path, file := archivePathKey(column); path {
		if err := policy.addPath(string(data), file); err != nil {
			return err
		}
	}
	return policy.walk(string(data), 0)
}

// Dry-run inspection reads without creating a staging copy or touching transport.
func inspectPrivateArchiveFile(path, format string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return valueErr("archive_source_not_regular")
	}
	if info.Size() > archiveMaxBytes {
		return valueErr("too_large")
	}
	marked, err := scanPrivate(io.LimitReader(f, archiveMaxBytes+1))
	if err != nil {
		return err
	}
	if marked {
		return errArchivePrivate
	}
	if format == "sqlite_snapshot" {
		return inspectPrivateSQLite(path)
	}
	return nil
}

// archiveBoundedWriter caps reconstructed output before it is written to disk.
// It preserves successful output bytes exactly and never reports a truncated
// archive as successful.
type archiveBoundedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *archiveBoundedWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, valueErr("too_large")
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	return n, err
}

// Read a stable SQLite snapshot incrementally, checking both the logical database
// size and a work budget. Concurrent growth cannot make the temporary copy grow
// without a bound or restart the backup forever.
func archiveSQLiteBackup(db *sqlite.Conn, target string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	old := db.SetInterrupt(ctx)
	defer db.SetInterrupt(old)
	if err := db.Exec("BEGIN"); err != nil {
		return err
	}
	defer db.Exec("ROLLBACK")
	pageSize, err := archiveSQLiteScalar(db, "PRAGMA page_size")
	if err != nil || pageSize <= 0 || pageSize > 65536 {
		return valueErr("archive_sqlite_inspection_failed")
	}
	pageCount, err := archiveSQLiteScalar(db, "PRAGMA page_count")
	if err != nil || pageCount < 0 {
		return valueErr("archive_sqlite_inspection_failed")
	}
	maxPages := archiveMaxBytes / pageSize
	if pageCount > maxPages {
		return valueErr("too_large")
	}
	backup, err := db.BackupInit("main", target)
	if err != nil {
		return err
	}
	defer backup.Close()
	for copied := int64(0); copied <= maxPages; copied += 64 {
		if ctx.Err() != nil {
			return valueErr("archive_sqlite_inspection_failed")
		}
		done, err := backup.Step(64)
		if err != nil {
			return err
		}
		if int64(backup.PageCount()) > maxPages {
			return valueErr("too_large")
		}
		if done {
			return backup.Close()
		}
	}
	return valueErr("too_large")
}

func archiveSQLiteScalar(db *sqlite.Conn, query string) (int64, error) {
	var result int64
	rows := 0
	err := eachRow(db, query, nil, func(_ []string, values []any) error {
		rows++
		if rows != 1 || len(values) != 1 {
			return valueErr("archive_sqlite_inspection_failed")
		}
		value, ok := values[0].(int64)
		if !ok {
			return valueErr("archive_sqlite_inspection_failed")
		}
		result = value
		return nil
	})
	if rows != 1 && err == nil {
		err = valueErr("archive_sqlite_inspection_failed")
	}
	return result, err
}
