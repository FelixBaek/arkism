// Package backup ports database backups: snapshot, bundle, encrypted archive and retention from tools/*.py.
// Each tool registers its arkism subcommand with host.Register in an init function.
// They rely on descriptor-relative file calls and flock, so they exist on Linux only.
package backup
