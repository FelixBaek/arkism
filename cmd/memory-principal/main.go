// memory-principal provisions canonical permissions with an init connection.
// Secrets are accepted only through the environment and are never printed.
package main

import (
	"context"
	"flag"
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
	principal := flag.String("principal", "", "stable principal ID")
	role := flag.String("role", "", "human, agent, service, or pipeline")
	organization := flag.String("organization", "", "organization/workspace ID")
	project := flag.String("project", "", "project ID")
	actions := flag.String("actions", "", "comma-separated explicit permissions; replaces project grant")
	revoke := flag.Bool("revoke", false, "disable this principal across all projects")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := memory.NewPostgres(ctx, os.Getenv("MEMORY_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer p.Close()
	if *revoke {
		return p.SetPrincipalActive(ctx, *principal, false)
	}
	return p.ProvisionPrincipal(ctx, memory.PrincipalGrant{PrincipalID: *principal, Role: *role, Token: os.Getenv("MEMORY_PRINCIPAL_TOKEN"), OrganizationID: *organization, ProjectID: *project, Actions: strings.Split(*actions, ",")})
}
