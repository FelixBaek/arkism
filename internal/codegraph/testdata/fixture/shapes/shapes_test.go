package shapes

import "testing"

func TestTotal(t *testing.T) {
	if Total([]Shape{NewSquare(1)}) != 1 {
		t.Fatal("total")
	}
}
