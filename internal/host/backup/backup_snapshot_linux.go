package backup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"golang.org/x/sys/unix"
)

// Pin a PostgreSQL snapshot and copy blobs before resuming writers; the long
// pg_dump then runs against the pinned snapshot while writers are back up.

func init() { host.Register("backup-snapshot", snapshotMain) }

const (
	serviceUID = 65532
	psqlLogin  = `export PGPASSWORD="$(cat /run/secrets/postgres_password)"; `
)

var (
	prodWriters = []string{"arkism-prod-api-1", "arkism-prod-worker-1"}
	prodReady   = "http://127.0.0.1:8091/readyz"
	snapshotID  = regexp.MustCompile(`^[0-9A-Fa-f]+-[0-9A-Fa-f]+-[0-9]+$`)
)

// dockerIO says where a docker call reads and writes; nil stdout is discarded.
// capture returns stdout as text and also swallows stderr, like capture_output.
type dockerIO struct {
	stdin   io.Reader
	stdout  io.Writer
	capture bool
}

type snapshotSession interface {
	export(ctx context.Context) (string, error)
	close()
}

// Replaced in tests.
var (
	docker      = runDocker
	ready       = waitReady
	newSnapshot = openSnapshot
	chown       = os.Lchown
)

func runDocker(ctx context.Context, timeout time.Duration, dio dockerIO, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = dio.stdin
	var out bytes.Buffer
	if dio.capture {
		cmd.Stdout, cmd.Stderr = &out, io.Discard
	} else {
		cmd.Stdout, cmd.Stderr = dio.stdout, os.Stderr
	}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %w", args[0], err)
	}
	return out.String(), nil
}

var readyClient = &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}

func waitReady(ctx context.Context, url string) error {
	for range 30 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		if resp, err := readyClient.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("writers did not become ready")
}

// psqlSnapshot holds a read-only transaction open; its exported snapshot lives
// only until close.
type psqlSnapshot struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan string
}

func openSnapshot(container string) (snapshotSession, error) {
	cmd := exec.Command("docker", "exec", "-i", container, "sh", "-c",
		psqlLogin+"exec psql -XqAt -v ON_ERROR_STOP=1 -h 127.0.0.1 -U supabase_admin -d arkism")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &psqlSnapshot{cmd: cmd, stdin: stdin, lines: make(chan string, 1)}
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		s.lines <- line
	}()
	return s, nil
}

func (s *psqlSnapshot) export(ctx context.Context) (string, error) {
	if _, err := io.WriteString(s.stdin, "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY; SELECT pg_export_snapshot();\n"); err != nil {
		return "", err
	}
	var line string
	select {
	case line = <-s.lines:
	case <-time.After(20 * time.Second):
		return "", errors.New("snapshot export timed out")
	case <-ctx.Done():
		return "", ctx.Err()
	}
	snapshot := strings.TrimSpace(line)
	if !snapshotID.MatchString(snapshot) {
		return "", errors.New("snapshot export failed")
	}
	return snapshot, nil
}

func (s *psqlSnapshot) close() {
	s.stdin.Close()
	done := make(chan struct{})
	go func() { s.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		s.cmd.Process.Kill()
		<-done
	}
}

// copyTree is shutil.copytree with copy2 and symlinks=True: dst must not exist,
// links stay links, and modes and times are kept.
func copyTree(ctx context.Context, src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o777); err != nil {
		return err
	}
	if err := os.Mkdir(dst, 0o777); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		from, to := filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(from)
			if err == nil {
				err = os.Symlink(target, to)
			}
			if err == nil {
				err = copyStat(from, to, false)
			}
			if err != nil {
				return err
			}
		case entry.IsDir():
			if err := copyTree(ctx, from, to); err != nil {
				return err
			}
		case !entry.Type().IsRegular():
			return fmt.Errorf("%s: special file", from) // never block on a FIFO while writers are stopped
		default:
			if err := copyFile(from, to); err != nil {
				return err
			}
		}
	}
	return copyStat(src, dst, true)
}

func copyFile(from, to string) error {
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return err
	}
	_, err = io.Copy(target, source)
	if cerr := target.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return copyStat(from, to, true)
}

// copyStat copies times and, unless the target is a link (Linux has no lchmod), mode bits.
func copyStat(from, to string, follow bool) error {
	var st unix.Stat_t
	flags := 0
	if follow {
		if err := unix.Stat(from, &st); err != nil {
			return &os.PathError{Op: "stat", Path: from, Err: err}
		}
	} else {
		flags = unix.AT_SYMLINK_NOFOLLOW
		if err := unix.Lstat(from, &st); err != nil {
			return &os.PathError{Op: "lstat", Path: from, Err: err}
		}
	}
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, to, []unix.Timespec{st.Atim, st.Mtim}, flags); err != nil {
		return &os.PathError{Op: "utime", Path: to, Err: err}
	}
	if !follow {
		return nil
	}
	if err := unix.Chmod(to, st.Mode&07777); err != nil {
		return &os.PathError{Op: "chmod", Path: to, Err: err}
	}
	return nil
}

// capture stops writers, pins a snapshot and copies blobs, restarts writers, then
// dumps the pinned snapshot. Writers are restarted on every failure, on a context
// that the caller's cancellation (a signal) does not reach.
func capture(ctx context.Context, container, blobRoot, output, snapshotRoot string, writers []string, readyURL string, stdout io.Writer) (err error) {
	if err := ready(ctx, readyURL); err != nil {
		return err
	}
	for _, name := range append(append([]string{}, writers...), container) {
		running, err := docker(ctx, time.Minute, dockerIO{capture: true}, "inspect", "-f", "{{.State.Running}}", name)
		if err != nil {
			return err
		}
		if strings.TrimSpace(running) != "true" {
			return errors.New("backup requires running containers")
		}
	}
	var snapshot snapshotSession
	stopped := true
	defer func() {
		if stopped {
			cleanup := context.Background()
			if _, rerr := docker(cleanup, time.Minute, dockerIO{}, append([]string{"start"}, writers...)...); rerr != nil {
				err = rerr
			} else if rerr := ready(cleanup, readyURL); rerr != nil {
				err = rerr
			}
		}
		if snapshot != nil {
			snapshot.close()
		}
	}()
	started := time.Now()
	if _, err := docker(ctx, 90*time.Second, dockerIO{}, append([]string{"stop", "--time", "30"}, writers...)...); err != nil {
		return err
	}
	if snapshot, err = newSnapshot(container); err != nil {
		return err
	}
	identity, err := snapshot.export(ctx)
	if err != nil {
		return err
	}
	// Copies have independent inodes: subsequent expiry cannot alter this backup.
	if err := copyTree(ctx, blobRoot, snapshotRoot); err != nil {
		return err
	}
	if err := chown(snapshotRoot, serviceUID, serviceUID); err != nil {
		return err
	}
	children, err := os.ReadDir(snapshotRoot)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := chown(filepath.Join(snapshotRoot, child.Name()), serviceUID, serviceUID); err != nil {
			return err
		}
	}
	if _, err := docker(ctx, time.Minute, dockerIO{}, append([]string{"start"}, writers...)...); err != nil {
		return err
	}
	if err := ready(ctx, readyURL); err != nil {
		return err
	}
	stopped = false
	fmt.Fprintf(stdout, "writers resumed after %.1fs\n", time.Since(started).Seconds())
	target, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return err
	}
	err = os.Chmod(output, 0o600)
	if err == nil {
		_, err = docker(ctx, 1080*time.Second, dockerIO{stdout: target}, "exec", container, "sh", "-c",
			psqlLogin+`exec pg_dump -Fc -h 127.0.0.1 -U supabase_admin -d arkism --snapshot="$1"`, "sh", identity)
	}
	if cerr := target.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	source, err := os.Open(output)
	if err != nil {
		return err
	}
	_, err = docker(ctx, time.Minute, dockerIO{stdin: source}, "exec", "-i", container, "pg_restore", "--list")
	source.Close()
	if err != nil {
		return err
	}
	return chown(output, serviceUID, serviceUID)
}

func snapshotMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flags("backup-snapshot", stderr)
	container := fs.String("container", "", "database container")
	blobRoot := fs.String("blob-root", "", "live blob directory")
	output := fs.String("output", "", "new pg_dump file")
	snapshotRoot := fs.String("snapshot-root", "", "new blob copy directory")
	if code := parse(fs, args, "container", "blob-root", "output", "snapshot-root"); code >= 0 {
		return code
	}
	unix.Umask(0o077)
	// A signal cancels the step in flight; capture still restarts the writers.
	var signum atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(signals)
	go func() {
		if sig, ok := <-signals; ok {
			signum.Store(int32(sig.(syscall.Signal)))
			cancel()
		}
	}()
	err := capture(ctx, *container, *blobRoot, *output, *snapshotRoot, prodWriters, prodReady, stdout)
	if sig := signum.Load(); sig != 0 {
		return 128 + int(sig)
	}
	if err != nil {
		fmt.Fprintln(stderr, "backup snapshot failed:", err)
		return 1
	}
	return 0
}
