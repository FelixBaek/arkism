package memory

// Legacy binary document files (.doc, .xls, .ppt) are compound files: a small
// FAT file system inside one file. The reader below opens the root streams
// with the standard library and pulls out the text: the .doc piece table,
// the spreadsheet's string and number cells, and the slides' text atoms.
// Encrypted files and the pre-1997 formats are refused as invalid.
// shortcut: no formula results, no embedded objects, no .doc headers or
// footnotes; an operator converter still covers those when configured.
import (
	"bytes"
	"encoding/binary"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
)

const (
	cfbEndOfChain = 0xFFFFFFFE
	cfbNoStream   = 0xFFFFFFFF
)

type cfbFile struct {
	raw       []byte
	shift     uint
	fat, mini []uint32
	ministrm  []byte
	cutoff    uint32
}

func le16(b []byte, i int) int { return int(binary.LittleEndian.Uint16(b[i:])) }
func le32(b []byte, i int) uint32 {
	return binary.LittleEndian.Uint32(b[i:])
}

// cfbStreams returns the named streams that sit directly under the root.
func cfbStreams(raw []byte, names ...string) (map[string][]byte, error) {
	if len(raw) < 512 || !bytes.HasPrefix(raw, []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}) {
		return nil, ErrInvalid
	}
	f := &cfbFile{raw: raw, shift: uint(le16(raw, 0x1E)), cutoff: le32(raw, 0x38)}
	if f.shift != 9 && f.shift != 12 || le16(raw, 0x20) != 6 {
		return nil, ErrInvalid
	}
	if err := f.readFAT(); err != nil {
		return nil, err
	}
	dir, err := f.chain(le32(raw, 0x30), f.fat, f.sector, -1)
	if err != nil || len(dir) < 128 {
		return nil, ErrInvalid
	}
	if f.ministrm, err = f.chain(le32(dir, 116), f.fat, f.sector, int64(le32(dir, 120))); err != nil {
		return nil, err
	}
	miniFAT, err := f.chain(le32(raw, 0x3C), f.fat, f.sector, -1)
	if err != nil {
		return nil, err
	}
	f.mini = cfbEntries(miniFAT)
	out := map[string][]byte{}
	for _, id := range f.rootChildren(dir) {
		e := dir[id*128:]
		name := cfbName(e)
		if e[66] != 2 || !strings.Contains("\x00"+strings.Join(names, "\x00")+"\x00", "\x00"+name+"\x00") {
			continue
		}
		size := int64(le32(e, 120))
		if size < int64(f.cutoff) {
			out[name], err = f.chain(le32(e, 116), f.mini, f.miniSector, size)
		} else {
			out[name], err = f.chain(le32(e, 116), f.fat, f.sector, size)
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func cfbEntries(b []byte) []uint32 {
	out := make([]uint32, len(b)/4)
	for i := range out {
		out[i] = le32(b, i*4)
	}
	return out
}

func cfbName(e []byte) string {
	n := max(min(le16(e, 64)/2-1, 31), 0)
	return utf16le(e[:n*2])
}

// readFAT collects the FAT sectors named in the header and the DIFAT chain.
func (f *cfbFile) readFAT() error {
	ids := cfbEntries(f.raw[0x4C:0x200])
	next, per := le32(f.raw, 0x44), (1<<f.shift)/4
	// A file cannot hold more FAT sectors than it has sectors, which also
	// bounds a DIFAT chain that loops.
	want, sectors := int(le32(f.raw, 0x2C)), len(f.raw)>>f.shift
	if want > sectors {
		return ErrInvalid
	}
	for next < cfbEndOfChain && len(ids) < want {
		s, ok := f.sector(next)
		if !ok {
			return ErrInvalid
		}
		all := cfbEntries(s)
		ids, next = append(ids, all[:per-1]...), all[per-1]
	}
	for _, id := range ids[:min(len(ids), want)] {
		s, ok := f.sector(id)
		if !ok {
			return ErrInvalid
		}
		f.fat = append(f.fat, cfbEntries(s)...)
	}
	return nil
}

func (f *cfbFile) sector(id uint32) ([]byte, bool) {
	size := 1 << f.shift
	start := (int64(id) + 1) * int64(size)
	if id >= cfbEndOfChain-2 || start+int64(size) > int64(len(f.raw)) {
		return nil, false
	}
	return f.raw[start : start+int64(size)], true
}

func (f *cfbFile) miniSector(id uint32) ([]byte, bool) {
	start := int64(id) * 64
	if id >= cfbEndOfChain-2 || start+64 > int64(len(f.ministrm)) {
		return nil, false
	}
	return f.ministrm[start : start+64], true
}

// chain follows a sector chain; a loop or a chain shorter than size is invalid.
func (f *cfbFile) chain(start uint32, table []uint32, read func(uint32) ([]byte, bool), size int64) ([]byte, error) {
	var out []byte
	for id, steps := start, 0; id != cfbEndOfChain && id != cfbNoStream; steps++ {
		s, ok := read(id)
		// An acyclic chain never yields more bytes than the file holds.
		if !ok || steps > len(table) || int(id) >= len(table) || len(out) > len(f.raw) {
			return nil, ErrInvalid
		}
		out, id = append(out, s...), table[id]
	}
	if size >= 0 {
		if int64(len(out)) < size {
			return nil, ErrInvalid
		}
		out = out[:size]
	}
	return out, nil
}

// rootChildren walks the root storage's sibling tree.
func (f *cfbFile) rootChildren(dir []byte) []int {
	count, out := len(dir)/128, []int{}
	seen := map[uint32]bool{}
	stack := []uint32{le32(dir, 76)}
	for len(stack) > 0 && len(out) < count {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id == cfbNoStream || int(id) >= count || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, int(id))
		stack = append(stack, le32(dir, int(id)*128+68), le32(dir, int(id)*128+72))
	}
	return out
}

// legacyTidy turns .doc control characters into text: paragraph and cell
// marks become line breaks and tabs, field codes keep only their result.
func legacyTidy(s string) string {
	var b strings.Builder
	depth, code := 0, false
	for _, r := range s {
		switch {
		case r == 0x13:
			depth, code = depth+1, true
		case r == 0x14:
			code = false
		case r == 0x15:
			depth, code = max(depth-1, 0), false
		case code && depth > 0:
		case r == '\r' || r == 0x0b || r == 0x0c:
			b.WriteByte('\n')
		case r == 0x07:
			b.WriteByte('\t')
		case r == '\n' || r == '\t' || r >= 0x20 && r != 0xFFFD:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// cp1252 maps the bytes code page 1252 uses differently from Latin-1.
var cp1252 = map[byte]rune{0x80: '€', 0x85: '…', 0x91: '‘', 0x92: '’', 0x93: '“', 0x94: '”', 0x96: '–', 0x97: '—'}

func latin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		if m, ok := cp1252[c]; ok {
			r[i] = m
		} else {
			r[i] = rune(c)
		}
	}
	return string(r)
}

func utf16le(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(le16(b, i*2))
	}
	return string(utf16.Decode(u))
}

// docPages reads a .doc (version 8) document's main text through its piece table.
func docPages(raw []byte) ([]string, bool, error) {
	s, err := cfbStreams(raw, "WordDocument", "0Table", "1Table")
	word := s["WordDocument"]
	if err != nil || len(word) < 0x200 || le16(word, 0) != 0xA5EC || le16(word, 0x0A)&0x0100 != 0 {
		return nil, false, ErrInvalid
	}
	table := s[map[bool]string{false: "0Table", true: "1Table"}[le16(word, 0x0A)&0x0200 != 0]]
	rgw := 32 + 2 + le16(word, 32)*2
	rglw := rgw + 2
	if rglw > len(word) {
		return nil, false, ErrInvalid
	}
	blob := rglw + le16(word, rgw)*4 + 2
	if blob+34*8 > len(word) {
		return nil, false, ErrInvalid
	}
	ccp := int(le32(word, rglw+12))
	fc, lcb := int(le32(word, blob+33*8)), int(le32(word, blob+33*8+4))
	if fc+lcb > len(table) || lcb < 5 {
		return nil, false, ErrInvalid
	}
	text, err := docPieces(word, table[fc:fc+lcb], ccp)
	if err != nil {
		return nil, false, err
	}
	if text = legacyTidy(text); text == "" {
		return nil, false, ErrInvalid
	}
	return []string{text}, false, nil
}

// docPieces skips the Clx's property runs and reads the main text's pieces.
func docPieces(word, clx []byte, ccp int) (string, error) {
	i := 0
	for i+3 <= len(clx) && clx[i] == 0x01 {
		i += 3 + le16(clx, i+1)
	}
	if i+5 > len(clx) || clx[i] != 0x02 {
		return "", ErrInvalid
	}
	plc := clx[i+5:]
	if lcb := int(le32(clx, i+1)); lcb < len(plc) {
		plc = plc[:lcb]
	}
	n := (len(plc) - 4) / 12
	var b strings.Builder
	for k := 0; k < n && ccp > 0; k++ {
		cp0, cp1 := int(le32(plc, k*4)), int(le32(plc, k*4+4))
		count := min(cp1-cp0, ccp)
		fc := le32(plc, (n+1)*4+k*8+2)
		start, width := int(fc&0x3FFFFFFF), 2
		if fc&0x40000000 != 0 {
			start, width = start/2, 1
		}
		if count < 0 || start+count*width > len(word) || b.Len() > documentMaxText {
			return "", ErrInvalid
		}
		if width == 1 {
			b.WriteString(latin1(word[start : start+count]))
		} else {
			b.WriteString(utf16le(word[start : start+count*2]))
		}
		ccp -= count
	}
	return b.String(), nil
}

// biffRecords splits a BIFF8 stream into records; fn gets each record's
// offset, type and data.
func biffRecords(stream []byte, fn func(off, typ int, data []byte) bool) {
	for i := 0; i+4 <= len(stream); {
		typ, n := le16(stream, i), le16(stream, i+2)
		if i+4+n > len(stream) || !fn(i, typ, stream[i+4:i+4+n]) {
			return
		}
		i += 4 + n
	}
}

// xlsPages reads every worksheet of a BIFF8 workbook into a Markdown table.
func xlsPages(raw []byte) ([]string, bool, error) {
	s, err := cfbStreams(raw, "Workbook")
	book := s["Workbook"]
	if err != nil || len(book) == 0 {
		return nil, false, ErrInvalid
	}
	w := &xlsBook{sheetNames: map[int]string{}, cells: map[int]map[int]map[int]string{}}
	biffRecords(book, w.record)
	if w.encrypted || len(w.order) == 0 {
		return nil, false, ErrInvalid
	}
	pages, total := []string{}, 0
	for _, sheet := range w.order {
		table, err := markdownTable(xlsRows(w.cells[sheet]))
		if err != nil {
			return nil, false, err
		}
		page := strings.TrimSpace("## " + w.sheetNames[sheet] + "\n\n" + table)
		if total += len(page); total > documentMaxText {
			return nil, false, errBlobTooLarge
		}
		pages = append(pages, page)
	}
	return pages, true, nil
}

type xlsBook struct {
	sst        []string
	sstData    [][]byte // the SST record and its CONTINUE records
	sheetNames map[int]string
	order      []int
	sheet      int
	cells      map[int]map[int]map[int]string
	count      int
	encrypted  bool
	last       int
}

func (w *xlsBook) record(off, typ int, d []byte) bool {
	switch {
	case typ == 0x002F:
		w.encrypted = true
		return false
	case typ == 0x00FC:
		w.sstData, w.last = [][]byte{d}, typ
		return true
	case typ == 0x003C && w.last == 0x00FC:
		w.sstData = append(w.sstData, d)
		return true
	case typ == 0x0085 && len(d) >= 8:
		w.sheetNames[int(le32(d, 0))] = xlsString(d[6:], true)
	case typ == 0x0809:
		w.sheet = off
		if _, ok := w.sheetNames[off]; ok {
			w.order = append(w.order, off)
		}
	default:
		w.cell(typ, d)
	}
	if w.last == 0x00FC {
		w.sst = xlsSST(w.sstData)
	}
	w.last = typ
	return w.count <= xlsxMaxCells
}

// cell keeps string and number cells: LABELSST, LABEL, NUMBER, RK and MULRK.
func (w *xlsBook) cell(typ int, d []byte) {
	if len(d) < 6 {
		return
	}
	row, col := le16(d, 0), le16(d, 2)
	switch typ {
	case 0x00FD:
		if i := int(le32(d, 6)); len(d) >= 10 && i < len(w.sst) {
			w.put(row, col, w.sst[i])
		}
	case 0x0204:
		w.put(row, col, xlsString(d[6:], false))
	case 0x0203:
		if len(d) >= 14 {
			w.put(row, col, strconv.FormatFloat(math.Float64frombits(binary.LittleEndian.Uint64(d[6:])), 'g', -1, 64))
		}
	case 0x027E:
		if len(d) >= 10 {
			w.put(row, col, xlsRK(le32(d, 6)))
		}
	case 0x00BD:
		for i := 4; i+6 <= len(d)-2; i += 6 {
			w.put(row, col+(i-4)/6, xlsRK(le32(d, i+2)))
		}
	}
}

func (w *xlsBook) put(row, col int, v string) {
	if col >= xlsxMaxColumns || v == "" {
		return
	}
	if w.cells[w.sheet] == nil {
		w.cells[w.sheet] = map[int]map[int]string{}
	}
	if w.cells[w.sheet][row] == nil {
		w.cells[w.sheet][row] = map[int]string{}
	}
	w.cells[w.sheet][row][col] = v
	w.count++
}

// xlsRows orders a sheet's cells; blank rows are left out.
func xlsRows(cells map[int]map[int]string) [][]string {
	rows := []int{}
	for r := range cells {
		rows = append(rows, r)
	}
	slices.Sort(rows)
	out := [][]string{}
	for _, r := range rows {
		width := 0
		for c := range cells[r] {
			width = max(width, c+1)
		}
		row := make([]string, width)
		for c, v := range cells[r] {
			row[c] = v
		}
		out = append(out, row)
	}
	return out
}

func xlsRK(rk uint32) string {
	var v float64
	if rk&2 != 0 {
		v = float64(int32(rk) >> 2)
	} else {
		v = math.Float64frombits(uint64(rk&0xFFFFFFFC) << 32)
	}
	if rk&1 != 0 {
		v /= 100
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// xlsString reads a short (one-byte count) or long string with its flags byte.
func xlsString(d []byte, short bool) string {
	n, i := 0, 0
	if short && len(d) >= 1 {
		n, i = int(d[0]), 1
	} else if len(d) >= 2 {
		n, i = le16(d, 0), 2
	}
	if i >= len(d) {
		return ""
	}
	wide := d[i]&1 != 0
	i++
	if wide {
		return utf16le(d[i:min(len(d), i+n*2)])
	}
	return latin1(d[i:min(len(d), i+n)])
}

// xlsSST reads the shared strings. A string's characters may continue in the
// next record, which then starts with a fresh flags byte for the rest.
func xlsSST(parts [][]byte) []string {
	if len(parts) == 0 || len(parts[0]) < 8 {
		return nil
	}
	r := &sstReader{parts: parts, i: 8}
	total := int(le32(parts[0], 4))
	out := []string{}
	for k := 0; k < total && k < xlsxMaxCells && r.left() >= 3; k++ {
		out = append(out, r.next())
	}
	return out
}

type sstReader struct {
	parts [][]byte
	p, i  int
}

func (r *sstReader) left() int {
	for r.p < len(r.parts) && r.i >= len(r.parts[r.p]) {
		r.p, r.i = r.p+1, 0
	}
	if r.p >= len(r.parts) {
		return 0
	}
	return len(r.parts[r.p]) - r.i
}

func (r *sstReader) bytes(n int) []byte {
	var out []byte
	for n > 0 && r.left() > 0 {
		k := min(n, r.left())
		out = append(out, r.parts[r.p][r.i:r.i+k]...)
		r.i, n = r.i+k, n-k
	}
	return out
}

func (r *sstReader) next() string {
	head := r.bytes(3)
	if len(head) < 3 {
		return ""
	}
	n, flags := le16(head, 0), head[2]
	runs, ext := 0, 0
	if flags&8 != 0 {
		runs = le16(append(r.bytes(2), 0, 0), 0)
	}
	if flags&4 != 0 {
		ext = int(le32(append(r.bytes(4), 0, 0, 0, 0), 0))
	}
	var b strings.Builder
	for n > 0 && r.left() > 0 {
		width := 1 + int(flags&1)
		take := max(min(n, r.left()/width), 1)
		chunk := r.bytes(take * width)
		if width == 2 {
			b.WriteString(utf16le(chunk))
		} else {
			b.WriteString(latin1(chunk))
		}
		// The rest continues in the next record, after a fresh flags byte.
		if n -= take; n > 0 && r.left() > 0 {
			flags = r.bytes(1)[0]
		}
	}
	r.bytes(runs*4 + ext)
	return b.String()
}

// pptPages reads the slides' text atoms; each slide is one page.
func pptPages(raw []byte) ([]string, bool, error) {
	s, err := cfbStreams(raw, "PowerPoint Document")
	doc := s["PowerPoint Document"]
	if err != nil || len(doc) < 8 {
		return nil, false, ErrInvalid
	}
	p := &pptText{}
	p.walk(doc, 0)
	pages := []string{}
	for _, page := range p.pages {
		// A page of only symbols is a footer field such as the slide number.
		if t := strings.TrimSpace(page.String()); strings.IndexFunc(t, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 {
			pages = append(pages, legacyTidy(t))
		}
	}
	if len(pages) == 0 {
		return nil, false, ErrInvalid
	}
	return pages, true, nil
}

type pptText struct {
	pages []*strings.Builder
	size  int
}

// walk visits records; masters are skipped (their text is placeholder prompts),
// and a slide or a slide's text list entry starts a new page.
func (p *pptText) walk(b []byte, depth int) {
	for i := 0; i+8 <= len(b) && depth < 16; {
		ver, typ, n := le16(b, i)&0xF, le16(b, i+2), int(le32(b, i+4))
		if n < 0 || i+8+n > len(b) {
			return
		}
		body := b[i+8 : i+8+n]
		switch {
		case typ == 0x03F8 || typ == 0x0FC9: // main master, handout
		case typ == 0x03EE || typ == 0x03F3:
			p.pages = append(p.pages, &strings.Builder{})
			if ver == 0xF {
				p.walk(body, depth+1)
			}
		case ver == 0xF:
			p.walk(body, depth+1)
		case typ == 0x0FA0:
			p.add(utf16le(body))
		case typ == 0x0FA8:
			p.add(latin1(body))
		}
		i += 8 + n
	}
}

func (p *pptText) add(s string) {
	if p.size += len(s); p.size > documentMaxText {
		return
	}
	if len(p.pages) == 0 {
		p.pages = append(p.pages, &strings.Builder{})
	}
	page := p.pages[len(p.pages)-1]
	page.WriteString(s + "\n")
}
