package codegraph

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// graph indexes facts by qualified name for assertions.
type graph struct {
	byID    map[string]Symbol
	byQName map[string]Symbol
	edges   map[string]bool // "fromQ kind toQ"
	files   map[string]FileFacts
}

func load(t *testing.T, res Result) graph {
	t.Helper()
	g := graph{byID: map[string]Symbol{}, byQName: map[string]Symbol{}, edges: map[string]bool{}, files: map[string]FileFacts{}}
	for _, f := range res.Files {
		g.files[f.Path] = f
		for _, s := range f.Symbols {
			if s.ID != SymbolID(s.Lang, s.QName) && !strings.Contains(s.QName, "#") {
				t.Fatalf("unstable id for %s", s.QName)
			}
			if s.Path != f.Path {
				t.Fatalf("symbol %s owned by %s, listed in %s", s.QName, s.Path, f.Path)
			}
			g.byID[s.ID], g.byQName[s.QName] = s, s
		}
	}
	for _, f := range res.Files {
		for _, e := range f.Edges {
			from, ok := g.byID[e.From]
			if !ok || from.Path != e.Path || e.Path != f.Path {
				t.Fatalf("edge %+v does not start at a symbol of its own file", e)
			}
			to, ok := g.byID[e.To]
			name := e.To
			if ok {
				name = to.QName
			}
			g.edges[from.QName+" "+e.Kind+" "+name] = true
		}
	}
	return g
}

// has also matches an edge whose target lives in a file this run did not return.
func (g graph) has(e string) bool {
	if g.edges[e] {
		return true
	}
	parts := strings.SplitN(e, " ", 3)
	lang := LangGo
	if strings.Contains(parts[2], "#") || strings.HasSuffix(parts[2], ".js") || strings.HasSuffix(parts[2], ".ts") {
		lang = LangJS
	}
	return g.edges[parts[0]+" "+parts[1]+" "+SymbolID(lang, parts[2])]
}

func (g graph) must(t *testing.T, edges ...string) {
	t.Helper()
	for _, e := range edges {
		if !g.has(e) {
			t.Errorf("missing edge %q", e)
		}
	}
}

func (g graph) mustNot(t *testing.T, edges ...string) {
	t.Helper()
	for _, e := range edges {
		if g.has(e) {
			t.Errorf("unexpected edge %q", e)
		}
	}
}

const fx = "example.com/fixture/"

func TestIndexFixtureGo(t *testing.T) {
	res, err := Index(context.Background(), Options{Root: "testdata/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.TypeErrors != 0 || res.Stats.GoPackages != 2 || res.Stats.Mode != "full" {
		t.Fatalf("stats %+v %v", res.Stats, res.Warnings)
	}
	g := load(t, res)
	g.must(t,
		fx+"report.Summary calls "+fx+"shapes.Total",
		fx+"report.Summary calls "+fx+"shapes.NewSquare",
		fx+"report.Summary references "+fx+"shapes.Shape",
		fx+"report.Summary references "+fx+"shapes.Circle",
		fx+"report.Twice calls "+fx+"report.Summary",
		fx+"report.Top references "+fx+"report.Twice", // a function value, not a call
		fx+"report.Top calls "+fx+"report.helper",
		fx+"shapes.Total calls "+fx+"shapes.Shape.Area", // through the interface
		fx+"shapes.Square implements "+fx+"shapes.Shape",
		fx+"shapes.Circle implements "+fx+"shapes.Shape", // pointer receiver
		fx+"shapes.Square.Area implements "+fx+"shapes.Shape.Area",
		fx+"shapes.Circle.Area implements "+fx+"shapes.Shape.Area",
		fx+"shapes.Circle.Area references "+fx+"shapes.Pi",
		fx+"shapes.Default calls "+fx+"shapes.NewSquare", // package-level initializer
		fx+"report imports "+fx+"shapes",
	)
	g.mustNot(t, fx+"report.Top calls "+fx+"report.Twice")
	sq := g.byQName[fx+"shapes.Square.Area"]
	if sq.Kind != KindMethod || sq.Signature != "func (Square).Area() float64" || sq.Doc != "Area of a square." || !sq.Exported || sq.StartLine != 12 {
		t.Fatalf("square area %+v", sq)
	}
	if s := g.byQName[fx+"shapes.Shape"]; s.Signature != "type Shape interface" || s.Doc != "Shape is anything with an area." {
		t.Fatalf("shape %+v", s)
	}
	if s := g.byQName[fx+"report.helper"]; s.Exported || s.Kind != KindFunc {
		t.Fatalf("helper %+v", s)
	}
	if s := g.byQName[fx+"shapes"]; s.Kind != KindPackage || s.Doc != "Package shapes is a code graph fixture." {
		t.Fatalf("package %+v", s)
	}
	// the test file is recorded (hash) without facts; tests are off by default
	if f, ok := g.files["shapes/shapes_test.go"]; !ok || len(f.Symbols) != 0 || len(f.Hash) != 64 {
		t.Fatalf("test file %+v", f)
	}
}

func TestIndexFixtureJS(t *testing.T) {
	res, err := Index(context.Background(), Options{Root: "testdata/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	g := load(t, res)
	g.must(t,
		"web/app.js#View.render calls web/app.js#View.title", // this.title()
		"web/app.js#View.render calls web/util.js#twice",     // imported under another name
		"web/app.js#View.title calls web/util.js#fmt",
		"web/app.js#main references web/app.js#View", // new View()
		"web/app.js#main calls web/util.js#strip",    // namespace import
		"web/app.js calls web/util.js#fmt",           // inside ${} of a module-level template
		"web/app.js calls web/app.js#main",
		"web/app.js imports web/util.js",
		"web/util.js#twice calls web/util.js#fmt",
		"web/util.js#strip references web/util.js#pattern",
		"web/types.ts#load calls web/types.ts#parse", // inside an arrow callback, after a TS return type
	)
	if s := g.byQName["web/util.js#fmt"]; s.Kind != KindFunc || !s.Exported || s.StartLine != 2 {
		t.Fatalf("fmt %+v", s)
	}
	if s := g.byQName["web/app.js#View.render"]; s.Kind != KindMethod || s.StartLine != 5 {
		t.Fatalf("render %+v", s)
	}
	if s := g.byQName["web/types.ts#Item"]; s.Kind != KindType || !s.Exported {
		t.Fatalf("interface %+v", s)
	}
	if s := g.byQName["web/app.js#View"]; s.EndLine != 7 {
		t.Fatalf("class range %+v", s)
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.Walk(from, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func known(res Result, prev map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range prev {
		out[k] = v
	}
	for _, p := range res.Removed {
		delete(out, p)
	}
	for _, f := range res.Files {
		out[f.Path] = f.Hash
	}
	return out
}

func TestIncrementalByContentHash(t *testing.T) {
	root := t.TempDir()
	copyTree(t, "testdata/fixture", root)
	ctx := context.Background()
	first, err := Index(ctx, Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	state := known(first, nil)
	if len(state) != 6 {
		t.Fatalf("files %v", state)
	}
	// nothing changed: nothing to upload
	again, err := Index(ctx, Options{Root: root, Known: state})
	if err != nil || len(again.Files) != 0 || len(again.Removed) != 0 || again.Stats.GoPackages != 0 || again.Stats.Mode != "incremental" {
		t.Fatalf("unchanged run %+v %v", again.Stats, err)
	}
	// one Go file changes: its package is re-read, the other package is not
	report := filepath.Join(root, "report", "report.go")
	b, _ := os.ReadFile(report)
	if err := os.WriteFile(report, []byte(strings.Replace(string(b), "return Summary() * 2", "return Summary() + Fresh()", 1)+"\nfunc Fresh() float64 { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// one JS file is deleted, one edited
	if err := os.Remove(filepath.Join(root, "web", "types.ts")); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(root, "web", "app.js")
	b, _ = os.ReadFile(app)
	if err := os.WriteFile(app, []byte(strings.Replace(string(b), "return u.strip(v.render());", "return u.fmt(v.render());", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	inc, err := Index(ctx, Options{Root: root, Known: state})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range inc.Files {
		paths = append(paths, f.Path)
	}
	if strings.Join(paths, ",") != "report/report.go,web/app.js" || strings.Join(inc.Removed, ",") != "web/types.ts" || inc.Stats.GoPackages != 1 {
		t.Fatalf("incremental files %v removed %v stats %+v", paths, inc.Removed, inc.Stats)
	}
	g := load(t, inc)
	g.must(t, fx+"report.Twice calls "+fx+"report.Fresh", "web/app.js#main calls web/util.js#fmt")
	g.mustNot(t, "web/app.js#main calls web/util.js#strip")
	// cross-package edges from the re-read package still point at the unchanged one
	g.must(t, fx+"report.Summary calls "+fx+"shapes.Total")
	state = known(inc, state)
	if _, ok := state["web/types.ts"]; ok || len(state) != 5 {
		t.Fatalf("state %v", state)
	}
}

func TestIndexArkismMemoryPackage(t *testing.T) {
	if testing.Short() {
		t.Skip("loads the repository's memory package")
	}
	started := time.Now()
	res, err := Index(context.Background(), Options{Root: "../..", Only: []string{"internal/memory"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("internal/memory: %d files, %d symbols, %d edges in %v", len(res.Files), res.Stats.Symbols, res.Stats.Edges, time.Since(started))
	for _, f := range res.Files {
		if !strings.HasPrefix(f.Path, "internal/memory/") {
			t.Fatalf("outside Only: %s", f.Path)
		}
	}
	g := load(t, res)
	const m = "github.com/FelixBaek/arkism/internal/memory."
	g.must(t,
		m+"Service.CodeReviews calls "+m+"Service.authorize",
		m+"Service.CodeReviews calls "+m+"validScope",
		m+"Service.authorize calls "+m+"Service.authorizeAt",
		m+"Postgres implements "+m+"Tx",
		m+"Postgres.Get implements "+m+"Tx.Get",
		m+"Service.CodeReviews calls "+m+"Tx.Get", // s.Repo.Get: Repository embeds Tx, the method belongs to Tx
	)
	if res.Stats.TypeErrors != 0 {
		t.Fatalf("type errors %d %v", res.Stats.TypeErrors, res.Warnings)
	}
}
