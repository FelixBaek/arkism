package memory

// Word (.docx) knowledge files: the text is read here with the standard
// library (zip + XML) so a person can upload the documents they already have.
// Headings become Markdown headings so the heading chunker keeps sections.
import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

const knowledgeDOCXMIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// docxPages returns the document body as one Markdown-like page.
func docxPages(raw []byte) ([]string, bool, error) {
	parts, err := zipParts(raw, func(name string) bool { return name == "word/document.xml" }, documentMaxText)
	if err != nil {
		return nil, false, err
	}
	body, ok := parts["word/document.xml"]
	if !ok {
		return nil, false, ErrInvalid
	}
	text, err := docxBodyText(bytes.NewReader(body))
	if err == nil && text == "" {
		err = ErrInvalid
	}
	return []string{text}, false, err
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
			docxStart(t, &para, &level, &inText)
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				docxWriteParagraph(&out, para.String(), level)
			}
		case xml.CharData:
			if inText {
				para.Write(t)
			}
		}
	}
	return strings.TrimSpace(out.String()), nil
}

// docxStart applies one start element to the paragraph being read.
func docxStart(t xml.StartElement, para *strings.Builder, level *int, inText *bool) {
	switch t.Name.Local {
	case "p":
		para.Reset()
		*level = 0
	case "pStyle":
		*level = docxHeadingLevel(t)
	case "t":
		*inText = true
	case "tab":
		para.WriteByte('\t')
	case "br", "cr":
		para.WriteByte('\n')
	}
}

// docxWriteParagraph writes a non-empty paragraph, as a heading when level > 0.
func docxWriteParagraph(out *strings.Builder, text string, level int) {
	line := strings.TrimSpace(text)
	if line == "" {
		return
	}
	if level > 0 {
		line = strings.Repeat("#", level) + " " + strings.ReplaceAll(line, "\n", " ")
	}
	out.WriteString(line)
	out.WriteString("\n\n")
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
