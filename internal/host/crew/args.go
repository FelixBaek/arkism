package crew

import (
	"fmt"
	"io"
	"strings"
)

// args is a small argparse: "--name value", "--name=value", boolean "--flag", and positionals in any
// order.
type args struct {
	vals  map[string]string
	multi map[string][]string
	flags map[string]bool
	pos   []string
}

// parseArgs reads argv; valued and bools list the option names (without "--"). An option named in
// repeat collects every value (argparse action="append").
func parseArgs(argv []string, valued, bools, repeat []string) (*args, error) {
	a := &args{vals: map[string]string{}, multi: map[string][]string{}, flags: map[string]bool{}}
	in := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			a.pos = append(a.pos, argv[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "--") {
			if strings.HasPrefix(arg, "-") && len(arg) > 1 && !isNumber(arg) {
				return nil, fmt.Errorf("unrecognized arguments: %s", arg)
			}
			a.pos = append(a.pos, arg)
			continue
		}
		name, value, hasValue := strings.Cut(arg[2:], "=")
		switch {
		case in(bools, name):
			if hasValue {
				return nil, fmt.Errorf("argument --%s: ignored explicit argument '%s'", name, value)
			}
			a.flags[name] = true
		case in(valued, name) || in(repeat, name):
			if !hasValue {
				if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "--") {
					return nil, fmt.Errorf("argument --%s: expected one argument", name)
				}
				i++
				value = argv[i]
			}
			if in(repeat, name) {
				a.multi[name] = append(a.multi[name], value)
			} else {
				a.vals[name] = value
			}
		default:
			return nil, fmt.Errorf("unrecognized arguments: %s", arg)
		}
	}
	return a, nil
}

func isNumber(s string) bool {
	for _, r := range s[1:] {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

func (a *args) has(name string) bool { _, ok := a.vals[name]; return ok }

// usageError is argparse's parser.error: usage line plus message on stderr, exit 2.
func usageError(stderr io.Writer, prog, usage string, err error) int {
	fmt.Fprintf(stderr, "usage: %s %s\n%s: error: %v\n", prog, usage, prog, err)
	return 2
}
