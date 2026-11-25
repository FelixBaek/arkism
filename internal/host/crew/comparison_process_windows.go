package crew

import (
	"os/exec"

	"github.com/FelixBaek/arkism/internal/host"
)

func startComparisonAdapter(cmd *exec.Cmd) (func(), error) {
	return host.StartProcessTree(cmd)
}
