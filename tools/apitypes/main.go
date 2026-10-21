// Command apitypes exposes Go JSON DTO shapes without loading application code
// or contacting a package registry. Unsupported syntax fails closed.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

type shape map[string]any

func fields(list *ast.FieldList) []shape {
	out := []shape{}
	for _, f := range list.List {
		tag := ""
		if f.Tag != nil {
			raw, e := strconv.Unquote(f.Tag.Value)
			must(e)
			tag = reflect.StructTag(raw).Get("json")
		}
		if tag == "-" {
			continue
		}
		parts := strings.Split(tag, ",")
		for _, n := range f.Names {
			if !n.IsExported() {
				continue
			}
			name := parts[0]
			if name == "" {
				name = n.Name
			}
			out = append(out, shape{"name": name, "type": expr(f.Type), "optional": strings.Contains(","+tag+",", ",omitempty,") || strings.Contains(","+tag+",", ",omitzero,"), "comment": strings.TrimSpace(f.Comment.Text())})
		}
		if len(f.Names) == 0 {
			out = append(out, shape{"embedded": true, "type": expr(f.Type)})
		}
	}
	return out
}

func expr(e ast.Expr) shape {
	switch v := e.(type) {
	case *ast.Ident:
		return shape{"kind": "name", "name": v.Name}
	case *ast.SelectorExpr:
		return shape{"kind": "name", "name": v.X.(*ast.Ident).Name + "." + v.Sel.Name}
	case *ast.StarExpr:
		return shape{"kind": "pointer", "elem": expr(v.X)}
	case *ast.ArrayType:
		return shape{"kind": "array", "elem": expr(v.Elt), "slice": v.Len == nil}
	case *ast.MapType:
		return shape{"kind": "map", "key": expr(v.Key), "elem": expr(v.Value)}
	case *ast.StructType:
		return shape{"kind": "struct", "fields": fields(v.Fields)}
	case *ast.InterfaceType:
		return shape{"kind": "interface"}
	case *ast.IndexExpr:
		return shape{"kind": "generic", "name": v.X.(*ast.Ident).Name, "args": []shape{expr(v.Index)}}
	case *ast.IndexListExpr:
		args := []shape{}
		for _, a := range v.Indices {
			args = append(args, expr(a))
		}
		return shape{"kind": "generic", "name": v.X.(*ast.Ident).Name, "args": args}
	default:
		return shape{"kind": "unsupported", "syntax": fmt.Sprintf("%T", e)}
	}
}

func must(e error) {
	if e != nil {
		panic(e)
	}
}

func main() {
	out := map[string]any{}
	for _, pkg := range []string{"memory", "notifycfg"} {
		paths, e := filepath.Glob("internal/" + pkg + "/*.go")
		must(e)
		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			f, e := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
			must(e)
			for _, decl := range f.Decls {
				g, ok := decl.(*ast.GenDecl)
				if !ok || g.Tok != token.TYPE {
					continue
				}
				for _, spec := range g.Specs {
					t := spec.(*ast.TypeSpec)
					params := []string{}
					if t.TypeParams != nil {
						for _, p := range t.TypeParams.List {
							for _, n := range p.Names {
								params = append(params, n.Name)
							}
						}
					}
					out[pkg+"."+t.Name.Name] = shape{"source": filepath.ToSlash(path), "comment": strings.TrimSpace(t.Doc.Text() + g.Doc.Text()), "params": params, "type": expr(t.Type)}
				}
			}
		}
	}
	must(json.NewEncoder(os.Stdout).Encode(out))
}
