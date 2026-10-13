package backup

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// flags builds an argparse-like parser: usage errors print to stderr and exit 2.
func flags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("arkism "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// parse runs fs and checks the required string flags; it returns -1 to continue,
// otherwise the exit code (0 for -h).
func parse(fs *flag.FlagSet, args []string, required ...string) int {
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	var missing []string
	for _, name := range required {
		if !set[name] {
			missing = append(missing, "--"+name)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(fs.Output(), "%s: error: the following arguments are required: %s\n", fs.Name(), strings.Join(missing, ", "))
		fs.Usage()
		return 2
	}
	return -1
}

// absolute is Python's Path(p).absolute(): joined to the working directory but
// with ".." kept, so a caller can still detect aliases.
func absolute(p string) (string, error) {
	if !filepath.IsAbs(p) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		p = cwd + "/" + p
	}
	var parts []string
	for _, part := range strings.Split(p, "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return "/" + strings.Join(parts, "/"), nil
}
