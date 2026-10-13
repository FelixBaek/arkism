// Package host holds the host-side tools: capture hooks, the crew runner, the
// model gateway, the PC work client, skills and backups. Each tool registers a
// subcommand of cmd/arkism (arkism session-archive, arkism backup-retention, ...).
package host

import (
	"fmt"
	"io"
	"os"
	"sort"
)

// Command runs one tool with its arguments (os.Args after the tool name) and
// returns the process exit code.
type Command func(args []string, stdin io.Reader, stdout, stderr io.Writer) int

var commands = map[string]Command{}

// Register adds a tool; call it from an init function in the tool's file.
func Register(name string, run Command) {
	if _, dup := commands[name]; dup {
		panic("host: duplicate command " + name)
	}
	commands[name] = run
}

// Main dispatches os.Args[1:] and returns the exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		if run, ok := commands[args[0]]; ok {
			return run(args[1:], stdin, stdout, stderr)
		}
	}
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintln(stderr, "usage: arkism COMMAND [ARGS]\ncommands:")
	for _, name := range names {
		fmt.Fprintln(stderr, "  "+name)
	}
	return 2
}

// Executable is this binary, for tools that start themselves in the background.
func Executable() string {
	if path, err := os.Executable(); err == nil {
		return path
	}
	return os.Args[0]
}
