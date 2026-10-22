package codegraph

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// JavaScript and TypeScript without a parser dependency: a lexer that knows
// comments, strings, template literals and regular expressions, and a
// declaration scanner on top of it. What it resolves, and nothing more:
//   - function declarations, `const f = (…) =>` / `function`, `a.b = (…) =>`,
//     classes and their methods, TS interfaces, type aliases and enums;
//   - calls of a name declared in an enclosing scope or the file, of a name or
//     namespace imported from a relative path (import or require), this.m()
//     inside a class and C.m() for a class C of the same file;
//   - other uses of those names as references, `new C()` as a reference.
// Calls through objects it cannot see (ctx.render(), arr.map()) are skipped
// rather than guessed.

type jsTok struct {
	kind byte // 'i' identifier, 's' string, 'n' number, 'p' punctuation, 'r' regex
	text string
	line int
}

var jsKeywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true, "return": true, "typeof": true, "new": true,
	"function": true, "class": true, "const": true, "let": true, "var": true, "await": true, "yield": true, "delete": true,
	"void": true, "throw": true, "case": true, "do": true, "else": true, "in": true, "of": true, "instanceof": true,
	"import": true, "export": true, "super": true, "this": true, "with": true, "default": true, "extends": true,
	"try": true, "finally": true, "break": true, "continue": true, "async": true, "static": true, "get": true, "set": true,
	"interface": true, "type": true, "enum": true, "as": true, "from": true, "constructor": true, "declare": true,
	"namespace": true, "abstract": true, "public": true, "private": true, "protected": true, "readonly": true, "override": true,
	"true": true, "false": true, "null": true, "undefined": true,
}

var memberModifiers = map[string]bool{"static": true, "async": true, "get": true, "set": true,
	"public": true, "private": true, "protected": true, "readonly": true, "override": true, "abstract": true}

// uses: a name right after these keywords is being declared, not used.
var declaresNext = map[string]bool{"function": true, "class": true, "const": true, "let": true, "var": true,
	"interface": true, "type": true, "enum": true, "as": true}

// exprEnd keywords: a '/' after them starts a regular expression.
var regexAfter = map[string]bool{"return": true, "typeof": true, "case": true, "do": true, "else": true, "in": true, "of": true,
	"new": true, "delete": true, "void": true, "throw": true, "instanceof": true, "yield": true, "await": true}

func isIdentStart(r rune) bool { return r == '_' || r == '$' || unicode.IsLetter(r) }
func isIdentPart(r rune) bool {
	return isIdentStart(r) || unicode.IsDigit(r) || r == '‌' || r == '‍'
}

func lexJS(src string) []jsTok {
	var toks []jsTok
	line := 1
	// stack of open template expressions: brace depth inside each ${ … }
	var tmpl []int
	i := 0
	n := len(src)
	prevAllowsRegex := func() bool {
		if len(toks) == 0 {
			return true
		}
		t := toks[len(toks)-1]
		switch t.kind {
		case 'i':
			return regexAfter[t.text]
		case 'n', 's', 'r':
			return false
		}
		return t.text != ")" && t.text != "]"
	}
	// scanTemplate reads template text from i (just after ` or }) up to ` or ${.
	scanTemplate := func() {
		for i < n {
			c := src[i]
			switch {
			case c == '\\':
				i += 2
				continue
			case c == '\n':
				line++
			case c == '`':
				i++
				toks = append(toks, jsTok{'s', "`", line})
				return
			case c == '$' && i+1 < n && src[i+1] == '{':
				i += 2
				tmpl = append(tmpl, 0)
				return
			}
			i++
		}
	}
	for i < n {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++
		case isJSSpace(c):
			i++
		case strings.HasPrefix(src[i:], "//"):
			if k := strings.IndexByte(src[i:], '\n'); k >= 0 {
				i += k
			} else {
				i = n
			}
		case strings.HasPrefix(src[i:], "/*"):
			i, line = skipBlockComment(src, i, line)
		case c == '\'' || c == '"':
			var t jsTok
			t, i, line = lexQuoted(src, i, line)
			toks = append(toks, t)
		case c == '`':
			i++
			scanTemplate()
		case c == '/' && prevAllowsRegex():
			var t jsTok
			t, i = lexRegex(src, i, line)
			toks = append(toks, t)
		case isNumberStart(src, i):
			start := i
			i = numberEnd(src, i)
			toks = append(toks, jsTok{'n', src[start:i], line})
		default:
			if end := identEnd(src, i); end > i {
				toks = append(toks, jsTok{'i', src[i:end], line})
				i = end
				continue
			}
			if op := jsOperator(src, i); op != "" {
				toks = append(toks, jsTok{'p', op, line})
				i += len(op)
				continue
			}
			if templateBrace(tmpl, c) {
				tmpl = tmpl[:len(tmpl)-1]
				i++
				scanTemplate()
				continue
			}
			_, size := utf8.DecodeRuneInString(src[i:])
			toks = append(toks, jsTok{'p', src[i : i+size], line})
			i += size
		}
	}
	return toks
}

func isJSSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v'
}

// skipBlockComment skips a /* … */ comment at i, counting its lines.
func skipBlockComment(src string, i, line int) (int, int) {
	n := len(src)
	i += 2
	for i < n && !(src[i] == '*' && i+1 < n && src[i+1] == '/') {
		if src[i] == '\n' {
			line++
		}
		i++
	}
	return i + 2, line
}

// lexQuoted reads a '…' or "…" string at i.
func lexQuoted(src string, i, line int) (jsTok, int, int) {
	n := len(src)
	c := src[i]
	start, l := i, line
	i++
	var b strings.Builder
	for i < n && src[i] != c && src[i] != '\n' {
		if src[i] == '\\' && i+1 < n {
			if src[i+1] == '\n' {
				line++
			}
			b.WriteByte(src[i+1])
			i += 2
			continue
		}
		b.WriteByte(src[i])
		i++
	}
	if i < n && src[i] == c {
		return jsTok{'s', b.String(), l}, i + 1, line
	}
	// unterminated (JSX text such as don't): treat the quote as punctuation
	return jsTok{'p', string(c), l}, start + 1, line
}

// lexRegex reads a regular expression literal at i, or a lone '/' when it is
// not closed on the line.
func lexRegex(src string, i, line int) (jsTok, int) {
	n := len(src)
	l := i
	i++
	class := false
	for i < n && src[i] != '\n' {
		if src[i] == '\\' {
			i += 2
			continue
		}
		if src[i] == '[' {
			class = true
		} else if src[i] == ']' {
			class = false
		} else if src[i] == '/' && !class {
			break
		}
		i++
	}
	if i < n && src[i] == '/' {
		i++
		for i < n && isIdentPart(rune(src[i])) {
			i++
		}
		return jsTok{'r', src[l:i], line}, i
	}
	return jsTok{'p', "/", line}, l + 1
}

func isNumberStart(src string, i int) bool {
	c := src[i]
	return c >= '0' && c <= '9' || c == '.' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9'
}

func numberEnd(src string, i int) int {
	for i < len(src) && (isIdentPart(rune(src[i])) || src[i] == '.') {
		i++
	}
	return i
}

// identEnd returns the end of an identifier (or #private name) at i, or i.
func identEnd(src string, i int) int {
	r, size := utf8.DecodeRuneInString(src[i:])
	if !isIdentStart(r) && !(r == '#' && i+1 < len(src) && isIdentStart(rune(src[i+1]))) {
		return i
	}
	i += size
	for i < len(src) {
		r2, s2 := utf8.DecodeRuneInString(src[i:])
		if !isIdentPart(r2) {
			break
		}
		i += s2
	}
	return i
}

// jsOperator returns the multi-character operator at i, or "".
func jsOperator(src string, i int) string {
	for _, op := range []string{"=>", "...", "?.", "===", "!==", "==", "!=", "&&", "||", "??"} {
		if strings.HasPrefix(src[i:], op) && !(op == "?." && i+2 < len(src) && src[i+2] >= '0' && src[i+2] <= '9') {
			return op
		}
	}
	return ""
}

// templateBrace counts braces inside the innermost open ${ … }; it reports
// whether c closes that expression.
func templateBrace(tmpl []int, c byte) bool {
	if len(tmpl) == 0 {
		return false
	}
	top := &tmpl[len(tmpl)-1]
	switch {
	case c == '{':
		*top++
	case c == '}' && *top == 0:
		return true
	case c == '}':
		*top--
	}
	return false
}

// jsFrame is a function or class body, or the file itself.
type jsFrame struct {
	sym      string // symbol id of the function/class; "" for the file
	qname    string // qualified prefix of declarations inside
	class    string // class name when this frame is a class body
	parent   int
	start    int // token index of the opening brace (or first token of an expression body)
	end      int // token index of the closing brace (or last token)
	decls    map[string]string
	methods  map[string]string // class frames: method name -> id
	isClass  bool
	exported map[string]bool
}

type jsFile struct {
	path    string
	toks    []jsTok
	frames  []*jsFrame
	syms    []Symbol
	edges   []Edge
	edgeKey map[string]bool
	ids     map[string]bool
	// import bindings: local name -> target qname (module#name), namespace imports -> module path
	named      map[string]string
	namespaces map[string]string
	imports    []jsImport
	exported   map[string]bool
	resolve    func(from, spec string) string // relative specifier -> repo path or ""
	classes    map[string]int                 // class name -> frame index (file level)
	declFrame  map[string]int                 // symbol id -> frame it is declared in
}

type jsImport struct {
	target string
	line   int
}

// indexJS reads one file. resolve maps a relative import specifier to a path of
// the indexed tree ("" when the target is not part of it).
func indexJS(p string, src string, resolve func(from, spec string) string) FileFacts {
	f := &jsFile{path: p, toks: lexJS(src), edgeKey: map[string]bool{}, ids: map[string]bool{}, named: map[string]string{},
		namespaces: map[string]string{}, exported: map[string]bool{}, resolve: resolve, classes: map[string]int{}, declFrame: map[string]int{}}
	lines := strings.Count(src, "\n") + 1
	modID := SymbolID(LangJS, p)
	f.ids[modID] = true
	f.syms = append(f.syms, Symbol{ID: modID, Path: p, Lang: LangJS, Kind: KindModule, Name: path.Base(p), QName: p, Signature: "module " + p, StartLine: 1, EndLine: lines, Exported: true})
	f.frames = append(f.frames, &jsFrame{sym: modID, qname: p + "#", parent: -1, start: -1, end: len(f.toks), decls: map[string]string{}, exported: map[string]bool{}})
	f.declare()
	for _, imp := range f.imports {
		f.edge(modID, SymbolID(LangJS, imp.target), EdgeImports, imp.line)
	}
	f.uses()
	for i := range f.syms {
		if f.exported[f.syms[i].Name] && f.frameOfSym(f.syms[i].ID) == 0 {
			f.syms[i].Exported = true
		}
	}
	return FileFacts{Path: p, Lang: LangJS, Symbols: f.syms, Edges: f.edges}
}

func (f *jsFile) frameOfSym(id string) int {
	if k, ok := f.declFrame[id]; ok {
		return k
	}
	return -1
}

func (f *jsFile) tok(i int) jsTok {
	if i < 0 || i >= len(f.toks) {
		return jsTok{}
	}
	return f.toks[i]
}

func (f *jsFile) is(i int, text string) bool {
	t := f.tok(i)
	return t.kind != 's' && t.kind != 0 && t.text == text
}

// match returns the index of the bracket closing the one at i.
func (f *jsFile) match(i int) int {
	open := f.toks[i].text
	closeText := map[string]string{"(": ")", "[": "]", "{": "}", "<": ">"}[open]
	depth := 0
	for j := i; j < len(f.toks); j++ {
		t := f.toks[j]
		if t.kind != 'p' {
			continue
		}
		if t.text == open {
			depth++
		} else if t.text == closeText {
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return len(f.toks) - 1
}

func (f *jsFile) text(from, to int) string {
	var b strings.Builder
	for j := from; j <= to && j < len(f.toks); j++ {
		t := f.toks[j]
		if t.kind == 's' {
			b.WriteString("'" + t.text + "'")
		} else {
			if b.Len() > 0 && t.kind == 'i' && j > from && f.toks[j-1].kind == 'i' {
				b.WriteByte(' ')
			}
			b.WriteString(t.text)
			if t.text == "," {
				b.WriteByte(' ')
			}
		}
	}
	return b.String()
}

func (f *jsFile) newSymbol(frame int, kind, name, sig string, startTok, endTok int, exported bool) string {
	fr := f.frames[frame]
	qname := fr.qname + name
	id := SymbolID(LangJS, qname)
	for k := 2; f.ids[id]; k++ {
		id = SymbolID(LangJS, qname+"~"+itoa(k))
	}
	f.ids[id] = true
	f.declFrame[id] = frame
	f.syms = append(f.syms, Symbol{ID: id, Path: f.path, Lang: LangJS, Kind: kind, Name: name, QName: qname, Signature: clip(sig, 200),
		StartLine: f.tok(startTok).line, EndLine: max(f.tok(endTok).line, f.tok(startTok).line), Exported: exported})
	return id
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func (f *jsFile) edge(from, to, kind string, line int) {
	if from == to || from == "" || to == "" {
		return
	}
	key := from + "|" + to + "|" + kind
	if f.edgeKey[key] {
		return
	}
	f.edgeKey[key] = true
	f.edges = append(f.edges, Edge{From: from, To: to, Kind: kind, Path: f.path, Line: line})
}

// innermost returns the frame containing token i that is a function, class or file.
func (f *jsFile) innermost(i int) int {
	best := 0
	for k, fr := range f.frames {
		if k == 0 {
			continue
		}
		if fr.start <= i && i <= fr.end && fr.start >= f.frames[best].start {
			best = k
		}
	}
	return best
}

// funcBody finds the body after a parameter list that closes at closeParen:
// skips a TS return type, returns (bodyStart, bodyEnd, ok). For an arrow with an
// expression body the range ends at the end of the expression.
func (f *jsFile) funcBody(closeParen int) (int, int, bool) {
	j := closeParen + 1
	if f.is(j, ":") {
		var ok bool
		if j, ok = f.skipReturnType(j); !ok {
			return 0, 0, false
		}
	}
	if f.is(j, "=>") {
		j++
		if f.is(j, "{") {
			return j, f.match(j), true
		}
		return j, f.exprEnd(j), true
	}
	if f.is(j, "{") {
		return j, f.match(j), true
	}
	return 0, 0, false
}

// skipReturnType skips a TS return type starting at the ':' at j: the body is
// the first '{' or '=>' that is not part of the type.
func (f *jsFile) skipReturnType(j int) (int, bool) {
	depth := 0
	for j++; j < len(f.toks); j++ {
		switch {
		case f.is(j, "(") || f.is(j, "[") || f.is(j, "<"):
			depth++
		case f.is(j, ")") || f.is(j, "]") || f.is(j, ">"):
			depth--
		case depth == 0 && f.is(j, "=>"):
			return j, true
		case depth == 0 && f.is(j, "{"):
			if f.is(j-1, ":") || f.is(j-1, "|") || f.is(j-1, "&") || f.is(j-1, ",") {
				j = f.match(j) // an object type
				continue
			}
			return j, true
		case f.is(j, ";"):
			return 0, false
		}
	}
	return 0, false
}

// exprEnd is the last token of an expression starting at i: before a ',' or ';'
// at the same depth, a closing bracket below it, or a statement keyword.
func (f *jsFile) exprEnd(i int) int {
	depth := 0
	for j := i; j < len(f.toks); j++ {
		t := f.toks[j]
		if t.kind == 'p' {
			switch t.text {
			case "(", "[", "{":
				depth++
			case ")", "]", "}":
				if depth == 0 {
					return j - 1
				}
				depth--
			case ",", ";":
				if depth == 0 {
					return j - 1
				}
			}
		} else if t.kind == 'i' && depth == 0 && j > i && t.line > f.toks[j-1].line {
			switch t.text {
			case "const", "let", "var", "function", "class", "export", "import", "return", "if", "for", "while":
				return j - 1
			}
		}
	}
	return len(f.toks) - 1
}

func (f *jsFile) addFrame(parent int, sym, qname string, start, end int, class string) int {
	f.frames = append(f.frames, &jsFrame{sym: sym, qname: qname, class: class, parent: parent, start: start, end: end,
		decls: map[string]string{}, methods: map[string]string{}, isClass: class != "", exported: map[string]bool{}})
	return len(f.frames) - 1
}

func (f *jsFile) importSpec(spec string, line int) string {
	if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") && !strings.HasPrefix(spec, "/") {
		return ""
	}
	target := f.resolve(f.path, spec)
	if target != "" {
		f.imports = append(f.imports, jsImport{target, line})
	}
	return target
}

// declare is the first pass: symbols, frames, imports and exports.
func (f *jsFile) declare() {
	toks := f.toks
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.kind != 'i' {
			continue
		}
		if i > 0 && (f.is(i-1, ".") || f.is(i-1, "?.")) {
			continue
		}
		frame := f.innermost(i)
		exported := f.exportedAt(i)
		switch t.text {
		case "import":
			f.declareImport(i)
		case "export":
			f.declareExport(i)
		case "function":
			i = f.declareFunction(i, frame, exported)
		case "class":
			i = f.declareClass(i, frame, exported)
		case "interface", "enum":
			i = f.declareTypeBlock(i, frame, exported)
		case "type":
			i = f.declareTypeAlias(i, frame, exported)
		case "const", "let", "var":
			if f.frames[frame].isClass {
				continue
			}
			f.declareBinding(i, frame, exported)
		default:
			i = f.declareAssignment(i, frame)
		}
	}
}

// exportedAt reports whether the declaration keyword at i follows export,
// export default or export declare.
func (f *jsFile) exportedAt(i int) bool {
	return f.is(i-1, "export") || f.is(i-2, "export") && f.is(i-1, "default") || f.is(i-1, "declare") && f.is(i-2, "export")
}

// declareExport records export { … } [from 's'] and export * from 's' at i.
func (f *jsFile) declareExport(i int) {
	if f.is(i+1, "{") {
		end := f.match(i + 1)
		for j := i + 2; j < end; j++ {
			if f.tok(j).kind == 'i' && !f.is(j-1, "as") {
				f.exported[f.tok(j).text] = true
			}
		}
		if f.is(end+1, "from") && f.tok(end+2).kind == 's' {
			f.importSpec(f.tok(end+2).text, f.tok(end+2).line)
		}
	} else if f.is(i+1, "*") {
		for j := i + 2; j < i+6 && j < len(f.toks); j++ {
			if f.is(j, "from") && f.tok(j+1).kind == 's' {
				f.importSpec(f.tok(j+1).text, f.tok(j+1).line)
				break
			}
		}
	}
}

// declareFunction handles function name(…) {…} at i and returns the index to
// continue from; anonymous function expressions are named by their binding.
func (f *jsFile) declareFunction(i, frame int, exported bool) int {
	fr := f.frames[frame]
	j := i + 1
	if f.is(j, "*") {
		j++
	}
	name := f.tok(j)
	if name.kind != 'i' || jsKeywords[name.text] || !f.is(j+1, "(") && !f.is(j+1, "<") {
		return i
	}
	k := j + 1
	if f.is(k, "<") {
		k = f.match(k) + 1
	}
	if !f.is(k, "(") {
		return i
	}
	cp := f.match(k)
	bs, be, ok := f.funcBody(cp)
	if !ok {
		return i
	}
	if fr.isClass {
		return i
	}
	isExp := exported || f.is(i-1, "async") && (f.is(i-2, "export") || f.is(i-2, "default"))
	id := f.newSymbol(frame, KindFunc, name.text, "function "+name.text+f.text(k, cp), i, be, isExp)
	fr.decls[name.text] = id
	f.addFrame(frame, id, fr.qname+name.text+".", bs, be, "")
	return k
}

// declareClass handles class Name … {…} at i and its methods.
func (f *jsFile) declareClass(i, frame int, exported bool) int {
	fr := f.frames[frame]
	name := f.tok(i + 1)
	if name.kind != 'i' || jsKeywords[name.text] {
		return i
	}
	j := i + 2
	for j < len(f.toks) && !f.is(j, "{") && j < i+40 {
		j++
	}
	if !f.is(j, "{") {
		return i
	}
	end := f.match(j)
	id := f.newSymbol(frame, KindType, name.text, "class "+name.text, i, end, exported)
	fr.decls[name.text] = id
	idx := f.addFrame(frame, id, fr.qname+name.text+".", j, end, name.text)
	if frame == 0 {
		f.classes[name.text] = idx
	}
	f.classMembers(idx, j, end)
	return j
}

// declareTypeBlock handles a TS interface or enum at i.
func (f *jsFile) declareTypeBlock(i, frame int, exported bool) int {
	fr := f.frames[frame]
	name := f.tok(i + 1)
	if name.kind != 'i' || jsKeywords[name.text] || f.is(i-1, ".") || !(f.is(i+2, "{") || f.is(i+2, "extends") || f.is(i+2, "<")) {
		return i
	}
	j := i + 2
	for j < len(f.toks) && !f.is(j, "{") && j < i+40 {
		j++
	}
	end := j
	if f.is(j, "{") {
		end = f.match(j)
	}
	id := f.newSymbol(frame, KindType, name.text, f.tok(i).text+" "+name.text, i, end, exported)
	fr.decls[name.text] = id
	return end
}

// declareTypeAlias handles a TS type alias at i.
func (f *jsFile) declareTypeAlias(i, frame int, exported bool) int {
	fr := f.frames[frame]
	name := f.tok(i + 1)
	if name.kind != 'i' || jsKeywords[name.text] || !(f.is(i+2, "=") || f.is(i+2, "<")) || fr.isClass {
		return i
	}
	end := f.exprEnd(i + 2)
	id := f.newSymbol(frame, KindType, name.text, "type "+name.text, i, end, exported)
	fr.decls[name.text] = id
	return end
}

// declareAssignment handles a.b.c = function/arrow at statement level.
func (f *jsFile) declareAssignment(i, frame int) int {
	fr := f.frames[frame]
	t := f.tok(i)
	if jsKeywords[t.text] || fr.isClass {
		return i
	}
	j := i
	chain := []string{t.text}
	for f.is(j+1, ".") && f.tok(j+2).kind == 'i' {
		chain = append(chain, f.tok(j+2).text)
		j += 2
	}
	if len(chain) < 2 || !f.is(j+1, "=") {
		return i
	}
	bs, be, sig, ok := f.funcValue(j + 2)
	if !ok {
		return i
	}
	names := chain
	isExp := false
	switch names[0] {
	case "window", "globalThis", "self":
		names, isExp = names[1:], true
	case "module":
		if len(names) > 2 && names[1] == "exports" {
			names, isExp = names[2:], true
		}
	case "exports":
		names, isExp = names[1:], true
	}
	name := names[len(names)-1]
	qual := strings.Join(names, ".")
	id := f.newSymbolQ(frame, KindFunc, name, qual, qual+" = "+sig, i, be, isExp)
	if len(names) == 1 {
		fr.decls[name] = id
	}
	f.addFrame(frame, id, fr.qname+qual+".", bs, be, "")
	return j + 1
}

// newSymbolQ is newSymbol with a dotted qualified suffix.
func (f *jsFile) newSymbolQ(frame int, kind, name, qual, sig string, startTok, endTok int, exported bool) string {
	fr := f.frames[frame]
	saved := fr.qname
	if i := strings.LastIndexByte(qual, '.'); i >= 0 {
		fr.qname = saved + qual[:i+1]
	}
	id := f.newSymbol(frame, kind, name, sig, startTok, endTok, exported)
	fr.qname = saved
	return id
}

// funcValue recognises a function value starting at i: [async] function …(…) {…},
// [async] (…) => …, [async] x => …. It returns its body range and a signature.
func (f *jsFile) funcValue(i int) (int, int, string, bool) {
	j := i
	if f.is(j, "async") {
		j++
	}
	if f.is(j, "function") {
		k := j + 1
		if f.is(k, "*") {
			k++
		}
		if f.tok(k).kind == 'i' && !f.is(k, "(") {
			k++
		}
		if !f.is(k, "(") {
			return 0, 0, "", false
		}
		cp := f.match(k)
		bs, be, ok := f.funcBody(cp)
		return bs, be, "function" + f.text(k, cp), ok
	}
	if f.is(j, "(") {
		cp := f.match(j)
		if !f.is(cp+1, "=>") && !f.is(cp+1, ":") {
			return 0, 0, "", false
		}
		bs, be, ok := f.funcBody(cp)
		if !ok || !f.is(bs-1, "=>") && !f.is(bs, "=>") && !f.arrowBefore(bs) {
			return 0, 0, "", false
		}
		return bs, be, f.text(j, cp) + " =>", true
	}
	if f.tok(j).kind == 'i' && !jsKeywords[f.tok(j).text] && f.is(j+1, "=>") {
		bs, be, ok := f.funcBody(j)
		return bs, be, f.tok(j).text + " =>", ok
	}
	return 0, 0, "", false
}

func (f *jsFile) arrowBefore(bs int) bool {
	for k := bs - 1; k >= 0 && k > bs-20; k-- {
		if f.is(k, "=>") {
			return true
		}
		if f.is(k, ")") {
			return false
		}
	}
	return false
}

// declareBinding handles const/let/var at i.
func (f *jsFile) declareBinding(i, frame int, exported bool) {
	fr := f.frames[frame]
	kw := f.tok(i).text
	name := f.tok(i + 1)
	if f.is(i+1, "{") || f.is(i+1, "[") {
		f.declareDestructured(i)
		return
	}
	if name.kind != 'i' || jsKeywords[name.text] {
		return
	}
	j := i + 2
	if f.is(j, ":") { // TS type annotation
		j = f.skipTypeAnnotation(j)
	}
	if !f.is(j, "=") {
		if frame == 0 {
			fr.decls[name.text] = f.newSymbol(frame, bindingKind(kw), name.text, kw+" "+name.text, i, i+1, exported)
		}
		return
	}
	if f.is(j+1, "require") && f.is(j+2, "(") && f.tok(j+3).kind == 's' {
		if target := f.importSpec(f.tok(j+3).text, f.tok(j+3).line); target != "" {
			f.namespaces[name.text] = target
		}
		return
	}
	if bs, be, sig, ok := f.funcValue(j + 1); ok {
		id := f.newSymbol(frame, KindFunc, name.text, kw+" "+name.text+" = "+sig, i, be, exported)
		fr.decls[name.text] = id
		f.addFrame(frame, id, fr.qname+name.text+".", bs, be, "")
		return
	}
	if frame == 0 {
		end := f.exprEnd(j + 1)
		fr.decls[name.text] = f.newSymbol(frame, bindingKind(kw), name.text, kw+" "+name.text, i, end, exported)
	}
}

func bindingKind(kw string) string {
	if kw == "const" {
		return KindConst
	}
	return KindVar
}

// declareDestructured handles const { a, b: c } = require('./x') at i.
func (f *jsFile) declareDestructured(i int) {
	end := f.match(i + 1)
	if !f.is(end+1, "=") || !f.is(end+2, "require") || !f.is(end+3, "(") || f.tok(end+4).kind != 's' {
		return
	}
	target := f.importSpec(f.tok(end+4).text, f.tok(end+4).line)
	if target == "" {
		return
	}
	for j := i + 2; j < end; j++ {
		if f.tok(j).kind != 'i' {
			continue
		}
		if f.is(j+1, ":") && f.tok(j+2).kind == 'i' {
			f.named[f.tok(j+2).text] = target + "#" + f.tok(j).text
			j += 2
		} else {
			f.named[f.tok(j).text] = target + "#" + f.tok(j).text
		}
	}
}

// skipTypeAnnotation returns the '=' or ';' ending the TS type annotation at j.
func (f *jsFile) skipTypeAnnotation(j int) int {
	depth := 0
	for ; j < len(f.toks); j++ {
		if f.is(j, "<") || f.is(j, "(") || f.is(j, "[") || f.is(j, "{") {
			depth++
		} else if f.is(j, ">") || f.is(j, ")") || f.is(j, "]") || f.is(j, "}") {
			depth--
		} else if depth == 0 && (f.is(j, "=") || f.is(j, ";")) {
			break
		}
	}
	return j
}

func (f *jsFile) declareImport(i int) {
	// import x from 's'; import {a, b as c} from 's'; import * as ns from 's'; import 's'
	if f.tok(i+1).kind == 's' {
		f.importSpec(f.tok(i+1).text, f.tok(i+1).line)
		return
	}
	if f.is(i+1, "(") { // dynamic import('s')
		if f.tok(i+2).kind == 's' {
			f.importSpec(f.tok(i+2).text, f.tok(i+2).line)
		}
		return
	}
	end := i + 1
	for end < len(f.toks) && end < i+200 && !f.is(end, "from") {
		end++
	}
	if !f.is(end, "from") || f.tok(end+1).kind != 's' {
		return
	}
	target := f.importSpec(f.tok(end+1).text, f.tok(end+1).line)
	if target == "" {
		return
	}
	mod := target
	j := i + 1
	if f.is(j, "type") {
		j++
	}
	for j < end {
		t := f.tok(j)
		switch {
		case f.is(j, "*") && f.is(j+1, "as") && f.tok(j+2).kind == 'i':
			f.namespaces[f.tok(j+2).text] = target
			j += 3
		case f.is(j, "{"):
			closeAt := f.match(j)
			f.importNames(j, closeAt, mod)
			j = closeAt + 1
		case t.kind == 'i':
			f.named[t.text] = mod + "#default"
			j++
		default:
			j++
		}
	}
}

// importNames binds the names of import { a, b as c } between open and closeAt.
func (f *jsFile) importNames(open, closeAt int, mod string) {
	for k := open + 1; k < closeAt; k++ {
		if f.tok(k).kind != 'i' || f.is(k, "type") {
			continue
		}
		if f.is(k+1, "as") && f.tok(k+2).kind == 'i' {
			f.named[f.tok(k+2).text] = mod + "#" + f.tok(k).text
			k += 2
		} else {
			f.named[f.tok(k).text] = mod + "#" + f.tok(k).text
		}
	}
}

// classMembers declares the methods of the class body between open and closeAt.
func (f *jsFile) classMembers(frame, open, closeAt int) {
	fr := f.frames[frame]
	for j := open + 1; j < closeAt; j++ {
		if f.is(j, "{") || f.is(j, "(") || f.is(j, "[") {
			j = f.match(j)
			continue
		}
		t := f.tok(j)
		if t.kind != 'i' {
			continue
		}
		// skip modifiers
		k := j
		for f.tok(k).kind == 'i' && memberModifiers[f.tok(k).text] && f.tok(k+1).kind == 'i' {
			k++
		}
		if f.is(k, "*") {
			k++
		}
		name := f.tok(k)
		if name.kind != 'i' {
			continue
		}
		var bs, be int
		var sig string
		ok := false
		if f.is(k+1, "(") || f.is(k+1, "<") {
			p := k + 1
			if f.is(p, "<") {
				p = f.match(p) + 1
			}
			if f.is(p, "(") {
				cp := f.match(p)
				bs, be, ok = f.funcBody(cp)
				sig = fr.class + "." + name.text + f.text(p, cp)
			}
		} else if f.is(k+1, "=") {
			bs, be, sig, ok = f.funcValue(k + 2)
			sig = fr.class + "." + name.text + " = " + sig
		}
		if !ok || be <= bs {
			continue
		}
		id := f.newSymbol(frame, KindMethod, name.text, sig, j, be, false)
		fr.methods[name.text] = id
		f.addFrame(frame, id, fr.qname+name.text+".", bs, be, "")
		j = be
	}
}

// lookup resolves a plain name used inside frame.
func (f *jsFile) lookup(frame int, name string) string {
	for k := frame; k >= 0; k = f.frames[k].parent {
		if id, ok := f.frames[k].decls[name]; ok {
			return id
		}
	}
	if q, ok := f.named[name]; ok {
		return SymbolID(LangJS, q)
	}
	return ""
}

func (f *jsFile) enclosingFunc(frame int) (string, int) {
	for k := frame; k >= 0; k = f.frames[k].parent {
		if !f.frames[k].isClass {
			return f.frames[k].sym, k
		}
	}
	return f.frames[0].sym, 0
}

func (f *jsFile) enclosingClass(frame int) int {
	for k := frame; k >= 0; k = f.frames[k].parent {
		if f.frames[k].isClass {
			return k
		}
	}
	return -1
}

// uses is the second pass: calls and references from each function.
func (f *jsFile) uses() {
	for i := 0; i < len(f.toks); i++ {
		t := f.toks[i]
		if t.kind != 'i' || jsKeywords[t.text] && t.text != "this" {
			continue
		}
		prev := f.tok(i - 1)
		if prev.kind == 'p' && (prev.text == "." || prev.text == "?.") {
			continue
		}
		if prev.kind == 'i' && declaresNext[prev.text] {
			continue
		}
		frame := f.innermost(i)
		if f.frames[frame].isClass {
			continue // a class body's own tokens declare members
		}
		from, _ := f.enclosingFunc(frame)
		// member call: this.m(), C.m(), ns.f()
		if f.is(i+1, ".") && f.tok(i+2).kind == 'i' {
			f.memberUse(i, frame, from)
			if t.text == "this" {
				i += 2
				continue
			}
		}
		if t.text == "this" {
			continue
		}
		f.nameUse(i, frame, from)
	}
}

// memberUse records this.m, C.m and ns.f at i (the token before the '.').
func (f *jsFile) memberUse(i, frame int, from string) {
	t := f.tok(i)
	member := f.tok(i + 2).text
	mcall := f.is(i+3, "(") || f.is(i+3, "?.") && f.is(i+4, "(")
	var target string
	switch {
	case t.text == "this":
		if c := f.enclosingClass(frame); c >= 0 {
			target = f.frames[c].methods[member]
		}
	case f.namespaces[t.text] != "":
		target = SymbolID(LangJS, f.namespaces[t.text]+"#"+member)
	default:
		if c, ok := f.classes[t.text]; ok && f.lookup(frame, t.text) == f.frames[c].sym {
			target = f.frames[c].methods[member]
		}
	}
	if target != "" {
		kind := EdgeReferences
		if mcall {
			kind = EdgeCalls
		}
		f.edge(from, target, kind, f.tok(i+2).line)
	}
}

// nameUse records a call of or reference to the plain name at i.
func (f *jsFile) nameUse(i, frame int, from string) {
	t := f.tok(i)
	// object keys ({ load: x }) and parameters with defaults are not uses
	if f.is(i+1, ":") && (f.is(i-1, "{") || f.is(i-1, ",")) {
		return
	}
	target := f.lookup(frame, t.text)
	if target == "" || target == from {
		return
	}
	kind := EdgeReferences
	call := f.is(i+1, "(") || f.is(i+1, "?.") && f.is(i+2, "(")
	if call && !f.is(i-1, "new") {
		kind = EdgeCalls
	}
	f.edge(from, target, kind, t.line)
}

// jsResolver maps relative specifiers to files of the indexed tree.
func jsResolver(files map[string]bool) func(from, spec string) string {
	exts := []string{"", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts", "/index.ts", "/index.tsx", "/index.js", "/index.jsx", "/index.mjs"}
	return func(from, spec string) string {
		base := path.Clean(path.Join(path.Dir(from), spec))
		if strings.HasPrefix(spec, "/") || strings.HasPrefix(base, "../") || base == ".." {
			return ""
		}
		// TS imports of ./x.js often mean ./x.ts
		candidates := []string{base}
		if ext := path.Ext(base); ext == ".js" || ext == ".jsx" || ext == ".mjs" {
			stem := strings.TrimSuffix(base, ext)
			candidates = append(candidates, stem+".ts", stem+".tsx", stem+".mts")
		}
		for _, c := range candidates {
			for _, e := range exts {
				if files[c+e] {
					return c + e
				}
			}
		}
		return ""
	}
}
