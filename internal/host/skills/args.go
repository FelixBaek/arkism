package skills

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// argParser is the small part of argparse these tools use: options anywhere, bools, values,
// repeated values and positionals. Errors print one line and the caller exits 2.
type argParser struct {
	prog   string
	stderr io.Writer
	values map[string]*string
	flags  map[string]*bool
	lists  map[string]*[]string
	ints   map[string]*int
	seen   map[string]bool
	pos    []string
}

func newArgs(prog string, stderr io.Writer) *argParser {
	return &argParser{prog: prog, stderr: stderr, values: map[string]*string{}, flags: map[string]*bool{},
		lists: map[string]*[]string{}, ints: map[string]*int{}, seen: map[string]bool{}}
}

func (p *argParser) str(name, def string) *string  { v := def; p.values[name] = &v; return &v }
func (p *argParser) flag(name string) *bool        { v := false; p.flags[name] = &v; return &v }
func (p *argParser) list(name string) *[]string    { v := []string{}; p.lists[name] = &v; return &v }
func (p *argParser) int(name string, def int) *int { v := def; p.ints[name] = &v; return &v }

func (p *argParser) fail(format string, a ...any) bool {
	fmt.Fprintf(p.stderr, "arkism %s: error: %s\n", p.prog, fmt.Sprintf(format, a...))
	return false
}

// parse reads args; maxPos < 0 means any number of positionals.
func (p *argParser) parse(args []string, minPos, maxPos int) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			p.pos = append(p.pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "--") || a == "-" {
			if argIsOption(a) {
				return p.fail("unrecognized arguments: %s", a)
			}
			p.pos = append(p.pos, a)
			continue
		}
		name, value, inline := strings.Cut(a, "=")
		if f, ok := p.flags[name]; ok {
			if inline {
				return p.fail("argument %s: ignored explicit argument '%s'", name, value)
			}
			*f = true
			continue
		}
		_, isStr := p.values[name]
		_, isList := p.lists[name]
		_, isInt := p.ints[name]
		if !isStr && !isList && !isInt {
			return p.fail("unrecognized arguments: %s", a)
		}
		if !inline {
			if i+1 >= len(args) || argIsOption(args[i+1]) {
				return p.fail("argument %s: expected one argument", name)
			}
			i++
			value = args[i]
		}
		p.seen[name] = true
		if !p.parseSet(name, value, isStr, isList) {
			return false
		}
	}
	if len(p.pos) < minPos {
		return p.fail("the following arguments are required")
	}
	if maxPos >= 0 && len(p.pos) > maxPos {
		return p.fail("unrecognized arguments: %s", strings.Join(p.pos[maxPos:], " "))
	}
	return true
}

// parseSet stores one option value by its kind.
func (p *argParser) parseSet(name, value string, isStr, isList bool) bool {
	switch {
	case isStr:
		*p.values[name] = value
	case isList:
		*p.lists[name] = append(*p.lists[name], value)
	default:
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return p.fail("argument %s: invalid int value: '%s'", name, value)
		}
		*p.ints[name] = n
	}
	return true
}

// argIsOption: a dash word that is not "-" or a negative number.
func argIsOption(a string) bool { return strings.HasPrefix(a, "-") && a != "-" && !isNumber(a) }

func isNumber(s string) bool { _, err := strconv.ParseFloat(s, 64); return err == nil }

// need fails unless every named option was given.
func (p *argParser) need(names ...string) bool {
	var missing []string
	for _, n := range names {
		if !p.seen[n] {
			missing = append(missing, n)
		}
	}
	if missing != nil {
		return p.fail("the following arguments are required: %s", strings.Join(missing, ", "))
	}
	return true
}

func (p *argParser) choice(name, value string, choices []string) bool {
	if containsStr(choices, value) {
		return true
	}
	return p.fail("argument %s: invalid choice: '%s' (choose from %s)", name, value, strings.Join(choices, ", "))
}
