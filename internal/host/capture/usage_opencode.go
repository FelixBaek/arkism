package capture

import (
	"path/filepath"

	"github.com/FelixBaek/arkism/internal/host"
)

// opencodeDataDir is where OpenCode keeps opencode.db (opencode-<channel>.db off the stable
// channel): $XDG_DATA_HOME/opencode or ~/.local/share/opencode on every system. Its session,
// message and part tables are the ones ZCode (an OpenCode fork) has, so zcodeDB reads it.
func opencodeDataDir() string {
	return filepath.Join(host.Env("XDG_DATA_HOME", filepath.Join(host.Home, ".local", "share")), "opencode")
}
