package skills

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

// Small Python-parity helpers shared by this package.

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func pyInt(v any) (int64, bool) { return host.Int(v) }

func utf8Valid(b []byte) bool { return utf8.Valid(b) }

func containsStr(list []string, s string) bool { return slices.Contains(list, s) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// truthy is Python's bool(v) for decoded JSON values.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case []string:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	case host.Obj:
		return len(x) > 0
	}
	return true
}

func str(v any) string { s, _ := v.(string); return s }

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

func asList(v any) []any { l, _ := v.([]any); return l }

// orMap is Python's `v or {}` for a dict value.
func orMap(v any) map[string]any {
	if m := asMap(v); m != nil {
		return m
	}
	return map[string]any{}
}

// pyStr is str(v) for decoded JSON scalars (None is "None", True is "True").
func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return x
	case json.Number:
		return x.String()
	case float64, int, int64:
		return host.MustDumps(x, host.DumpOpts{})
	}
	return host.MustDumps(v, host.DumpOpts{NoASCII: true})
}

// purePosix is str(PurePosixPath(s)): empty and "." parts drop, "" becomes ".".
func purePosix(s string) string {
	lead := ""
	switch {
	case strings.HasPrefix(s, "//") && !strings.HasPrefix(s, "///"):
		lead = "//"
	case strings.HasPrefix(s, "/"):
		lead = "/"
	}
	var parts []string
	for _, p := range strings.Split(s, "/") {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	out := lead + strings.Join(parts, "/")
	if out == "" {
		return "."
	}
	return out
}

// comparePaths orders like sorted() over pathlib paths: part by part, so "a/b" < "a.txt".
func comparePaths(a, b string) int {
	return slices.Compare(strings.Split(a, "/"), strings.Split(b, "/"))
}

func sortPaths(paths []string) { slices.SortFunc(paths, comparePaths) }

// dumpIndent is json.dumps(v, indent=n) with ensure_ascii off unless ascii.
func dumpIndent(v any, n int, ascii bool) (string, error) {
	compact, err := host.Dumps(v, host.DumpOpts{Compact: true, NoASCII: !ascii})
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(compact), "", strings.Repeat(" ", n)); err != nil {
		return "", err
	}
	// json.Indent escapes nothing but writes "[]"/"{}" for empty values like Python does.
	return out.String(), nil
}

func printJSON(w io.Writer, v any, indent int) {
	s, err := dumpIndent(v, indent, false)
	if err != nil {
		s = fmt.Sprint(v)
	}
	fmt.Fprintln(w, s)
}

// pyQuote is urllib.parse.quote(s, safe="").
func pyQuote(s string) string { return quote(s, false) }

// pyQuotePlus is urllib.parse.quote_plus(s).
func pyQuotePlus(s string) string { return quote(s, true) }

func quote(s string, plus bool) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if plus && c == ' ' {
			b.WriteByte('+')
		} else if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("_.-~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// pyURLEncode is urlencode over ordered pairs.
func pyURLEncode(pairs [][2]string) string {
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = pyQuotePlus(p[0]) + "=" + pyQuotePlus(p[1])
	}
	return strings.Join(out, "&")
}

// scopeQuery is urlencode({k: v for k, v in scope().items() if v}) in Python's key order.
func scopeQuery(scope map[string]any) string {
	var pairs [][2]string
	for _, k := range []string{"workspace_id", "project_id", "user_id", "agent_id"} {
		if v := str(scope[k]); v != "" {
			pairs = append(pairs, [2]string{k, v})
		}
	}
	return pyURLEncode(pairs)
}

// splitLines is str.splitlines() (no keepends).
func splitLines(s string) []string {
	var out []string
	start := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\n', '\v', '\f', '\x1c', '\x1d', '\x1e', '\x85', ' ', ' ':
			out = append(out, string(rs[start:i]))
			start = i + 1
		case '\r':
			out = append(out, string(rs[start:i]))
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

// splitLinesKeep is str.splitlines(keepends=True).
func splitLinesKeep(s string) []string {
	var out []string
	start := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\n', '\v', '\f', '\x1c', '\x1d', '\x1e', '\x85', ' ', ' ':
			out = append(out, string(rs[start:i+1]))
			start = i + 1
		case '\r':
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			out = append(out, string(rs[start:i+1]))
			start = i + 1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

// runeCut is s[:n] on code points.
func runeCut(s string, n int) string {
	if n < 0 {
		n = 0
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// resolve is Path.resolve(strict=False): links followed as far as the path exists.
func resolve(p string) string {
	p, _ = filepath.Abs(p)
	rest := ""
	for cur := p; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func isDir(p string) bool  { st, err := os.Stat(p); return err == nil && st.IsDir() }
func isFile(p string) bool { st, err := os.Stat(p); return err == nil && st.Mode().IsRegular() }
