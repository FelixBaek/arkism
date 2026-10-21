package installtxn

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"
)

func TestInstallShortcutUsesFixedPathAcrossFolderSwap(t *testing.T) {
	f := newFixture(t, true)
	// The shell link stores long paths; compare against long spellings even when TEMP is an 8.3 name.
	live, err := longPath(f.live)
	if err != nil {
		t.Fatal(err)
	}
	root, err := longPath(f.root)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(live, "Arkism.exe")
	data, err := newShortcut(target)
	if err != nil {
		t.Fatal(err)
	}
	before, err := inspectShortcut(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(before.Path, target) || before.Arguments != "" || before.Flags&fixedShortcutFlags != fixedShortcutFlags {
		t.Fatalf("unexpected fixed shortcut: %+v", before)
	}
	previous := filepath.Join(root, "previous")
	if err = os.Rename(f.live, previous); err != nil {
		t.Fatal(err)
	}
	after, err := inspectShortcut(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(after.Path, target) || strings.EqualFold(after.Path, filepath.Join(previous, "Arkism.exe")) {
		t.Fatalf("shortcut followed previous file: %+v", after)
	}
	if err = os.Rename(f.source, f.live); err != nil {
		t.Fatal(err)
	}
	after, err = inspectShortcut(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(after.Path, target) {
		t.Fatalf("lost target after replacement: %+v", after)
	}
	// Save an owned fixture for an independent Windows Script Host read in native tests.
	if err = os.WriteFile(filepath.Join(f.root, "fixed-path.lnk"), data, 0600); err != nil {
		t.Fatal(err)
	}

	shortcutFile := filepath.Join(f.root, "fixed-path.lnk")
	script := `[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false);$shell=New-Object -ComObject WScript.Shell;[Console]::Write($shell.CreateShortcut($env:ARKISM_LNK_TEST_PATH).TargetPath)`
	command := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoProfile", "-NonInteractive", "-Command", script)
	command.Env = append(os.Environ(), "ARKISM_LNK_TEST_PATH="+shortcutFile)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Windows Script Host shortcut read failed: %v %s", err, output)
	}
	if !strings.EqualFold(strings.TrimPrefix(string(output), "\ufeff"), target) {
		t.Fatalf("Windows Script Host saw another target: %q", output)
	}
	err = withShellLink(func(object *comObject) error {
		persist, err := object.query(&persistStreamIID)
		if err != nil {
			return err
		}
		defer persist.release()
		stream, err := memoryStream(data)
		if err != nil {
			return err
		}
		defer stream.release()
		if err = persist.call(5, uintptr(unsafe.Pointer(stream))); err != nil {
			return err
		}
		if err = object.call(19, 0, 1|8); err != nil {
			return err
		} // SLR_NO_UI | SLR_NOUPDATE; use normal resolution.
		resolved, err := object.getString(3, true)
		if err != nil {
			return err
		}
		if !strings.EqualFold(resolved, target) {
			t.Fatalf("normal Windows resolve followed previous file: %q", resolved)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestInstallShortcutRejectsMalformedBytes(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("not a shell link"), make([]byte, maxShortcutBytes+1)} {
		if _, err := inspectShortcut(data); err == nil {
			t.Fatal("accepted invalid shortcut")
		}
	}
}
