//go:build !linux

package skills

import "errors"

// openTerminal: MCP sign-ins are replayed on the Linux server only.
func openTerminal(argv []string) (*terminal, error) {
	return nil, errors.New("로그인은 Linux 서버에서만 할 수 있어요")
}
