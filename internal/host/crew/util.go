package crew

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

// errValue is a Python ValueError/KeyError/TypeError: a refusal without a fixed code.
var errValue = errors.New("invalid value")

// strictJSON is json.loads that refuses duplicate keys, NaN/Infinity, invalid UTF-8 and trailing
// data. Numbers come back as json.Number in Python's own text (1.0, 100.0, 1e+16), so canonical()
// writes what Python's json.dumps would after a round trip.
func strictJSON(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errValue
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := strictValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errValue
	}
	return v, nil
}

func strictValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		if msg := err.Error(); strings.Contains(msg, "character 'N'") || strings.Contains(msg, "character 'I'") {
			return nil, &comparisonError{"nonfinite_json"} // NaN, Infinity, -Infinity
		}
		return nil, errValue
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := map[string]any{}
			for dec.More() {
				k, err := dec.Token()
				if err != nil {
					return nil, errValue
				}
				key, _ := k.(string)
				if _, dup := obj[key]; dup {
					return nil, &comparisonError{"duplicate_json_key"}
				}
				if obj[key], err = strictValue(dec); err != nil {
					return nil, err
				}
			}
			if _, err := dec.Token(); err != nil {
				return nil, errValue
			}
			return obj, nil
		case '[':
			list := []any{}
			for dec.More() {
				v, err := strictValue(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, errValue
			}
			return list, nil
		}
		return nil, errValue
	case json.Number:
		return pyNumber(t)
	default:
		return t, nil
	}
}

// memberKeys lists, in file order, the keys of the object at raw[field] (Go maps forget the order
// Python dicts keep). raw must already have passed strictJSON.
func memberKeys(raw []byte, field string) []string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	skip := func() { var v json.RawMessage; _ = dec.Decode(&v) }
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return nil
		}
		if k != field {
			skip()
			continue
		}
		if t, err := dec.Token(); err != nil || t != json.Delim('{') {
			return nil
		}
		var keys []string
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return keys
			}
			keys = append(keys, k.(string))
			skip()
		}
		return keys
	}
	return nil
}

// pyNumber re-writes a JSON number the way Python prints it after json.loads.
func pyNumber(n json.Number) (json.Number, error) {
	s := string(n)
	if !strings.ContainsAny(s, ".eE") {
		i, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return "", errValue
		}
		return json.Number(i.String()), nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", errValue
	}
	return pyFloat(f), nil
}

// pyFloat is Python's repr(float): shortest digits, exponent form below 1e-4 or from 1e16.
func pyFloat(f float64) json.Number {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return json.Number(strconv.FormatFloat(f, 'g', -1, 64)) // never encoded: callers check finiteness
	}
	e := strconv.FormatFloat(f, 'e', -1, 64)
	exp, _ := strconv.Atoi(e[strings.IndexByte(e, 'e')+1:])
	if exp < -4 || exp >= 16 {
		return json.Number(e)
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return json.Number(s)
}

// canonical is json.dumps(v, sort_keys=True, ensure_ascii=False, separators=(",", ":")).
func canonical(v any) (string, error) {
	return host.Dumps(v, host.DumpOpts{Compact: true, NoASCII: true, SortKeys: true})
}

func mustCanonical(v any) string {
	s, err := canonical(v)
	if err != nil {
		panic(err)
	}
	return s
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// num reads a JSON number (json.Number, int or float64) as a float, the way Python compares them.
func num(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		return f, err == nil
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

// same is Python ==: numbers compare by value, containers element by element.
func same(a, b any) bool {
	if x, ok := num(a); ok {
		y, ok := num(b)
		return ok && x == y
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !same(v, w) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !same(x[i], y[i]) {
				return false
			}
		}
		return true
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	}
	// Other shapes (built in code): compare what they encode to.
	ca, err1 := canonical(a)
	cb, err2 := canonical(b)
	return err1 == nil && err2 == nil && ca == cb
}

// truthy is Python's bool(v) for JSON values.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, _ := num(x)
		return f != 0
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
	}
	return true
}

func str(v any) string { s, _ := v.(string); return s }

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }

func list(v any) []any { l, _ := v.([]any); return l }

// keysAre reports whether m's keys are exactly keys (Python set(m) == {...}).
func keysAre(m map[string]any, keys ...string) bool {
	if len(m) != len(keys) {
		return false
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// pyQuote is urllib.parse.quote(s, safe): everything but letters, digits, "_.-~" and safe is %XX.
func pyQuote(s, safe string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_.-~", c) >= 0 ||
			strings.IndexByte(safe, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// pyStr is Python str() for a JSON scalar (urlencode values).
func pyStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case nil:
		return "None"
	}
	return fmt.Sprint(v)
}

// dumpIndent is json.dumps(v, ensure_ascii=False, indent=1).
func dumpIndent(v any) string {
	compact := host.MustDumps(v, host.DumpOpts{Compact: true, NoASCII: true})
	var b bytes.Buffer
	if json.Indent(&b, []byte(compact), "", " ") != nil {
		return compact
	}
	return b.String()
}

// dumpLine is json.dumps(v, ensure_ascii=False).
func dumpLine(v any) string { return host.MustDumps(v, host.DumpOpts{NoASCII: true}) }

func expandUser(p string) string {
	if p == "~" {
		return host.Home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(host.Home, p[2:])
	}
	return p
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// tailRunes is s[-n:].
func tailRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[len(r)-n:])
	}
	return s
}

// at is Python's m[k]: a missing key or a non-object panics with errValue, which catch turns into
// the error Python's KeyError/TypeError would have been.
func at(v any, k string) any {
	m, ok := v.(map[string]any)
	if !ok {
		panic(errValue)
	}
	x, ok := m[k]
	if !ok {
		panic(errValue)
	}
	return x
}

// getOr is Python's m.get(k, def) (a non-object panics like AttributeError).
func getOr(v any, k string, def any) any {
	m, ok := v.(map[string]any)
	if !ok {
		panic(errValue)
	}
	if x, ok := m[k]; ok {
		return x
	}
	return def
}

// mustStr is a value Python used as a str (x.encode(), "a" + x): anything else panics.
func mustStr(v any) string {
	s, ok := v.(string)
	if !ok {
		panic(errValue)
	}
	return s
}

// catch turns errValue and runtime panics (bad type assertions, nil maps, short slices) into errValue,
// the way the Python tools caught KeyError/TypeError/ValueError around the same code.
func catch(err *error) {
	if r := recover(); r != nil {
		if e, ok := r.(error); ok && (e == errValue || isRuntime(e)) {
			*err = errValue
			return
		}
		panic(r)
	}
}

func isRuntime(e error) bool {
	_, ok := e.(interface{ RuntimeError() })
	return ok
}

// pySum is Python 3.12+ sum() over JSON numbers: exact while every item is an int, then a float
// with Neumaier compensation. The result keeps Python's int/float type (json.Number text).
func pySum(values []any) (json.Number, error) {
	total, isInt := new(big.Int), true
	var f, c float64
	for _, v := range values {
		if isInt {
			if _, ok := host.Int(v); ok {
				n, _ := new(big.Int).SetString(fmt.Sprint(v), 10)
				total.Add(total, n)
				continue
			}
			isInt = false
			f, _ = new(big.Float).SetInt(total).Float64()
		}
		x, ok := num(v)
		if !ok {
			return "", errValue
		}
		t := f + x
		if math.Abs(f) >= math.Abs(x) {
			c += (f - t) + x
		} else {
			c += (x - t) + f
		}
		f = t
	}
	if isInt {
		return json.Number(total.String()), nil
	}
	if c != 0 && !math.IsInf(c, 0) && !math.IsNaN(c) {
		f += c
	}
	return pyFloat(f), nil
}

// pySplitlines is str.splitlines: every Unicode line boundary, no trailing empty line.
func pySplitlines(s string) []string {
	var lines []string
	start := 0
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '\n', '\v', '\f', '\x1c', '\x1d', '\x1e', '\u0085', ' ', ' ':
		case '\r':
			if i+1 < len(runes) && runes[i+1] == '\n' {
				lines = append(lines, string(runes[start:i]))
				i++
				start = i + 1
				continue
			}
		default:
			continue
		}
		lines = append(lines, string(runes[start:i]))
		start = i + 1
	}
	if start < len(runes) {
		lines = append(lines, string(runes[start:]))
	}
	return lines
}

// withKey is {**m, k: v}: a copy of m with k set.
func withKey(m map[string]any, k string, v any) map[string]any {
	out := make(map[string]any, len(m))
	for mk, mv := range m {
		out[mk] = mv
	}
	out[k] = v
	return out
}
