package memory

// File formats a person can upload as knowledge or for memory extraction. Each
// reader turns a file into Markdown-like page text with the standard library;
// the text then goes through the same privacy filters and candidate flow as
// any pasted text. An operator may name one external converter for other
// formats; without it those formats are refused as unsupported (415).
import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	// documentMaxFile bounds an uploaded file before it is read.
	documentMaxFile = 4 << 20
	// documentMaxText bounds the text read out of one file; a small zip can inflate a lot.
	documentMaxText = 32 << 20
	xlsxMIME        = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	pptxMIME        = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
)

var errUnsupportedFormat = errors.New("unsupported document format")

// documentFormat is one known extension; read nil means UTF-8 text kept as is.
type documentFormat struct {
	mime string
	read func([]byte) ([]string, bool, error)
}

var documentFormats = map[string]documentFormat{
	".txt": {mime: "text/plain"}, ".md": {mime: "text/markdown"}, ".json": {mime: "application/json"},
	".docx": {mime: knowledgeDOCXMIME, read: docxPages},
	".pdf":  {mime: "application/pdf", read: pdfPages},
	".html": {mime: "text/html", read: textPage(htmlText)}, ".htm": {mime: "text/html", read: textPage(htmlText)},
	".csv": {mime: "text/csv", read: textPage(csvTable(','))}, ".tsv": {mime: "text/tab-separated-values", read: textPage(csvTable('\t'))},
	".xlsx": {mime: xlsxMIME, read: xlsxPages},
	".pptx": {mime: pptxMIME, read: pptxPages},
	".eml":  {mime: "message/rfc822", read: textPage(emlText)},
}

// documentMIME checks a file name and the MIME type the client gave (may be
// empty) and returns the type to record.
func documentMIME(filename, given string) (string, error) {
	ext := strings.ToLower(path.Ext(filename))
	if f, ok := documentFormats[ext]; ok {
		if given != "" && given != f.mime {
			return "", ErrInvalid
		}
		return f.mime, nil
	}
	if !converterHandles(ext) {
		return "", errUnsupportedFormat
	}
	if given == "" {
		return "application/octet-stream", nil
	}
	if !validRunLabel(given, 128) || !strings.Contains(given, "/") {
		return "", ErrInvalid
	}
	return given, nil
}

// documentPages reads a file into page texts; paged says each page is a page
// or slide of its own. Text formats come back normalized.
func documentPages(filename string, raw []byte) ([]string, bool, error) {
	if len(raw) == 0 {
		return nil, false, ErrInvalid
	}
	if len(raw) > documentMaxFile {
		return nil, false, errBlobTooLarge
	}
	ext := strings.ToLower(path.Ext(filename))
	f, known := documentFormats[ext]
	switch {
	case known && f.read != nil:
		return f.read(raw)
	case known:
		if !utf8.Valid(raw) || slices.Contains(raw, 0) {
			return nil, false, ErrInvalid
		}
		text, failed := normalizeKnowledgeContent(string(raw), ext)
		if failed {
			return nil, false, ErrInvalid
		}
		return []string{text}, false, nil
	case converterHandles(ext):
		text, err := convertDocument(ext, raw)
		return []string{text}, false, err
	}
	return nil, false, errUnsupportedFormat
}

// documentText joins a file's pages into one Markdown text.
func documentText(filename string, raw []byte) (string, error) {
	pages, _, err := documentPages(filename, raw)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(strings.Join(pages, "\n\n"))
	if text == "" {
		return "", ErrInvalid
	}
	return text, nil
}

func pdfPages(raw []byte) ([]string, bool, error) {
	pages, err := pdfPagesText(raw)
	return pages, true, err
}

// textPage reads a UTF-8 text format through convert into one page.
func textPage(convert func(string) (string, error)) func([]byte) ([]string, bool, error) {
	return func(raw []byte) ([]string, bool, error) {
		if !utf8.Valid(raw) || slices.Contains(raw, 0) {
			return nil, false, ErrInvalid
		}
		text, err := convert(strings.ReplaceAll(string(raw), "\r\n", "\n"))
		if err == nil && strings.TrimSpace(text) == "" {
			err = ErrInvalid
		}
		return []string{strings.TrimSpace(text)}, false, err
	}
}

// officeParts opens an Office (zip) file and reads the wanted parts, with
// their unpacked size counted against documentMaxText.
func officeParts(raw []byte, want func(string) bool) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, ErrInvalid
	}
	out, budget := map[string][]byte{}, int64(documentMaxText)
	for _, f := range zr.File {
		if !want(f.Name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, ErrInvalid
		}
		data, err := io.ReadAll(io.LimitReader(rc, budget+1))
		rc.Close()
		if err != nil {
			return nil, ErrInvalid
		}
		if budget -= int64(len(data)); budget < 0 {
			return nil, errBlobTooLarge
		}
		out[f.Name] = data
	}
	return out, nil
}

// csvTable reads comma or tab separated rows into a Markdown table.
func csvTable(sep rune) func(string) (string, error) {
	return func(text string) (string, error) {
		r := csv.NewReader(strings.NewReader(text))
		r.Comma, r.FieldsPerRecord, r.LazyQuotes = sep, -1, true
		rows, err := r.ReadAll()
		if err != nil {
			return "", ErrInvalid
		}
		return markdownTable(rows), nil
	}
}

// markdownTable writes rows as a Markdown table; the first row is the header.
func markdownTable(rows [][]string) string {
	width := 0
	for _, row := range rows {
		width = max(width, len(row))
	}
	if width == 0 {
		return ""
	}
	var b strings.Builder
	for i, row := range rows {
		b.WriteString("|")
		for c := range width {
			cell := ""
			if c < len(row) {
				cell = strings.Join(strings.Fields(strings.ReplaceAll(row[c], "|", `\|`)), " ")
			}
			b.WriteString(" " + cell + " |")
		}
		b.WriteString("\n")
		if i == 0 {
			b.WriteString("|" + strings.Repeat(" --- |", width) + "\n")
		}
	}
	return b.String()
}

// pageNumber reads N out of a part name such as ppt/slides/slide12.xml.
func pageNumber(name, prefix string) (int, bool) {
	rest, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return 0, false
	}
	rest, ok = strings.CutSuffix(rest, ".xml")
	n := 0
	if _, err := fmt.Sscanf(rest, "%d", &n); err != nil || !ok || fmt.Sprint(n) != rest || n < 1 {
		return 0, false
	}
	return n, true
}
