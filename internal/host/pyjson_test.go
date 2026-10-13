package host

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// testdata/python_parity.json holds outputs of the Python tools for the same inputs.
func pythonParity(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/python_parity.json")
	if err != nil {
		t.Fatal(err)
	}
	v, err := Loads(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return v.(map[string]any)
}

func TestDumpsMatchesPython(t *testing.T) {
	for _, raw := range pythonParity(t)["dumps"].([]any) {
		c := raw.(map[string]any)
		opts := c["opts"].(map[string]any)
		o := DumpOpts{Compact: opts["separators"] != nil, NoASCII: opts["ensure_ascii"] == false, SortKeys: opts["sort_keys"] == true}
		value := c["value"]
		got, err := Dumps(asFloats(value), o)
		if err != nil || got != c["output"] {
			t.Errorf("Dumps(%v, %+v)\n got %s\nwant %s", value, o, got, c["output"])
		}
	}
}

// asFloats turns decoded numbers with a fraction or exponent into float64, as Python's json.loads would.
func asFloats(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = asFloats(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = asFloats(e)
		}
		return out
	default:
		if n, ok := v.(json.Number); ok && strings.ContainsAny(string(n), ".eE") {
			{
				f, _ := n.Float64()
				return f
			}
		}
		return v
	}
}

func TestRenderAndAssembleMatchPython(t *testing.T) {
	for i, raw := range pythonParity(t)["cases"].([]any) {
		c := raw.(map[string]any)
		var got string
		var err error
		switch c["kind"] {
		case "render":
			n, _ := Int(c["max_chars"])
			got, err = RenderHookContext(c["input"].(map[string]any), int(n))
		case "assemble":
			got, err = Assemble(c["memory"].(string), c["work"].(string), c["crew"].(string), ContextMaxChars)
		}
		if err != nil {
			got = "error"
		}
		if got != c["output"] {
			t.Errorf("case %d (%s): output differs\n got %.300s\nwant %.300s", i, c["kind"], got, c["output"])
		}
	}
}
