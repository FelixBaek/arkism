package skills

import (
	"errors"
	"fmt"
	"os"
)

// ToolCallHook lets a program that embeds this package check every MCP tool call
// of the skill router before it runs, without a fork. There is no plugin loader:
// set it in an init function of the embedding program (compiled in). It sees the
// tool name and arguments and must not change them; an error refuses the call,
// and a panic counts as a refusal (fail closed). nil skips it.
var ToolCallHook func(name string, args map[string]any) error

var errToolRefused = errors.New("refused by extension")

// toolCallAllowed runs ToolCallHook; the refusal reason stays on stderr, never in the answer.
func toolCallAllowed(name string, args map[string]any) (err error) {
	if ToolCallHook == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "arkism skill-router: tool hook panicked on %s\n", name)
			err = errToolRefused
		}
	}()
	if e := ToolCallHook(name, args); e != nil {
		fmt.Fprintf(os.Stderr, "arkism skill-router: tool hook refused %s\n", name)
		return errToolRefused
	}
	return nil
}
