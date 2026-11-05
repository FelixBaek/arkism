package main

import (
	"context"
	"os"

	"github.com/FelixBaek/arkism/internal/memory"
)

// daemonExtensions are compiled-in hooks (memory.Extensions); this build has none.
// To build with your own, add a file to this package, for example
// extensions_local.go behind a build tag, whose init sets the fields, then
// build with `go build -tags yourtag ./cmd/memoryd`.
// Without recompiling, ARKISM_EXTENSIONS_FILE adds extension programs (memory.LoadProcessExtensions).
var daemonExtensions memory.Extensions

// daemonExtensionPrograms are the running extension programs; closeDaemonExtensions stops them at shutdown.
var daemonExtensionPrograms *memory.ProcessExtensions

func closeDaemonExtensions() {
	if daemonExtensionPrograms != nil {
		daemonExtensionPrograms.Close()
	}
}

// configureDaemonExtensions sets the compiled-in hooks, the extension programs and the server MCP tool allowlist.
func configureDaemonExtensions(svc *memory.Service) error {
	svc.Extensions = daemonExtensions
	var err error
	if svc.MCPTools, err = memory.ParseMCPTools(os.Getenv("ARKISM_MCP_TOOLS")); err != nil {
		return err
	}
	path := os.Getenv("ARKISM_EXTENSIONS_FILE")
	if path == "" {
		return nil
	}
	ext, err := memory.LoadProcessExtensions(context.Background(), path)
	if err != nil {
		return err
	}
	daemonExtensionPrograms = ext
	ext.Apply(&svc.Extensions) // the programs live as long as the daemon (closeDaemonExtensions)
	return nil
}
