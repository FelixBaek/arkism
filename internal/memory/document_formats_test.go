//go:build linux

package memory

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func zipOf(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testXLSX(t *testing.T) []byte {
	return zipOf(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="일정" sheetId="1" r:id="rId2"/><sheet name="Costs" sheetId="2" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Target="/xl/worksheets/sheet2.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>항목</t></si><si><r><t>배포</t></r><r><t> 날짜</t></r><rPh><t>はいふ</t></rPh></si></sst>`,
		"xl/worksheets/sheet2.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="C1" t="s"><v>1</v></c></row><row r="2"><c r="A2" t="inlineStr"><is><t>a|b</t></is></c><c r="C2"><v>2026</v></c></row></sheetData></worksheet>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="b"><v>1</v></c></row></sheetData></worksheet>`,
	})
}

func TestDocumentReaders(t *testing.T) {
	pptx := zipOf(t, map[string]string{
		"ppt/slides/slide10.xml":           `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:r><a:t>열 번째</a:t></a:r></a:p></p:sld>`,
		"ppt/slides/slide2.xml":            `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:r><a:t>Second</a:t></a:r><a:r><a:t> slide</a:t></a:r></a:p><a:p><a:r><a:t>line two</a:t></a:r></a:p></p:sld>`,
		"ppt/slides/_rels/slide2.xml.rels": `<x/>`,
	})
	eml := "Subject: =?UTF-8?B?7ZqM7J2Y66Gd?=\r\nFrom: a@example.com\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=XX\r\n\r\n--XX\r\nContent-Type: multipart/alternative; boundary=YY\r\n\r\n--YY\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n=EB=B0=B0=ED=8F=AC=EB=8A=94 =\r\n=EA=B8=88=EC=9A=94=EC=9D=BC\r\n--YY\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>html copy</p>\r\n--YY--\r\n--XX\r\nContent-Type: application/pdf; name=\"plan.pdf\"\r\nContent-Disposition: attachment; filename=\"plan.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0=\r\n--XX--\r\n"
	cases := []struct {
		name  string
		raw   []byte
		want  []string
		pages int
	}{
		{"page.html", []byte(`<html><head><title>x</title><style>p{}</style></head><body><h2>규칙</h2><script>if (a<b) alert(1)</script><p>배포 &amp; 테스트</p><!-- hidden --><ul><li>하나</li></ul><table><tr><td>a</td><td>b</td></tr></table></body></html>`), []string{"## 규칙", "배포 & 테스트", "- 하나", "| a | b"}, 1},
		{"data.csv", []byte("name,note\n\"Kim, J\",\"a|b\"\nLee\n"), []string{"| name | note |", "| --- | --- |", `| Kim, J | a\|b |`, "| Lee |  |"}, 1},
		{"data.tsv", []byte("a\tb\n1\t2\n"), []string{"| a | b |", "| 1 | 2 |"}, 1},
		{"book.xlsx", testXLSX(t), []string{"## 일정", "| 항목 |  | 배포 날짜 |", `| a\|b |  | 2026 |`, "## Costs", "| TRUE |"}, 2},
		{"deck.pptx", pptx, []string{"Second slide\nline two", "열 번째"}, 2},
		{"mail.eml", []byte(eml), []string{"Subject: 회의록", "From: a@example.com", "배포는 금요일", "Attachment: plan.pdf"}, 1},
	}
	for _, c := range cases {
		pages, _, err := documentPages(c.name, c.raw)
		joined := strings.Join(pages, "\n\n")
		if err != nil || len(pages) != c.pages {
			t.Fatalf("%s: %d pages %v\n%s", c.name, len(pages), err, joined)
		}
		for _, w := range c.want {
			if !strings.Contains(joined, w) {
				t.Fatalf("%s misses %q:\n%s", c.name, w, joined)
			}
		}
		for _, bad := range []string{"alert", "hidden", "p{}", "html copy", "はいふ", "JVBERi0"} {
			if strings.Contains(joined, bad) {
				t.Fatalf("%s kept %q:\n%s", c.name, bad, joined)
			}
		}
	}
	if pages, _, _ := documentPages("deck.pptx", pptx); pages[0] != "Second slide\nline two" {
		t.Fatalf("slide order %q", pages)
	}
	for name, raw := range map[string][]byte{"a.xlsx": []byte("not a zip"), "a.pptx": zipOf(t, map[string]string{"x": "y"}), "a.csv": {0xff}, "a.eml": []byte("no headers")} {
		if _, _, err := documentPages(name, raw); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, _, err := documentPages("a.rtf", []byte("x")); !errors.Is(err, errUnsupportedFormat) {
		t.Fatalf("unknown format: %v", err)
	}
	if code, status := errorResponse(errUnsupportedFormat); code != "unsupported_media_type" || status != 415 {
		t.Fatal(code, status)
	}
}

func TestDocumentZipBombIsBounded(t *testing.T) {
	big := strings.Repeat("<a:p><a:t>x</a:t></a:p>", documentMaxText/20)
	if _, _, err := documentPages("deck.pptx", zipOf(t, map[string]string{"ppt/slides/slide1.xml": big})); !errors.Is(err, errBlobTooLarge) {
		t.Fatalf("bomb: %v", err)
	}
}

func TestDocumentConverter(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "convert")
	body := "#!/bin/sh\nprintf '# converted\\n'\nwhile IFS= read -r l; do printf '%s\\n' \"$l\"; done < \"$1\"\n[ -z \"$SECRET_TOKEN\" ] || printf 'env leaked\\n'\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SECRET_TOKEN", "x")
	t.Setenv("MEMORY_DOCUMENT_CONVERTER", script)
	t.Setenv("MEMORY_DOCUMENT_CONVERTER_EXTENSIONS", ".doc, .rtf")
	if mime, err := documentMIME("a.rtf", ""); err != nil || mime != "application/octet-stream" {
		t.Fatal(mime, err)
	}
	if _, err := documentMIME("a.exe", ""); !errors.Is(err, errUnsupportedFormat) {
		t.Fatal(err)
	}
	// Not a compound file: the built-in .doc reader fails and the converter reads it.
	text, err := documentText("a.doc", []byte("본문 한 줄\n"))
	if err != nil || text != "# converted\n본문 한 줄" {
		t.Fatalf("%q %v", text, err)
	}
	loop := filepath.Join(dir, "loop")
	if err = os.WriteFile(loop, []byte("#!/bin/sh\nwhile :; do printf 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEMORY_DOCUMENT_CONVERTER", loop)
	if _, err = documentText("a.rtf", []byte("x")); !errors.Is(err, errBlobTooLarge) {
		t.Fatalf("endless output: %v", err)
	}
	t.Setenv("MEMORY_DOCUMENT_CONVERTER", "relative/convert")
	if _, err = documentText("a.rtf", []byte("x")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("relative converter: %v", err)
	}
}

func TestFileMemoriesAndKnowledgeReadNewFormats(t *testing.T) {
	quote := `"evidence_quote":"배포 날짜"`
	claim := `{"content":"배포 날짜를 표로 관리한다","kind":"semantic","semantic_kind":"fact","evidence_kind":"inference",` + quote + `,"confidence":0.7}`
	f, llm := wikiFixture(t, extractAnswer(claim), extractAnswer(), extractAnswer())
	out, err := f.svc.ExtractFileMemories(testCtx, f.human, FileMemoriesInput{Scope: scopeA, Filename: "plan.xlsx", ContentBase64: base64.StdEncoding.EncodeToString(testXLSX(t))})
	if err != nil || out.CandidatesCreated != 1 || len(llm.prompts) != 2 || !strings.Contains(llm.prompts[0], "| 항목 |") {
		t.Fatalf("xlsx extract %+v %v", out, err)
	}
	if c := f.repo.rows("memory_candidates"); len(c) != 1 || c[0].Status != "candidate" {
		t.Fatalf("candidate %+v", c)
	}
	// The privacy filter applies to converted text too.
	html := `<p>규칙</p><p>&lt;private&gt;집 주소&lt;/private&gt;</p>`
	_, err = f.svc.ExtractFileMemories(testCtx, f.human, FileMemoriesInput{Scope: scopeA, Filename: "p.html", ContentBase64: base64.StdEncoding.EncodeToString([]byte(html)), DryRun: true})
	if err != nil || strings.Contains(llm.prompts[len(llm.prompts)-1], "집 주소") {
		t.Fatalf("private html reached the model: %v %v", err, llm.prompts)
	}
	in := knowledgeFixtureInput()
	in.SourceKey, in.Filename, in.MIMEType, in.Content = "guide", "guide.html", "text/html", "<h1>배송</h1><p>사흘 안에 보내요.</p>"
	ing, err := f.svc.IngestKnowledge(testCtx, f.human, in)
	if err != nil || len(ing.Chunks) != 1 || ing.Chunks[0].Knowledge.SectionPath != "배송" || strings.Contains(ing.Chunks[0].Text, "<p>") {
		t.Fatalf("html knowledge %+v %v", ing.Chunks, err)
	}
	in.SourceKey, in.Filename, in.MIMEType, in.Content = "deck", "deck.pptx", pptxMIME, ""
	in.ContentBase64 = base64.StdEncoding.EncodeToString(zipOf(t, map[string]string{"ppt/slides/slide1.xml": `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:t>api_key=sk-abcdefghijklmnop1234</a:t></a:p></p:sld>`}))
	if _, err = f.svc.IngestKnowledge(testCtx, f.human, in); !errors.Is(err, ErrSecretDetected) {
		t.Fatalf("secret in a slide: %v", err)
	}
	in.Filename = "deck.key"
	if _, err = f.svc.IngestKnowledge(testCtx, f.human, in); !errors.Is(err, errUnsupportedFormat) {
		t.Fatalf("unknown format: %v", err)
	}
}

// TestDocumentReaderHardening covers private markers in HTML, slide order
// from the presentation list and inflation through padding or repeated parts.
func TestDocumentReaderHardening(t *testing.T) {
	text, err := documentText("p.html", []byte(`<p>공개</p><private class="x"><p>비밀 문단</p></private><p>끝</p>`))
	if err != nil || !strings.Contains(text, "<private>") || !strings.Contains(text, "</private>") {
		t.Fatalf("private marker lost: %q %v", text, err)
	}
	if masked, n := extractClean(text); n == 0 || strings.Contains(masked, "비밀") || !strings.Contains(masked, "끝") {
		t.Fatalf("private span reached the model text: %q", masked)
	}
	deck := zipOf(t, map[string]string{
		"ppt/presentation.xml":            `<p:presentation xmlns:p="p" xmlns:r="r"><p:sldIdLst><p:sldId id="257" r:id="rId9"/><p:sldId id="256" r:id="rId8"/><p:sldId id="258" r:id="rId9"/></p:sldIdLst></p:presentation>`,
		"ppt/_rels/presentation.xml.rels": `<Relationships><Relationship Id="rId8" Target="slides/slide1.xml"/><Relationship Id="rId9" Target="/ppt/slides/slide2.xml"/></Relationships>`,
		"ppt/slides/slide1.xml":           `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:t>first by number</a:t></a:p></p:sld>`,
		"ppt/slides/slide2.xml":           `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:t>first in the deck</a:t></a:p></p:sld>`,
		"ppt/slides/slide3.xml":           `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:t>not in the deck</a:t></a:p></p:sld>`,
	})
	pages, _, err := documentPages("d.pptx", deck)
	if err != nil || len(pages) != 2 || pages[0] != "first in the deck" || pages[1] != "first by number" {
		t.Fatalf("presentation order: %q %v", pages, err)
	}
	rows := strings.Repeat(`<row><c r="IV1"><v>1</v></c></row>`, xlsxMaxCells/200)
	wide := zipOf(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets><sheet name="a" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData>` + rows + `</sheetData></worksheet>`,
	})
	if _, _, err = documentPages("w.xlsx", wide); !errors.Is(err, errBlobTooLarge) {
		t.Fatalf("padded sheet: %v", err)
	}
	long := strings.Repeat("y", 1<<20)
	shared := zipOf(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets>` + strings.Repeat(`<sheet name="a" r:id="rId1"/>`, 1000) + `</sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>` + long + `</t></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row>` + strings.Repeat(`<c t="s"><v>0</v></c>`, 40) + `</row></sheetData></worksheet>`,
	})
	if _, _, err = documentPages("s.xlsx", shared); !errors.Is(err, errBlobTooLarge) {
		t.Fatalf("repeated shared string: %v", err)
	}
	if _, err = markdownTable([][]string{{"a"}, make([]string, documentMaxText/2)}); !errors.Is(err, errBlobTooLarge) {
		t.Fatalf("padded table: %v", err)
	}
	many := map[string]string{}
	for i := range documentMaxZipEntries + 1 {
		many[fmt.Sprintf("x/%d", i)] = ""
	}
	if _, _, err = documentPages("m.docx", zipOf(t, many)); !errors.Is(err, errBlobTooLarge) {
		t.Fatalf("zip entries: %v", err)
	}
}
