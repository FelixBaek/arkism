package memory

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type routeSpec struct {
	file, method, path string
	decodesFirst       bool // handler body starts with `var in T; if !decode(...)`
}

// sourceRoutes reads every mux.HandleFunc pattern from the package's own
// sources, so a newly added route is covered without editing this test.
func sourceRoutes(t *testing.T) []routeSpec {
	t.Helper()
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	var out []routeSpec
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "HandleFunc" {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok {
				return true
			}
			pattern, _ := strconv.Unquote(lit.Value)
			method, path, ok := strings.Cut(pattern, " ")
			if !ok {
				return true
			}
			spec := routeSpec{file: name, method: method, path: path}
			if fn, ok := call.Args[1].(*ast.FuncLit); ok && len(fn.Body.List) >= 2 {
				if _, isDecl := fn.Body.List[0].(*ast.DeclStmt); isDecl {
					if ifs, isIf := fn.Body.List[1].(*ast.IfStmt); isIf {
						if u, isNot := ifs.Cond.(*ast.UnaryExpr); isNot {
							if c, isCall := u.X.(*ast.CallExpr); isCall {
								if id, isID := c.Fun.(*ast.Ident); isID && strings.HasPrefix(id.Name, "decode") {
									spec.decodesFirst = true
								}
							}
						}
					}
				}
			}
			out = append(out, spec)
			return true
		})
	}
	return out
}

var routeWildcard = regexp.MustCompile(`\{[^}]+\}`)

func concretePath(p string) string {
	p = strings.TrimSuffix(p, "{$}")
	return routeWildcard.ReplaceAllString(p, "x1")
}

// Every JSON route is strict: an unknown field is refused before the service
// runs, even for a fully privileged caller.
func TestHTTPRoutesRejectUnknownJSONFields(t *testing.T) {
	f := newFixture(t)
	h := Handler(f.svc)
	n := 0
	for _, r := range sourceRoutes(t) {
		if !r.decodesFirst {
			continue
		}
		n++
		for _, body := range []string{`{"not_a_field_zz":1}`, `{}{}`, `{`} {
			req := httptest.NewRequest(r.method, concretePath(r.path), strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+f.agent)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			var got map[string]string
			_ = json.Unmarshal(w.Body.Bytes(), &got)
			if w.Code != 400 || got["code"] != "invalid_request" {
				t.Errorf("%s %s %s (%s) body %q: %d %s", r.file, r.method, r.path, concretePath(r.path), body, w.Code, w.Body.String())
			}
		}
	}
	if n < 50 {
		t.Fatalf("only %d decoding routes found; parser drifted", n)
	}
}

// Anonymous callers never get a success or an internal error from any route.
func TestHTTPRoutesRefuseAnonymousCallers(t *testing.T) {
	f := newFixture(t)
	h := Handler(f.svc)
	public := map[string]bool{"/{$}": true, "/healthz": true, "/readyz": true}
	for _, r := range sourceRoutes(t) {
		if public[r.path] {
			continue
		}
		req := httptest.NewRequest(r.method, concretePath(r.path)+"?workspace_id=ws-a&project_id=proj-a", strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code < 400 || w.Code >= 500 && w.Code != 503 {
			t.Errorf("%s %s %s: %d %s", r.file, r.method, r.path, w.Code, w.Body.String())
		}
	}
}
