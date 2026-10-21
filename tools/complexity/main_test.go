package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestBranchCountingKeepsClosureIndependent(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", `package p
func f(a,b bool) { if a && b {} else if a {} ; for a {} ; switch { case a: ; default: }; _ = func() { if a || b {} } }`, 0)
	if err != nil {
		t.Fatal(err)
	}
	fn := f.Decls[0].(*ast.FuncDecl)
	if got := complexity(fn.Body); got != 6 {
		t.Fatalf("parent = %d, want 6", got)
	}
	var closure *ast.FuncLit
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if v, ok := n.(*ast.FuncLit); ok {
			closure = v
		}
		return true
	})
	if got := complexity(closure.Body); got != 3 {
		t.Fatalf("closure = %d, want 3", got)
	}
}

func TestNewAndExistingDebtCannotIncrease(t *testing.T) {
	base := map[string]int{"legacy": 31}
	for _, tc := range []struct {
		name            string
		score, failures int
	}{
		{"legacy", 31, 0}, {"legacy", 32, 1}, {"legacy", 20, 0}, {"new", 20, 0}, {"new", 21, 1},
	} {
		if got := len(violations([]entry{{Name: tc.name, Complexity: tc.score}}, base)); got != tc.failures {
			t.Fatalf("%s=%d: got %d failures, want %d", tc.name, tc.score, got, tc.failures)
		}
	}
}
