package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func typeOf(t *testing.T, src, name string) shape {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", "package x\n"+src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		for _, s := range d.(*ast.GenDecl).Specs {
			if ts := s.(*ast.TypeSpec); ts.Name.Name == name {
				return expr(ts.Type)
			}
		}
	}
	t.Fatalf("type %s not found", name)
	return nil
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFieldsFollowJSONTags(t *testing.T) {
	got := typeOf(t, "type Item struct {\n"+
		"\tID string `json:\"id\"`\n"+
		"\tNote string `json:\"note,omitempty\"` // shown only when set\n"+
		"\tAt Time `json:\"at,omitzero\"`\n"+
		"\tSecret string `json:\"-\"`\n"+
		"\thidden string\n"+
		"\tPlain int\n"+
		"\tBase\n"+
		"}\n", "Item")
	want := `{"fields":[` +
		`{"comment":"","name":"id","optional":false,"type":{"kind":"name","name":"string"}},` +
		`{"comment":"shown only when set","name":"note","optional":true,"type":{"kind":"name","name":"string"}},` +
		`{"comment":"","name":"at","optional":true,"type":{"kind":"name","name":"Time"}},` +
		`{"comment":"","name":"Plain","optional":false,"type":{"kind":"name","name":"int"}},` +
		`{"embedded":true,"type":{"kind":"name","name":"Base"}}` +
		`],"kind":"struct"}`
	if s := jsonOf(t, got); s != want {
		t.Fatalf("got  %s\nwant %s", s, want)
	}
}

func TestExprShapes(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{"type T *time.Time", `{"elem":{"kind":"name","name":"time.Time"},"kind":"pointer"}`},
		{"type T []byte", `{"elem":{"kind":"name","name":"byte"},"kind":"array","slice":true}`},
		{"type T [4]int", `{"elem":{"kind":"name","name":"int"},"kind":"array","slice":false}`},
		{"type T map[string]any", `{"elem":{"kind":"name","name":"any"},"key":{"kind":"name","name":"string"},"kind":"map"}`},
		{"type T interface{ M() }", `{"kind":"interface"}`},
		{"type T Page[Item]", `{"args":[{"kind":"name","name":"Item"}],"kind":"generic","name":"Page"}`},
		{"type T Pair[K, V]", `{"args":[{"kind":"name","name":"K"},{"kind":"name","name":"V"}],"kind":"generic","name":"Pair"}`},
		// Unknown syntax is reported, not guessed, so the contract check fails closed.
		{"type T func() error", `{"kind":"unsupported","syntax":"*ast.FuncType"}`},
		{"type T chan int", `{"kind":"unsupported","syntax":"*ast.ChanType"}`},
	} {
		if s := jsonOf(t, typeOf(t, c.src, "T")); s != c.want {
			t.Errorf("%s: got %s, want %s", c.src, s, c.want)
		}
	}
}

func TestBadTagPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("an unquotable tag must stop the export")
		}
	}()
	// The parser only yields valid tags, so build a broken one by hand.
	fields(&ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent("X")}, Type: ast.NewIdent("int"), Tag: &ast.BasicLit{Kind: token.STRING, Value: `"unterminated`}}}})
}
