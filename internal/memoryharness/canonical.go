package memoryharness

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"unicode/utf8"
)

// CanonicalBytes encodes a validated concrete contract value using
// harness-typed-c14n-v0. It does not normalize or modify the caller's value.
func CanonicalBytes(typeName string, value any) ([]byte, error) {
	if err := ValidateTyped(typeName, value); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := appendCanonical(&out, reflect.ValueOf(value), "$"); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// CanonicalSHA256 returns the lowercase SHA256 of CanonicalBytes.
func CanonicalSHA256(typeName string, value any) (string, error) {
	encoded, err := CanonicalBytes(typeName, value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// appendCanonical is deliberately private: primitive vectors exercise the
// encoding, while the public entry points require a concrete contract DTO.
func appendCanonical(out *bytes.Buffer, value reflect.Value, path string) error {
	if !value.IsValid() {
		out.WriteByte('n')
		return nil
	}
	switch value.Kind() {
	case reflect.Pointer:
		if value.Type().Elem().Kind() != reflect.Int64 && value.Type().Elem().Kind() != reflect.Float64 {
			return contractError("invalid_type", path)
		}
		if value.IsNil() {
			out.WriteByte('n')
			return nil
		}
		return appendCanonical(out, value.Elem(), path)
	case reflect.Bool:
		if value.Bool() {
			out.WriteByte('t')
		} else {
			out.WriteByte('f')
		}
	case reflect.Int64:
		out.WriteByte('i')
		out.WriteString(strconv.FormatInt(value.Int(), 10))
		out.WriteByte(';')
	case reflect.Uint64:
		out.WriteByte('i')
		out.WriteString(strconv.FormatUint(value.Uint(), 10))
		out.WriteByte(';')
	case reflect.Float64:
		n := value.Float()
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return contractError("invalid_value", path)
		}
		if n == 0 {
			n = 0 // Both signs of zero have the positive-zero encoding.
		}
		fmt.Fprintf(out, "d%016x;", math.Float64bits(n))
	case reflect.String:
		s := value.String()
		if !utf8.ValidString(s) {
			return contractError("invalid_unicode", path)
		}
		out.WriteByte('s')
		out.WriteString(strconv.Itoa(len(s)))
		out.WriteByte(':')
		out.WriteString(s)
	case reflect.Slice:
		return appendCanonicalArray(out, value, path)
	case reflect.Struct:
		return appendCanonicalObject(out, value, path)
	default:
		return contractError("invalid_type", path)
	}
	return nil
}

func appendCanonicalArray(out *bytes.Buffer, value reflect.Value, path string) error {
	if value.IsNil() {
		return contractError("invalid_type", path)
	}
	out.WriteByte('a')
	out.WriteString(strconv.Itoa(value.Len()))
	out.WriteByte('[')
	for i := 0; i < value.Len(); i++ {
		if err := appendCanonical(out, value.Index(i), path+"["+strconv.Itoa(i)+"]"); err != nil {
			return err
		}
	}
	out.WriteByte(']')
	return nil
}

func appendCanonicalObject(out *bytes.Buffer, value reflect.Value, path string) error {
	type field struct {
		name  string
		index int
	}
	fields := make([]field, value.NumField())
	seen := make(map[string]bool, len(fields))
	for i := range fields {
		f := value.Type().Field(i)
		name := f.Tag.Get("json")
		if f.PkgPath != "" || name == "" || seen[name] {
			return contractError("invalid_type", path)
		}
		for _, c := range []byte(name) {
			if c > 127 || c == ',' || c == '-' {
				return contractError("invalid_type", path)
			}
		}
		seen[name] = true
		fields[i] = field{name: name, index: i}
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].name < fields[j].name })
	out.WriteByte('o')
	out.WriteString(strconv.Itoa(len(fields)))
	out.WriteByte('{')
	for _, f := range fields {
		if err := appendCanonical(out, reflect.ValueOf(f.name), path); err != nil {
			return err
		}
		if err := appendCanonical(out, value.Field(f.index), path+"."+f.name); err != nil {
			return err
		}
	}
	out.WriteByte('}')
	return nil
}
