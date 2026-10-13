package memory

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sqlite "github.com/ncruces/go-sqlite3"
)

func sqliteProtoNumber(key, value uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, key<<3), value)
}
func sqliteProtoBytes(key uint64, value []byte) []byte {
	out := binary.AppendUvarint(nil, key<<3|2)
	out = binary.AppendUvarint(out, uint64(len(value)))
	return append(out, value...)
}
func sqliteStepBody(kind int64, text string) []byte {
	field, textField := uint64(19), uint64(2)
	if kind == 15 {
		field, textField = 20, 1
	}
	nested := sqliteProtoBytes(textField, []byte(text))
	if kind == 15 {
		nested = append(nested, sqliteProtoBytes(3, []byte("HIDDEN_THINKING"))...)
		nested = append(nested, sqliteProtoBytes(7, []byte("HIDDEN_TOOL_CALL"))...)
	}
	out := append(sqliteProtoNumber(1, uint64(kind)), sqliteProtoNumber(4, 3)...)
	return append(out, sqliteProtoBytes(field, nested)...)
}

type sqliteFixtureRow struct {
	index, kind, status, format int64
	payload                     []byte
}

func sqliteArchiveFixture(t *testing.T, rows []sqliteFixtureRow) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.db")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(`CREATE TABLE trajectory_meta(trajectory_id TEXT,cascade_id TEXT); INSERT INTO trajectory_meta VALUES('search-session','search-session'); CREATE TABLE steps(idx INTEGER PRIMARY KEY,step_type INTEGER,status INTEGER,step_format INTEGER,step_payload BLOB);`); err != nil {
		t.Fatal(err)
	}
	stmt, _, err := db.Prepare(`INSERT INTO steps VALUES(?1,?2,?3,?4,?5)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		for i, v := range []int64{r.index, r.kind, r.status, r.format} {
			if err = stmt.BindInt64(i+1, v); err != nil {
				t.Fatal(err)
			}
		}
		if err = stmt.BindBlob(5, r.payload); err != nil {
			t.Fatal(err)
		}
		if stmt.Step() || stmt.Err() != nil {
			t.Fatal(stmt.Err())
		}
		if err = stmt.Reset(); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func uploadSQLiteArchive(t *testing.T, f *fixture, now time.Time, id string, raw []byte, chunkSize int) SessionArchiveCommitOutput {
	t.Helper()
	var chunks []string
	for at := 0; at < len(raw); at += chunkSize {
		part := raw[at:min(at+chunkSize, len(raw))]
		row, err := f.svc.UploadSessionArchiveChunk(testCtx, f.human, SessionArchiveChunkInput{Scope: scopeA, ParentID: id, SHA256: ContentHash(string(part)), Data: base64.StdEncoding.EncodeToString(part)})
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, row.ID)
	}
	out, err := f.svc.CommitSessionArchive(testCtx, f.human, SessionArchiveCommitInput{Scope: scopeA, ID: id, SessionID: "search-session", Device: "test", Origin: "antigravity-cli", Format: "sqlite_snapshot", ObservedAt: now, SHA256: ContentHash(string(raw)), Chunks: chunks})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestSessionArchiveSQLiteSearch(t *testing.T) { testArchiveSQLiteSearch(t, nil) }
func TestPostgresSessionArchiveSQLiteSearch(t *testing.T) {
	testArchiveSQLiteSearch(t, isolatedPostgres(t))
}
func testArchiveSQLiteSearch(t *testing.T, repo Repository) {
	f, now := archiveSearchFixture(t, repo)
	long := strings.Repeat("한국어 답변 ", 10000)
	raw := sqliteArchiveFixture(t, []sqliteFixtureRow{
		{0, 14, 3, 0, sqliteStepBody(14, "한국어 질문")},
		{1, 15, 3, 0, sqliteStepBody(15, long)},
		{2, 101, 3, 0, sqliteStepBody(101, "HIDDEN_SYSTEM")},
		{3, 132, 3, 0, sqliteStepBody(132, "HIDDEN_TOOL")},
		{4, 15, 2, 0, sqliteStepBody(15, "HIDDEN_PENDING")},
		{5, 14, 3, 0, []byte{0xff}},
		{6, 14, 3, 0, sqliteStepBody(15, "HIDDEN_MISMATCH")},
		{7, 14, 3, 0, sqliteStepBody(14, "ghp_"+strings.Repeat("a", 36))},
		{8, 15, 3, 1, sqliteStepBody(15, "HIDDEN_FORMAT")},
	})
	id := ContentHash("sqlite-search")
	header := uploadSQLiteArchive(t, f, *now, id, raw, 10000)
	in := ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "한국어", Limit: 1}
	var items []ArchiveMessageView
	invalid, secrets, unsupported := 0, 0, 0
	for page := 0; page < 10; page++ {
		out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, in)
		if err != nil {
			t.Fatal(err)
		}
		if out.ScannedBytes > archiveSearchBudget || out.Unsupported != 0 {
			t.Fatal("unsupported/budget", out)
		}
		items = append(items, out.Items...)
		invalid += out.Invalid
		secrets += out.Secrets
		unsupported += out.UnsupportedRecords
		if out.Next == "" {
			break
		}
		in.After = out.Next
	}
	if len(items) != 2 || invalid != 2 || secrets != 1 || unsupported != 1 {
		t.Fatalf("counts: items=%d invalid=%d secret=%d unsupported=%d", len(items), invalid, secrets, unsupported)
	}
	for i, item := range items {
		if item.Ref.SQLiteStep != strconv.Itoa(i) || item.Ref.MessageSHA256 == "" || item.Role != []string{"user", "assistant"}[i] {
			t.Fatal("bad reference", item.Ref)
		}
		var text string
		offset := 0
		for {
			out, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: item.Ref, TextOffset: offset})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.Text, "HIDDEN") {
				t.Fatal("internal content exposed")
			}
			text += out.Text
			if out.NextOffset == nil {
				break
			}
			offset = *out.NextOffset
		}
		if text != []string{"한국어 질문", long}[i] {
			t.Fatal("message paging changed text")
		}
	}
	before, err := f.svc.Repo.Get(testCtx, colSessionArchives, scopeA, id)
	if err != nil {
		t.Fatal(err)
	}
	if before.ExpiresAt == nil || !before.ExpiresAt.Equal(*header.ExpiresAt) {
		t.Fatal("search extended retention")
	}
	// The normal source path must accept and preserve the SQLite reference.
	source, err := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "source from sqlite", ArchiveMessages: []ArchiveMessageRef{items[0].Ref}}))
	if err != nil || len(source.ArchiveMessages) != 1 || source.ArchiveMessages[0] != items[0].Ref {
		t.Fatal("source provenance", err)
	}
	work := WorkCheckpointSaveInput{Scope: scopeA, ID: "sqlite-work", EventID: "first", Package: workCheckpointPackage(), ArchiveMessages: []ArchiveMessageRef{items[0].Ref, items[1].Ref}}
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, work); err != nil {
		t.Fatal(err)
	}
	view, err := f.svc.GetWorkCheckpoint(testCtx, f.agent, WorkCheckpointGetInput{Scope: scopeA, ID: work.ID})
	if err != nil || len(view.ArchiveMessages) != 2 || view.ArchiveMessages[0] != items[0].Ref || view.ArchiveMessages[1] != items[1].Ref {
		t.Fatal("work lost distinct SQLite rows", err)
	}
	ref := items[0].Ref
	ref.SQLiteStep = "01"
	if _, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ref}); !errors.Is(err, ErrInvalid) {
		t.Fatal("noncanonical row accepted", err)
	}
	if _, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeB, Ref: items[0].Ref}); err == nil {
		t.Fatal("scope escape")
	}
	*now = now.Add(8 * 24 * time.Hour)
	if _, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: items[0].Ref}); !errors.Is(err, ErrConflict) {
		t.Fatal("expired SQLite read", err)
	}
}
func TestSessionArchiveSQLiteStableRow(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	id := ContentHash("sqlite-stable")
	row := sqliteFixtureRow{12, 14, 3, 0, sqliteStepBody(14, "원문 유지")}
	raw := sqliteArchiveFixture(t, []sqliteFixtureRow{row})
	uploadSQLiteArchive(t, f, *now, id, raw, SessionArchiveChunkSize)
	out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "원문"})
	if err != nil || len(out.Items) != 1 {
		t.Fatal(err)
	}
	ref := out.Items[0].Ref
	raw = sqliteArchiveFixture(t, []sqliteFixtureRow{{0, 15, 3, 0, sqliteStepBody(15, "앞에 새 행")}, row})
	*now = now.Add(time.Second)
	uploadSQLiteArchive(t, f, *now, id, raw, SessionArchiveChunkSize)
	view, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ref})
	if err != nil || view.Text != "원문 유지" || view.Ref.SHA256 == ref.SHA256 {
		t.Fatal("stable row failed", err)
	}
	row.payload = sqliteStepBody(14, "변경된 원문")
	raw = sqliteArchiveFixture(t, []sqliteFixtureRow{row})
	*now = now.Add(time.Second)
	uploadSQLiteArchive(t, f, *now, id, raw, SessionArchiveChunkSize)
	if _, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ref}); !errors.Is(err, ErrConflict) {
		t.Fatal("changed row accepted", err)
	}
}
func TestAntigravitySafeFields(t *testing.T) {
	for _, kind := range []int64{14, 15} {
		p := sqliteStepBody(kind, "정확한 본문")
		text, _, ok := archiveAntigravityText(kind, 3, 0, p)
		if !ok || text != "정확한 본문" {
			t.Fatal("body lost")
		}
		for _, bad := range [][]byte{append(bytes.Clone(p), sqliteProtoNumber(1, uint64(kind))...), append(bytes.Clone(p), 0), p[:len(p)-1]} {
			if _, _, valid := archiveAntigravityText(kind, 3, 0, bad); valid {
				t.Fatal("malformed/duplicate accepted")
			}
		}
	}
}

func TestAntigravityHashPreservesTrailingByte(t *testing.T) {
	payload := sqliteStepBody(14, "본문\n")
	if payload[len(payload)-1] != '\n' {
		t.Fatal("fixture needs final LF")
	}
	if archiveSQLiteMessageHash(0, 14, 3, 0, "user", payload) == archiveSQLiteMessageHash(0, 14, 3, 0, "user", payload[:len(payload)-1]) {
		t.Fatal("binary payload trailing byte ignored")
	}
}

func TestSessionArchiveSQLiteBudgetAndOversized(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	rows := []sqliteFixtureRow{{0, 14, 3, 0, sqliteStepBody(14, "찾기 "+strings.Repeat("x", archiveRecordLimit))}}
	for i := 1; i <= 12; i++ {
		rows = append(rows, sqliteFixtureRow{int64(i), 14, 3, 0, sqliteStepBody(14, "찾기 "+strings.Repeat("a", 1<<20))})
	}
	raw := sqliteArchiveFixture(t, rows)
	id := ContentHash("sqlite-budget")
	uploadSQLiteArchive(t, f, *now, id, raw, SessionArchiveChunkSize)
	in := ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "찾기", Limit: 50}
	count, oversized, pages := 0, 0, 0
	for ; pages < 20; pages++ {
		out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, in)
		if err != nil {
			t.Fatal(err)
		}
		if pages == 0 {
			if out.ScannedBytes <= archiveSearchBudget || len(out.Items) != 1 || out.Items[0].Ref.SQLiteStep != "0" {
				t.Fatal("large row cost or result missing")
			}
		} else if out.ScannedBytes > archiveSearchBudget {
			t.Fatal("small row page budget exceeded")
		}
		count += len(out.Items)
		oversized += out.Oversized
		if out.Next == "" {
			break
		}
		if out.Next == in.After {
			t.Fatal("cursor made no progress")
		}
		in.After = out.Next
	}
	if count != 13 || oversized != 0 || pages == 0 || pages == 20 {
		t.Fatalf("count=%d oversized=%d pages=%d", count, oversized, pages)
	}
}

func TestSessionArchiveSQLiteUnsupportedSchema(t *testing.T) {
	for _, mode := range []string{"malformed", "session", "view", "descending-key", "without-rowid"} {
		t.Run(mode, func(t *testing.T) {
			f, now := archiveSearchFixture(t, nil)
			raw := []byte("SQLite format 3\x00broken")
			if mode != "malformed" {
				raw = sqliteArchiveFixture(t, nil)
				path := filepath.Join(t.TempDir(), "changed.db")
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				db, err := sqlite.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				sql := "UPDATE trajectory_meta SET trajectory_id='foreign',cascade_id='foreign'"
				if mode == "view" {
					sql = "ALTER TABLE steps RENAME TO hidden; CREATE VIEW steps AS SELECT * FROM hidden"
				}
				if mode == "descending-key" {
					sql = "DROP TABLE steps; CREATE TABLE steps(idx INTEGER PRIMARY KEY DESC,step_type INTEGER,status INTEGER,step_format INTEGER,step_payload BLOB)"
				}
				if mode == "without-rowid" {
					sql = "DROP TABLE steps; CREATE TABLE steps(idx INTEGER PRIMARY KEY,step_type INTEGER,status INTEGER,step_format INTEGER,step_payload BLOB) WITHOUT ROWID"
				}
				if err = db.Exec(sql); err != nil {
					t.Fatal(err)
				}
				if err = db.Close(); err != nil {
					t.Fatal(err)
				}
				raw, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			id := ContentHash(mode)
			uploadSQLiteArchive(t, f, *now, id, raw, SessionArchiveChunkSize)
			out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "찾기"})
			if err != nil || out.Unsupported != 1 || len(out.Items) != 0 {
				t.Fatalf("unsupported=%d error=%v", out.Unsupported, err)
			}
		})
	}
}

func TestPostgresSessionArchiveSQLiteResponseFence(t *testing.T) {
	for _, mode := range []string{"search", "read"} {
		for _, change := range []string{"append", "expire"} {
			t.Run(mode+"/"+change, func(t *testing.T) {
				p := isolatedPostgres(t)
				f, now := archiveSearchFixture(t, p)
				id := ContentHash(mode + change)
				rows := []sqliteFixtureRow{{0, 14, 3, 0, sqliteStepBody(14, "찾기")}}
				raw := sqliteArchiveFixture(t, rows)
				uploadSQLiteArchive(t, f, *now, id, raw, SessionArchiveChunkSize)
				ref := ArchiveMessageRef{ArchiveID: id, SHA256: ContentHash(string(raw)), SQLiteStep: "0"}
				mutator := &Service{Repo: p, Auth: f.auth, SessionArchiveKey: f.svc.SessionArchiveKey, SessionArchiveNow: f.svc.SessionArchiveNow}
				f.svc.Repo = &archiveSearchFenceMutation{Postgres: p, before: func() {
					if change == "expire" {
						*now = now.Add(SessionArchiveTTL)
					} else {
						other := *f
						other.svc = mutator
						*now = now.Add(time.Second)
						rows = append(rows, sqliteFixtureRow{1, 14, 3, 0, sqliteStepBody(14, "추가")})
						uploadSQLiteArchive(t, &other, *now, id, sqliteArchiveFixture(t, rows), SessionArchiveChunkSize)
					}
				}}
				if mode == "search" {
					out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "찾기"})
					if !errors.Is(err, ErrConflict) || len(out.Items) != 0 {
						t.Fatal("search leaked", err)
					}
				} else {
					out, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ref})
					if !errors.Is(err, ErrConflict) || out.Text != "" {
						t.Fatal("read leaked", err)
					}
				}
			})
		}
	}
}
