package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

// Values in this package are the JSON trees Parse returns, matching what Python's json.loads
// hands the translators:
//   - objects are host.Obj, so key order survives into the output like a Python dict;
//   - arrays are []any, strings string, true/false bool, null nil;
//   - every finite number is a json.Number spelled the way Python re-serializes it
//     (integers as written, floats as repr: 0.50 -> 0.5, 1e2 -> 100.0);
//   - NaN, Infinity and overflowing floats are float64, which every JSON check rejects;
//   - a lone UTF-16 surrogate escape becomes its WTF-8 bytes, so the string is not valid
//     UTF-8 and fails the same checks Python's UnicodeEncodeError did.

// maxParseDepth only protects the stack; Python's own limit sits near 58k levels.
const maxParseDepth = 10000

// maxNesting stands in for Python's RecursionError while copying or converting caller schemas
// (deepcopy fails near 500 levels); deeper input is refused with the caller's code.
const maxNesting = 500

var errSyntax = errors.New("invalid JSON")

// Parse decodes JSON like Python's json.loads: NaN and Infinity are accepted and a repeated key
// keeps its first position with the last value.
func Parse(data string) (any, error) { return parse(data, false) }

func parse(data string, strict bool) (any, error) {
	if !utf8.ValidString(data) {
		return nil, errSyntax
	}
	p := &parser{s: data, strict: strict}
	p.space()
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.space()
	if p.i != len(p.s) {
		return nil, errSyntax
	}
	return v, nil
}

type parser struct {
	s      string
	i      int
	strict bool // _loads: duplicate keys and NaN/Infinity are errors
}

func (p *parser) space() {
	for p.i < len(p.s) && strings.IndexByte(" \t\n\r", p.s[p.i]) >= 0 {
		p.i++
	}
}

func (p *parser) literal(word string) bool {
	if strings.HasPrefix(p.s[p.i:], word) {
		p.i += len(word)
		return true
	}
	return false
}

func (p *parser) value(depth int) (any, error) {
	if depth > maxParseDepth || p.i >= len(p.s) {
		return nil, errSyntax
	}
	switch c := p.s[p.i]; {
	case c == '{':
		p.i++
		obj := host.Obj{}
		p.space()
		if p.literal("}") {
			return obj, nil
		}
		for {
			p.space()
			if p.i >= len(p.s) || p.s[p.i] != '"' {
				return nil, errSyntax
			}
			key, err := p.str()
			if err != nil {
				return nil, err
			}
			p.space()
			if !p.literal(":") {
				return nil, errSyntax
			}
			p.space()
			item, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			if _, dup := obj.Get(key); dup && p.strict {
				return nil, errSyntax
			}
			obj.Set(key, item)
			p.space()
			if p.literal("}") {
				return obj, nil
			}
			if !p.literal(",") {
				return nil, errSyntax
			}
		}
	case c == '[':
		p.i++
		list := []any{}
		p.space()
		if p.literal("]") {
			return list, nil
		}
		for {
			p.space()
			item, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			list = append(list, item)
			p.space()
			if p.literal("]") {
				return list, nil
			}
			if !p.literal(",") {
				return nil, errSyntax
			}
		}
	case c == '"':
		return p.str()
	case p.literal("true"):
		return true, nil
	case p.literal("false"):
		return false, nil
	case p.literal("null"):
		return nil, nil
	case p.literal("NaN"):
		return p.constant(math.NaN())
	case p.literal("Infinity"):
		return p.constant(math.Inf(1))
	case p.literal("-Infinity"):
		return p.constant(math.Inf(-1))
	default:
		return p.number()
	}
}

func (p *parser) constant(f float64) (any, error) {
	if p.strict {
		return nil, errSyntax
	}
	return f, nil
}

// number follows Python's scanner: -?(0|[1-9]\d*)(\.\d+)?([eE][-+]?\d+)?
func (p *parser) number() (any, error) {
	start := p.i
	digits := func() int {
		n := 0
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
			n++
		}
		return n
	}
	if p.i < len(p.s) && p.s[p.i] == '-' {
		p.i++
	}
	if p.i < len(p.s) && p.s[p.i] == '0' {
		p.i++
	} else if digits() == 0 {
		return nil, errSyntax
	}
	float := false
	if p.i+1 < len(p.s) && p.s[p.i] == '.' && p.s[p.i+1] >= '0' && p.s[p.i+1] <= '9' {
		p.i++
		digits()
		float = true
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		save := p.i
		p.i++
		if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
			p.i++
		}
		if digits() == 0 {
			p.i = save
		} else {
			float = true
		}
	}
	text := p.s[start:p.i]
	if !float {
		if text == "-0" {
			text = "0"
		}
		return json.Number(text), nil
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil && !math.IsInf(f, 0) {
		return nil, errSyntax
	}
	if math.IsInf(f, 0) {
		return f, nil
	}
	return json.Number(pyFloat(f)), nil
}

func (p *parser) str() (string, error) {
	p.i++ // opening quote
	var b strings.Builder
	for {
		if p.i >= len(p.s) {
			return "", errSyntax
		}
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return b.String(), nil
		case c < 0x20:
			return "", errSyntax
		case c != '\\':
			b.WriteByte(c)
			p.i++
			continue
		}
		if p.i+1 >= len(p.s) {
			return "", errSyntax
		}
		e := p.s[p.i+1]
		p.i += 2
		if r, ok := map[byte]byte{'"': '"', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}[e]; ok {
			b.WriteByte(r)
			continue
		}
		if e != 'u' {
			return "", errSyntax
		}
		r, ok := p.hex4()
		if !ok {
			return "", errSyntax
		}
		if r >= 0xd800 && r < 0xdc00 && strings.HasPrefix(p.s[p.i:], `\u`) {
			save := p.i
			p.i += 2
			if low, ok := p.hex4(); ok && low >= 0xdc00 && low < 0xe000 {
				b.WriteRune(0x10000 + (r-0xd800)<<10 + (low - 0xdc00))
				continue
			}
			p.i = save
		}
		if r >= 0xd800 && r < 0xe000 {
			// WTF-8 for a lone surrogate: deliberately invalid UTF-8.
			b.Write([]byte{0xed, byte(0x80 | (r>>6)&0x3f), byte(0x80 | r&0x3f)})
			continue
		}
		b.WriteRune(r)
	}
}

func (p *parser) hex4() (rune, bool) {
	if p.i+4 > len(p.s) {
		return 0, false
	}
	n, err := strconv.ParseUint(p.s[p.i:p.i+4], 16, 32)
	if err != nil || strings.ContainsAny(p.s[p.i:p.i+4], "+-_") {
		return 0, false
	}
	p.i += 4
	return rune(n), true
}

// pyFloat spells a finite float like Python's repr.
func pyFloat(f float64) string {
	s := strconv.FormatFloat(f, 'e', -1, 64) // d.ddde±XX
	mant, exp, _ := strings.Cut(s, "e")
	e, _ := strconv.Atoi(exp)
	neg := strings.HasPrefix(mant, "-")
	digits := strings.Replace(strings.TrimPrefix(mant, "-"), ".", "", 1)
	var out string
	switch {
	case e < -4 || e >= 16:
		out = digits[:1]
		if len(digits) > 1 {
			out += "." + digits[1:]
		}
		sign := "+"
		if e < 0 {
			sign, e = "-", -e
		}
		out += fmt.Sprintf("e%s%02d", sign, e)
	case e < 0:
		out = "0." + strings.Repeat("0", -e-1) + digits
	case len(digits) <= e+1:
		out = digits + strings.Repeat("0", e+1-len(digits)) + ".0"
	default:
		out = digits[:e+1] + "." + digits[e+1:]
	}
	if neg {
		out = "-" + out
	}
	return out
}

// validJSON is the part of json.dumps(ensure_ascii=False, allow_nan=False).encode() that can fail.
func validJSON(v any, depth int) bool {
	if depth > maxParseDepth {
		return false
	}
	switch x := v.(type) {
	case nil, bool, json.Number, int, int64:
		return true
	case string:
		return utf8.ValidString(x)
	case float64:
		return !math.IsNaN(x) && !math.IsInf(x, 0)
	case host.Obj:
		for _, kv := range x {
			if !utf8.ValidString(kv.Key) || !validJSON(kv.Value, depth+1) {
				return false
			}
		}
		return true
	case []any:
		for _, item := range x {
			if !validJSON(item, depth+1) {
				return false
			}
		}
		return true
	}
	return false
}

// dumps is json.dumps(v, ensure_ascii=False, separators=(",", ":")) for a validated value.
func dumps(v any) string { return host.MustDumps(v, host.DumpOpts{Compact: true, NoASCII: true}) }

// clone is copy.deepcopy for JSON trees.
func clone(v any) any {
	switch x := v.(type) {
	case host.Obj:
		out := make(host.Obj, len(x))
		for i, kv := range x {
			out[i] = host.KV{Key: kv.Key, Value: clone(kv.Value)}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = clone(item)
		}
		return out
	}
	return v
}

// deepcopy is clone for caller-owned trees, refused with code past maxNesting like Python's
// RecursionError.
func deepcopy(v any, code string) any {
	if nesting(v, 0) > maxNesting {
		fail(code)
	}
	return clone(v)
}

func nesting(v any, depth int) int {
	best := depth
	switch x := v.(type) {
	case host.Obj:
		for _, kv := range x {
			best = max(best, nesting(kv.Value, depth+1))
		}
	case []any:
		for _, item := range x {
			best = max(best, nesting(item, depth+1))
		}
	}
	return best
}

func get(v any, key string) any {
	if o, ok := v.(host.Obj); ok {
		value, _ := o.Get(key)
		return value
	}
	return nil
}

// getd is dict.get(key, fallback).
func getd(o host.Obj, key string, fallback any) any {
	if value, ok := o.Get(key); ok {
		return value
	}
	return fallback
}

func has(o host.Obj, key string) bool {
	_, ok := o.Get(key)
	return ok
}

// isInt is Python's type(v) is int.
func isInt(v any) bool {
	switch x := v.(type) {
	case int, int64:
		return true
	case json.Number:
		return !strings.ContainsAny(string(x), ".eE")
	}
	return false
}

// isNumber is type(v) in (int, float).
func isNumber(v any) bool {
	switch v.(type) {
	case int, int64, json.Number, float64:
		return true
	}
	return false
}

func number(v any) float64 {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	case float64:
		return x
	}
	return math.NaN()
}

// truthy is Python truthiness for JSON values.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case host.Obj:
		return len(x) > 0
	case json.Number:
		return number(x) != 0
	case float64:
		return x != 0
	}
	return true
}

func set(items ...string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		out[item] = true
	}
	return out
}
