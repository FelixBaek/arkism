package backup

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func writeMode(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// blobFixture builds a private blob root bound to source/root ids a and b.
func blobFixture(t *testing.T, root string, identity, rootID byte, blobs ...[]byte) (string, string) {
	t.Helper()
	dir := privateDir(t, filepath.Join(root, "blobs"))
	writeMode(t, filepath.Join(dir, ".lock"), nil, 0o600)
	marker := bytes.Repeat([]byte{identity}, 64)
	marker = append(append(append(marker, '\n'), bytes.Repeat([]byte{rootID}, 64)...), '\n')
	writeMode(t, filepath.Join(dir, ".database"), marker, 0o600)
	for _, data := range blobs {
		writeMode(t, filepath.Join(dir, digestOf(data)), data, 0o600)
	}
	dump := filepath.Join(root, "dump")
	writeMode(t, dump, []byte("synthetic database archive"), 0o600)
	return dir, dump
}

func zipMembers(t *testing.T, path string) map[string][]byte {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	members := map[string][]byte{}
	for _, f := range r.File {
		if members[f.Name], err = readEntry(f); err != nil {
			t.Fatal(err)
		}
	}
	return members
}

// writeZip stores members uncompressed, in the given order.
func writeZip(t *testing.T, path string, names []string, members map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, name := range names {
		w, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		w.Write(members[name])
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	os.Remove(path)
	writeMode(t, path, buf.Bytes(), 0o600)
}

func TestBundleQuarantineAndIntegrity(t *testing.T) {
	root := t.TempDir()
	data := []byte("original bytes\x00\xff")
	digest := digestOf(data)
	blobs, dump := blobFixture(t, root, '1', '2', data)
	var buffer bytes.Buffer
	if err := create(dump, blobs, &buffer, true); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "bundle.zip")
	writeMode(t, archive, buffer.Bytes(), 0o600)
	output := filepath.Join(root, "restored")
	if err := extract(archive, output); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(output, "database.archive")); string(b) != "synthetic database archive" {
		t.Fatal("database differs")
	}
	if b, _ := os.ReadFile(filepath.Join(output, "files", digest)); !bytes.Equal(b, data) {
		t.Fatal("blob differs")
	}
	if info, _ := os.Stat(filepath.Join(output, "files", digest)); info.Mode().Perm() != 0o600 {
		t.Fatal(info.Mode())
	}
	var manifest map[string]any
	pending, _ := os.ReadFile(filepath.Join(output, "files", ".restore-pending"))
	if err := json.Unmarshal(pending, &manifest); err != nil {
		t.Fatal(err)
	}
	if want := []any{map[string]any{"digest": digest, "bytes": float64(len(data))}}; !reflect.DeepEqual(manifest["files"], want) {
		t.Fatal(manifest["files"])
	}
	if _, err := os.Stat(filepath.Join(output, "files", ".database")); !os.IsNotExist(err) {
		t.Fatal(".database restored")
	}
	if err := extract(archive, output); !os.IsExist(err) {
		t.Fatal("second extract", err)
	}
	if err := create(dump, blobs, &bytes.Buffer{}, false); err == nil {
		t.Fatal("writers not stopped accepted")
	}

	// The ZIP still has valid CRCs; the manifest's cryptographic binding fails.
	members := zipMembers(t, archive)
	order := []string{"database.archive", "blobs/" + digest, "manifest.json"}
	bad := filepath.Join(root, "bad.zip")
	for _, changed := range order[:2] {
		altered := map[string][]byte{}
		for name, value := range members {
			altered[name] = value
		}
		altered[changed] = bytes.Repeat([]byte("x"), len(members[changed]))
		writeZip(t, bad, order, altered)
		partial := filepath.Join(root, "partial-"+filepath.Base(changed))
		if err := extract(bad, partial); err == nil {
			t.Fatal(changed, "accepted")
		}
		if _, err := os.Stat(filepath.Join(partial, "files", ".restore-pending")); err != nil {
			t.Fatal(changed, "quarantine marker missing", err)
		}
	}

	members["../outside"] = []byte("bad")
	writeZip(t, bad, append(order, "../outside"), members)
	if err := extract(bad, filepath.Join(root, "escape")); err == nil {
		t.Fatal("escape accepted")
	}
	for _, path := range []string{"escape", "outside"} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatal(path, "created")
		}
	}
	delete(members, "../outside")
	writeZip(t, bad, append(order, "manifest.json"), members)
	if err := extract(bad, filepath.Join(root, "duplicate")); err == nil {
		t.Fatal("duplicate entry accepted")
	}

	// Root-internal aliases must be rejected, not followed.
	blob := filepath.Join(blobs, digest)
	os.Rename(blob, filepath.Join(blobs, "alias"))
	os.Symlink("alias", blob)
	if err := create(dump, blobs, &bytes.Buffer{}, true); err == nil {
		t.Fatal("symlink accepted")
	}
	os.Remove(blob)
	os.Rename(filepath.Join(blobs, "alias"), blob)
	os.Link(blob, filepath.Join(root, "hardlink"))
	if err := create(dump, blobs, &bytes.Buffer{}, true); err == nil {
		t.Fatal("hard link accepted")
	}
}

// testdata/python_bundle.zip is backup_bundle.create on the same fixture as below.
func TestBundleMatchesPython(t *testing.T) {
	root := t.TempDir()
	blobs, dump := blobFixture(t, root, 'a', 'b', []byte("original bytes\x00\xff"), []byte("second blob"))
	var buffer bytes.Buffer
	if err := create(dump, blobs, &buffer, true); err != nil {
		t.Fatal(err)
	}
	ours := filepath.Join(root, "go.zip")
	writeMode(t, ours, buffer.Bytes(), 0o600)
	python, err := os.ReadFile("testdata/python_bundle.zip")
	if err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(root, "python.zip")
	writeMode(t, theirs, python, 0o600)
	if a, b := zipMembers(t, ours), zipMembers(t, theirs); !reflect.DeepEqual(a, b) {
		t.Fatalf("members differ:\n go %s\npy %s", a["manifest.json"], b["manifest.json"])
	}
	output := filepath.Join(root, "restored")
	if err := extract(theirs, output); err != nil {
		t.Fatal("extract Python bundle:", err)
	}
	pending, _ := os.ReadFile(filepath.Join(output, "files", ".restore-pending"))
	if !bytes.Equal(pending, zipMembers(t, theirs)["manifest.json"]) {
		t.Fatal("pending marker differs from Python manifest bytes")
	}
}

func TestValidateManifestRejectsLooseTypes(t *testing.T) {
	d := digestOf(nil)
	head := `{"database":{"bytes":1,"digest":"` + d + `"},"files":[],"source_identity":"` + d + `","source_root_id":"` + d + `",`
	for tail, ok := range map[string]bool{
		`"version":1}`: true, `"version":true}`: false, `"version":1.0}`: false, `"version":2}`: false, `"version":1,"extra":0}`: false,
	} {
		parsed, err := host.Loads(head + tail)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := validateManifest(parsed); (err == nil) != ok {
			t.Errorf("%s: %v", tail, err)
		}
	}
}
