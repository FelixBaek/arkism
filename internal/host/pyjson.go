package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// JSON in this package follows Python's json module, because the tools it
// replaces wrote hashes, budgets and files with json.dumps:
//   - decode with UseNumber (Decode), so integers stay integers and 1.0 is not an int;
//   - map[string]any encodes with sorted keys (sort_keys=True); use Obj when
//     insertion order matters (a plain Python dict);
//   - Dumps mirrors json.dumps' separators and ensure_ascii;
//   - text budgets count runes (Python len), never bytes: use Len.

// KV is one key of an ordered object.
type KV struct {
	Key   string
	Value any
}

// Obj is a JSON object that keeps insertion order, like a Python dict.
type Obj []KV

// Get returns the value for key.
func (o Obj) Get(key string) (any, bool) {
	for _, kv := range o {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return nil, false
}

// Set replaces key in place or appends it.
func (o *Obj) Set(key string, value any) {
	for i := range *o {
		if (*o)[i].Key == key {
			(*o)[i].Value = value
			return
		}
	}
	*o = append(*o, KV{key, value})
}

// Del removes key.
func (o *Obj) Del(key string) {
	for i := range *o {
		if (*o)[i].Key == key {
			*o = append((*o)[:i], (*o)[i+1:]...)
			return
		}
	}
}

// MarshalJSON writes compact JSON in insertion order.
func (o Obj) MarshalJSON() ([]byte, error) {
	s, err := Dumps(o, DumpOpts{Compact: true})
	return []byte(s), err
}

// DumpOpts mirrors json.dumps keywords. The zero value is json.dumps(v):
// separators ", " and ": ", ensure_ascii=True.
type DumpOpts struct {
	Compact  bool // separators=(",", ":")
	NoASCII  bool // ensure_ascii=False
	SortKeys bool // sort_keys=True for Obj too (maps are always sorted)
}

// Dumps encodes v like Python's json.dumps.
func Dumps(v any, opt DumpOpts) (string, error) {
	var b strings.Builder
	if err := dump(&b, v, opt); err != nil {
		return "", err
	}
	return b.String(), nil
}

// MustDumps is Dumps for values built in code, where an error is a bug.
func MustDumps(v any, opt DumpOpts) string {
	s, err := Dumps(v, opt)
	if err != nil {
		panic(err)
	}
	return s
}

func dump(b *strings.Builder, v any, opt DumpOpts) error {
	item, key := ", ", ": "
	if opt.Compact {
		item, key = ",", ":"
	}
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case string:
		writeString(b, x, !opt.NoASCII)
	case json.Number:
		b.WriteString(x.String())
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return fmt.Errorf("json: unsupported float %v", x)
		}
		// Python repr: plain digits for 1e-4 <= |x| < 1e16 (3.0, 12345678.5), exponent otherwise (1e-05, 1e+16).
		if a := math.Abs(x); a == 0 || (a >= 1e-4 && a < 1e16) {
			s := strconv.FormatFloat(x, 'f', -1, 64)
			if !strings.ContainsRune(s, '.') {
				s += ".0"
			}
			b.WriteString(s)
		} else {
			b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
		}
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(item)
			}
			if err := dump(b, e, opt); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(item)
			}
			writeString(b, e, !opt.NoASCII)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		o := make(Obj, len(keys))
		for i, k := range keys {
			o[i] = KV{k, x[k]}
		}
		return dump(b, o, opt)
	case Obj:
		if opt.SortKeys {
			x = append(Obj(nil), x...)
			sort.SliceStable(x, func(i, j int) bool { return x[i].Key < x[j].Key })
		}
		b.WriteByte('{')
		for i, kv := range x {
			if i > 0 {
				b.WriteString(item)
			}
			writeString(b, kv.Key, !opt.NoASCII)
			b.WriteString(key)
			if err := dump(b, kv.Value, opt); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		// Structs and other types: round-trip through encoding/json, then re-encode.
		raw, err := json.Marshal(x)
		if err != nil {
			return err
		}
		parsed, err := Decode(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		return dump(b, parsed, opt)
	}
	return nil
}

func writeString(b *strings.Builder, s string, ascii bool) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20 || (ascii && r > 0x7e):
				if r > 0xffff {
					r -= 0x10000
					fmt.Fprintf(b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
				} else {
					fmt.Fprintf(b, `\u%04x`, r)
				}
			default:
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// Decode reads one JSON value with numbers as json.Number and objects as map[string]any.
func Decode(r io.Reader) (any, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF { // json.loads rejects trailing data
		return nil, errors.New("json: extra data after value")
	}
	return v, nil
}

// Loads is Decode for a string.
func Loads(s string) (any, error) { return Decode(strings.NewReader(s)) }

// Int reports v as an integer the way Python's `type(v) is int` does: bools and
// floats (1.0) are not integers.
func Int(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int64:
		return x, true
	case json.Number:
		if strings.ContainsAny(string(x), ".eE") {
			return 0, false
		}
		n, err := x.Int64()
		return n, err == nil
	}
	return 0, false
}

// Len is Python's len for a str: the rune count.
func Len(s string) int { return utf8.RuneCountInString(s) }
