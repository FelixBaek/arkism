package skills

import "testing"

func TestUnifiedDiffMatchesPython(t *testing.T) {
	g := loadGolden(t, "difflib.json")
	strs := func(v any) []string {
		var out []string
		for _, s := range asList(v) {
			out = append(out, s.(string))
		}
		return out
	}
	for i, c := range asList(g["cases"]) {
		m := c.(map[string]any)
		n, _ := m["n"].(interface{ Int64() (int64, error) }).Int64()
		if got := unifiedDiff(strs(m["a"]), strs(m["b"]), "old", "new", int(n)); got != m["out"] {
			t.Fatalf("case %d:\n%s\nwant:\n%s", i, got, m["out"])
		}
	}
}
