package main

import (
	"bytes"
	"errors"
	"github.com/FelixBaek/arkism/internal/installtxn"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func cliFixture(t *testing.T) (command, *bytes.Buffer, *bytes.Buffer, *int) {
	t.Helper()
	home := t.TempDir()
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	c := newCommand(home, out, errOut)
	calls := new(int)
	c.install = func(string, string, installtxn.Package) (installtxn.Result, error) {
		*calls++
		return installtxn.Result{Committed: true}, nil
	}
	c.repair = func(string) (installtxn.Result, error) { *calls++; return installtxn.Result{Recovered: true}, nil }
	c.stop = func(string, time.Duration) error { *calls++; return nil }
	c.check = func(string, string) error { *calls++; return nil }
	c.checkFolders = c.check
	c.remove = c.check
	c.payloadRemove = c.check
	return c, out, errOut, calls
}
func TestInstallerCLIRejectsUnexpectedCommandsWithoutEffects(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"install"}, {"install", "0.2.0", "x", "extra"}, {"stop", "x", "extra"}, {"repair"}, {"check", "0.2.0"}, {"remove-metadata", "0.2.0"}, {"remove-payload"}, {"pack"}, {"verify", "extra"}, {"check-folders"}, {"check-folders", "x", "y", "extra"}} {
		c, _, _, calls := cliFixture(t)
		if code := c.run(args); code != 2 || *calls != 0 {
			t.Fatalf("%v code=%d effects=%d", args, code, *calls)
		}
	}
}
func TestInstallerCLIMissingBundleNeverStartsInstall(t *testing.T) {
	c, _, _, calls := cliFixture(t)
	if code := c.run([]string{"install", "0.2.0", `C:\owned`}); code != 1 || *calls != 0 {
		t.Fatal("started without bundle")
	}
}
func TestInstallerCLIUninstallCommandsPropagateFailures(t *testing.T) {
	for _, name := range []string{"check", "remove-metadata", "remove-payload"} {
		c, _, _, calls := cliFixture(t)
		c.check = func(string, string) error { return errors.New("injected") }
		c.remove = c.check
		c.payloadRemove = c.check
		if code := c.run([]string{name, "0.2.0", `C:\owned`}); code != 1 || *calls != 0 {
			t.Fatal("ignored uninstall error")
		}
	}
}
func TestInstallerCLIStopAndRepairDispatchOnce(t *testing.T) {
	for _, args := range [][]string{{"check-folders", `C:\owned\Programs`, `C:\owned\Desktop`}, {"stop", `C:\owned`}, {"repair", `C:\owned`}, {"check", "0.2.0", `C:\owned`}, {"remove-metadata", "0.2.0", `C:\owned`}, {"remove-payload", "0.2.0", `C:\owned`}} {
		c, out, _, calls := cliFixture(t)
		if code := c.run(args); code != 0 || *calls != 1 || out.Len() == 0 {
			t.Fatalf("%v code=%d count=%d", args, code, *calls)
		}
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestInstallerCLICommittedWarningsNeverRequestRollback(t *testing.T) {
	c, out, _, _ := cliFixture(t)
	if c.finish(installtxn.Result{Committed: true}, errors.New("cleanup")) != 0 || !bytes.Contains(out.Bytes(), []byte("committed_with_cleanup_warning")) {
		t.Fatal("committed cleanup became install failure")
	}
	c.out = failedWriter{}
	if c.finish(installtxn.Result{Committed: true}, nil) != 0 {
		t.Fatal("output failure invalidated durable commit")
	}
	if c.finish(installtxn.Result{}, errors.New("uncommitted")) != 1 {
		t.Fatal("lost real install failure")
	}
}
func cliBundle(t *testing.T, c command) {
	t.Helper()
	source := filepath.Join(c.home, "payload")
	names := []string{"Arkism.exe", "uninstall.exe", "integration/arkism.exe", "runtime/arkism-installer.exe", "runtime/memoryd.exe", "runtime/memory-principal.exe", "runtime/roles.sql", "runtime/pgsql/bin/postgres.exe", "runtime/pgsql/bin/pg_ctl.exe", "runtime/pgsql/bin/initdb.exe", "runtime/pgsql/bin/psql.exe", "runtime/pgsql/bin/pg_dump.exe", "runtime/pgsql/bin/pg_restore.exe", "runtime/pgsql/lib/vector.dll", "runtime/pgsql/share/extension/vector.control"}
	for _, name := range names {
		path := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("inert CLI payload"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if code := c.run([]string{"pack", "0.2.0", source, filepath.Join(c.home, "payload.json")}); code != 0 {
		t.Fatal("CLI pack failed")
	}
}
func TestInstallerCLIVersionMustMatchEmbeddedManifest(t *testing.T) {
	c, _, errOut, calls := cliFixture(t)
	cliBundle(t, c)
	if code := c.run([]string{"install", "0.3.0", `C:\owned`}); code != 1 || *calls != 0 || !bytes.Contains(errOut.Bytes(), []byte("bundle_version_mismatch")) {
		t.Fatal("accepted mismatching bundle or failed before version comparison")
	}
}
func TestInstallerCLIValidBundleDispatchesAndVerifyRejectsTamper(t *testing.T) {
	c, _, _, calls := cliFixture(t)
	cliBundle(t, c)
	if code := c.run([]string{"verify"}); code != 0 {
		t.Fatal("verification failed")
	}
	if code := c.run([]string{"install", "0.2.0", `C:\owned`}); code != 0 || *calls != 1 {
		t.Fatal("valid install did not dispatch")
	}
	if err := os.WriteFile(filepath.Join(c.home, "payload", "uninstall.exe"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := c.run([]string{"verify"}); code != 1 {
		t.Fatal("ignored tampered uninstaller")
	}
}

func TestInstallerCLIFolderMismatchFailsBeforeInstall(t *testing.T) {
	c, _, _, calls := cliFixture(t)
	c.checkFolders = func(programs, desktop string) error {
		if programs != `C:\owned\Programs` || desktop != `C:\owned\Desktop` {
			t.Fatal("changed expected known folders")
		}
		return errors.New("install_shortcut_folder_not_isolated")
	}
	if c.run([]string{"check-folders", `C:\owned\Programs`, `C:\owned\Desktop`}) != 1 || *calls != 0 {
		t.Fatal("ignored unsafe profile")
	}
}
