// arkism runs the host-side tools (capture hooks, crew runner, gateway, PC work
// client, skills, backups) as subcommands: arkism COMMAND [ARGS].
package main

import (
	"os"

	"github.com/FelixBaek/arkism/internal/host"
	_ "github.com/FelixBaek/arkism/internal/host/backup"
	_ "github.com/FelixBaek/arkism/internal/host/capture"
	_ "github.com/FelixBaek/arkism/internal/host/crew"
	_ "github.com/FelixBaek/arkism/internal/host/discord"
	_ "github.com/FelixBaek/arkism/internal/host/doctor"
	_ "github.com/FelixBaek/arkism/internal/host/gateway"
	_ "github.com/FelixBaek/arkism/internal/host/skills"
	_ "github.com/FelixBaek/arkism/internal/host/subscription"
)

func main() {
	os.Exit(host.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
