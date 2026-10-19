package codegraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Options of one indexing run.
type Options struct {
	Root string
	// Known is path -> content hash of what the server already has. nil means a
	// full run; otherwise only changed packages and files are read.
	Known map[string]string
	// Tests includes Go _test.go files (as test variants of their packages).
	Tests bool
	// MaxFileBytes skips larger JS/TS files without loading them into memory
	// (their streamed hash is still recorded). Default 1 MiB.
	MaxFileBytes int64
	// Only limits the run to these directory prefixes (slash paths relative to
	// Root, e.g. internal/memory). Known paths outside them are left alone.
	Only []string
}

func (o Options) inOnly(p string) bool {
	if len(o.Only) == 0 {
		return true
	}
	for _, prefix := range o.Only {
		prefix = strings.Trim(prefix, "/")
		if prefix == "" || prefix == "." || p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// Stats describe one run.
type Stats struct {
	Mode         string         `json:"mode"` // full | incremental
	Files        int            `json:"files"`
	Changed      int            `json:"changed_files"`
	Removed      int            `json:"removed_files"`
	Uploaded     int            `json:"uploaded_files"`
	GoPackages   int            `json:"go_packages"`
	GoFiles      int            `json:"go_files"`
	JSFiles      int            `json:"js_files"`
	Symbols      int            `json:"symbols"`
	Edges        int            `json:"edges"`
	ByKind       map[string]int `json:"by_kind"`
	TypeErrors   int            `json:"type_errors"`
	ExternalCall int            `json:"external_calls"`
	SkippedLarge int            `json:"skipped_large"`
	DurationMS   int64          `json:"duration_ms"`
	JSParser     string         `json:"js_parser"`
}

// Result is what to upload: the complete facts of every file in Files (they
// replace whatever the server had for those paths) and the paths to drop.
type Result struct {
	Files    []FileFacts
	Removed  []string
	Stats    Stats
	Warnings []string
}

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true, "target": true,
	"coverage": true, "testdata": true, "__pycache__": true, ".venv": true, "venv": true, ".next": true, "out": true}

var jsExts = map[string]bool{".js": true, ".mjs": true, ".cjs": true, ".jsx": true, ".ts": true, ".tsx": true, ".mts": true, ".cts": true}

type srcFile struct {
	path, hash, lang string
	abs              string
	size             int64
}

// scan lists the Go and JS/TS files under root with their content hashes.
func scan(root string, keep func(string) bool) (map[string]srcFile, []string, error) {
	files := map[string]srcFile{}
	var modules []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (skipDirs[name] || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		lang := ""
		switch ext := path.Ext(name); {
		case name == "go.mod":
			modules = append(modules, path.Dir(rel))
			return nil
		case ext == ".go":
			lang = LangGo
		case jsExts[ext] && !strings.HasSuffix(name, ".min.js") && !strings.HasSuffix(name, ".d.ts"):
			lang = LangJS
		default:
			return nil
		}
		if !keep(rel) {
			return nil
		}
		hash, size, err := hashFile(p)
		if err != nil {
			return err
		}
		files[rel] = srcFile{path: rel, hash: hash, lang: lang, abs: p, size: size}
		return nil
	})
	sort.Strings(modules)
	return files, modules, err
}

// hashFile streams the file, so the scan never holds a whole file in memory;
// it matches HashBytes of the same content.
func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// moduleOf is the innermost module directory containing rel ("" if none).
func moduleOf(modules []string, rel string) (string, bool) {
	best, ok := "", false
	for _, m := range modules {
		if m == "." || rel == m || strings.HasPrefix(rel, m+"/") {
			if !ok || len(m) > len(best) {
				best, ok = m, true
			}
		}
	}
	return best, ok
}

// Index reads root and returns the facts to upload.
func Index(ctx context.Context, opt Options) (Result, error) {
	started := time.Now()
	root, err := filepath.Abs(opt.Root)
	if err != nil {
		return Result{}, err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return Result{}, errors.New("codegraph: not a directory: " + opt.Root)
	}
	if opt.MaxFileBytes <= 0 {
		opt.MaxFileBytes = 1 << 20
	}
	files, modules, err := scan(root, opt.inOnly)
	if err != nil {
		return Result{}, err
	}
	res := Result{Stats: Stats{Mode: "full", Files: len(files), ByKind: map[string]int{}, JSParser: "lexer-v1"}}
	full := opt.Known == nil
	if !full {
		res.Stats.Mode = "incremental"
	}
	changed := map[string]bool{}
	for p, f := range files {
		if full || opt.Known[p] != f.hash {
			changed[p] = true
		}
		if f.lang == LangGo {
			res.Stats.GoFiles++
		} else {
			res.Stats.JSFiles++
		}
	}
	for p := range opt.Known {
		if _, ok := files[p]; !ok && opt.inOnly(p) {
			res.Removed = append(res.Removed, p)
		}
	}
	sort.Strings(res.Removed)
	res.Stats.Changed, res.Stats.Removed = len(changed), len(res.Removed)

	out := map[string]FileFacts{}

	// Go: reload every package with a changed or removed file.
	g := newGoIndexer(root)
	patterns := map[string]map[string]bool{} // module dir -> patterns
	touch := func(rel string) {
		m, ok := moduleOf(modules, rel)
		if !ok {
			return
		}
		dir := path.Dir(rel)
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir))); err != nil {
			return
		}
		pat := "./..."
		if !full || len(opt.Only) > 0 {
			r := strings.TrimPrefix(strings.TrimPrefix(dir, m), "/")
			if m == "." {
				r = dir
			}
			pat = "./" + r
			if r == "." || r == "" {
				pat = "."
			}
		}
		if patterns[m] == nil {
			patterns[m] = map[string]bool{}
		}
		patterns[m][pat] = true
	}
	for p := range changed {
		if files[p].lang == LangGo {
			touch(p)
		}
	}
	for _, p := range res.Removed {
		if strings.HasSuffix(p, ".go") {
			touch(p)
		}
	}
	mods := make([]string, 0, len(patterns))
	for m := range patterns {
		mods = append(mods, m)
	}
	sort.Strings(mods)
	for _, m := range mods {
		var pats []string
		for p := range patterns[m] {
			pats = append(pats, p)
		}
		sort.Strings(pats)
		if err := g.load(ctx, filepath.Join(root, filepath.FromSlash(m)), pats, opt.Tests); err != nil {
			res.Warnings = append(res.Warnings, "go load "+m+": "+err.Error())
		}
	}
	g.implements()
	for p, ff := range g.facts {
		f, ok := files[p]
		if !ok {
			continue // generated or outside the tree
		}
		ff.Hash = f.hash
		out[p] = *ff
	}
	res.Stats.GoPackages, res.Stats.TypeErrors, res.Stats.ExternalCall = g.packages, g.typeErrors, g.external
	if res.Stats.TypeErrors > 0 {
		res.Warnings = append(res.Warnings, "go: type errors were ignored (partial facts kept)")
	}

	// JS/TS: per file.
	jsFiles := map[string]bool{}
	for p, f := range files {
		if f.lang == LangJS {
			jsFiles[p] = true
		}
	}
	resolve := jsResolver(jsFiles)
	for p := range changed {
		f := files[p]
		switch f.lang {
		case LangJS:
			if f.size > opt.MaxFileBytes {
				res.Stats.SkippedLarge++
				out[p] = FileFacts{Path: p, Hash: f.hash, Lang: LangJS, Symbols: []Symbol{}, Edges: []Edge{}}
				continue
			}
			b, err := os.ReadFile(f.abs)
			if err != nil {
				return Result{}, err
			}
			ff := indexJS(p, string(b), resolve)
			ff.Hash = f.hash
			out[p] = ff
		case LangGo:
			if _, ok := out[p]; !ok { // excluded by build tags or a test file
				out[p] = FileFacts{Path: p, Hash: f.hash, Lang: LangGo, Symbols: []Symbol{}, Edges: []Edge{}}
			}
		}
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
	}
	paths := make([]string, 0, len(out))
	for p := range out {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		ff := out[p]
		res.Files = append(res.Files, ff)
		res.Stats.Symbols += len(ff.Symbols)
		res.Stats.Edges += len(ff.Edges)
		for _, s := range ff.Symbols {
			res.Stats.ByKind[s.Kind]++
		}
		for _, e := range ff.Edges {
			res.Stats.ByKind["edge:"+e.Kind]++
		}
	}
	res.Stats.Uploaded = len(res.Files)
	res.Stats.DurationMS = time.Since(started).Milliseconds()
	return res, nil
}
