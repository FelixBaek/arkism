package backup

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
	"golang.org/x/sys/unix"
)

// Bundle a quiescent database dump and private blob files; no online snapshot.
// create emits ZIP bytes for backup-archive encrypt. extract only takes
// authenticated plaintext into a new private directory, quarantined until memoryd
// adoption.

const (
	maxFile     = 200 << 20
	maxFiles    = 100000
	maxManifest = 8 << 20
)

var (
	digestName = regexp.MustCompile(`^[0-9a-f]{64}$`)
	tempName   = regexp.MustCompile(`^\.(upload|binding)-[0-9a-f]{32}$`)
)

func init() { host.Register("backup-bundle", bundleMain) }

// privateFile opens a regular 0600 single-link file owned by us, never through a
// link. dir is a directory descriptor, or unix.AT_FDCWD.
func privateFile(dir int, name string) (*os.File, error) {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		unix.Close(fd)
		return nil, err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Mode&07777 != 0o600 || int(info.Uid) != os.Geteuid() || info.Nlink != 1 {
		unix.Close(fd)
		return nil, errors.New("not a private regular file")
	}
	return os.NewFile(uintptr(fd), name), nil
}

// copyChecked copies source to target and returns {"digest","bytes"}; limit < 0 means none.
func copyChecked(source io.Reader, target io.Writer, expected map[string]any, limit int64) (map[string]any, error) {
	digest, size := sha256.New(), int64(0)
	buf := make([]byte, 1<<20)
	for {
		n, err := source.Read(buf)
		if n > 0 {
			size += int64(n)
			if limit >= 0 && size > limit {
				return nil, errors.New("entry too large")
			}
			digest.Write(buf[:n])
			if _, werr := target.Write(buf[:n]); werr != nil {
				return nil, werr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	actual := map[string]any{"digest": hex.EncodeToString(digest.Sum(nil)), "bytes": size}
	if expected != nil {
		want, _ := host.Int(expected["bytes"])
		if expected["digest"] != actual["digest"] || want != size {
			return nil, errors.New("entry integrity mismatch")
		}
	}
	return actual, nil
}

func manifestBytes(m map[string]any) []byte {
	return []byte(host.MustDumps(m, host.DumpOpts{Compact: true, SortKeys: true}))
}

func validateManifest(raw any) (map[string]any, error) {
	m, ok := raw.(map[string]any)
	if !ok || len(m) != 5 {
		return nil, errors.New("unsupported manifest")
	}
	for _, k := range []string{"version", "source_identity", "source_root_id", "database", "files"} {
		if _, ok := m[k]; !ok {
			return nil, errors.New("unsupported manifest")
		}
	}
	if v, ok := host.Int(m["version"]); !ok || v != 1 {
		return nil, errors.New("unsupported manifest")
	}
	for _, k := range []string{"source_identity", "source_root_id"} {
		if s, ok := m[k].(string); !ok || !digestName.MatchString(s) {
			return nil, errors.New("invalid binding")
		}
	}
	files, ok := m["files"].([]any)
	if !ok || len(files) > maxFiles {
		return nil, errors.New("too many files")
	}
	seen := map[string]bool{}
	for index, raw := range append([]any{m["database"]}, files...) {
		row, ok := raw.(map[string]any)
		if !ok || len(row) != 2 {
			return nil, errors.New("invalid file metadata")
		}
		digest, isStr := row["digest"].(string)
		size, isInt := host.Int(row["bytes"])
		if !isStr || !digestName.MatchString(digest) || !isInt || size < 1 {
			return nil, errors.New("invalid file metadata")
		}
		if index > 0 {
			if size > maxFile || seen[digest] {
				return nil, errors.New("duplicate or oversized file")
			}
			seen[digest] = true
		}
	}
	if len(manifestBytes(m)) > maxManifest {
		return nil, errors.New("manifest too large")
	}
	return m, nil
}

// storedEntry starts an uncompressed member. Go adds ZIP64 records only when a
// size needs them; Python's reader accepts both forms.
func storedEntry(z *zip.Writer, name string) (io.Writer, error) {
	h := &zip.FileHeader{Name: name, Method: zip.Store}
	h.SetMode(0o600)
	return z.CreateHeader(h)
}

func create(databaseArchive, blobRoot string, output io.Writer, writersStopped bool) error {
	if !writersStopped {
		return errors.New("all writers must be stopped throughout dump and bundle")
	}
	dir, err := os.OpenFile(blobRoot, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer dir.Close()
	root := int(dir.Fd())
	var info unix.Stat_t
	if err := unix.Fstat(root, &info); err != nil {
		return err
	}
	if info.Mode&07777 != 0o700 || int(info.Uid) != os.Geteuid() {
		return errors.New("not a private root")
	}
	lock, err := privateFile(root, ".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	marker, err := privateFile(root, ".database")
	if err != nil {
		return err
	}
	head, err := io.ReadAll(io.LimitReader(marker, 131))
	marker.Close()
	if err != nil {
		return err
	}
	for _, c := range head {
		if c > 0x7f {
			return errors.New("invalid database binding")
		}
	}
	parts := strings.Split(string(head), "\n")
	if len(parts) != 3 || parts[2] != "" || !digestName.MatchString(parts[0]) || !digestName.MatchString(parts[1]) {
		return errors.New("invalid database binding")
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if name == ".lock" || name == ".database" {
			continue
		}
		if !entry.Type().IsRegular() {
			return errors.New("irregular entry")
		}
		if tempName.MatchString(name) {
			continue
		}
		if !digestName.MatchString(name) || len(names) == maxFiles {
			return errors.New("unknown or excessive entries")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	files := []any{}
	m := map[string]any{"version": int64(1), "source_identity": parts[0], "source_root_id": parts[1]}
	bundle := zip.NewWriter(output)
	source, err := privateFile(unix.AT_FDCWD, databaseArchive)
	if err != nil {
		return err
	}
	target, err := storedEntry(bundle, "database.archive")
	if err == nil {
		m["database"], err = copyChecked(source, target, nil, -1)
	}
	source.Close()
	if err != nil {
		return err
	}
	for _, name := range names {
		source, err := privateFile(root, name)
		if err != nil {
			return err
		}
		target, err := storedEntry(bundle, "blobs/"+name)
		var row map[string]any
		if err == nil {
			row, err = copyChecked(source, target, nil, maxFile)
		}
		source.Close()
		if err != nil {
			return err
		}
		if row["digest"] != name || row["bytes"] == int64(0) {
			return errors.New("blob integrity mismatch")
		}
		files = append(files, row)
	}
	m["files"] = files
	if _, err := validateManifest(m); err != nil {
		return err
	}
	target, err = storedEntry(bundle, "manifest.json")
	if err != nil {
		return err
	}
	if _, err := target.Write(manifestBytes(m)); err != nil {
		return err
	}
	return bundle.Close()
}

func writePrivate(path string, source io.Reader, expected map[string]any) error {
	target, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer target.Close()
	size, _ := host.Int(expected["bytes"])
	if _, err := copyChecked(source, target, expected, size); err != nil {
		return err
	}
	if err := target.Sync(); err != nil {
		return err
	}
	return target.Close()
}

func syncDirectory(path string) error {
	dir, err := os.OpenFile(path, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func extract(source, output string) error {
	output, err := absolute(output)
	if err != nil {
		return err
	}
	raw, err := privateFile(unix.AT_FDCWD, source)
	if err != nil {
		return err
	}
	defer raw.Close()
	info, err := raw.Stat()
	if err != nil {
		return err
	}
	bundle, err := zip.NewReader(raw, info.Size())
	if err != nil {
		return err
	}
	byName := map[string]*zip.File{}
	for _, entry := range bundle.File {
		byName[entry.Name] = entry
	}
	if len(bundle.File) > maxFiles+2 || len(byName) != len(bundle.File) {
		return errors.New("duplicate or excessive entries")
	}
	manifest, ok := byName["manifest.json"]
	if !ok || manifest.UncompressedSize64 > maxManifest || manifest.Method != zip.Store {
		return errors.New("unsupported manifest")
	}
	text, err := readEntry(manifest)
	if err != nil {
		return err
	}
	if !json.Valid(text) { // Python's json.loads also refuses trailing data.
		return errors.New("unsupported manifest")
	}
	parsed, err := host.Loads(string(text))
	if err != nil {
		return err
	}
	m, err := validateManifest(parsed)
	if err != nil {
		return err
	}
	type member struct {
		name     string
		metadata map[string]any
	}
	expected := []member{{"database.archive", m["database"].(map[string]any)}}
	for _, row := range m["files"].([]any) {
		r := row.(map[string]any)
		expected = append(expected, member{"blobs/" + r["digest"].(string), r})
	}
	want := map[string]map[string]any{"manifest.json": nil}
	for _, e := range expected {
		want[e.name] = e.metadata
	}
	if len(want) != len(byName) {
		return errors.New("unexpected paths")
	}
	for name := range want {
		if byName[name] == nil {
			return errors.New("unexpected paths")
		}
	}
	for name, entry := range byName {
		if entry.Method != zip.Store || entry.Flags&1 != 0 {
			return errors.New("unsupported entry")
		}
		if name != "manifest.json" {
			size, _ := host.Int(want[name]["bytes"])
			if entry.UncompressedSize64 != uint64(size) {
				return errors.New("unsupported entry")
			}
		}
	}
	if err := os.Mkdir(output, 0o700); err != nil { // Never overwrite or delete an earlier attempt.
		return err
	}
	files := filepath.Join(output, "files")
	if err := os.Mkdir(files, 0o700); err != nil {
		return err
	}
	pending, err := os.OpenFile(filepath.Join(files, ".restore-pending"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return err
	}
	_, err = pending.Write(manifestBytes(m))
	if err == nil {
		err = pending.Chmod(0o600)
	}
	if err == nil {
		err = pending.Sync()
	}
	if cerr := pending.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := syncAll(files, output); err != nil {
		return err
	}
	for _, e := range expected {
		destination := filepath.Join(files, e.metadata["digest"].(string))
		if e.name == "database.archive" {
			destination = filepath.Join(output, "database.archive")
		}
		entry, err := byName[e.name].Open()
		if err != nil {
			return err
		}
		err = writePrivate(destination, entry, e.metadata)
		entry.Close()
		if err != nil {
			return err
		}
	}
	return syncAll(files, output, filepath.Dir(output))
}

func syncAll(paths ...string) error {
	for _, p := range paths {
		if err := syncDirectory(p); err != nil {
			return err
		}
	}
	return nil
}

func readEntry(f *zip.File) ([]byte, error) {
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func bundleMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: arkism backup-bundle {create,extract} ...")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	var err error
	switch args[0] {
	case "create":
		fs := flags("backup-bundle create", stderr)
		stopped := fs.Bool("writers-stopped", false, "all writers are stopped throughout dump and bundle")
		database := fs.String("database-archive", "", "private pg_dump file")
		blobs := fs.String("blob-root", "", "private blob directory")
		if code := parse(fs, args[1:], "writers-stopped", "database-archive", "blob-root"); code >= 0 {
			return code
		}
		err = create(*database, *blobs, stdout, *stopped)
	case "extract":
		fs := flags("backup-bundle extract", stderr)
		input := fs.String("input", "", "authenticated bundle zip")
		output := fs.String("output", "", "new directory")
		if code := parse(fs, args[1:], "input", "output"); code >= 0 {
			return code
		}
		err = extract(*input, *output)
	default:
		return usage()
	}
	if err != nil {
		fmt.Fprintln(stderr, "backup bundle failed; keep partial extraction quarantined")
		return 1
	}
	return 0
}
