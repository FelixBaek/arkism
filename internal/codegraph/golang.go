package codegraph

import (
	"context"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/tools/go/packages"
)

const goLoadMode = packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports |
	packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule

// goIndexer turns loaded packages into facts. Only objects of the indexed
// modules become symbols or edge targets; calls into the standard library and
// third-party modules are counted, not stored.
type goIndexer struct {
	root       string
	modules    []string // module paths of the indexed tree
	facts      map[string]*FileFacts
	edgeSeen   map[string]bool
	typeErrors int
	external   int
	packages   int
	// for implements: concrete named types and interfaces seen, with where they live
	concrete   []goType
	interfaces map[string]*types.TypeName
	where      map[string]symPos // symbol id -> declaring file and line
}

type goType struct {
	obj  *types.TypeName
	path string
	line int
}

func newGoIndexer(root string) *goIndexer {
	return &goIndexer{root: root, facts: map[string]*FileFacts{}, edgeSeen: map[string]bool{}, interfaces: map[string]*types.TypeName{}, where: map[string]symPos{}}
}

// load reads patterns in one module directory and adds the facts of every
// package it found. Type errors keep the partial information go/types still has.
func (g *goIndexer) load(ctx context.Context, moduleDir string, patterns []string, tests bool) error {
	cfg := &packages.Config{Context: ctx, Mode: goLoadMode, Dir: moduleDir, Tests: tests, Env: append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=readonly")}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, p := range pkgs {
		if p.Module != nil && p.Module.Path != "" && !contains(g.modules, p.Module.Path) {
			g.modules = append(g.modules, p.Module.Path)
		}
	}
	for _, p := range pkgs {
		// With tests on, "p [p.test]" repeats p plus its test files; keep the
		// widest variant and skip the generated test main.
		if strings.HasSuffix(p.PkgPath, ".test") || seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		if tests && !strings.Contains(p.ID, " [") && hasTestVariant(pkgs, p) {
			continue
		}
		g.typeErrors += len(p.Errors) + len(p.TypeErrors)
		if p.Types == nil || p.TypesInfo == nil {
			continue
		}
		g.packages++
		g.pkg(p)
		g.collectInterfaces(p, map[string]bool{})
	}
	return nil
}

func hasTestVariant(all []*packages.Package, p *packages.Package) bool {
	for _, q := range all {
		if q != p && q.PkgPath == p.PkgPath && strings.HasPrefix(q.ID, p.PkgPath+" [") {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (g *goIndexer) inRepo(pkg *types.Package) bool {
	if pkg == nil {
		return false
	}
	path := pkg.Path()
	for _, m := range g.modules {
		if path == m || strings.HasPrefix(path, m+"/") {
			return true
		}
	}
	return false
}

func (g *goIndexer) rel(filename string) (string, bool) {
	r, err := filepath.Rel(g.root, filename)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return "", false
	}
	return filepath.ToSlash(r), true
}

func (g *goIndexer) file(path string) *FileFacts {
	f := g.facts[path]
	if f == nil {
		f = &FileFacts{Path: path, Lang: LangGo, Symbols: []Symbol{}, Edges: []Edge{}}
		g.facts[path] = f
	}
	return f
}

func (g *goIndexer) addSymbol(s Symbol) {
	s.Lang = LangGo
	s.ID = SymbolID(LangGo, s.QName)
	s.Signature = clip(s.Signature, 300)
	s.Doc = clip(s.Doc, 200)
	if _, dup := g.where[s.ID]; dup {
		return // a package seen twice (test variants) keeps its first declaration
	}
	g.where[s.ID] = symPos{s.Path, s.StartLine}
	f := g.file(s.Path)
	f.Symbols = append(f.Symbols, s)
}

func (g *goIndexer) addEdge(from, toQName, kind, path string, line int) {
	to := SymbolID(LangGo, toQName)
	if to == from {
		return
	}
	key := from + "|" + to + "|" + kind
	if g.edgeSeen[key] {
		return
	}
	g.edgeSeen[key] = true
	f := g.file(path)
	f.Edges = append(f.Edges, Edge{From: from, To: to, Kind: kind, Path: path, Line: line})
}

// objQName is the qualified name of a package-level object or method:
// importpath.Name or importpath.Recv.Name. Empty for objects that get no symbol.
func objQName(obj types.Object) string {
	if obj == nil || obj.Pkg() == nil {
		return ""
	}
	pkg := obj.Pkg().Path()
	if fn, ok := obj.(*types.Func); ok {
		fn = fn.Origin()
		sig, _ := fn.Type().(*types.Signature)
		if sig != nil && sig.Recv() != nil {
			name := recvName(sig.Recv().Type())
			if name == "" {
				return ""
			}
			return pkg + "." + name + "." + fn.Name()
		}
		return pkg + "." + fn.Name()
	}
	if obj.Parent() != obj.Pkg().Scope() {
		return "" // locals, fields and parameters are not symbols
	}
	return pkg + "." + obj.Name()
}

func recvName(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := types.Unalias(t).(*types.Named); ok {
		return n.Origin().Obj().Name()
	}
	return ""
}

func firstLine(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}
	text := strings.TrimSpace(doc.Text())
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	return text
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func (g *goIndexer) pkg(p *packages.Package) {
	fset := p.Fset
	qual := types.RelativeTo(p.Types)
	files := make([]string, 0, len(p.Syntax))
	type fileAST struct {
		path string
		f    *ast.File
	}
	var asts []fileAST
	for _, f := range p.Syntax {
		path, ok := g.rel(fset.Position(f.Pos()).Filename)
		if !ok {
			continue
		}
		files = append(files, path)
		asts = append(asts, fileAST{path, f})
		g.file(path) // a file with no declarations still records its hash
	}
	if len(asts) == 0 {
		return
	}
	sort.Strings(files)
	pkgID := SymbolID(LangGo, p.PkgPath)
	doc := ""
	for _, a := range asts {
		if d := firstLine(a.f.Doc); d != "" {
			doc = d
			break
		}
	}
	g.addSymbol(Symbol{Path: files[0], Kind: KindPackage, Name: p.Name, QName: p.PkgPath, Signature: "package " + p.Name, StartLine: 1, EndLine: 1, Exported: true, Doc: doc})
	for _, a := range asts {
		for _, imp := range a.f.Imports {
			path := strings.Trim(imp.Path.Value, "\"`")
			if ip := p.Imports[path]; ip != nil && ip.Types != nil && g.inRepo(ip.Types) {
				g.addEdge(pkgID, path, EdgeImports, files[0], fset.Position(imp.Pos()).Line)
			}
		}
	}
	for _, a := range asts {
		for _, decl := range a.f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				g.funcDecl(p, a.path, d, qual)
			case *ast.GenDecl:
				g.genDecl(p, a.path, d, qual)
			}
		}
	}
}

func (g *goIndexer) lines(fset *token.FileSet, from, to token.Pos) (int, int) {
	return fset.Position(from).Line, fset.Position(to).Line
}

func (g *goIndexer) funcDecl(p *packages.Package, path string, d *ast.FuncDecl, qual types.Qualifier) {
	fn, _ := p.TypesInfo.Defs[d.Name].(*types.Func)
	if fn == nil || d.Name.Name == "_" {
		return
	}
	qname := objQName(fn)
	if qname == "" {
		return
	}
	kind, exported := KindFunc, fn.Exported()
	if d.Recv != nil {
		kind = KindMethod
		sig := fn.Type().(*types.Signature)
		exported = exported && ast.IsExported(recvName(sig.Recv().Type()))
	}
	if d.Name.Name == "init" && d.Recv == nil {
		qname += "#" + path // several init functions per package
	}
	start, end := g.lines(p.Fset, d.Pos(), d.End())
	g.addSymbol(Symbol{Path: path, Kind: kind, Name: fn.Name(), QName: qname, Signature: types.ObjectString(fn, qual), StartLine: start, EndLine: end, Exported: exported, Doc: firstLine(d.Doc)})
	if d.Body != nil {
		g.walk(p, path, SymbolID(LangGo, qname), d.Body)
	}
	if d.Type != nil {
		g.walkTypes(p, path, SymbolID(LangGo, qname), d.Type)
	}
}

func typeSummary(tn *types.TypeName, qual types.Qualifier) string {
	if tn.IsAlias() {
		return "type " + tn.Name() + " = " + types.TypeString(tn.Type(), qual)
	}
	head := "type " + tn.Name()
	if n, ok := tn.Type().(*types.Named); ok && n.TypeParams().Len() > 0 {
		var tp []string
		for i := 0; i < n.TypeParams().Len(); i++ {
			v := n.TypeParams().At(i)
			tp = append(tp, v.Obj().Name()+" "+types.TypeString(v.Constraint(), qual))
		}
		head += "[" + strings.Join(tp, ", ") + "]"
	}
	switch u := tn.Type().Underlying().(type) {
	case *types.Struct:
		return head + " struct"
	case *types.Interface:
		return head + " interface"
	default:
		return head + " " + types.TypeString(u, qual)
	}
}

func (g *goIndexer) genDecl(p *packages.Package, path string, d *ast.GenDecl, qual types.Qualifier) {
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			g.typeSpec(p, path, d, s, qual)
		case *ast.ValueSpec:
			g.valueSpec(p, path, d, s, qual)
		}
	}
}

// specDoc is the doc comment of a spec, or of its declaration when it is the only spec.
func specDoc(d *ast.GenDecl, doc *ast.CommentGroup) *ast.CommentGroup {
	if doc == nil && len(d.Specs) == 1 {
		return d.Doc
	}
	return doc
}

func (g *goIndexer) typeSpec(p *packages.Package, path string, d *ast.GenDecl, s *ast.TypeSpec, qual types.Qualifier) {
	tn, _ := p.TypesInfo.Defs[s.Name].(*types.TypeName)
	if tn == nil || s.Name.Name == "_" {
		return
	}
	qname := objQName(tn)
	start, end := g.lines(p.Fset, s.Pos(), s.End())
	id := SymbolID(LangGo, qname)
	g.addSymbol(Symbol{Path: path, Kind: KindType, Name: tn.Name(), QName: qname, Signature: typeSummary(tn, qual), StartLine: start, EndLine: end, Exported: tn.Exported(), Doc: firstLine(specDoc(d, s.Doc))})
	g.walkTypes(p, path, id, s.Type)
	if it, ok := s.Type.(*ast.InterfaceType); ok && !tn.IsAlias() {
		g.interfaceMethods(p, path, tn, it, qual)
	} else if !tn.IsAlias() {
		if n, ok := tn.Type().(*types.Named); ok && n.TypeParams().Len() == 0 {
			if _, isIface := n.Underlying().(*types.Interface); !isIface {
				g.concrete = append(g.concrete, goType{obj: tn, path: path, line: start})
			}
		}
	}
}

func (g *goIndexer) valueSpec(p *packages.Package, path string, d *ast.GenDecl, s *ast.ValueSpec, qual types.Qualifier) {
	kind := KindVar
	if d.Tok == token.CONST {
		kind = KindConst
	}
	var first string
	for _, name := range s.Names {
		obj := p.TypesInfo.Defs[name]
		if obj == nil || name.Name == "_" {
			continue
		}
		qname := objQName(obj)
		if qname == "" {
			continue
		}
		start, end := g.lines(p.Fset, s.Pos(), s.End())
		g.addSymbol(Symbol{Path: path, Kind: kind, Name: obj.Name(), QName: qname, Signature: kind + " " + obj.Name() + " " + types.TypeString(obj.Type(), qual), StartLine: start, EndLine: end, Exported: obj.Exported(), Doc: firstLine(specDoc(d, s.Doc))})
		if first == "" {
			first = SymbolID(LangGo, qname)
		}
	}
	if first == "" {
		return
	}
	for _, v := range s.Values {
		g.walk(p, path, first, v)
	}
	if s.Type != nil {
		g.walkTypes(p, path, first, s.Type)
	}
}

// interfaceMethods gives each method a named interface declares its own symbol,
// so a call through the interface has a target.
func (g *goIndexer) interfaceMethods(p *packages.Package, path string, tn *types.TypeName, it *ast.InterfaceType, qual types.Qualifier) {
	if it.Methods == nil {
		return
	}
	for _, field := range it.Methods.List {
		for _, name := range field.Names {
			fn, _ := p.TypesInfo.Defs[name].(*types.Func)
			if fn == nil {
				continue
			}
			qname := p.PkgPath + "." + tn.Name() + "." + fn.Name()
			start, end := g.lines(p.Fset, field.Pos(), field.End())
			doc := field.Doc
			if doc == nil {
				doc = field.Comment
			}
			g.addSymbol(Symbol{Path: path, Kind: KindMethod, Name: fn.Name(), QName: qname, Signature: "func (" + tn.Name() + ")." + fn.Name() + strings.TrimPrefix(types.TypeString(fn.Type(), qual), "func"), StartLine: start, EndLine: end, Exported: fn.Exported() && tn.Exported(), Doc: firstLine(doc)})
		}
	}
}

// walkTypes records the repository types a declaration's type expression uses.
func (g *goIndexer) walkTypes(p *packages.Package, path, from string, expr ast.Node) {
	ast.Inspect(expr, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if tn, ok := p.TypesInfo.Uses[id].(*types.TypeName); ok && g.inRepo(tn.Pkg()) {
			if q := objQName(tn); q != "" {
				g.addEdge(from, q, EdgeReferences, path, p.Fset.Position(id.Pos()).Line)
			}
		}
		return true
	})
}

func unparen(e ast.Expr) ast.Expr {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.IndexExpr: // generic instantiation f[T](...)
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		default:
			return e
		}
	}
}

// walk records calls and references inside one declaration. Function literals
// belong to the declaration that contains them.
func (g *goIndexer) walk(p *packages.Package, path, from string, body ast.Node) {
	info := p.TypesInfo
	callee := map[*ast.Ident]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			var id *ast.Ident
			switch f := unparen(x.Fun).(type) {
			case *ast.Ident:
				id = f
			case *ast.SelectorExpr:
				id = f.Sel
			}
			if id == nil {
				return true
			}
			switch obj := info.Uses[id].(type) {
			case *types.Func:
				callee[id] = true
				if !g.inRepo(obj.Pkg()) {
					g.external++
					return true
				}
				if q := objQName(obj); q != "" {
					g.addEdge(from, q, EdgeCalls, path, p.Fset.Position(x.Lparen).Line)
				}
			}
		case *ast.Ident:
			if callee[x] {
				return true
			}
			obj := info.Uses[x]
			switch obj.(type) {
			case *types.Func, *types.TypeName, *types.Var, *types.Const:
			default:
				return true
			}
			if !g.inRepo(obj.Pkg()) {
				return true
			}
			if q := objQName(obj); q != "" {
				g.addEdge(from, q, EdgeReferences, path, p.Fset.Position(x.Pos()).Line)
			}
		}
		return true
	})
}

// collectInterfaces remembers the named, non-empty interfaces of the indexed
// modules reachable from p (export data is enough for imported packages).
func (g *goIndexer) collectInterfaces(p *packages.Package, seen map[string]bool) {
	var visit func(pkg *types.Package)
	visit = func(pkg *types.Package) {
		if pkg == nil || seen[pkg.Path()] || !g.inRepo(pkg) {
			return
		}
		seen[pkg.Path()] = true
		scope := pkg.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			n, ok := tn.Type().(*types.Named)
			if !ok || n.TypeParams().Len() > 0 {
				continue
			}
			if it, ok := n.Underlying().(*types.Interface); ok && it.NumMethods() > 0 {
				g.interfaces[objQName(tn)] = tn
			}
		}
		for _, imp := range pkg.Imports() {
			visit(imp)
		}
	}
	visit(p.Types)
}

// implements adds type → interface edges and concrete method → interface
// method edges, owned by the file of the concrete declaration.
func (g *goIndexer) implements() {
	names := make([]string, 0, len(g.interfaces))
	for q := range g.interfaces {
		names = append(names, q)
	}
	sort.Strings(names)
	for _, c := range g.concrete {
		t := c.obj.Type()
		ptr := types.NewPointer(t)
		from := SymbolID(LangGo, objQName(c.obj))
		for _, q := range names {
			tn := g.interfaces[q]
			it := tn.Type().Underlying().(*types.Interface)
			if tn == c.obj || !(types.Implements(t, it) || types.Implements(ptr, it)) {
				continue
			}
			g.addEdge(from, q, EdgeImplements, c.path, c.line)
			for i := 0; i < it.NumMethods(); i++ {
				m := it.Method(i)
				obj, _, _ := types.LookupFieldOrMethod(ptr, true, m.Pkg(), m.Name())
				fn, ok := obj.(*types.Func)
				if !ok || fn.Pkg() != c.obj.Pkg() {
					continue
				}
				mq, iq := objQName(fn), interfaceMethodQName(m)
				if mq == "" || iq == "" || !g.inRepo(m.Pkg()) {
					continue
				}
				at, ok := g.where[SymbolID(LangGo, mq)]
				if !ok {
					continue
				}
				g.addEdge(SymbolID(LangGo, mq), iq, EdgeImplements, at.path, at.line)
			}
		}
	}
}

// interfaceMethodQName names an abstract method after the named interface that declares it.
func interfaceMethodQName(m *types.Func) string {
	sig, _ := m.Type().(*types.Signature)
	if sig == nil || sig.Recv() == nil || m.Pkg() == nil {
		return ""
	}
	name := recvName(sig.Recv().Type())
	if name == "" {
		return ""
	}
	return m.Pkg().Path() + "." + name + "." + m.Name()
}

type symPos struct {
	path string
	line int
}
