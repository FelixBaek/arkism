package backup

import (
	"context"
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
// them in preview. With keep > 0 it also removes the oldest fresh bundles beyond
// that count, so repeated manual backups cannot fill the disk. The caller must
// hold arkism-backup.lock throughout execute.
func expire(directory string, execute bool, now float64, keep int) (host.Obj, error) {
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
	if err := checkPrivateDir(fd); err != nil {
		return nil, err
	}
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	mode := "preview"
	if execute {
		mode = "execute"
	}
	expired, fresh, excluded, err := sortBundles(fd, names, now, keep)
	if err != nil {
		return nil, err
	}
	expiredNames, removed := []any{}, 0
	for _, b := range expired {
		expiredNames = append(expiredNames, b.name)
		if execute {
			if err := unix.Unlinkat(fd, b.name, 0); err != nil {
				return nil, &os.PathError{Op: "unlink", Path: b.name, Err: err}
			}
			removed++
		}
	}
	if execute && removed > 0 {
		if err := unix.Fsync(fd); err != nil {
			return nil, err
		}
	}
	oldest := 0.0
	if len(fresh) > 0 {
		oldest = fresh[0].at
	}
	return host.Obj{{Key: "mode", Value: mode}, {Key: "expired", Value: expiredNames}, {Key: "removed", Value: removed},
		{Key: "retained", Value: len(fresh)}, {Key: "excluded", Value: excluded}, {Key: "oldest_retained", Value: oldestRetained(oldest, excluded)}}, nil
}

type bundle struct {
	name string
	at   float64
}

// sortBundles splits names into expired and fresh bundles (fresh oldest first)
// and counts the files that are not managed bundles. With keep > 0 the oldest
// fresh bundles beyond that count join the expired ones.
func sortBundles(fd int, names []string, now float64, keep int) (expired, fresh []bundle, excluded int, err error) {
	sort.Strings(names)
	for _, name := range names {
		at, ok, err := bundleTime(fd, name)
		if err != nil {
			return nil, nil, 0, err
		}
		if !ok {
			excluded++
		} else if at+TTL > now {
			fresh = append(fresh, bundle{name, at})
		} else {
			expired = append(expired, bundle{name, at})
		}
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].at < fresh[j].at })
	if keep > 0 && len(fresh) > keep {
		expired = append(expired, fresh[:len(fresh)-keep]...)
		fresh = fresh[len(fresh)-keep:]
	}
	return expired, fresh, excluded, nil
}

// oldestRetained formats the oldest retained time. An unknown file may be an
// older copy, so it hides the oldest retained time.
func oldestRetained(oldest float64, excluded int) any {
	if oldest > 0 && excluded == 0 {
		return time.Unix(int64(oldest), 0).UTC().Format(time.RFC3339)
	}
	return nil
}

func checkPrivateDir(fd int) error {
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		return err
	}
	if info.Mode&07777 != 0o700 || int(info.Uid) != os.Geteuid() {
		return errors.New("backup_directory_not_private")
	}
	return nil
}

// bundleTime reads name's creation time; ok is false unless it is a single-link private bundle file.
func bundleTime(fd int, name string) (float64, bool, error) {
	at, ok := createdAt(name)
	var entry unix.Stat_t
	if err := unix.Fstatat(fd, name, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return 0, false, &os.PathError{Op: "stat", Path: name, Err: err}
	}
	if !ok || entry.Mode&unix.S_IFMT != unix.S_IFREG || entry.Nlink != 1 || int(entry.Uid) != os.Geteuid() {
		return 0, false, nil
	}
	return at, true, nil
}

// notifyPrivacy hands the oldest retained backup time to the memory worker,
// which finishes privacy deletions parked before it.
func notifyPrivacy(container string, result host.Obj) error {
	oldest, _ := result.Get("oldest_retained")
	if container == "" || oldest == nil {
		return nil
	}
	_, err := docker(context.Background(), 5*time.Minute, dockerIO{capture: true}, "exec", container, "/memoryd", "--privacy-backup-expiry", oldest.(string))
	return err
}

func retentionMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flags("backup-retention", stderr)
	directory := fs.String("directory", "", "managed backup directory")
	execute := fs.Bool("execute", false, "delete expired bundles instead of previewing")
	worker := fs.String("privacy-worker", "", "memory worker container told the oldest retained backup after execute")
	keep := fs.Int("keep", 0, "also expire the oldest fresh bundles beyond this count (0: time limit only)")
	if code := parse(fs, args, "directory"); code >= 0 {
		return code
	}
	if *keep < 0 {
		fmt.Fprintln(stderr, "backup retention: --keep must be 0 or more")
		return 2
	}
	result, err := expire(*directory, *execute, float64(time.Now().UnixNano())/1e9, *keep)
	if err != nil {
		fmt.Fprintln(stderr, "backup retention failed; inspect managed directory before retry")
		return 1
	}
	fmt.Fprintln(stdout, host.MustDumps(result, host.DumpOpts{}))
	if *execute {
		if err := notifyPrivacy(*worker, result); err != nil {
			fmt.Fprintln(stderr, "privacy backup expiry notice failed; deletions stay waiting until the next run")
			return 1
		}
	}
	return 0
}
