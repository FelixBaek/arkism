// complexity reports Go cyclomatic complexity, retaining visible legacy debt
// while refusing new functions above 20 or increases above the frozen baseline.
// This metric is not Sonar's cognitive complexity.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const limit = 20

type entry struct {
	Name       string `json:"name"`
	Line       int    `json:"line"`
	Complexity int    `json:"cyclomatic_complexity"`
}

func complexity(body *ast.BlockStmt) int {
	n := 1
	ast.Inspect(body, func(node ast.Node) bool {
		switch v := node.(type) {
		case *ast.FuncLit:
			// Closures have independent control flow; their parent does not
			// inherit it. They are inventoried separately below.
			return false
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
			n++
		case *ast.CaseClause:
			if v.List != nil {
				n++
			}
		case *ast.CommClause:
			if v.Comm != nil {
				n++
			}
		case *ast.BinaryExpr:
			if v.Op == token.LAND || v.Op == token.LOR {
				n++
			}
		}
		return true
	})
	return n
}

func fileEntries(path string) ([]entry, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	var entries []entry
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		name := fn.Name.Name
		if fn.Recv != nil {
			// Receiver names are obtained structurally rather than printing
			// source positions, so file movement never resets the baseline.
			receiverName := ""
			ast.Inspect(fn.Recv.List[0].Type, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok {
					receiverName = id.Name
				}
				return true
			})
			name = receiverName + "." + name
		}
		prefix := filepath.ToSlash(path) + ":" + name
		entries = append(entries, entry{prefix, fset.Position(fn.Pos()).Line, complexity(fn.Body)})
		closures := 0
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if lit, ok := n.(*ast.FuncLit); ok {
				closures++
				entries = append(entries, entry{fmt.Sprintf("%s/closure%d", prefix, closures), fset.Position(lit.Pos()).Line, complexity(lit.Body)})
			}
			return true
		})
	}
	return entries, nil
}

func inventory() ([]entry, error) {
	var out []entry
	for _, root := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "testdata" || d.Name() == "dshzstd") {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			entries, err := fileEntries(path)
			out = append(out, entries...)
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func violations(entries []entry, baseline map[string]int) []entry {
	var failed []entry
	for _, item := range entries {
		allowed := max(limit, baseline[item.Name])
		if item.Complexity > allowed {
			failed = append(failed, item)
		}
	}
	return failed
}

func run() error {
	baselinePath := flag.String("baseline", "config/go-complexity-baseline.json", "frozen legacy baseline")
	reportPath := flag.String("report", "", "JSON report including all measured functions")
	write := flag.Bool("write-baseline", false, "explicit baseline initialization; never used by CI")
	flag.Parse()
	entries, err := inventory()
	if err != nil {
		return err
	}
	baseline := map[string]int{}
	if *write {
		return writeBaseline(entries, *baselinePath)
	}
	data, err := os.ReadFile(*baselinePath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &baseline); err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Complexity == entries[j].Complexity {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].Complexity > entries[j].Complexity
	})
	if *reportPath != "" {
		if err := writeJSON(*reportPath, entries); err != nil {
			return err
		}
	}
	return report(entries, baseline)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func writeBaseline(entries []entry, path string) error {
	baseline := map[string]int{}
	for _, item := range entries {
		if item.Complexity > limit {
			baseline[item.Name] = item.Complexity
		}
	}
	return writeJSON(path, baseline)
}

func report(entries []entry, baseline map[string]int) error {
	debt := 0
	for _, item := range entries {
		if item.Complexity > limit {
			debt++
		}
	}
	fmt.Printf("Go cyclomatic complexity: %d functions, %d above %d (legacy debt is reported, not hidden)\n", len(entries), debt, limit)
	for _, item := range entries[:min(20, len(entries))] {
		fmt.Printf("%4d %s (line %d)\n", item.Complexity, item.Name, item.Line)
	}
	failed := violations(entries, baseline)
	for _, item := range failed {
		fmt.Fprintf(os.Stderr, "complexity increased or new function exceeds %d: %s = %d\n", limit, item.Name, item.Complexity)
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d complexity violations", len(failed))
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
