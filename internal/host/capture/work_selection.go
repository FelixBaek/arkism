package capture

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// Explicit per-checkout work selection shared by local agent programs.

var selectionKey = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// selectionRename replaces the selection file; tests make it fail.
var selectionRename = os.Rename

// selectionNormcase is os.path.normcase.
func selectionNormcase(p string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(strings.ReplaceAll(p, "/", `\`))
	}
	return p
}

// selectionPath returns the normalized checkout and the file holding its selection.
func selectionPath(cwd any) (normalized, path string, err error) {
	dir, ok := cwd.(string)
	if !ok || !filepath.IsAbs(dir) {
		return "", "", valueErr("absolute checkout required")
	}
	directory, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", "", err
	}
	if fi, err := os.Stat(directory); err != nil || !fi.IsDir() {
		return "", "", valueErr("checkout directory required")
	}
	normalized = selectionNormcase(directory)
	root := absPath(host.Env("ARKISM_WORK_SELECTION_STATE", filepath.Join(host.Home, ".config/arkism/work-selections")))
	if !resolved(root) {
		return "", "", valueErr("selection directory alias")
	}
	return normalized, filepath.Join(root, sha256Hex([]byte(normalized))+".json"), nil
}

// selectionSelected returns the checkout's selected work id, or "" on any problem.
func selectionSelected(cwd any) (id string) {
	defer func() {
		if recover() != nil {
			id = ""
		}
	}()
	normalized, path, err := selectionPath(cwd)
	if err != nil || isSymlink(path) {
		return ""
	}
	f, err := openNoFollow(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 4096 {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return ""
	}
	value, err := loadObjBytes(raw)
	if err != nil || len(value) != 3 {
		return ""
	}
	version, isInt := host.Int(value["version"])
	got, _ := value["id"].(string)
	if _, has := value["cwd"]; !has || !isInt || version != 1 || value["cwd"] != normalized || !selectionKey.MatchString(got) {
		return ""
	}
	return got
}

// selectionSelect stores workID as the checkout's selection.
func selectionSelect(cwd, workID any) (host.Obj, error) {
	id, ok := workID.(string)
	if !ok || !selectionKey.MatchString(id) {
		return nil, valueErr("invalid work id")
	}
	normalized, path, err := selectionPath(cwd)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if isSymlink(path) {
		return nil, valueErr("selection file alias")
	}
	body := host.MustDumps(host.Obj{{Key: "version", Value: 1}, {Key: "cwd", Value: normalized}, {Key: "id", Value: id}}, host.DumpOpts{NoASCII: true})
	if len(body) > 4096 {
		return nil, valueErr("selection too large")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".selection-*.tmp")
	if err != nil {
		return nil, err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	_, err = f.WriteString(body)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = selectionRename(temporary, path)
	}
	if err != nil {
		return nil, err
	}
	return host.Obj{{Key: "id", Value: id}, {Key: "selected", Value: true}, {Key: "server_verified", Value: false}}, nil
}

// selectionClear removes the checkout's selection; the work itself stays.
func selectionClear(cwd any) (host.Obj, error) {
	_, path, err := selectionPath(cwd)
	if err != nil {
		return nil, err
	}
	if isSymlink(path) {
		return nil, valueErr("selection file alias")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return host.Obj{{Key: "selected", Value: false}, {Key: "work_deleted", Value: false}}, nil
}

const selectionUsage = "usage: arkism work-selection [-h] --cwd CWD [--id ID] {select,get,clear}\n"

// selectionMain is `arkism work-selection {select,get,clear} --cwd DIR [--id ID]`.
func selectionMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fail := func(msg string) int {
		fmt.Fprint(stderr, selectionUsage+"arkism work-selection: error: "+msg+"\n")
		return 2
	}
	var action string
	values := map[string]*string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			fmt.Fprint(stdout, selectionUsage)
			return 0
		case a == "--cwd" || a == "--id":
			if i+1 >= len(args) {
				return fail("argument " + a + ": expected one argument")
			}
			v := args[i+1]
			values[a] = &v
			i++
		case strings.HasPrefix(a, "--cwd=") || strings.HasPrefix(a, "--id="):
			k, v, _ := strings.Cut(a, "=")
			values[k] = &v
		case strings.HasPrefix(a, "-") && a != "-":
			return fail("unrecognized arguments: " + a)
		case action == "":
			if a != "select" && a != "get" && a != "clear" {
				return fail(fmt.Sprintf("argument action: invalid choice: '%s' (choose from 'select', 'get', 'clear')", a))
			}
			action = a
		default:
			return fail("unrecognized arguments: " + a)
		}
	}
	if action == "" {
		return fail("the following arguments are required: action")
	}
	if values["--cwd"] == nil {
		return fail("the following arguments are required: --cwd")
	}
	return selectionRun(action, values, stdout)
}

// selectionRun runs one parsed work-selection action and prints its JSON result.
func selectionRun(action string, values map[string]*string, stdout io.Writer) int {
	cwd := *values["--cwd"]
	var workID any
	if values["--id"] != nil {
		workID = *values["--id"]
	}
	var result host.Obj
	var err error
	switch action {
	case "select":
		result, err = selectionSelect(cwd, workID)
	case "clear":
		result, err = selectionClear(cwd)
	default:
		var id any
		if got := selectionSelected(cwd); got != "" {
			id = got
		}
		result = host.Obj{{Key: "id", Value: id}, {Key: "server_verified", Value: false}}
	}
	if err != nil {
		fmt.Fprintln(stdout, host.MustDumps(host.Obj{{Key: "error", Value: "invalid_selection"}}, host.DumpOpts{}))
		return 1
	}
	fmt.Fprintln(stdout, host.MustDumps(result, host.DumpOpts{}))
	return 0
}

func init() { host.Register("work-selection", selectionMain) }
