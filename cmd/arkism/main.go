// arkism runs the host-side tools (capture hooks, crew runner, gateway, PC work
// client, skills, backups) as subcommands: arkism COMMAND [ARGS].
package main

import (
	"os"

	"github.com/FelixBaek/arkism/internal/host"
	_ "github.com/FelixBaek/arkism/internal/host/backup"       // registers its subcommands in init
	_ "github.com/FelixBaek/arkism/internal/host/capture"      // registers its subcommands in init
	_ "github.com/FelixBaek/arkism/internal/host/crew"         // registers its subcommands in init
	_ "github.com/FelixBaek/arkism/internal/host/discord"      // registers its subcommands in init
	_ "github.com/FelixBaek/arkism/internal/host/doctor"       // registers its subcommands in init
	_ "github.com/FelixBaek/arkism/internal/host/gateway"      // registers its subcommands in init
	_ "github.com/FelixBaek/arkism/internal/host/skills"       // registers its subcommands in init
	_ "github.com/FelixBaek/arkism/internal/host/subscription" // installs the host.Accounts* hooks in init
)

func main() {
	os.Exit(host.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
