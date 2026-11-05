package memory

// PDF text for memory extraction, standard library only: the desktop build
// targets Windows without cgo, and a few hundred lines cover the PDFs office
// suites and browsers write (page tree, Flate streams, object streams and
// ToUnicode maps). Scanned or encrypted files have no text here and say so.
// shortcut: no Form XObject text, no non-Flate filters, no built-in CJK
// encodings without a ToUnicode map; add them when real files need them.

import (
	"bytes"
	"compress/zlib"
	"errors"
	"image"
	"image/png"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

var errPDFNoText = errors.New("pdf_no_extractable_text")

const (
	pdfMaxStream   = 16 << 20
	pdfMaxInflated = 64 << 20
	pdfMaxObjects  = 200000
	pdfMaxPages    = 1000
	pdfMaxDepth    = 32
	pdfMaxCMap     = 1 << 16
	pdfMaxText     = 2 << 20
	pdfMaxCMapDst  = 64 // bytes of UTF-16 one code may map to (a ligature is a few)
)

type pdfName string
type pdfOp string
type pdfRef struct{ num int }
type pdfDict map[pdfName]any
type pdfStream struct {
	dict pdfDict
	data []byte
}

type pdfLexer struct {
	b     []byte
	i     int
	depth int
}

func pdfSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f' || c == 0
}

func pdfRegular(c byte) bool {
	return !pdfSpace(c) && strings.IndexByte("()<>[]{}/%", c) < 0
}

func (l *pdfLexer) skipSpace() {
	for l.i < len(l.b) {
		switch c := l.b[l.i]; {
		case c == '%':
			for l.i < len(l.b) && l.b[l.i] != '\n' && l.b[l.i] != '\r' {
				l.i++
			}
		case pdfSpace(c):
			l.i++
		default:
			return
		}
	}
}

// at is false past the end: a dict or array closing at the end steps over it.
func (l *pdfLexer) at(s string) bool { return l.i < len(l.b) && bytes.HasPrefix(l.b[l.i:], []byte(s)) }

// pdfInt reads a non-negative count or offset; a huge, infinite or NaN number
// is refused instead of wrapping to a negative int.
func pdfInt(v any) (int, bool) {
	n, ok := v.(float64)
	if !ok || !(n >= 0 && n <= math.MaxInt32) {
		return 0, false
	}
	return int(n), true
}

// value reads one object or content operator; false at the end or on a
// token that cannot start one (the caller skips a byte and goes on).
func (l *pdfLexer) value() (any, bool) {
	l.skipSpace()
	if l.i >= len(l.b) || l.depth > pdfMaxDepth {
		return nil, false
	}
	switch c := l.b[l.i]; {
	case c == '/':
		l.i++
		return pdfName(l.word()), true
	case c == '(':
		return l.literal(), true
	case l.at("<<"):
		return l.dict()
	case c == '<':
		return l.hex(), true
	case c == '[':
		return l.array()
	case pdfRegular(c):
		return l.atom(), true
	}
	return nil, false
}

func (l *pdfLexer) word() string {
	start := l.i
	for l.i < len(l.b) && pdfRegular(l.b[l.i]) {
		l.i++
	}
	return string(l.b[start:l.i])
}

// atom is a number, a reference "n g R", a keyword or a content operator.
func (l *pdfLexer) atom() any {
	w := l.word()
	n, err := strconv.ParseFloat(w, 64)
	if err != nil {
		return pdfOp(w)
	}
	if num, e := strconv.Atoi(w); e == nil {
		save := l.i
		l.skipSpace()
		if _, e = strconv.Atoi(l.word()); e == nil {
			l.skipSpace()
			if l.word() == "R" {
				return pdfRef{num}
			}
		}
		l.i = save
	}
	return n
}

func (l *pdfLexer) dict() (any, bool) {
	l.i += 2
	l.depth++
	defer func() { l.depth-- }()
	d := pdfDict{}
	for {
		l.skipSpace()
		if l.i >= len(l.b) || l.at(">>") {
			l.i += 2
			return d, true
		}
		key, ok := l.value()
		name, isName := key.(pdfName)
		if !ok || !isName {
			return d, false
		}
		v, ok := l.value()
		if !ok {
			return d, false
		}
		d[name] = v
	}
}

func (l *pdfLexer) array() (any, bool) {
	l.i++
	l.depth++
	defer func() { l.depth-- }()
	out := []any{}
	for {
		l.skipSpace()
		if l.i >= len(l.b) || l.b[l.i] == ']' {
			l.i++
			return out, true
		}
		v, ok := l.value()
		if !ok {
			return out, false
		}
		out = append(out, v)
	}
}

func (l *pdfLexer) hex() string {
	l.i++
	var digits []byte
	for l.i < len(l.b) && l.b[l.i] != '>' {
		if c := l.b[l.i]; strings.IndexByte("0123456789abcdefABCDEF", c) >= 0 {
			digits = append(digits, c)
		}
		l.i++
	}
	l.i++
	if len(digits)%2 == 1 {
		digits = append(digits, '0')
	}
	out := make([]byte, len(digits)/2)
	for i := range out {
		v, _ := strconv.ParseUint(string(digits[2*i:2*i+2]), 16, 8)
		out[i] = byte(v)
	}
	return string(out)
}

var pdfEscapes = map[byte]byte{'n': '\n', 'r': '\r', 't': '\t', 'b': '\b', 'f': '\f', '(': '(', ')': ')', '\\': '\\'}

func (l *pdfLexer) literal() string {
	l.i++
	var out []byte
	for nest := 1; l.i < len(l.b); l.i++ {
		c := l.b[l.i]
		switch {
		case c == '\\' && l.i+1 < len(l.b):
			l.i++
			out = l.escape(out)
		case c == '(':
			nest++
			out = append(out, c)
		case c == ')':
			if nest--; nest == 0 {
				l.i++
				return string(out)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

// escape appends the escaped byte at l.i (octal, named or a line continuation).
func (l *pdfLexer) escape(out []byte) []byte {
	c := l.b[l.i]
	if e, ok := pdfEscapes[c]; ok {
		return append(out, e)
	}
	if c < '0' || c > '7' {
		if c == '\r' && l.i+1 < len(l.b) && l.b[l.i+1] == '\n' {
			l.i++
		}
		if c == '\r' || c == '\n' {
			return out
		}
		return append(out, c)
	}
	v := 0
	for n := 0; n < 3 && l.i < len(l.b) && l.b[l.i] >= '0' && l.b[l.i] <= '7'; n++ {
		v = v*8 + int(l.b[l.i]-'0')
		l.i++
	}
	l.i--
	return append(out, byte(v))
}

// stream reads the data after a stream dictionary, if there is one.
func (l *pdfLexer) stream(d pdfDict) (pdfStream, bool) {
	l.skipSpace()
	if !l.at("stream") {
		return pdfStream{}, false
	}
	l.i += len("stream")
	if l.at("\r\n") {
		l.i += 2
	} else if l.at("\n") || l.at("\r") {
		l.i++
	}
	start := l.i
	if n, ok := pdfInt(d["Length"]); ok && start+n <= len(l.b) {
		end := start + n
		rest := bytes.TrimLeft(l.b[end:], " \r\n")
		if bytes.HasPrefix(rest, []byte("endstream")) {
			l.i = len(l.b) - len(rest) + len("endstream")
			return pdfStream{dict: d, data: l.b[start:end]}, true
		}
	}
	end := bytes.Index(l.b[start:], []byte("endstream"))
	if end < 0 {
		return pdfStream{}, false
	}
	l.i = start + end + len("endstream")
	return pdfStream{dict: d, data: bytes.TrimRight(l.b[start:start+end], "\r\n")}, true
}

type pdfDoc struct {
	objs     map[int]any
	inflated int
	fonts    map[int]*pdfFont
}

var pdfObjHeader = regexp.MustCompile(`(\d+)\s+\d+\s+obj\b`)

// pdfParse indexes a PDF's objects. Objects are found by scanning, not
// through the cross-reference table, so a damaged table or an incremental
// update still reads; a later definition wins.
func pdfParse(raw []byte) (*pdfDoc, error) {
	if !bytes.HasPrefix(bytes.TrimLeft(raw, " \r\n\t"), []byte("%PDF-")) {
		return nil, ErrInvalid
	}
	if bytes.Contains(raw, []byte("/Encrypt")) {
		return nil, errPDFNoText
	}
	d := &pdfDoc{objs: map[int]any{}, fonts: map[int]*pdfFont{}}
	pos := 0
	for _, m := range pdfObjHeader.FindAllSubmatchIndex(raw, pdfMaxObjects) {
		if m[0] < pos {
			continue // inside the previous object's stream data
		}
		num, _ := strconv.Atoi(string(raw[m[2]:m[3]]))
		l := &pdfLexer{b: raw, i: m[1]}
		v, ok := l.value()
		if !ok {
			continue
		}
		if dict, isDict := v.(pdfDict); isDict {
			if s, ok := l.stream(dict); ok {
				v = s
			}
		}
		d.objs[num], pos = v, l.i
	}
	d.expandObjectStreams()
	return d, nil
}

// pdfPagesText returns each page's text in page order.
func pdfPagesText(raw []byte) ([]string, error) {
	d, err := pdfParse(raw)
	if err != nil {
		return nil, err
	}
	pages := d.pages()
	out := make([]string, 0, len(pages))
	found, total := false, 0
	for _, page := range pages {
		text, over := d.pageText(page)
		if total += len(text); over || total > pdfMaxText {
			return nil, errBlobTooLarge // more text than one extraction reads
		}
		found = found || strings.TrimSpace(text) != ""
		out = append(out, text)
	}
	if !found {
		return nil, errPDFNoText
	}
	return out, nil
}

// pdfPageImages returns each page's largest image a model can read: a JPEG
// as stored (the form a scanner usually writes), or an 8-bit gray or RGB
// Flate image re-encoded as PNG. A page without one gets nil.
// shortcut: CCITT, JBIG2, JPEG 2000, indexed colour and predictor-coded
// Flate images are not read; add them when real scans need them.
func pdfPageImages(raw []byte) ([][]byte, error) {
	d, err := pdfParse(raw)
	if err != nil {
		return nil, err
	}
	pages := d.pages()
	out := make([][]byte, len(pages))
	for i, page := range pages {
		var best pdfStream
		for _, ref := range d.dictOf(d.dictOf(page["Resources"])["XObject"]) {
			st, ok := d.resolve(ref).(pdfStream)
			if ok && st.dict["Subtype"] == pdfName("Image") && d.imageFilter(st) != "" && len(st.data) > len(best.data) {
				best = st
			}
		}
		if best.dict != nil {
			out[i] = d.pageImage(best)
		}
	}
	return out, nil
}

// imageFilter names a readable image stream's single filter, or "".
func (d *pdfDoc) imageFilter(st pdfStream) pdfName {
	filter := d.resolve(st.dict["Filter"])
	if arr, single := filter.([]any); single && len(arr) == 1 {
		filter = d.resolve(arr[0])
	}
	if filter == pdfName("DCTDecode") || filter == pdfName("FlateDecode") && st.dict["DecodeParms"] == nil {
		return filter.(pdfName)
	}
	return ""
}

// pageImage returns a JPEG as stored or a Flate image as PNG; nil when the
// image is in a form not read here or inflates past the document limit.
func (d *pdfDoc) pageImage(st pdfStream) []byte {
	if d.imageFilter(st) == "DCTDecode" {
		return st.data
	}
	w, wok := pdfInt(d.resolve(st.dict["Width"]))
	h, hok := pdfInt(d.resolve(st.dict["Height"]))
	bits, _ := pdfInt(d.resolve(st.dict["BitsPerComponent"]))
	comps := map[any]int{pdfName("DeviceRGB"): 3, pdfName("DeviceGray"): 1}[d.resolve(st.dict["ColorSpace"])]
	if !wok || !hok || w < 1 || h < 1 || w > 1<<15 || h > 1<<15 || bits != 8 || comps == 0 || w*h*comps > pdfMaxStream {
		return nil
	}
	pixels, ok := d.decode(st)
	if !ok || len(pixels) != w*h*comps {
		return nil
	}
	var img image.Image
	if comps == 1 {
		img = &image.Gray{Pix: pixels, Stride: w, Rect: image.Rect(0, 0, w, h)}
	} else {
		rgba := image.NewRGBA(image.Rect(0, 0, w, h))
		for p := 0; p < w*h; p++ {
			copy(rgba.Pix[4*p:4*p+3], pixels[3*p:3*p+3])
			rgba.Pix[4*p+3] = 0xff
		}
		img = rgba
	}
	var buf bytes.Buffer
	if png.Encode(&buf, img) != nil {
		return nil
	}
	return buf.Bytes()
}

func (d *pdfDoc) resolve(v any) any {
	for n := 0; n < 8; n++ {
		r, ok := v.(pdfRef)
		if !ok {
			return v
		}
		v = d.objs[r.num]
	}
	return nil
}

func (d *pdfDoc) dictOf(v any) pdfDict {
	switch x := d.resolve(v).(type) {
	case pdfDict:
		return x
	case pdfStream:
		return x.dict
	}
	return nil
}

// decode inflates a stream; any filter other than Flate leaves it unread.
func (d *pdfDoc) decode(s pdfStream) ([]byte, bool) {
	filters := []any{}
	switch f := d.resolve(s.dict["Filter"]).(type) {
	case pdfName:
		filters = append(filters, f)
	case []any:
		filters = f
	}
	data := s.data
	for _, f := range filters {
		if d.resolve(f) != pdfName("FlateDecode") {
			return nil, false
		}
		// The document-wide cap is checked before inflating, so a stream shared
		// by many pages or fonts cannot be inflated over and over.
		limit := min(pdfMaxStream, pdfMaxInflated-d.inflated)
		zr, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil || limit <= 0 {
			return nil, false
		}
		out, err := io.ReadAll(io.LimitReader(zr, int64(limit)+1))
		d.inflated += len(out)
		if err != nil && len(out) == 0 || len(out) > limit {
			return nil, false
		}
		data = out
	}
	return data, true
}

func (d *pdfDoc) expandObjectStreams() {
	for _, v := range d.objs {
		s, ok := v.(pdfStream)
		if !ok || s.dict["Type"] != pdfName("ObjStm") {
			continue
		}
		first, okFirst := pdfInt(s.dict["First"])
		count, okCount := pdfInt(s.dict["N"])
		if !okFirst || !okCount {
			continue
		}
		data, okData := d.decode(s)
		if !okData || first > len(data) {
			continue
		}
		head := &pdfLexer{b: data[:first]}
		for n := 0; n < count; n++ {
			num, ok1 := head.value()
			off, ok2 := head.value()
			nf, isNum := pdfInt(num)
			of, isOff := pdfInt(off)
			if !ok1 || !ok2 || !isNum || !isOff || first+of > len(data) {
				break
			}
			if _, exists := d.objs[nf]; exists {
				continue
			}
			if obj, ok := (&pdfLexer{b: data, i: first + of}).value(); ok {
				d.objs[nf] = obj
			}
		}
	}
}

// pages walks the page tree from the catalog; Resources are inherited.
func (d *pdfDoc) pages() []pdfDict {
	var root pdfDict
	for _, v := range d.objs {
		if c := d.dictOf(v); c != nil && c["Type"] == pdfName("Catalog") && c["Pages"] != nil {
			root = d.dictOf(c["Pages"])
			break
		}
	}
	out := []pdfDict{}
	visits := 0 // a page tree that loops back on itself stops here
	var walk func(node pdfDict, res any, depth int)
	walk = func(node pdfDict, res any, depth int) {
		visits++
		if node == nil || depth > pdfMaxDepth || len(out) >= pdfMaxPages || visits > 4*pdfMaxPages {
			return
		}
		if node["Resources"] != nil {
			res = node["Resources"]
		}
		kids, isTree := d.resolve(node["Kids"]).([]any)
		if !isTree {
			page := pdfDict{"Contents": node["Contents"], "Resources": res}
			out = append(out, page)
			return
		}
		for _, kid := range kids {
			walk(d.dictOf(kid), res, depth+1)
		}
	}
	walk(root, nil, 0)
	return out
}

func (d *pdfDoc) pageContent(page pdfDict) []byte {
	parts := []any{page["Contents"]}
	if arr, ok := d.resolve(page["Contents"]).([]any); ok {
		parts = arr
	}
	var out []byte
	for _, p := range parts {
		if s, ok := d.resolve(p).(pdfStream); ok {
			if data, ok := d.decode(s); ok {
				out = append(append(out, data...), '\n')
			}
		}
	}
	return out
}

func (d *pdfDoc) pageFonts(page pdfDict) map[pdfName]*pdfFont {
	fonts := map[pdfName]*pdfFont{}
	for name, ref := range d.dictOf(d.dictOf(page["Resources"])["Font"]) {
		fonts[name] = d.font(ref)
	}
	return fonts
}

type pdfText struct {
	out   pdfOut
	font  *pdfFont
	fonts map[pdfName]*pdfFont
	lastY float64
}

func (d *pdfDoc) pageText(page pdfDict) (string, bool) {
	t := &pdfText{fonts: d.pageFonts(page), lastY: math.NaN()}
	l := &pdfLexer{b: d.pageContent(page)}
	var args []any
	for l.i < len(l.b) {
		v, ok := l.value()
		if !ok {
			l.i++
			continue
		}
		op, isOp := v.(pdfOp)
		if !isOp {
			if len(args) < 64 {
				args = append(args, v)
			}
			continue
		}
		if op == "ID" { // inline image data runs to EI
			if end := bytes.Index(l.b[l.i:], []byte("EI")); end >= 0 {
				l.i += end + 2
			}
		}
		t.operator(string(op), args)
		args = args[:0]
	}
	return pdfTidy(t.out.String()), t.out.over
}

func pdfNumber(args []any, i int) float64 {
	if i < len(args) {
		if n, ok := args[i].(float64); ok {
			return n
		}
	}
	return 0
}

func (t *pdfText) operator(op string, args []any) {
	switch op {
	case "Tf":
		if len(args) > 0 {
			name, _ := args[0].(pdfName)
			t.font = t.fonts[name]
		}
	case "Tj":
		t.show(args)
	case "'", "\"":
		t.out.add("\n")
		t.show(args)
	case "TJ":
		t.showArray(args)
	case "T*", "ET":
		t.out.add("\n")
	case "Td", "TD":
		if pdfNumber(args, 1) != 0 {
			t.out.add("\n")
		}
	case "Tm":
		if y := pdfNumber(args, 5); y != t.lastY {
			t.lastY = y
			t.out.add("\n")
		}
	}
}

func (t *pdfText) show(args []any) {
	if len(args) > 0 {
		if s, ok := args[len(args)-1].(string); ok {
			t.out.add(t.font.text(s))
		}
	}
}

func (t *pdfText) showArray(args []any) {
	if len(args) == 0 {
		return
	}
	items, _ := args[len(args)-1].([]any)
	for _, it := range items {
		switch x := it.(type) {
		case string:
			t.out.add(t.font.text(x))
		case float64:
			if x < -250 { // a wide kerning gap is a word space
				t.out.add(" ")
			}
		}
	}
}

// pdfOut stops growing at pdfMaxText, so a font map that turns one code into
// a long string cannot blow a small stream up into gigabytes of text.
type pdfOut struct {
	strings.Builder
	over bool
}

func (o *pdfOut) add(s string) {
	if o.over = o.over || o.Len()+len(s) > pdfMaxText; !o.over {
		o.WriteString(s)
	}
}

var pdfBlankLines = regexp.MustCompile(`\n{3,}`)

func pdfTidy(s string) string {
	lines := strings.Split(strings.ToValidUTF8(s, ""), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.TrimSpace(pdfBlankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// pdfFont maps character codes to text through the font's ToUnicode CMap.
type pdfFont struct {
	width   int
	twoByte bool
	cmap    map[string]string
}

func (d *pdfDoc) font(ref any) *pdfFont {
	r, isRef := ref.(pdfRef)
	if f, ok := d.fonts[r.num]; isRef && ok {
		return f
	}
	dict := d.dictOf(ref)
	f := &pdfFont{width: 1, twoByte: dict["Subtype"] == pdfName("Type0")}
	if f.twoByte {
		f.width = 2
	}
	if s, ok := d.resolve(dict["ToUnicode"]).(pdfStream); ok {
		if data, ok := d.decode(s); ok {
			f.readCMap(data)
		}
	}
	if isRef {
		d.fonts[r.num] = f
	}
	return f
}

func (f *pdfFont) readCMap(data []byte) {
	f.cmap = map[string]string{}
	l := &pdfLexer{b: data}
	var args []any
	for l.i < len(l.b) && len(f.cmap) < pdfMaxCMap {
		v, ok := l.value()
		if !ok {
			l.i++
			continue
		}
		op, isOp := v.(pdfOp)
		if !isOp {
			args = append(args, v)
			continue
		}
		switch op {
		case "endcodespacerange":
			if lo, ok := pdfArg(args, 0); ok && len(lo) > 0 && len(lo) <= 4 {
				f.width = len(lo)
			}
		case "endbfchar":
			for i := 0; i+1 < len(args); i += 2 {
				src, ok1 := args[i].(string)
				dst, ok2 := args[i+1].(string)
				if ok1 && ok2 && len(dst) <= pdfMaxCMapDst {
					f.cmap[src] = pdfUTF16(dst)
				}
			}
		case "endbfrange":
			f.readRanges(args)
		}
		args = args[:0]
	}
}

func pdfArg(args []any, i int) (string, bool) {
	if i >= len(args) {
		return "", false
	}
	s, ok := args[i].(string)
	return s, ok
}

func (f *pdfFont) readRanges(args []any) {
	for i := 0; i+2 < len(args); i += 3 {
		lo, ok1 := args[i].(string)
		hi, ok2 := args[i+1].(string)
		if !ok1 || !ok2 || len(lo) != len(hi) || len(lo) == 0 || len(lo) > 4 {
			continue
		}
		first, last := pdfCode(lo), pdfCode(hi)
		// A range wider than the map cap (up to 2^32 codes) is cut, so an
		// unusable destination cannot spin through billions of codes (uint64
		// codes cannot wrap).
		last = min(last, first+pdfMaxCMap-1)
		for c := first; c <= last && len(f.cmap) < pdfMaxCMap; c++ {
			key := pdfCodeBytes(c, len(lo))
			switch dst := args[i+2].(type) {
			case string:
				if len(dst) > pdfMaxCMapDst {
					continue
				}
				u := []rune(pdfUTF16(dst))
				if len(u) > 0 {
					u[len(u)-1] += rune(c - first)
					f.cmap[key] = string(u)
				}
			case []any:
				if s, ok := pdfArg(dst, int(c-first)); ok && len(s) <= pdfMaxCMapDst {
					f.cmap[key] = pdfUTF16(s)
				}
			}
		}
	}
}

func pdfCode(s string) uint64 {
	var v uint64
	for i := 0; i < len(s); i++ {
		v = v<<8 | uint64(s[i])
	}
	return v
}

func pdfCodeBytes(v uint64, n int) string {
	b := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	return string(b)
}

func pdfUTF16(s string) string {
	u := make([]uint16, 0, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		u = append(u, uint16(s[i])<<8|uint16(s[i+1]))
	}
	return string(utf16.Decode(u))
}

// text decodes a shown string. Without a map a simple font is read as
// Latin-1 and a two-byte font cannot be read at all.
func (f *pdfFont) text(s string) string {
	if f == nil {
		f = &pdfFont{width: 1}
	}
	if f.cmap == nil {
		if f.twoByte {
			return ""
		}
		out := make([]rune, 0, len(s))
		for i := 0; i < len(s); i++ {
			out = append(out, rune(s[i]))
		}
		return string(out)
	}
	var b strings.Builder
	for i := 0; i+f.width <= len(s); i += f.width {
		if u, ok := f.cmap[s[i:i+f.width]]; ok && utf8.ValidString(u) {
			b.WriteString(u)
		} else if f.width == 1 && s[i] >= 0x20 && s[i] < 0x7f {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
