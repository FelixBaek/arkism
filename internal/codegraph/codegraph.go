// Package codegraph builds a symbol and call graph of a source tree on the host
// where the code lives. Go is read with go/packages and go/types (exact: every
// edge is a resolved object); JavaScript and TypeScript are read with a small
// lexer (heuristic: only same-file, lexical-scope and relative-import calls are
// resolved, dynamic dispatch is not). The server only stores what this package
// produces; it never parses code.
//
// Facts are owned by the file that declares them, so an upload can replace the
// facts of changed files only. Go is re-read per package (a changed file reloads
// its whole package), JavaScript/TypeScript per file.
package codegraph

import (
	"crypto/sha256"
	"encoding/hex"
)

// Symbol kinds and edge kinds the server accepts.
const (
	KindFunc    = "func"
	KindMethod  = "method"
	KindType    = "type"
	KindVar     = "var"
	KindConst   = "const"
	KindPackage = "package" // a Go package, the endpoint of imports edges
	KindModule  = "module"  // a JS/TS file, the endpoint of imports edges

	EdgeCalls      = "calls"
	EdgeReferences = "references"
	EdgeImplements = "implements"
	EdgeImports    = "imports"

	LangGo = "go"
	LangJS = "js" // also TypeScript
)

// Symbol is one declaration. ID is stable for the same qualified name, so an
// edge from an unchanged file keeps pointing at a symbol that was re-indexed.
type Symbol struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Lang      string `json:"lang"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	QName     string `json:"qname"`
	Signature string `json:"signature"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Exported  bool   `json:"exported"`
	Doc       string `json:"doc,omitempty"`
}

// Edge is a directed fact from one symbol to another, owned by Path (the file
// of From) and located at Line there.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

// FileFacts is everything one file contributes. A file with no facts (a test
// file, a file excluded by build tags, a too-large file) is still listed so its
// hash is recorded and an incremental run skips it next time.
type FileFacts struct {
	Path    string   `json:"path"`
	Hash    string   `json:"hash"`
	Lang    string   `json:"lang"`
	Symbols []Symbol `json:"symbols"`
	Edges   []Edge   `json:"edges"`
}

// SymbolID is the stable id of a qualified name in one language.
func SymbolID(lang, qname string) string {
	sum := sha256.Sum256([]byte(lang + "\x00" + qname))
	return hex.EncodeToString(sum[:12])
}

// HashBytes is the content hash an incremental run compares.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
