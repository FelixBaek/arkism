// arkism-installer is an internal Windows installer participant. It is always
// extracted outside the live payload; it has no fault flags or alternate hive.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/FelixBaek/arkism/internal/installtxn"
)

type command struct {
	home          string
	out, err      io.Writer
	install       func(string, string, installtxn.Package) (installtxn.Result, error)
	repair        func(string) (installtxn.Result, error)
	stop          func(string, time.Duration) error
	check         func(string, string) error
	remove        func(string, string) error
	payloadRemove func(string, string) error
	checkFolders  func(string, string) error
}

func newCommand(home string, out, err io.Writer) command {
	return command{home, out, err, installtxn.InstallPackage, installtxn.RepairInstall, installtxn.StopPayload, installtxn.CheckInstalled, installtxn.RemoveInstalledMetadata, installtxn.RemoveInstalledPayload, installtxn.CheckShortcutFolders}
}
func main() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "installer_executable_path_failed")
		os.Exit(2)
	}
	os.Exit(newCommand(filepath.Dir(exe), os.Stdout, os.Stderr).run(os.Args[1:]))
}
func (c command) run(args []string) int {
	if len(args) == 0 {
		return c.failure(errors.New("installer_command_required"), 2)
	}
	switch args[0] {
	case "check-folders":
		if len(args) != 3 {
			return c.failure(errors.New("installer_folder_check_arguments"), 2)
		}
		if err := c.checkFolders(args[1], args[2]); err != nil {
			return c.failure(err, 1)
		}
		return c.report("shortcut_folders_isolated", installtxn.Result{}, nil)
	case "pack":
		if len(args) != 4 {
			return c.failure(errors.New("installer_pack_arguments"), 2)
		}
		source, destination := filepath.Clean(args[2]), filepath.Clean(args[3])
		if err := installtxn.WritePackage(source, args[1], destination); err != nil {
			return c.failure(err, 1)
		}
		return c.report("packed", installtxn.Result{}, nil)
	case "verify":
		if len(args) != 1 {
			return c.failure(errors.New("installer_verify_arguments"), 2)
		}
		p, source, err := c.bundle()
		if err == nil {
			err = installtxn.VerifyPackage(source, p)
		}
		if err != nil {
			return c.failure(err, 1)
		}
		return c.report("verified", installtxn.Result{}, nil)
	default:
		return c.operation(args)
	}
}
func (c command) operation(args []string) int {
	switch args[0] {
	case "install":
		if len(args) != 3 {
			return c.failure(errors.New("installer_install_arguments"), 2)
		}
		p, source, err := c.bundle()
		if err != nil {
			return c.failure(err, 1)
		}
		if args[1] != p.Version {
			return c.failure(errors.New("installer_bundle_version_mismatch"), 1)
		}
		result, err := c.install(filepath.Clean(args[2]), source, p)
		return c.finish(result, err)
	case "check", "remove-metadata", "remove-payload":
		if len(args) != 3 {
			return c.failure(errors.New("installer_uninstall_arguments"), 2)
		}
		action := c.check
		if args[0] == "remove-metadata" {
			action = c.remove
		}
		if args[0] == "remove-payload" {
			action = c.payloadRemove
		}
		if err := action(filepath.Clean(args[2]), args[1]); err != nil {
			return c.failure(err, 1)
		}
		return c.report("uninstall_metadata_ready", installtxn.Result{}, nil)
	case "repair":
		if len(args) != 2 {
			return c.failure(errors.New("installer_repair_arguments"), 2)
		}
		result, err := c.repair(filepath.Clean(args[1]))
		return c.finish(result, err)
	case "stop":
		if len(args) != 2 {
			return c.failure(errors.New("installer_stop_arguments"), 2)
		}
		if err := c.stop(filepath.Clean(args[1]), 30*time.Second); err != nil {
			return c.failure(err, 1)
		}
		return c.report("stopped", installtxn.Result{}, nil)
	default:
		return c.failure(errors.New("installer_command_unknown"), 2)
	}
}
func (c command) bundle() (installtxn.Package, string, error) {
	source := filepath.Join(c.home, "payload")
	p, err := installtxn.ReadPackage(filepath.Join(c.home, "payload.json"))
	return p, source, err
}
func (c command) finish(result installtxn.Result, err error) int {
	if err != nil && !result.Committed {
		return c.failure(err, 1)
	}
	status := "ready"
	if result.Committed {
		status = "committed"
	}
	if err != nil {
		status = "committed_with_cleanup_warning"
	}
	return c.report(status, result, err)
}
func (c command) report(status string, result installtxn.Result, warning error) int {
	report := struct {
		Status               string
		Committed, Recovered bool
		Archive, Warning     string
	}{Status: status, Committed: result.Committed, Recovered: result.Recovered, Archive: result.Archive}
	if warning != nil {
		report.Warning = warning.Error()
	}
	if err := json.NewEncoder(c.out).Encode(report); err != nil {
		if result.Committed {
			c.failure(err, 1)
			return 0
		}
		return c.failure(err, 1)
	}
	return 0
}
func (c command) failure(err error, code int) int {
	json.NewEncoder(c.err).Encode(struct{ Error string }{err.Error()})
	return code
}
