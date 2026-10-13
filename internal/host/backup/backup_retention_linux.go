package backup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"golang.org/x/sys/unix"
)

// TTL is how long a managed encrypted bundle is kept, in seconds.
const TTL = 7 * 86400

var bundleName = regexp.MustCompile(`^arkism-prod-(?:pg-)?([0-9]{8}T[0-9]{6}Z)\.cms$`)

func init() { host.Register("backup-retention", retentionMain) }

// createdAt reads the UTC stamp in a managed bundle name; ok is false for any other name.
func createdAt(name string) (float64, bool) {
	m := bundleName.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	t, err := time.Parse("20060102T150405Z", m[1])
	if err != nil {
		return 0, false
	}
	return float64(t.Unix()), true
}

// expire removes bundles older than TTL from a private directory, or only lists
// them in preview. The caller must hold arkism-backup.lock throughout execute.
func expire(directory string, execute bool, now float64) (host.Obj, error) {
	root, err := absolute(directory)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != root {
		return nil, errors.New("backup_directory_alias")
	}
	dir, err := os.OpenFile(root, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	fd := int(dir.Fd())
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		return nil, err
	}
	if info.Mode&07777 != 0o700 || int(info.Uid) != os.Geteuid() {
		return nil, errors.New("backup_directory_not_private")
	}
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	mode := "preview"
	if execute {
		mode = "execute"
	}
	expired := []any{}
	removed, retained, excluded := 0, 0, 0
	for _, name := range names {
		at, ok := createdAt(name)
		var entry unix.Stat_t
		if err := unix.Fstatat(fd, name, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return nil, &os.PathError{Op: "stat", Path: name, Err: err}
		}
		if !ok || entry.Mode&unix.S_IFMT != unix.S_IFREG || entry.Nlink != 1 || int(entry.Uid) != os.Geteuid() {
			excluded++
			continue
		}
		if at+TTL > now {
			retained++
			continue
		}
		expired = append(expired, name)
		if execute {
			if err := unix.Unlinkat(fd, name, 0); err != nil {
				return nil, &os.PathError{Op: "unlink", Path: name, Err: err}
			}
			removed++
		}
	}
	if execute && removed > 0 {
		if err := unix.Fsync(fd); err != nil {
			return nil, err
		}
	}
	return host.Obj{{Key: "mode", Value: mode}, {Key: "expired", Value: expired}, {Key: "removed", Value: removed},
		{Key: "retained", Value: retained}, {Key: "excluded", Value: excluded}}, nil
}

func retentionMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flags("backup-retention", stderr)
	directory := fs.String("directory", "", "managed backup directory")
	execute := fs.Bool("execute", false, "delete expired bundles instead of previewing")
	if code := parse(fs, args, "directory"); code >= 0 {
		return code
	}
	result, err := expire(*directory, *execute, float64(time.Now().UnixNano())/1e9)
	if err != nil {
		fmt.Fprintln(stderr, "backup retention failed; inspect managed directory before retry")
		return 1
	}
	fmt.Fprintln(stdout, host.MustDumps(result, host.DumpOpts{}))
	return 0
}
