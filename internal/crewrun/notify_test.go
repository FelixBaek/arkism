package crewrun

import "testing"

func TestPickDestPrefersRunnerScope(t *testing.T) {
	m := map[string]int{"default": 1, "ws/p/default": 2, "other/p/default": 3}
	if d, _ := pickDest(m, "ws/p", "default"); d != 2 {
		t.Fatal("scoped", d)
	}
	if d, _ := pickDest(m, "", "default"); d != 1 {
		t.Fatal("host", d)
	}
	if _, ok := pickDest(m, "ws/p", "missing"); ok {
		t.Fatal("missing found")
	}
}
