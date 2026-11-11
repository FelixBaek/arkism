package main

import "testing"

func TestReflectPassagesFromEnv(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
		bad  bool
	}{
		{"", 0, false}, {"0", 0, false}, {"1", 1, false},
		{"10", 10, false}, {"50", 50, false},
		{"-1", 0, true}, {"51", 0, true}, {"1.5", 0, true},
		{"many", 0, true}, {" 10 ", 0, true}, {"999999999999999999999", 0, true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("ARKISM_REFLECT_PASSAGES", tc.raw)
			got, err := reflectPassagesFromEnv()
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("setting %q: got %d, error %v; want %d, invalid %v", tc.raw, got, err, tc.want, tc.bad)
			}
		})
	}
}
