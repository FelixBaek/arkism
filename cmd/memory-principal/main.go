// memory-principal provisions canonical permissions with an init connection.
// Secrets are accepted only through the environment and are never printed.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/memory"
)

func main() {
	if err := run(); err != nil {
		slog.Error("principal update failed; check administrative connection and grant fields")
		os.Exit(1)
	}
}

func run() error {
	return runArgs(os.Args[1:], os.Getenv)
}

func runArgs(args []string, getenv func(string) string) error {
	flags := flag.NewFlagSet("memory-principal", flag.ContinueOnError)
	flags.SetOutput(io.Discard) // Parse errors must not echo a mistyped secret.
	principal := flags.String("principal", "", "stable principal ID")
	role := flags.String("role", "", "human, agent, service, pipeline, or classifier")
	organization := flags.String("organization", "", "organization/workspace ID")
	project := flags.String("project", "", "project ID")
	actions := flags.String("actions", "", "comma-separated explicit permissions; replaces project grant")
	revoke := flags.Bool("revoke", false, "disable this principal across all projects")
	rotate := flags.Bool("rotate-token", false, "replace current token; requires MEMORY_PREVIOUS_PRINCIPAL_TOKEN")
	status := flags.Bool("token-status", false, "inspect current token lifecycle; no raw secret or digest output")
	ttl := flags.Duration("expires-in", memory.MaxPrincipalTokenTTL, "new token lifetime (1m..2160h); does not refresh existing tokens")
	generate := flags.String("generate-token-file", "", "generate a 256-bit token into a new private file; no DB connection")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			flags.SetOutput(os.Stdout)
			flags.PrintDefaults()
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || !exclusivePrincipalMode(*revoke, *rotate, *status) || *ttl < time.Minute || *ttl > memory.MaxPrincipalTokenTTL {
		return memory.ErrInvalid
	}
	if *generate != "" {
		count := 0
		flags.Visit(func(*flag.Flag) { count++ })
		if count != 1 {
			return memory.ErrInvalid
		}
		return writeTokenFile(*generate)
	}
	if !*revoke && !*status && !memory.ValidPrincipalToken(getenv("MEMORY_PRINCIPAL_TOKEN")) {
		return memory.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := memory.NewPostgres(ctx, getenv("MEMORY_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer p.Close()
	if *revoke {
		return p.SetPrincipalActive(ctx, *principal, false)
	}
	if *status {
		out, err := p.PrincipalTokenStatus(ctx, *principal, getenv("MEMORY_PRINCIPAL_TOKEN"))
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	if *rotate {
		if hasGrantOptions(*role, *organization, *project, *actions) {
			return memory.ErrInvalid
		}
		return p.RotatePrincipalToken(ctx, memory.PrincipalTokenRotation{PrincipalID: *principal, PreviousToken: getenv("MEMORY_PREVIOUS_PRINCIPAL_TOKEN"), Token: getenv("MEMORY_PRINCIPAL_TOKEN"), TTL: *ttl})
	}
	return p.ProvisionPrincipal(ctx, memory.PrincipalGrant{PrincipalID: *principal, Role: *role, Token: getenv("MEMORY_PRINCIPAL_TOKEN"), OrganizationID: *organization, ProjectID: *project, Actions: strings.Split(*actions, ","), TokenTTL: *ttl})
}

func exclusivePrincipalMode(enabled ...bool) bool {
	count := 0
	for _, value := range enabled {
		if value {
			count++
		}
	}
	return count <= 1
}

func hasGrantOptions(role, organization, project, actions string) bool {
	return role != "" || organization != "" || project != "" || actions != ""
}

func writeTokenFile(path string) error {
	token, err := memory.NewPrincipalToken()
	if err != nil {
		return err
	}
	f, err := openTokenFile(path)
	if err != nil {
		return memory.ErrUnavailable
	}
	_, err = f.WriteString(token + "\n")
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(path)
		return memory.ErrUnavailable
	}
	return nil
}
