package memory

// Word (.docx) knowledge files: the text is read here with the standard
// library (zip + XML) so a person can upload the documents they already have.
// Headings become Markdown headings so the heading chunker keeps sections.
import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

const (
	knowledgeDOCXMIME     = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	knowledgeDOCXMaxBytes = 4 << 20
	// The unpacked document.xml is bounded too: a small zip can inflate a lot.
	knowledgeDOCXMaxXML = 32 << 20
)

// knowledgeDOCXText returns the document body as Markdown-like text.
func knowledgeDOCXText(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) == 0 || len(raw) > knowledgeDOCXMaxBytes {
		return "", ErrInvalid
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", ErrInvalid
	}
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", ErrInvalid
		}
		defer rc.Close()
		return docxBodyText(io.LimitReader(rc, knowledgeDOCXMaxXML))
	}
	return "", ErrInvalid
}

// docxBodyText walks w:p paragraphs: w:t is text, w:tab a tab, w:br a line
// break, and a HeadingN or Title paragraph style becomes a Markdown heading.
func docxBodyText(r io.Reader) (string, error) {
	dec := xml.NewDecoder(r)
	var out, para strings.Builder
	level := 0
	inText := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", ErrInvalid
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				para.Reset()
				level = 0
			case "pStyle":
				level = docxHeadingLevel(t)
			case "t":
				inText = true
			case "tab":
				para.WriteByte('\t')
			case "br", "cr":
				para.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				line := strings.TrimSpace(para.String())
				if line == "" {
					continue
				}
				if level > 0 {
					line = strings.Repeat("#", level) + " " + strings.ReplaceAll(line, "\n", " ")
				}
				out.WriteString(line)
				out.WriteString("\n\n")
			}
		case xml.CharData:
			if inText {
				para.Write(t)
			}
		}
	}
	return strings.TrimSpace(out.String()), nil
}

func docxHeadingLevel(t xml.StartElement) int {
	for _, a := range t.Attr {
		if a.Name.Local != "val" {
			continue
		}
		v := strings.ToLower(a.Value)
		if v == "title" {
			return 1
		}
		if n, ok := strings.CutPrefix(v, "heading"); ok && len(n) == 1 && n[0] >= '1' && n[0] <= '6' {
			return int(n[0] - '0')
		}
	}
	return 0
}
