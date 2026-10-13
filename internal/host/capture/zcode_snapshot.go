package capture

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	sqlite "github.com/ncruces/go-sqlite3"

	"github.com/FelixBaek/arkism/internal/host"
)

// Portable read-only Zcode export, shared by the server and the Windows client.

// zcodeMissing is Python's ZcodeSessionMissing (a LookupError).
type zcodeMissing string

func (e zcodeMissing) Error() string { return string(e) }

// sessionTimestamp reads a Zcode time: epoch seconds or milliseconds, or an ISO string.
func sessionTimestamp(v any) (float64, bool) {
	if f, ok := num(v); ok {
		for f > 1e11 {
			f /= 1000
		}
		return f, true
	}
	s, _ := v.(string)
	if s == "" {
		return 0, false
	}
	ts, _, ok := fromISO(s)
	return ts, ok
}

// fileURI is Path(p).as_uri() for an absolute path.
// On Windows the SQLite driver opens the URI's path as given, so "/C:/x" would fail: there the
// URI is file:C:/x.
func fileURI(p string) string {
	p = filepath.ToSlash(p)
	if runtime.GOOS == "windows" && filepath.VolumeName(filepath.FromSlash(p)) != "" {
		return "file:" + (&url.URL{Path: p}).EscapedPath()
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "file://" + (&url.URL{Path: p}).EscapedPath()
}

// openRO opens an SQLite file read-only through a URI, like sqlite3.connect(uri+"?mode=ro").
func openRO(path string) (*sqlite.Conn, error) {
	return sqlite.OpenFlags(fileURI(path)+"?mode=ro", sqlite.OPEN_READONLY|sqlite.OPEN_URI)
}

// eachRow runs query and calls fn with column names and Python-shaped values per row.
func eachRow(db *sqlite.Conn, query string, args []any, fn func(cols []string, vals []any) error) error {
	stmt, _, err := db.Prepare(query)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, a := range args {
		switch x := a.(type) {
		case string:
			err = stmt.BindText(i+1, x)
		case int64:
			err = stmt.BindInt64(i+1, x)
		case float64:
			err = stmt.BindFloat(i+1, x)
		default:
			err = fmt.Errorf("bind %T", a)
		}
		if err != nil {
			return err
		}
	}
	cols := make([]string, stmt.ColumnCount())
	for i := range cols {
		cols[i] = stmt.ColumnName(i)
	}
	for stmt.Step() {
		vals := make([]any, len(cols))
		for i := range cols {
			switch stmt.ColumnType(i) {
			case sqlite.INTEGER:
				vals[i] = stmt.ColumnInt64(i)
			case sqlite.FLOAT:
				vals[i] = stmt.ColumnFloat(i)
			case sqlite.TEXT:
				vals[i] = stmt.ColumnText(i)
			case sqlite.BLOB:
				vals[i] = stmt.ColumnBlob(i, nil)
			default:
				vals[i] = nil
			}
		}
		if err := fn(cols, vals); err != nil {
			return err
		}
	}
	return stmt.Err()
}

// rowObj is dict(sqlite3.Row): columns in order; bytes become str(bytes) (json default=str).
func rowObj(cols []string, vals []any) host.Obj {
	o := make(host.Obj, len(cols))
	for i, c := range cols {
		v := vals[i]
		if b, isBytes := v.([]byte); isBytes {
			v = bytesRepr(b)
		}
		o[i] = host.KV{Key: c, Value: v}
	}
	return o
}

// bytesRepr is Python's str(b"...").
func bytesRepr(b []byte) string {
	quote := byte('\'')
	if strings.IndexByte(string(b), '\'') >= 0 && strings.IndexByte(string(b), '"') < 0 {
		quote = '"'
	}
	var s strings.Builder
	s.WriteString("b")
	s.WriteByte(quote)
	for _, c := range b {
		switch {
		case c == quote || c == '\\':
			s.WriteByte('\\')
			s.WriteByte(c)
		case c == '\t':
			s.WriteString(`\t`)
		case c == '\n':
			s.WriteString(`\n`)
		case c == '\r':
			s.WriteString(`\r`)
		case c < 0x20 || c >= 0x7f:
			fmt.Fprintf(&s, `\x%02x`, c)
		default:
			s.WriteByte(c)
		}
	}
	s.WriteByte(quote)
	return s.String()
}

// zcodeTables are exported in this order, rows by rowid.
var zcodeTables = []string{"message", "part", "session_entry", "todo", "session_input", "model_usage", "turn_usage", "tool_usage"}

// zcodeSnapshot exports one consistent read-only snapshot of session sid in the archive
// wire format, then calls fn with the temporary file, its observed time and the raw
// time_updated (the revision). The file is removed afterwards.
func zcodeSnapshot(path, sid string, fn func(snapshot string, observed float64, revision any) error) error {
	path = absPath(path)
	if !resolved(path) {
		return valueErr("zcode_path_alias")
	}
	if !isFile(path) {
		return zcodeMissing("zcode_session_missing")
	}
	temp, err := os.MkdirTemp("", "arkism-session-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	target := filepath.Join(temp, "session.jsonl")
	session, err := writeZcodeSnapshot(path, sid, target)
	if err != nil {
		return err
	}
	updated, _ := session.Get("time_updated")
	observed, ok := sessionTimestamp(updated)
	if !ok {
		return valueErr("zcode_observed_time_missing")
	}
	return fn(target, observed, updated)
}

func writeZcodeSnapshot(path, sid, target string) (host.Obj, error) {
	db, err := openRO(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := db.Exec("BEGIN"); err != nil {
		return nil, err
	}
	defer db.Exec("ROLLBACK")
	var session host.Obj
	err = eachRow(db, "SELECT * FROM session WHERE id=?", []any{sid}, func(cols []string, vals []any) error {
		if session == nil {
			session = rowObj(cols, vals)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, zcodeMissing("zcode_session_missing")
	}
	f, err := os.Create(target)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := bufio.NewWriter(f)
	line := func(v any) error {
		s, err := host.Dumps(v, host.DumpOpts{})
		if err != nil {
			return err
		}
		_, err = out.WriteString(s + "\n")
		return err
	}
	if err := line(host.Obj{{Key: "session", Value: session}}); err != nil {
		return nil, err
	}
	for _, table := range zcodeTables {
		columns := map[string]bool{}
		err := eachRow(db, "PRAGMA table_info("+table+")", nil, func(_ []string, vals []any) error {
			columns[fmt.Sprint(vals[1])] = true
			return nil
		})
		if err != nil {
			return nil, err
		}
		var query string
		switch {
		case columns["session_id"]:
			query = "SELECT * FROM " + table + " WHERE session_id=? ORDER BY rowid"
		case table == "part" && columns["message_id"]:
			query = "SELECT p.* FROM part p WHERE p.message_id IN (SELECT id FROM message WHERE session_id=?) ORDER BY p.rowid"
		default:
			continue
		}
		err = eachRow(db, query, []any{sid}, func(cols []string, vals []any) error {
			return line(host.Obj{{Key: "table", Value: table}, {Key: "record", Value: rowObj(cols, vals)}})
		})
		if err != nil {
			return nil, err
		}
	}
	if err := out.Flush(); err != nil {
		return nil, err
	}
	return session, f.Close()
}
