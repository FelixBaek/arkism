package memory

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	sqlite "github.com/ncruces/go-sqlite3"
)

func validSQLiteStep(s string) bool {
	if s == "" {
		return true
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n >= 0 && strconv.FormatInt(n, 10) == s
}
func sqliteArchiveSchema(db *sqlite.Conn, session string) error {
	stmt, _, err := db.Prepare("SELECT name,type,sql FROM sqlite_schema WHERE name IN ('steps','trajectory_meta') ORDER BY name")
	if err != nil {
		return errArchiveSQLiteUnsupported
	}
	tables := 0
	for stmt.Step() {
		if stmt.ColumnText(1) != "table" || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(stmt.ColumnText(2))), "create table ") {
			stmt.Close()
			return errArchiveSQLiteUnsupported
		}
		tables++
	}
	err = stmt.Err()
	stmt.Close()
	if err != nil || tables != 2 {
		return errArchiveSQLiteUnsupported
	}
	stmt, _, err = db.Prepare("PRAGMA table_xinfo(steps)")
	if err != nil {
		return errArchiveSQLiteUnsupported
	}
	required := map[string]bool{"idx": false, "step_type": false, "status": false, "step_format": false, "step_payload": false}
	primary := 0
	for stmt.Step() {
		name := stmt.ColumnText(1)
		if stmt.ColumnInt(5) > 0 {
			primary++
		}
		if _, ok := required[name]; ok {
			if stmt.ColumnInt(6) != 0 || name == "idx" && (strings.ToUpper(stmt.ColumnText(2)) != "INTEGER" || stmt.ColumnInt(5) != 1) {
				stmt.Close()
				return errArchiveSQLiteUnsupported
			}
			required[name] = true
		}
	}
	err = stmt.Err()
	stmt.Close()
	if err != nil || primary != 1 {
		return errArchiveSQLiteUnsupported
	}
	for _, found := range required {
		if !found {
			return errArchiveSQLiteUnsupported
		}
	}
	// Incremental BLOB reads use the internal rowid. INTEGER PRIMARY KEY DESC
	// and WITHOUT ROWID have a separate pk index: idx is not a rowid alias.
	stmt, _, err = db.Prepare("PRAGMA index_list(steps)")
	if err != nil {
		return errArchiveSQLiteUnsupported
	}
	for stmt.Step() {
		if stmt.ColumnText(3) == "pk" {
			stmt.Close()
			return errArchiveSQLiteUnsupported
		}
	}
	err = stmt.Err()
	stmt.Close()
	if err != nil {
		return errArchiveSQLiteUnsupported
	}
	stmt, _, err = db.Prepare("SELECT trajectory_id,cascade_id FROM trajectory_meta LIMIT 2")
	if err != nil {
		return errArchiveSQLiteUnsupported
	}
	defer stmt.Close()
	if !stmt.Step() || (stmt.ColumnText(0) != session && stmt.ColumnText(1) != session) {
		return errArchiveSQLiteUnsupported
	}
	if stmt.Step() || stmt.Err() != nil {
		return errArchiveSQLiteUnsupported
	}
	// Only our fixed SELECTs can run after schema validation. Views, attached
	// databases, pragmas and arbitrary functions are not an input language.
	return db.SetAuthorizer(func(action sqlite.AuthorizerActionCode, a, b, schema, inner string) sqlite.AuthorizerReturnCode {
		if inner != "" {
			return sqlite.AUTH_DENY
		}
		switch action {
		case sqlite.AUTH_SELECT:
			return sqlite.AUTH_OK
		case sqlite.AUTH_READ:
			if schema == "main" && (a == "steps" || a == "sqlite_master" || a == "sqlite_schema") {
				return sqlite.AUTH_OK
			}
		case sqlite.AUTH_FUNCTION:
			if b == "length" || b == "typeof" {
				return sqlite.AUTH_OK
			}
		}
		return sqlite.AUTH_DENY
	})
}

const sqliteArchiveRows = `SELECT idx,step_type,status,step_format,CASE WHEN typeof(step_payload)='blob' THEN length(step_payload) ELSE -1 END,
 CASE WHEN typeof(step_payload)='blob' AND length(step_payload)<=8388608 THEN step_payload ELSE NULL END, typeof(step_payload)
 FROM steps WHERE idx>?1 ORDER BY idx`

func (s *Service) searchArchiveSQLite(ctx context.Context, h Record, in ArchiveSearchInput, cursor *archiveSearchCursor, out *ArchiveSearchOutput, budget *int) error {
	if h.SessionArchive.Origin != "antigravity-cli" {
		return errArchiveSQLiteUnsupported
	}
	if cursor.Skip || cursor.Ref.Chunk != 0 || cursor.Ref.Offset != 0 {
		return ErrInvalid
	}
	// Start a fresh page before a row could consume the whole remaining budget.
	if *budget < archiveRecordLimit+(2<<20) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	reader, err := newArchiveSQLiteReader(ctx, s, h, budget)
	if err != nil {
		return err
	}
	db, close, err := openArchiveSQLite(ctx, reader)
	if err != nil {
		return err
	}
	defer close()
	if err = sqliteArchiveSchema(db, h.SessionArchive.SessionID); err != nil {
		if reader.failure != nil {
			return reader.failure
		}
		return err
	}
	stmt, _, err := db.Prepare(sqliteArchiveRows)
	if err != nil {
		return ErrInvalid
	}
	defer stmt.Close()
	after := int64(-1)
	if cursor.Ref.SQLiteStep != "" {
		after, _ = strconv.ParseInt(cursor.Ref.SQLiteStep, 10, 64)
	}
	if err = stmt.BindInt64(1, after); err != nil {
		return ErrInvalid
	}
	for stmt.Step() {
		idx, kind, status, format, size := stmt.ColumnInt64(0), stmt.ColumnInt64(1), stmt.ColumnInt64(2), stmt.ColumnInt64(3), stmt.ColumnInt64(4)
		if stmt.ColumnType(0) != sqlite.INTEGER || stmt.ColumnType(1) != sqlite.INTEGER || stmt.ColumnType(2) != sqlite.INTEGER || stmt.ColumnType(3) != sqlite.INTEGER || idx < 0 {
			return ErrInvalid
		}
		cursor.Ref.SQLiteStep = strconv.FormatInt(idx, 10)
		if format != 0 {
			out.UnsupportedRecords++
		} else if stmt.ColumnText(6) != "blob" {
			out.Invalid++
		} else if size > archiveRecordLimit {
			view, state, readErr := archiveSQLiteLargeMessage(ctx, db, reader, cursor.Ref, idx, kind, status, format, size, in.Query, 0, false)
			if readErr != nil {
				if errors.Is(readErr, ErrInvalid) || errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
					out.Invalid++
				} else {
					return readErr
				}
			} else if state == "secret" {
				out.Secrets++
			} else if state == "found" {
				out.Items = append(out.Items, view)
			}
		} else if stmt.ColumnType(5) != sqlite.BLOB {
			out.Invalid++
		} else {
			payload := stmt.ColumnRawBlob(5)
			text, role, valid := archiveAntigravityText(kind, status, format, payload)
			if !valid {
				out.Invalid++
			} else if text != "" {
				if containsSecret(text) {
					out.Secrets++
				} else if at := strings.Index(text, in.Query); at >= 0 {
					begin := max(0, at-256)
					for begin > 0 && !utf8.RuneStart(text[begin]) {
						begin--
					}
					ref := cursor.Ref
					ref.MessageSHA256 = archiveSQLiteMessageHash(idx, kind, status, format, role, payload)
					out.Items = append(out.Items, archiveTextView(ref, role, text, begin, 4096, len(payload)))
				}
			}
		}
		if len(out.Items) >= in.Limit || *budget < archiveRecordLimit+(2<<20) {
			return nil
		}
	}
	if err = stmt.Err(); err != nil {
		if reader.failure != nil {
			return reader.failure
		}
		return ErrInvalid
	}
	cursor.After = h.ID
	cursor.Ref = ArchiveMessageRef{}
	cursor.Skip = false
	return nil
}
func (s *Service) readArchiveSQLite(ctx context.Context, token string, h Record, in ArchiveMessageInput, actor Decision) (ArchiveMessageView, error) {
	empty := ArchiveMessageView{}
	if h.SessionArchive.Origin != "antigravity-cli" || in.Ref.SQLiteStep == "" || in.Ref.Chunk != 0 || in.Ref.Offset != 0 || in.Ref.RecordKey != "" {
		return empty, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	budget := archiveSearchBudget
	reader, err := newArchiveSQLiteReader(ctx, s, h, &budget)
	if err != nil {
		return empty, err
	}
	db, close, err := openArchiveSQLite(ctx, reader)
	if err != nil {
		return empty, ErrInvalid
	}
	defer close()
	if err = sqliteArchiveSchema(db, h.SessionArchive.SessionID); err != nil {
		return empty, ErrInvalid
	}
	stmt, _, err := db.Prepare(strings.Replace(sqliteArchiveRows, "idx>?1", "idx=?1", 1))
	if err != nil {
		return empty, ErrInvalid
	}
	defer stmt.Close()
	idx, _ := strconv.ParseInt(in.Ref.SQLiteStep, 10, 64)
	if err = stmt.BindInt64(1, idx); err != nil {
		return empty, ErrInvalid
	}
	if !stmt.Step() {
		if stmt.Err() != nil {
			return empty, ErrInvalid
		}
		return empty, ErrNotFound
	}
	kind, status, format := stmt.ColumnInt64(1), stmt.ColumnInt64(2), stmt.ColumnInt64(3)
	if stmt.ColumnType(1) != sqlite.INTEGER || stmt.ColumnType(2) != sqlite.INTEGER || stmt.ColumnType(3) != sqlite.INTEGER || stmt.ColumnText(6) != "blob" {
		return empty, ErrInvalid
	}
	if size := stmt.ColumnInt64(4); size > archiveRecordLimit {
		view, state, err := archiveSQLiteLargeMessage(ctx, db, reader, in.Ref, idx, kind, status, format, size, "", in.TextOffset, true)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				err = ErrInvalid
			}
			return empty, err
		}
		switch state {
		case "secret":
			return empty, ErrDenied
		case "unmatched":
			return empty, ErrInvalid
		case "found":
		default:
			return empty, ErrNotFound
		}
		if err = s.fenceArchiveSearch(ctx, token, []Record{h}, actor); err != nil {
			return empty, err
		}
		view.Ref.SHA256 = h.SessionArchive.SHA256
		return view, nil
	}
	if stmt.ColumnType(5) != sqlite.BLOB {
		return empty, ErrInvalid
	}
	payload := stmt.ColumnRawBlob(5)
	text, role, valid := archiveAntigravityText(kind, status, format, payload)
	if !valid || text == "" {
		return empty, ErrNotFound
	}
	if containsSecret(text) {
		return empty, ErrDenied
	}
	hash := archiveSQLiteMessageHash(idx, kind, status, format, role, payload)
	if in.Ref.MessageSHA256 != "" && in.Ref.MessageSHA256 != hash {
		return empty, ErrConflict
	}
	if in.TextOffset >= len(text) || !utf8.RuneStart(text[in.TextOffset]) {
		return empty, ErrInvalid
	}
	if err = s.fenceArchiveSearch(ctx, token, []Record{h}, actor); err != nil {
		return empty, err
	}
	ref := in.Ref
	ref.SHA256 = h.SessionArchive.SHA256
	ref.MessageSHA256 = hash
	return archiveTextView(ref, role, text, in.TextOffset, archiveMessagePage, len(payload)), nil
}

func sqliteArchiveSearchError(err error) error {
	if errors.Is(err, errArchiveSQLiteBudget) {
		return ErrUnavailable
	}
	return err
}
