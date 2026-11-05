package memory

// File formats a person can upload as knowledge or for memory extraction. Each
// reader turns a file into Markdown-like page text with the standard library;
// the text then goes through the same privacy filters and candidate flow as
// any pasted text. An operator may name one external converter for other
// formats; without it those formats are refused as unsupported (415).
import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// fileMaxBytesDefault bounds one uploaded file unless MEMORY_FILE_MAX_BYTES says otherwise.
	fileMaxBytesDefault = 4 << 20
	// fileMaxBytesCap is the largest per-file limit an operator may set.
	fileMaxBytesCap         = 200 << 20
	fileBatchFilesDefault   = 10
	fileBatchFilesCap       = 100
	fileBatchBytesDefault   = 100 << 20
	fileBatchBytesCap       = 1 << 30
	fileVisionPagesDefault  = 8
	fileVisionPagesCap      = 200
	fileRequestJSONOverhead = 64 << 10
	// documentMaxText bounds the text read out of one file; a small zip can inflate a lot.
	documentMaxText = 32 << 20
	// documentMaxZipEntries bounds the parts listed in an OOXML file.
	documentMaxZipEntries = 10000
	xlsxMIME              = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	pptxMIME              = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
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
	".doc":  {mime: "application/msword", read: docPages},
	".xls":  {mime: "application/vnd.ms-excel", read: xlsPages},
	".ppt":  {mime: "application/vnd.ms-powerpoint", read: pptPages},
}

// Reader names a request or the operator may list, in the order they are tried.
const (
	parserBuiltin    = "builtin"
	parserConverter  = "converter"
	parserVision     = "vision"
	parserTranscribe = "transcribe"
)

var defaultParserOrder = []string{parserBuiltin, parserConverter, parserVision, parserTranscribe}

// FileSettings are the operator's upload limits and reader order
// (MEMORY_FILE_*). A zero value means the defaults: 4 MiB per file, 10 files
// and 100 MiB per batch, 8 scanned pages, readers builtin, converter, vision,
// transcribe, every configured reader allowed.
type FileSettings struct {
	MaxBytes    int64
	BatchFiles  int
	BatchBytes  int64
	VisionPages int
	// Parsers is the default order; Allowed limits what a request may name (empty: every configured reader).
	Parsers []string
	Allowed []string
	// Hosted are the external reading services the operator configured, by reader name.
	Hosted map[string]HostedParser
}

func (f FileSettings) maxBytes() int64 { return positiveOr(f.MaxBytes, fileMaxBytesDefault) }
func (f FileSettings) batchFiles() int {
	return int(positiveOr(int64(f.BatchFiles), fileBatchFilesDefault))
}
func (f FileSettings) batchBytes() int64 {
	return positiveOr(f.BatchBytes, fileBatchBytesDefault)
}
func (f FileSettings) visionPages() int {
	return int(positiveOr(int64(f.VisionPages), fileVisionPagesDefault))
}

// requestBytes fits one file as base64 plus the JSON around it.
func (f FileSettings) requestBytes() int64 {
	return int64(base64.StdEncoding.EncodedLen(int(f.maxBytes()))) + fileRequestJSONOverhead
}

// known reports whether name is a reader this server has.
func (f FileSettings) known(name string) bool {
	return slices.Contains(defaultParserOrder, name) || f.Hosted[name] != nil
}

// allowed reports whether a request may name this reader.
func (f FileSettings) allowed(name string) bool {
	return f.known(name) && (len(f.Allowed) == 0 || slices.Contains(f.Allowed, name))
}

// parserOrder is the readers to try for one file: the request's list, which
// must name only allowed readers, or the operator's order.
func (f FileSettings) parserOrder(requested []string) ([]string, error) {
	if len(requested) == 0 {
		if len(f.Parsers) > 0 {
			return f.Parsers, nil
		}
		order := slices.Clone(defaultParserOrder)
		for name := range f.Hosted {
			order = append(order, name)
		}
		slices.Sort(order[len(defaultParserOrder):])
		return order, nil
	}
	if len(requested) > 8 {
		return nil, ErrInvalid
	}
	for i, name := range requested {
		if !f.allowed(name) || slices.Contains(requested[:i], name) {
			return nil, ErrInvalid
		}
	}
	return requested, nil
}

// FileSettingsFromEnv reads MEMORY_FILE_MAX_BYTES, MEMORY_FILE_BATCH_MAX_FILES,
// MEMORY_FILE_BATCH_MAX_BYTES, MEMORY_FILE_VISION_MAX_PAGES,
// MEMORY_FILE_PARSERS, MEMORY_FILE_PARSER_ALLOWLIST and the hosted reader
// settings. Every value is bounded; a bad one stops the server from starting.
func FileSettingsFromEnv() (FileSettings, error) {
	var f FileSettings
	var err error
	var files, pages int64
	for _, v := range []struct {
		name  string
		limit int64
		dest  *int64
	}{
		{"MEMORY_FILE_MAX_BYTES", fileMaxBytesCap, &f.MaxBytes},
		{"MEMORY_FILE_BATCH_MAX_BYTES", fileBatchBytesCap, &f.BatchBytes},
		{"MEMORY_FILE_BATCH_MAX_FILES", fileBatchFilesCap, &files},
		{"MEMORY_FILE_VISION_MAX_PAGES", fileVisionPagesCap, &pages},
	} {
		if *v.dest, err = envBound(v.name, v.limit); err != nil {
			return FileSettings{}, err
		}
	}
	f.BatchFiles, f.VisionPages = int(files), int(pages)
	if f.Hosted, err = hostedParsersFromEnv(); err != nil {
		return FileSettings{}, err
	}
	if f.Parsers, err = envParserList(f, "MEMORY_FILE_PARSERS"); err != nil {
		return FileSettings{}, err
	}
	if f.Allowed, err = envParserList(f, "MEMORY_FILE_PARSER_ALLOWLIST"); err != nil {
		return FileSettings{}, err
	}
	return f, nil
}

// envBound reads a positive integer no larger than limit; unset is 0 (the default).
func envBound(name string, limit int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 1 || n > limit {
		return 0, fmt.Errorf("%s must be 1..%d: %w", name, limit, ErrInvalid)
	}
	return n, nil
}

func envParserList(f FileSettings, name string) ([]string, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if !f.known(p) || slices.Contains(out, p) {
			return nil, fmt.Errorf("%s names an unknown or repeated reader: %w", name, ErrInvalid)
		}
		out = append(out, p)
	}
	return out, nil
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
	return givenMIME(given)
}

// givenMIME is the client's MIME type for a format no built-in reader names.
func givenMIME(given string) (string, error) {
	if given == "" {
		return "application/octet-stream", nil
	}
	if !validRunLabel(given, 128) || !strings.Contains(given, "/") {
		return "", ErrInvalid
	}
	return given, nil
}

// documentPages reads a file into page texts with the built-in reader and the
// operator's converter after it; paged says each page is a page or slide of
// its own. Text formats come back normalized.
func documentPages(filename string, raw []byte) ([]string, bool, error) {
	if len(raw) == 0 {
		return nil, false, ErrInvalid
	}
	if len(raw) > fileMaxBytesCap {
		return nil, false, errBlobTooLarge
	}
	ext := strings.ToLower(path.Ext(filename))
	err := errUnsupportedFormat
	if _, known := documentFormats[ext]; known {
		var pages []string
		var paged bool
		if pages, paged, err = builtinPages(ext, raw); !parserFallsThrough(err) {
			return pages, paged, err
		}
	}
	if converterHandles(ext) {
		return converterPages(ext, raw) // the operator's converter reads what the built-in reader cannot
	}
	return nil, false, err
}

// parserFallsThrough says the next reader may try after this error: the
// reader could not read the file, as opposed to a limit, a refusal or a stop.
func parserFallsThrough(err error) bool {
	if err == nil {
		return false
	}
	for _, stop := range []error{errBlobTooLarge, ErrDenied, ErrModelBudget, errMediaDryRun, ErrSecretDetected, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, stop) {
			return false
		}
	}
	return true
}

// builtinPages reads a known format with the standard library.
func builtinPages(ext string, raw []byte) ([]string, bool, error) {
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
	}
	return nil, false, errUnsupportedFormat
}

func converterPages(ext string, raw []byte) ([]string, bool, error) {
	text, err := convertDocument(ext, raw)
	return []string{text}, false, err
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

// officeParts opens an OOXML (zip) file and reads the wanted parts, with
// their unpacked size counted against documentMaxText. Go's XML decoder does
// not expand entities declared in a DTD, so parts cannot inflate further.
func officeParts(raw []byte, want func(string) bool) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, ErrInvalid
	}
	if len(zr.File) > documentMaxZipEntries {
		return nil, errBlobTooLarge
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
		return markdownTable(rows)
	}
}

// markdownTable writes rows as a Markdown table; the first row is the header.
// The text is bounded by documentMaxText: padding short rows to the widest one
// or repeating a long shared cell could otherwise inflate a small file.
func markdownTable(rows [][]string) (string, error) {
	width := 0
	for _, row := range rows {
		width = max(width, len(row))
	}
	if width == 0 {
		return "", nil
	}
	var b strings.Builder
	for i, row := range rows {
		b.WriteString("|")
		for c := range width {
			cell := ""
			if c < len(row) {
				cell = strings.Join(strings.Fields(strings.ReplaceAll(row[c], "|", `\|`)), " ")
			}
			if b.Len()+len(cell) > documentMaxText {
				return "", errBlobTooLarge
			}
			b.WriteString(" " + cell + " |")
		}
		b.WriteString("\n")
		if i == 0 {
			b.WriteString("|" + strings.Repeat(" --- |", width) + "\n")
		}
	}
	return b.String(), nil
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
