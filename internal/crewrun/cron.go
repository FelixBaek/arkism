package crewrun

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed five-field schedule: minute hour day-of-month month
// day-of-week. Fields take *, numbers, a-b ranges, /step and comma lists.
// Day of week is 0-7 (0 and 7 are Sunday). As in classic cron, when both
// day fields are restricted a time matches if either matches. Names
// (MON, JAN) and @-macros are not supported.
type Cron struct {
	minute, hour, dom, month, dow [64]bool
	domAny, dowAny                bool
}

func ParseCron(spec string) (*Cron, error) {
	f := strings.Fields(spec)
	if len(f) != 5 {
		return nil, fmt.Errorf("cron needs 5 fields, got %d", len(f))
	}
	c := &Cron{}
	parts := []struct {
		set      *[64]bool
		min, max int
	}{{&c.minute, 0, 59}, {&c.hour, 0, 23}, {&c.dom, 1, 31}, {&c.month, 1, 12}, {&c.dow, 0, 7}}
	for i, p := range parts {
		if err := parseCronField(f[i], p.min, p.max, p.set); err != nil {
			return nil, fmt.Errorf("cron field %d: %w", i+1, err)
		}
	}
	if c.dow[7] {
		c.dow[0] = true
	}
	c.domAny, c.dowAny = strings.HasPrefix(f[2], "*"), strings.HasPrefix(f[4], "*")
	return c, nil
}

func parseCronField(field string, lo, hi int, set *[64]bool) error {
	for _, part := range strings.Split(field, ",") {
		rng, step := part, 1
		if r, s, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				return fmt.Errorf("bad step %q", part)
			}
			rng, step = r, n
		}
		from, to := lo, hi
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			x, err1 := strconv.Atoi(a)
			y, err2 := strconv.Atoi(b)
			if err1 != nil || err2 != nil || x > y {
				return fmt.Errorf("bad range %q", part)
			}
			from, to = x, y
		default:
			n, err := strconv.Atoi(rng)
			if err != nil {
				return fmt.Errorf("bad value %q", part)
			}
			from, to = n, n
			if step > 1 { // "5/10" means from 5 to the end
				to = hi
			}
		}
		if from < lo || to > hi {
			return fmt.Errorf("%q out of %d-%d", part, lo, hi)
		}
		for v := from; v <= to; v += step {
			set[v] = true
		}
	}
	return nil
}

// Match reports whether t (in its own location) is a firing minute.
func (c *Cron) Match(t time.Time) bool {
	if !c.minute[t.Minute()] || !c.hour[t.Hour()] || !c.month[int(t.Month())] {
		return false
	}
	// Vixie cron: when either day field starts with "*" both must match
	// (so "*/2" still limits the days); otherwise either may.
	dom, dow := c.dom[t.Day()], c.dow[int(t.Weekday())]
	if c.domAny || c.dowAny {
		return dom && dow
	}
	return dom || dow
}
