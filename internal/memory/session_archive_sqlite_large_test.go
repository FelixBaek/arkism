package memory

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	sqlite "github.com/ncruces/go-sqlite3"
)

func TestSessionArchiveSQLiteLargeMessage(t *testing.T) { testSQLiteLargeMessage(t, nil) }
func TestPostgresSessionArchiveSQLiteLargeMessage(t *testing.T) {
	testSQLiteLargeMessage(t, isolatedPostgres(t))
}
func testSQLiteLargeMessage(t *testing.T, repo Repository) {
	f, now := archiveSearchFixture(t, repo)
	text := strings.Repeat("x", 9<<20) + " 큰행한국어단서 " + strings.Repeat("가나다", 15000)
	payload := sqliteStepBody(15, text)
	rows := []sqliteFixtureRow{{7, 15, 3, 0, payload}, {8, 14, 3, 0, sqliteStepBody(14, "다음행한국어단서")}}
	raw := sqliteArchiveFixture(t, rows)
	id := ContentHash("sqlite-large-record")
	header := uploadSQLiteArchive(t, f, *now, id, raw, SessionArchiveChunkSize)
	in := ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "한국어단서"}
	first, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, in)
	if err != nil || len(first.Items) != 1 || first.Items[0].Ref.SQLiteStep != "7" || first.Next == "" || first.Oversized != 0 || first.ScannedBytes <= archiveSearchBudget {
		t.Fatalf("large SQLite search: %+v %v", first, err)
	}
	if first.Items[0].Ref.MessageSHA256 != archiveSQLiteMessageHash(7, 15, 3, 0, "assistant", payload) {
		t.Fatal("payload hash changed")
	}
	in.After = first.Next
	next, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, in)
	if err != nil || len(next.Items) != 1 || next.Items[0].Ref.SQLiteStep != "8" {
		t.Fatal("large row lost next page", err)
	}
	read := ArchiveMessageInput{Scope: scopeA, Ref: first.Items[0].Ref, TextOffset: first.Items[0].TextOffset}
	page, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, read)
	if err != nil || page.NextOffset == nil || len(page.Text) > archiveMessagePage || strings.Contains(page.Text, "HIDDEN") {
		t.Fatal("large original read", err)
	}
	read.TextOffset = *page.NextOffset
	tail, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, read)
	if err != nil || page.Text+tail.Text != text[page.TextOffset:page.TextOffset+len(page.Text)+len(tail.Text)] {
		t.Fatal("continuation differs", err)
	}
	stored, err := f.svc.Repo.Get(testCtx, colSessionArchives, scopeA, id)
	if err != nil || stored.Revision != header.Revision || !stored.ExpiresAt.Equal(*header.ExpiresAt) {
		t.Fatal("read changed retention", err)
	}
	// A rebuilt snapshot may move SQLite pages but retains the stable row identity.
	rows = append(rows, sqliteFixtureRow{9, 14, 3, 0, sqliteStepBody(14, "새 행")})
	*now = now.Add(time.Second)
	header = uploadSQLiteArchive(t, f, *now, id, sqliteArchiveFixture(t, rows), SessionArchiveChunkSize)
	read.TextOffset = page.TextOffset
	rebuilt, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, read)
	if err != nil || rebuilt.Text != page.Text || rebuilt.Ref.MessageSHA256 != page.Ref.MessageSHA256 {
		t.Fatal("rebuilt SQLite lost original", err)
	}
	if _, err = f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeB, Ref: read.Ref}); err == nil {
		t.Fatal("foreign scope exposed")
	}
	ctx, cancel := context.WithCancel(testCtx)
	cancel()
	if out, err := f.svc.ReadSessionArchiveMessage(ctx, f.agent, read); err == nil || out.Text != "" {
		t.Fatal("cancelled read returned original", err)
	}
	*now = header.ExpiresAt.Add(1)
	if _, err = f.svc.ReadSessionArchiveMessage(testCtx, f.agent, read); !errors.Is(err, ErrConflict) {
		t.Fatal("expired original exposed", err)
	}
}
func TestArchiveProtoStreamParity(t *testing.T) {
	for _, kind := range []int64{14, 15} {
		good := sqliteStepBody(kind, "한국어 원문 😀")
		cases := [][]byte{good, append(append([]byte(nil), good...), 0xff), append(append([]byte(nil), good...), sqliteProtoNumber(1, uint64(kind))...), {0}, {0xff}, sqliteStepBody(kind, string([]byte{0xff}))}
		for _, value := range cases {
			expected, role, valid := archiveAntigravityText(kind, 3, 0, value)
			span, gotRole, err := archiveAntigravityStream(bytes.NewReader(value), int64(len(value)), kind, 3, 0)
			actual := ""
			if err == nil {
				b, e := io.ReadAll(&archiveUTF8Reader{source: io.NewSectionReader(bytes.NewReader(value), span.start, span.size)})
				err = e
				actual = string(b)
			}
			if valid && (err != nil || expected != actual || role != gotRole) || !valid && err == nil {
				t.Fatal("protobuf stream differs", kind, err)
			}
		}
	}
}
func TestArchiveUTF8ReaderBoundaries(t *testing.T) {
	for _, text := range []string{"", "한국어😀", strings.Repeat("a", 32767) + "😀끝", "a\xff", "a\xe3\x81", "a\xed\xa0\x80"} {
		for _, width := range []int{1, 2, 3, 32768} {
			actual, err := io.ReadAll(&archiveUTF8Reader{source: archiveSplitReader{strings.NewReader(text), width}})
			valid := utf8.ValidString(text)
			if valid && (err != nil || string(actual) != text) || !valid && err == nil {
				t.Fatalf("UTF-8 boundary width=%d input=%q err=%v", width, text, err)
			}
		}
	}
}

func TestSessionArchiveSQLiteLargeRejectsUnsafe(t *testing.T) {
	filler := strings.Repeat("x", (8<<20)+100)
	for _, mode := range []string{"secret", "utf8", "truncated", "duplicate", "mismatch", "ambiguous", "hidden-sibling"} {
		t.Run(mode, func(t *testing.T) {
			f, now := archiveSearchFixture(t, nil)
			text := "검사단서 " + filler
			if mode == "secret" {
				text += " password: hidden"
			}
			if mode == "utf8" {
				text += "\xff"
			}
			payload := sqliteStepBody(14, text)
			kind := int64(14)
			switch mode {
			case "truncated":
				payload = payload[:len(payload)-1]
			case "duplicate":
				payload = append(payload, sqliteProtoNumber(1, 14)...)
			case "mismatch":
				kind = 15
			case "ambiguous":
				payload = append(payload, sqliteProtoBytes(20, sqliteProtoBytes(1, []byte("금지된답변")))...)
			case "hidden-sibling":
				kind = 15
				nested := append(sqliteProtoBytes(3, []byte(filler+" password: hidden")), sqliteProtoBytes(1, []byte("검사단서 안전답변"))...)
				payload = append(sqliteProtoNumber(1, 15), sqliteProtoNumber(4, 3)...)
				payload = append(payload, sqliteProtoBytes(20, nested)...)
			}
			id := ContentHash("sqlite-large-unsafe-" + mode)
			uploadSQLiteArchive(t, f, *now, id, sqliteArchiveFixture(t, []sqliteFixtureRow{{0, kind, 3, 0, payload}}), SessionArchiveChunkSize)
			out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "검사단서"})
			if err != nil || out.Oversized != 0 {
				t.Fatalf("large safety query: %+v %v", out, err)
			}
			if mode == "hidden-sibling" {
				if len(out.Items) != 1 || out.Items[0].Text != "검사단서 안전답변" || out.Secrets != 0 {
					t.Fatal("hidden sibling contaminated response")
				}
			} else {
				if len(out.Items) != 0 {
					t.Fatal("unsafe body exposed")
				}
				if mode == "secret" {
					if out.Secrets != 1 {
						t.Fatal("secret not counted")
					}
				} else if out.Invalid != 1 {
					t.Fatal("invalid not counted")
				}
			}
		})
	}
}
func FuzzArchiveProtoStreamParity(f *testing.F) {
	for _, b := range [][]byte{sqliteStepBody(14, "한국어"), sqliteStepBody(15, "😀답변"), {0xff}, {0}, sqliteStepBody(14, "\xff")} {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 65536 {
			return
		}
		for _, kind := range []int64{14, 15} {
			expected, role, valid := archiveAntigravityText(kind, 3, 0, raw)
			span, actualRole, err := archiveAntigravityStream(bytes.NewReader(raw), int64(len(raw)), kind, 3, 0)
			actual := ""
			if err == nil {
				b, e := io.ReadAll(&archiveUTF8Reader{source: io.NewSectionReader(bytes.NewReader(raw), span.start, span.size)})
				err = e
				actual = string(b)
			}
			if valid && (err != nil || actual != expected || actualRole != role) || !valid && err == nil {
				t.Fatalf("protobuf parity kind=%d err=%v", kind, err)
			}
		}
	})
}
func FuzzArchiveUTF8Reader(f *testing.F) {
	for _, b := range [][]byte{[]byte("한국어😀"), {0xff}, {0xed, 0xa0, 0x80}, {0xc2}, []byte(strings.Repeat("a", 17) + "끝")} {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 65536 {
			return
		}
		got, err := io.ReadAll(&archiveUTF8Reader{source: archiveSplitReader{bytes.NewReader(raw), 7}})
		if utf8.Valid(raw) && (err != nil || !bytes.Equal(got, raw)) || !utf8.Valid(raw) && err == nil {
			t.Fatalf("UTF-8 parity: %v", err)
		}
	})
}

func TestSessionArchiveSQLiteLargeTypeAndReadFailures(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	raw := sqliteArchiveFixture(t, []sqliteFixtureRow{{0, 14, 3, 0, sqliteStepBody(14, strings.Repeat("x", 9<<20))}})
	id := ContentHash("sqlite-large-io-failures")
	uploadSQLiteArchive(t, f, *now, id, raw, SessionArchiveChunkSize)
	for _, failure := range []error{ErrUnavailable, context.Canceled} {
		ctx, cancel := context.WithCancel(testCtx)
		wrapper := &archiveLargeReadFailure{archiveFake: archiveFake{f.repo}, remaining: 14, fail: failure}
		if failure == context.Canceled {
			wrapper.cancel = cancel
		}
		f.svc.Repo = wrapper
		out, err := f.svc.SearchSessionArchiveMessages(ctx, f.agent, ArchiveSearchInput{Scope: scopeA, ArchiveID: id})
		cancel()
		if !errors.Is(err, failure) || len(out.Items) != 0 || out.Invalid != 0 {
			t.Fatalf("SQLite read failure hidden: expected=%v got=%v remaining=%d", failure, err, wrapper.remaining)
		}
	}
	f.svc.Repo = archiveFake{f.repo}
	path := filepath.Join(t.TempDir(), "type.db")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("UPDATE steps SET step_payload=CAST(step_payload AS TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	id = ContentHash("sqlite-large-not-blob")
	header := uploadSQLiteArchive(t, f, *now, id, raw, SessionArchiveChunkSize)
	out, err := f.svc.SearchSessionArchiveMessages(testCtx, f.agent, ArchiveSearchInput{Scope: scopeA, ArchiveID: id})
	if err != nil || len(out.Items) != 0 || out.Invalid != 1 {
		t.Fatalf("non-BLOB was interpreted: %+v %v", out, err)
	}
	if _, err = f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ArchiveMessageRef{ArchiveID: id, SHA256: header.SHA256, SQLiteStep: "0"}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("non-BLOB direct read accepted", err)
	}
}
