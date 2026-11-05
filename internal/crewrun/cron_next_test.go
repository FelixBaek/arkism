package crewrun

import (
	"testing"
	"time"
)

func TestCronNext(t *testing.T) {
	at := time.Date(2026, 1, 31, 23, 59, 30, 0, time.UTC)
	for spec, want := range map[string]time.Time{
		"* * * * *":    time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		"30 4 * * *":   time.Date(2026, 2, 1, 4, 30, 0, 0, time.UTC),
		"0 9 * * 1":    time.Date(2026, 2, 2, 9, 0, 0, 0, time.UTC),
		"0 0 29 2 *":   time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC), // beyond a year: not found
		"15 */6 1 3 *": time.Date(2026, 3, 1, 0, 15, 0, 0, time.UTC),
	} {
		c, err := ParseCron(spec)
		if err != nil {
			t.Fatal(spec, err)
		}
		got, ok := c.Next(at)
		if spec == "0 0 29 2 *" {
			if ok {
				t.Fatal("leap day found within a year", got)
			}
			continue
		}
		if !ok || !got.Equal(want) || !c.Match(got) {
			t.Fatal(spec, got, ok)
		}
	}
}
