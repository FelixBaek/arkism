package skills

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// loadsOrdered is json.loads keeping object key order (objects come back as host.Obj), for
// files this package rewrites where Python kept the original key order.
func loadsOrdered(s string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	v, err := orderedValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("Extra data")
	}
	return v, nil
}

func orderedValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch tok {
	case json.Delim('{'):
		o := host.Obj{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := orderedValue(dec)
			if err != nil {
				return nil, err
			}
			o.Set(k.(string), v) // a repeated key keeps its first place and the last value, like dict
		}
		_, err := dec.Token()
		return o, err
	case json.Delim('['):
		l := []any{}
		for dec.More() {
			v, err := orderedValue(dec)
			if err != nil {
				return nil, err
			}
			l = append(l, v)
		}
		_, err := dec.Token()
		return l, err
	}
	return tok, nil
}

// objGet is dict.get on an ordered object.
func objGet(o host.Obj, k string) any { v, _ := o.Get(k); return v }

// pyListRepr is repr() of a list of plain ASCII str.
func pyListRepr(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = "'" + s + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// field is dict.get on either a plain map or an ordered object.
func field(v any, k string) any {
	switch x := v.(type) {
	case host.Obj:
		r, _ := x.Get(k)
		return r
	case map[string]any:
		return x[k]
	}
	return nil
}
