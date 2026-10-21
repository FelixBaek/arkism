package memory

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"os"
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
// Patterns built in a loop over a literal list ("GET /v1/"+col) are expanded;
// any other non-literal pattern fails the test instead of being skipped.
func sourceRoutes(t *testing.T) []routeSpec {
	t.Helper()
	names, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	consts := map[string]string{}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = f
		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.CONST {
				for _, sp := range g.Specs {
					vs := sp.(*ast.ValueSpec)
					for i, id := range vs.Names {
						if i < len(vs.Values) {
							if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
								consts[id.Name], _ = strconv.Unquote(lit.Value)
							}
						}
					}
				}
			}
		}
	}
	var out []routeSpec
	for name, f := range files {
		type loopVar struct {
			name     string
			from, to token.Pos
			vals     []string
		}
		var loops []loopVar // outer loops come before the loops they contain
		var eval func(e ast.Expr, at token.Pos) []string
		eval = func(e ast.Expr, at token.Pos) []string {
			switch e := e.(type) {
			case *ast.BasicLit:
				if s, err := strconv.Unquote(e.Value); err == nil && e.Kind == token.STRING {
					return []string{s}
				}
			case *ast.Ident:
				for i := len(loops) - 1; i >= 0; i-- {
					if l := loops[i]; l.name == e.Name && l.from <= at && at < l.to {
						return l.vals
					}
				}
				if s, ok := consts[e.Name]; ok {
					return []string{s}
				}
			case *ast.ParenExpr:
				return eval(e.X, at)
			case *ast.BinaryExpr:
				var joined []string
				if e.Op == token.ADD {
					for _, a := range eval(e.X, at) {
						for _, b := range eval(e.Y, at) {
							joined = append(joined, a+b)
						}
					}
				}
				return joined
			}
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if rs, ok := n.(*ast.RangeStmt); ok {
				if cl, ok := rs.X.(*ast.CompositeLit); ok {
					v := rs.Value
					if _, isMap := cl.Type.(*ast.MapType); isMap {
						v = rs.Key
					}
					var vals []string
					for _, el := range cl.Elts {
						if kv, ok := el.(*ast.KeyValueExpr); ok {
							el = kv.Key
						}
						vals = append(vals, eval(el, el.Pos())...)
					}
					if id, ok := v.(*ast.Ident); ok && len(vals) == len(cl.Elts) {
						loops = append(loops, loopVar{id.Name, rs.Body.Pos(), rs.Body.End(), vals})
					}
				}
				return true
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "HandleFunc" {
				return true
			}
			patterns := eval(call.Args[0], call.Pos())
			if len(patterns) == 0 {
				t.Fatalf("%s: HandleFunc pattern is neither a literal nor built from a literal loop list; the route contract tests cannot see it", fset.Position(call.Pos()))
			}
			decodesFirst := false
			if fn, ok := call.Args[1].(*ast.FuncLit); ok && len(fn.Body.List) >= 2 {
				if _, isDecl := fn.Body.List[0].(*ast.DeclStmt); isDecl {
					if ifs, isIf := fn.Body.List[1].(*ast.IfStmt); isIf {
						if u, isNot := ifs.Cond.(*ast.UnaryExpr); isNot {
							if c, isCall := u.X.(*ast.CallExpr); isCall {
								if id, isID := c.Fun.(*ast.Ident); isID && strings.HasPrefix(id.Name, "decode") {
									decodesFirst = true
								}
							}
						}
					}
				}
			}
			for _, pattern := range patterns {
				method, path, ok := strings.Cut(pattern, " ")
				if !ok {
					continue
				}
				out = append(out, routeSpec{file: name, method: method, path: path, decodesFirst: decodesFirst})
			}
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
	// Transport validation is exercised with every primary permission granted.
	// Authorization-denial coverage lives in the common middleware tests.
	f.svc.Auth = policyDecision(Decision{Allowed: true, PrincipalID: "human-1", Role: roleHuman}, nil)
	h := Handler(f.svc)
	n := 0
	for _, r := range sourceRoutes(t) {
		if httpRoutePolicies[r.method+" "+r.path].scope != routeJSON {
			continue
		}
		n++
		// Supply valid coarse metadata so unknown-field checks reach the typed
		// transport decoder instead of failing only because scope is absent.
		invalid := structuredText(map[string]any{"scope": scopeA, "skill": map[string]any{"scope": scopeA}, "kind": "topic", "memories": []Ref{{Collection: "memories"}, {Collection: "memories"}}, "not_a_field_zz": 1})
		for _, body := range []string{invalid, invalid + `{}`, `{`} {
			path := concretePath(strings.ReplaceAll(r.path, "{collection}", "memories"))
			req := httptest.NewRequest(r.method, path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+f.human)
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

// Route coverage is enforced alongside the runtime authorization/input contracts.
func TestOpenAPICoversRegisteredRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err = json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	for _, r := range sourceRoutes(t) {
		path := strings.ReplaceAll(r.path, "/{$}", "/")
		if _, ok := spec.Paths[path][strings.ToLower(r.method)]; !ok {
			t.Errorf("missing OpenAPI operation: %s %s (%s)", r.method, path, r.file)
		}
		policy, ok := httpRoutePolicies[r.method+" "+r.path]
		if !ok || policy.scope == routePublic {
			continue
		}
		var operation struct {
			Responses map[string]json.RawMessage `json:"responses"`
		}
		if err := json.Unmarshal(spec.Paths[path][strings.ToLower(r.method)], &operation); err != nil {
			t.Fatal(err)
		}
		if _, ok := operation.Responses["401"]; !ok {
			t.Errorf("missing authentication response: %s %s", r.method, path)
		}
	}
}
