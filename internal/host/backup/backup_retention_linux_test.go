package backup

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func bundleFile(at int64, legacy bool) string {
	prefix := "arkism-prod-pg-"
	if legacy {
		prefix = "arkism-prod-"
	}
	return prefix + time.Unix(at, 0).UTC().Format("20060102T150405Z") + ".cms"
}

func privateDir(t *testing.T, path string) string {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func listNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func field(r interface{ Get(string) (any, bool) }, key string) any {
	v, _ := r.Get(key)
	return v
}

func TestCreatedAtMatchesPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/created_at.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]*float64
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	for name, at := range want {
		got, ok := createdAt(name)
		if ok != (at != nil) || (ok && got != *at) {
			t.Errorf("createdAt(%q) = %v, %v; Python %v", name, got, ok, at)
		}
	}
}

func TestExactBoundaryAndRestoreMtimeDoNotExtendLife(t *testing.T) {
	const now = 1791158400
	root := privateDir(t, filepath.Join(t.TempDir(), "managed"))
	expired := []string{bundleFile(now-TTL, false), bundleFile(now-TTL-1, true)}
	fresh := bundleFile(now-TTL+1, false)
	for _, name := range append(append([]string{}, expired...), fresh) {
		path := filepath.Join(root, name)
		write(t, path, "encrypted fixture")
		os.Chtimes(path, time.Unix(now, 0), time.Unix(now, 0))
	}
	// An old mtime must not expire a recent backup either.
	os.Chtimes(filepath.Join(root, fresh), time.Unix(1, 0), time.Unix(1, 0))
	preview, err := expire(root, false, now)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, name := range field(preview, "expired").([]any) {
		got = append(got, name.(string))
	}
	sort.Strings(got)
	sort.Strings(expired)
	if !reflect.DeepEqual(got, expired) || field(preview, "removed") != 0 || field(preview, "retained") != 1 {
		t.Fatalf("preview %v", preview)
	}
	if len(listNames(t, root)) != 3 {
		t.Fatal("preview removed files")
	}
	executed, err := expire(root, true, now)
	if err != nil || field(executed, "removed") != 2 {
		t.Fatal(executed, err)
	}
	if names := listNames(t, root); !reflect.DeepEqual(names, []string{fresh}) {
		t.Fatal(names)
	}
	if again, _ := expire(root, true, now); field(again, "removed") != 0 {
		t.Fatal(again)
	}
}

func TestUnknownIrregularLinksAndOtherLocationsUntouched(t *testing.T) {
	root := t.TempDir()
	managed := privateDir(t, filepath.Join(root, "managed"))
	for _, name := range []string{"other.cms", "arkism-prod-pg-20260230T000000Z.cms", ".backup-temp"} {
		write(t, filepath.Join(managed, name), "keep")
	}
	outside := filepath.Join(root, "outside")
	write(t, outside, "keep outside")
	if err := os.Symlink(outside, filepath.Join(managed, bundleFile(1, false))); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(managed, bundleFile(2, false))); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(managed, bundleFile(3, false))
	os.Mkdir(child, 0o755)
	write(t, filepath.Join(child, "original"), "keep")
	result, err := expire(managed, true, float64(time.Now().Unix()))
	if err != nil || field(result, "removed") != 0 || field(result, "excluded") != 6 {
		t.Fatal(result, err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep outside" {
		t.Fatal("outside changed")
	}
	if b, _ := os.ReadFile(filepath.Join(child, "original")); string(b) != "keep" {
		t.Fatal("child changed")
	}
}

func TestDirectoryAliasAndNonPrivateRootAreRejected(t *testing.T) {
	root := t.TempDir()
	managed := privateDir(t, filepath.Join(root, "managed"))
	path := filepath.Join(managed, bundleFile(1, false))
	write(t, path, "keep")
	alias := filepath.Join(root, "alias")
	os.Symlink(managed, alias)
	if _, err := expire(alias, true, 2e9); err == nil {
		t.Fatal("alias accepted")
	}
	// Python's Path.absolute keeps "..", so a dotted path is an alias too.
	if _, err := expire(managed+"/../managed", true, 2e9); err == nil {
		t.Fatal("dotted alias accepted")
	}
	os.Chmod(managed, 0o755)
	if _, err := expire(managed, true, 2e9); err == nil {
		t.Fatal("non-private root accepted")
	}
	unix.Chmod(managed, 0o2700)
	if _, err := expire(managed, true, 2e9); err == nil {
		t.Fatal("setgid root accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// serviceCommand is the retention unit's ExecStart with production paths moved
// into the test root and the arkism binary replaced by the test binary.
func serviceCommand(t *testing.T, bin, lock, managed string) []string {
	t.Helper()
	unit, err := os.ReadFile("../../../deploy/systemd/arkism-backup-retention.service")
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("deploy files are not in this checkout")
	}
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(string(unit), "\n") {
		if strings.HasPrefix(l, "ExecStart=") {
			line = strings.TrimPrefix(l, "ExecStart=")
		}
	}
	var command []string
	for _, arg := range strings.Fields(line) {
		switch {
		case arg == "/run/lock/arkism-backup.lock":
			command = append(command, lock)
		case arg == "/srv/arkism/backups":
			command = append(command, managed)
		case arg == "/srv/arkism/bin/arkism":
			command = append(command, bin)
		default:
			command = append(command, arg)
		}
	}
	for _, flag := range []string{"backup-retention", "--directory", "--execute"} {
		if !strings.Contains(strings.Join(command, " "), flag) {
			t.Fatalf("service command lacks %s: %v", flag, command)
		}
	}
	return command
}

func TestCLIDefaultsToPreviewAndServiceSharesBackupLock(t *testing.T) {
	root := t.TempDir()
	managed := privateDir(t, filepath.Join(root, "backups"))
	old := filepath.Join(managed, bundleFile(1, false))
	write(t, old, "keep until execute")
	code, out, _ := run(t, []string{"backup-retention", "--directory", managed})
	var preview map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &preview) != nil || preview["mode"] != "preview" {
		t.Fatal(code, out)
	}
	if !strings.HasPrefix(out, `{"mode": "preview", "expired": ["arkism-prod-pg-`) {
		t.Fatalf("not Python json.dumps layout: %s", out)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal(err)
	}
	bin, env := arkismEnv(t)
	lock := filepath.Join(root, "backup.lock")
	command := serviceCommand(t, bin, lock, managed)
	held, err := os.Create(lock)
	if err != nil {
		t.Fatal(err)
	}
	unix.Flock(int(held.Fd()), unix.LOCK_EX)
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = env
	if err := cmd.Run(); cmd.ProcessState.ExitCode() != 75 {
		t.Fatal("lock not shared", err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal(err)
	}
	held.Close()
	cmd = exec.Command(command[0], command[1:]...)
	cmd.Env = env
	result, err := cmd.Output()
	var executed map[string]any
	if err != nil || json.Unmarshal(result, &executed) != nil || executed["removed"] != 1.0 {
		t.Fatal(string(result), err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old bundle kept")
	}
}

func TestFailedNewBackupStillExpiresOldBundleBeforeServiceAccess(t *testing.T) {
	root := t.TempDir()
	backups := privateDir(t, filepath.Join(root, "backups"))
	old := filepath.Join(backups, bundleFile(1, false))
	write(t, old, "expired")
	fresh := filepath.Join(backups, bundleFile(time.Now().Unix(), false))
	write(t, fresh, "keep")
	script, err := os.ReadFile("../../../tools/backup_postgres.sh")
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("deploy files are not in this checkout")
	}
	if err != nil {
		t.Fatal(err)
	}
	// Production defaults stay in the script; the test points them at a temporary root.
	if strings.Count(string(script), "${ARKISM_ROOT:-/srv/arkism}") != 1 || strings.Count(string(script), "/run/lock/arkism-backup.lock") != 1 {
		t.Fatal("script defaults changed")
	}
	commands := filepath.Join(root, "bin")
	os.Mkdir(commands, 0o700)
	if err := os.WriteFile(filepath.Join(commands, "docker"), []byte("#!/bin/sh\nexit 55\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, env := arkismEnv(t, "PATH="+commands+":"+os.Getenv("PATH"), "MEMORY_PG_CONTAINER=isolated-test-only",
		"ARKISM_ROOT="+root, "ARKISM_BACKUP_LOCK="+filepath.Join(root, "backup.lock"))
	cmd := exec.Command("/bin/sh", "../../../tools/backup_postgres.sh")
	cmd.Env = env
	out, err := cmd.Output()
	var result map[string]any
	if err == nil || json.Unmarshal(out, &result) != nil || result["removed"] != 1.0 {
		t.Fatal(string(out), err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old bundle kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal(err)
	}
}
