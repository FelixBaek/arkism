package memory

// Excel (.xlsx) and PowerPoint (.pptx) files are read like Word files: zip +
// XML with the standard library. A sheet becomes a Markdown table under its
// name; a slide becomes one page of its paragraphs.
import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
)

// xlsxMaxColumns bounds a sheet's width; cells further right are left out.
const xlsxMaxColumns = 256

// xmlWalk calls fn for every start element, end element and text of an XML part.
func xmlWalk(data []byte, fn func(tok xml.Token)) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return ErrInvalid
		}
		fn(tok)
	}
}

func xmlAttr(t xml.StartElement, local string) string {
	for _, a := range t.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// xmlParagraphs joins the text elements of each paragraph element into a line.
func xmlParagraphs(data []byte, para, text string) (string, error) {
	var out, line strings.Builder
	inText := false
	err := xmlWalk(data, func(tok xml.Token) {
		switch t := tok.(type) {
		case xml.StartElement:
			inText = t.Name.Local == text
		case xml.EndElement:
			inText = false
			if t.Name.Local == para {
				if l := strings.TrimSpace(line.String()); l != "" {
					out.WriteString(l + "\n")
				}
				line.Reset()
			}
		case xml.CharData:
			if inText {
				line.Write(t)
			}
		}
	})
	return strings.TrimSpace(out.String()), err
}

// pptxPages returns one page per slide, in slide-number order.
// shortcut: slide-number order, not the presentation's own order; read ppt/presentation.xml if decks reorder slides.
func pptxPages(raw []byte) ([]string, bool, error) {
	parts, err := officeParts(raw, func(name string) bool { _, ok := pageNumber(name, "ppt/slides/slide"); return ok })
	if err != nil {
		return nil, false, err
	}
	names := make([]string, 0, len(parts))
	for name := range parts {
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b string) int {
		x, _ := pageNumber(a, "ppt/slides/slide")
		y, _ := pageNumber(b, "ppt/slides/slide")
		return x - y
	})
	pages := []string{}
	for _, name := range names {
		text, err := xmlParagraphs(parts[name], "p", "t")
		if err != nil {
			return nil, false, err
		}
		pages = append(pages, text)
	}
	if strings.TrimSpace(strings.Join(pages, "")) == "" {
		return nil, false, ErrInvalid
	}
	return pages, true, nil
}

// xlsxPages returns one Markdown table per sheet, in workbook order.
func xlsxPages(raw []byte) ([]string, bool, error) {
	parts, err := officeParts(raw, func(name string) bool {
		return name == "xl/workbook.xml" || name == "xl/_rels/workbook.xml.rels" || name == "xl/sharedStrings.xml" || strings.HasPrefix(name, "xl/worksheets/sheet")
	})
	if err != nil {
		return nil, false, err
	}
	shared, err := xlsxSharedStrings(parts["xl/sharedStrings.xml"])
	if err != nil {
		return nil, false, err
	}
	sheets, err := xlsxSheets(parts)
	if err != nil {
		return nil, false, err
	}
	pages, text := []string{}, 0
	for _, sh := range sheets {
		rows, err := xlsxRows(parts[sh.part], shared)
		if err != nil {
			return nil, false, err
		}
		page := "## " + sh.name + "\n\n" + markdownTable(rows)
		if text += len(page); text > documentMaxText {
			return nil, false, errBlobTooLarge
		}
		pages = append(pages, strings.TrimSpace(page))
	}
	if len(pages) == 0 {
		return nil, false, ErrInvalid
	}
	return pages, true, nil
}

// xlsxSharedStrings reads the shared string table; phonetic runs are skipped.
func xlsxSharedStrings(data []byte) ([]string, error) {
	out := []string{}
	if data == nil {
		return out, nil
	}
	var cur strings.Builder
	inText, phonetic := false, 0
	err := xmlWalk(data, func(tok xml.Token) {
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				cur.Reset()
			case "rPh":
				phonetic++
			case "t":
				inText = phonetic == 0
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				out = append(out, cur.String())
			case "rPh":
				phonetic--
			case "t":
				inText = false
			}
		case xml.CharData:
			if inText {
				cur.Write(t)
			}
		}
	})
	return out, err
}

type xlsxSheet struct{ name, part string }

// xlsxSheets lists the sheets in workbook order with their part names.
func xlsxSheets(parts map[string][]byte) ([]xlsxSheet, error) {
	targets := map[string]string{}
	if err := xmlWalk(parts["xl/_rels/workbook.xml.rels"], func(tok xml.Token) {
		if t, ok := tok.(xml.StartElement); ok && t.Name.Local == "Relationship" {
			targets[xmlAttr(t, "Id")] = path.Clean(path.Join("xl", strings.TrimPrefix(xmlAttr(t, "Target"), "/xl/")))
		}
	}); err != nil {
		return nil, err
	}
	sheets := []xlsxSheet{}
	if err := xmlWalk(parts["xl/workbook.xml"], func(tok xml.Token) {
		if t, ok := tok.(xml.StartElement); ok && t.Name.Local == "sheet" {
			if part := targets[xmlAttr(t, "id")]; parts[part] != nil {
				sheets = append(sheets, xlsxSheet{name: strings.Join(strings.Fields(xmlAttr(t, "name")), " "), part: part})
			}
		}
	}); err != nil {
		return nil, err
	}
	return sheets, nil
}

// xlsxCell is the cell being read: its column and value parts.
type xlsxCell struct {
	col          int
	kind         string
	value        strings.Builder
	inValue, any bool
}

// xlsxRows reads a sheet's rows; a cell's column comes from its reference.
func xlsxRows(data []byte, shared []string) ([][]string, error) {
	rows := [][]string{}
	var row []string
	var cell xlsxCell
	err := xmlWalk(data, func(tok xml.Token) {
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				row = []string{}
			case "c":
				cell = xlsxCell{col: xlsxColumn(xmlAttr(t, "r"), len(row)), kind: xmlAttr(t, "t")}
			case "v", "t":
				cell.inValue, cell.any = true, true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v", "t":
				cell.inValue = false
			case "c":
				row = xlsxPut(row, &cell, shared)
			case "row":
				rows = append(rows, row)
			}
		case xml.CharData:
			if cell.inValue {
				cell.value.Write(t)
			}
		}
	})
	return rows, err
}

func xlsxPut(row []string, cell *xlsxCell, shared []string) []string {
	if !cell.any || cell.col >= xlsxMaxColumns {
		return row
	}
	v := cell.value.String()
	switch cell.kind {
	case "s":
		i, err := strconv.Atoi(v)
		if err != nil || i < 0 || i >= len(shared) {
			return row
		}
		v = shared[i]
	case "b":
		v = map[string]string{"0": "FALSE", "1": "TRUE"}[v]
	}
	for len(row) <= cell.col {
		row = append(row, "")
	}
	row[cell.col] = v
	return row
}

// xlsxColumn turns a reference such as "AB12" into a zero-based column.
func xlsxColumn(ref string, next int) int {
	col := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		col = col*26 + int(r-'A'+1)
		if col > xlsxMaxColumns {
			return xlsxMaxColumns
		}
	}
	if col == 0 {
		return next
	}
	return col - 1
}
