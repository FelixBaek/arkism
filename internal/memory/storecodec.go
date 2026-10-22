package memory

// Storage codec for Postgres payload/doc: relaxed Extended JSON shaped the
// way records were stored while the MongoDB driver encoded them, so every
// existing doc->'field' query and stored row keeps working without the
// driver. Field names come from `bson` struct tags (the storage names; json
// tags are the API names and hide internal fields). Times are
// {"$date": RFC3339 millis} (or {"$numberLong"} outside 1970-9999), the
// driver's relaxed form; non-finite floats use {"$numberDouble"}.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
)

type storeField struct {
	index     int
	name      string
	omitEmpty bool
}

var timeType = reflect.TypeOf(time.Time{})

func storeFields(t reflect.Type) []storeField {
	out := make([]storeField, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("bson")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		out = append(out, storeField{index: i, name: name, omitEmpty: strings.Contains(","+opts+",", ",omitempty,")})
	}
	return out
}

// storeEmpty mirrors the driver's omitempty: IsZero() when defined,
// length for strings/slices/maps, nil for pointers, zero for scalars.
// Structs without IsZero are never empty.
func storeEmpty(v reflect.Value) bool {
	if z, ok := v.Interface().(interface{ IsZero() bool }); ok && (v.Kind() != reflect.Pointer || !v.IsNil()) {
		return z.IsZero()
	}
	switch v.Kind() {
	case reflect.String, reflect.Slice, reflect.Map, reflect.Array:
		return v.Len() == 0
	case reflect.Pointer, reflect.Interface:
		return v.IsNil()
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	}
	return false
}

func storeMarshal(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := storeWrite(&b, reflect.ValueOf(v)); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

const storeMaxDateMillis = 253402300800000 // 10000-01-01T00:00:00Z

// storableTime is the range SQL can read back with (->>'$date')::timestamptz.
// Outside it the codec writes {"$numberLong"}, which those casts reject, so
// one such client time would fail every list or search over its scope.
func storableTime(t time.Time) bool {
	ms := t.UnixMilli()
	return ms >= 0 && ms < storeMaxDateMillis
}

func storeWrite(b *bytes.Buffer, v reflect.Value) error {
	if !v.IsValid() {
		b.WriteString("null")
		return nil
	}
	if v.Type() == timeType {
		ms := v.Interface().(time.Time).UnixMilli()
		if ms >= 0 && ms < storeMaxDateMillis {
			fmt.Fprintf(b, `{"$date":%q}`, time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.999Z07:00"))
		} else {
			fmt.Fprintf(b, `{"$date":{"$numberLong":"%d"}}`, ms)
		}
		return nil
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			b.WriteString("null")
			return nil
		}
		return storeWrite(b, v.Elem())
	case reflect.Struct:
		return storeWriteStruct(b, v)
	case reflect.Map:
		return storeWriteMap(b, v)
	case reflect.Slice, reflect.Array:
		return storeWriteSlice(b, v)
	case reflect.String:
		s, _ := json.Marshal(v.String())
		b.Write(s)
	case reflect.Bool:
		b.WriteString(strconv.FormatBool(v.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		b.WriteString(strconv.FormatInt(v.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		b.WriteString(strconv.FormatUint(v.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		switch {
		case math.IsNaN(f):
			b.WriteString(`{"$numberDouble":"NaN"}`)
		case math.IsInf(f, 1):
			b.WriteString(`{"$numberDouble":"Infinity"}`)
		case math.IsInf(f, -1):
			b.WriteString(`{"$numberDouble":"-Infinity"}`)
		default:
			b.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
		}
	default:
		return fmt.Errorf("store codec: unsupported kind %s: %w", v.Kind(), ErrInvalid)
	}
	return nil
}

// storeUnmarshal decodes a stored document into dest (a pointer). Unknown
// keys are ignored so newer rows stay readable by older fields.
func storeUnmarshal(data []byte, dest any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var raw any
	if err := d.Decode(&raw); err != nil {
		return ErrUnavailable
	}
	v := reflect.ValueOf(dest)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return ErrInvalid
	}
	return storeAssign(v.Elem(), raw)
}

// storeAssign follows the driver's decode rules for the kinds records use:
// JSON null zeroes the target, extended wrappers ($date, $numberLong,
// $numberDouble, $numberInt) unwrap to their scalar.
func storeAssign(v reflect.Value, raw any) error {
	if raw == nil {
		v.Set(reflect.Zero(v.Type()))
		return nil
	}
	if v.Type() == timeType {
		t, err := storeTime(raw)
		if err != nil {
			return err
		}
		v.Set(reflect.ValueOf(t))
		return nil
	}
	switch v.Kind() {
	case reflect.Pointer:
		n := reflect.New(v.Type().Elem())
		if err := storeAssign(n.Elem(), raw); err != nil {
			return err
		}
		v.Set(n)
	case reflect.Struct:
		return storeAssignStruct(v, raw)
	case reflect.Map:
		return storeAssignMap(v, raw)
	case reflect.Slice:
		return storeAssignSlice(v, raw)
	case reflect.String:
		s, ok := raw.(string)
		if !ok {
			return ErrUnavailable
		}
		v.SetString(s)
	case reflect.Bool:
		b, ok := raw.(bool)
		if !ok {
			return ErrUnavailable
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return storeAssignInt(v, raw)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := storeNumber(raw)
		if err != nil {
			return err
		}
		u, err := strconv.ParseUint(n.String(), 10, 64)
		if err != nil {
			return ErrUnavailable
		}
		v.SetUint(u)
	case reflect.Float32, reflect.Float64:
		return storeAssignFloat(v, raw)
	default:
		return ErrUnavailable
	}
	return nil
}

func storeWriteStruct(b *bytes.Buffer, v reflect.Value) error {
	b.WriteByte('{')
	for i, f := range storeFields(v.Type()) {
		fv := v.Field(f.index)
		if f.omitEmpty && storeEmpty(fv) {
			continue
		}
		if b.Bytes()[b.Len()-1] != '{' {
			b.WriteByte(',')
		}
		_ = i
		name, _ := json.Marshal(f.name)
		b.Write(name)
		b.WriteByte(':')
		if err := storeWrite(b, fv); err != nil {
			return err
		}
	}
	b.WriteByte('}')
	return nil
}

func storeWriteMap(b *bytes.Buffer, v reflect.Value) error {
	if v.IsNil() {
		b.WriteString("null")
		return nil
	}
	if v.Type().Key().Kind() != reflect.String {
		return fmt.Errorf("store codec: map key %s: %w", v.Type().Key(), ErrInvalid)
	}
	b.WriteByte('{')
	iter := v.MapRange()
	first := true
	for iter.Next() {
		if !first {
			b.WriteByte(',')
		}
		first = false
		name, _ := json.Marshal(iter.Key().String())
		b.Write(name)
		b.WriteByte(':')
		if err := storeWrite(b, iter.Value()); err != nil {
			return err
		}
	}
	b.WriteByte('}')
	return nil
}

func storeWriteSlice(b *bytes.Buffer, v reflect.Value) error {
	if v.Kind() == reflect.Slice && v.IsNil() {
		b.WriteString("null")
		return nil
	}
	if v.Type().Elem().Kind() == reflect.Uint8 {
		return fmt.Errorf("store codec: binary fields are not stored: %w", ErrInvalid)
	}
	b.WriteByte('[')
	for i := 0; i < v.Len(); i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := storeWrite(b, v.Index(i)); err != nil {
			return err
		}
	}
	b.WriteByte(']')
	return nil
}

func storeAssignStruct(v reflect.Value, raw any) error {
	m, ok := raw.(map[string]any)
	if !ok {
		return ErrUnavailable
	}
	for _, f := range storeFields(v.Type()) {
		value, present := m[f.name]
		if !present {
			continue
		}
		if err := storeAssign(v.Field(f.index), value); err != nil {
			return err
		}
	}
	return nil
}

func storeAssignMap(v reflect.Value, raw any) error {
	m, ok := raw.(map[string]any)
	if !ok {
		return ErrUnavailable
	}
	out := reflect.MakeMapWithSize(v.Type(), len(m))
	for key, value := range m {
		elem := reflect.New(v.Type().Elem()).Elem()
		if err := storeAssign(elem, value); err != nil {
			return err
		}
		out.SetMapIndex(reflect.ValueOf(key).Convert(v.Type().Key()), elem)
	}
	v.Set(out)
	return nil
}

func storeAssignSlice(v reflect.Value, raw any) error {
	list, ok := raw.([]any)
	if !ok {
		return ErrUnavailable
	}
	out := reflect.MakeSlice(v.Type(), len(list), len(list))
	for i, value := range list {
		if err := storeAssign(out.Index(i), value); err != nil {
			return err
		}
	}
	v.Set(out)
	return nil
}

func storeAssignInt(v reflect.Value, raw any) error {
	n, err := storeNumber(raw)
	if err != nil {
		return err
	}
	i, err := n.Int64()
	if err != nil {
		f, ferr := n.Float64()
		if ferr != nil || f != math.Trunc(f) {
			return ErrUnavailable
		}
		i = int64(f)
	}
	v.SetInt(i)
	return nil
}

func storeAssignFloat(v reflect.Value, raw any) error {
	if m, ok := raw.(map[string]any); ok {
		if s, ok := m["$numberDouble"].(string); ok {
			f, err := strconv.ParseFloat(strings.Replace(s, "Infinity", "Inf", 1), 64)
			if err != nil {
				return ErrUnavailable
			}
			v.SetFloat(f)
			return nil
		}
	}
	n, err := storeNumber(raw)
	if err != nil {
		return err
	}
	f, err := n.Float64()
	if err != nil {
		return ErrUnavailable
	}
	v.SetFloat(f)
	return nil
}

func storeNumber(raw any) (json.Number, error) {
	switch x := raw.(type) {
	case json.Number:
		return x, nil
	case map[string]any:
		for _, key := range []string{"$numberLong", "$numberInt", "$numberDouble"} {
			if s, ok := x[key].(string); ok {
				return json.Number(s), nil
			}
		}
	}
	return "", ErrUnavailable
}

func storeTime(raw any) (time.Time, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return time.Time{}, ErrUnavailable
	}
	switch d := m["$date"].(type) {
	case string:
		t, err := time.Parse(time.RFC3339Nano, d)
		if err != nil {
			return time.Time{}, ErrUnavailable
		}
		return t.UTC(), nil
	case map[string]any, json.Number:
		n, err := storeNumber(d)
		if err != nil {
			return time.Time{}, err
		}
		ms, err := n.Int64()
		if err != nil {
			return time.Time{}, ErrUnavailable
		}
		return time.UnixMilli(ms).UTC(), nil
	}
	return time.Time{}, ErrUnavailable
}
