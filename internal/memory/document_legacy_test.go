package memory

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func legacyFixture(t *testing.T, name string) []byte {
	t.Helper()
	f, err := os.Open("testdata/legacy/" + name + ".gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLegacyOfficeReaders(t *testing.T) {
	for _, tc := range []struct {
		name  string
		paged bool
		want  []string
	}{
		{"memo.doc", false, []string{"Legacy memo", "배포 전에 항상 백업을 확인한다.", "Second paragraph."}},
		{"sheet.xls", true, []string{"## ", "| name | rule |", "| 백업 | 매일 확인 |", "| Deploy | Friday never |"}},
		{"deck.ppt", true, []string{"Release checklist", "금요일에는 배포하지 않는다.", "Second slide note"}},
	} {
		pages, paged, err := documentPages(tc.name, legacyFixture(t, tc.name))
		if err != nil || paged != tc.paged {
			t.Fatalf("%s: %v %v", tc.name, paged, err)
		}
		text := strings.Join(pages, "\n\n")
		for _, w := range tc.want {
			if !strings.Contains(text, w) {
				t.Fatalf("%s: missing %q in %q", tc.name, w, text)
			}
		}
		if strings.Contains(text, "Click to") {
			t.Fatalf("%s: master placeholder text kept: %q", tc.name, text)
		}
	}
	if pages, _, _ := documentPages("deck.ppt", legacyFixture(t, "deck.ppt")); len(pages) != 2 {
		t.Fatalf("one page per slide: %q", pages)
	}
	if mime, err := documentMIME("a.XLS", ""); err != nil || mime != "application/vnd.ms-excel" {
		t.Fatal(mime, err)
	}
}

func TestLegacyOfficeReadersRefuseDamage(t *testing.T) {
	raw := legacyFixture(t, "memo.doc")
	for _, bad := range [][]byte{
		raw[:600],
		append(append([]byte{}, raw[:0x30]...), bytes.Repeat([]byte{0xFF}, len(raw)-0x30)...),
		[]byte("plain text"),
	} {
		if _, _, err := documentPages("x.doc", bad); err == nil {
			t.Fatal("damaged file read")
		}
	}
	// A FAT entry pointing at itself is a loop, not an endless read.
	loop := append([]byte{}, raw...)
	fatSector := int(le32(loop, 0x4C))
	off := (fatSector + 1) * 512
	for i := 0; i < 128; i++ {
		copy(loop[off+i*4:], []byte{byte(i), 0, 0, 0})
	}
	if _, _, err := documentPages("x.doc", loop); !errors.Is(err, ErrInvalid) {
		t.Fatalf("loop: %v", err)
	}
}

func TestXLSSharedStringContinues(t *testing.T) {
	// "ab" + "가나": the string starts narrow, then continues wide in the next record.
	first := []byte{2, 0, 0, 0, 2, 0, 0, 0, 4, 0, 0, 'a', 'b'}
	next := []byte{1, 0x00, 0xAC, 0x98, 0xB0, 1, 0, 0, 'z'}
	got := xlsSST([][]byte{first, next})
	if len(got) != 2 || got[0] != "ab가나" || got[1] != "z" {
		t.Fatalf("%q", got)
	}
}
