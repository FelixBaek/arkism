package skills

import (
	"errors"
	"sort"
)

// Only public manifest metadata is returned. Inline bytes are consumed by the
// standard validator and never stored in the comparison result.
type selectionFile struct {
	SHA256   string `json:"sha256"`
	HashOnly bool   `json:"hash_only"`
	Omitted  string `json:"omitted,omitempty"`
	Size     int    `json:"bytes"`
}
type selectionManifest struct {
	Known  bool   `json:"known"`
	SHA256 string `json:"sha256,omitempty"`
	Count  *int   `json:"file_count,omitempty"`
}
type selectionFileChange struct {
	Path                string         `json:"path"`
	State               string         `json:"state"`
	Selected            *selectionFile `json:"selected,omitempty"`
	Latest              *selectionFile `json:"latest_approved,omitempty"`
	AvailabilityChanged bool           `json:"availability_changed,omitempty"`
}
type selectionBundles struct {
	Selected selectionManifest     `json:"selected"`
	Latest   selectionManifest     `json:"latest_approved"`
	State    string                `json:"state"`
	Counts   map[string]int        `json:"counts"`
	Files    []selectionFileChange `json:"files"`
}

func inspectSelectionBundle(raw any) (selectionManifest, map[string]*selectionFile, error) {
	meta := selectionManifest{}
	files := map[string]*selectionFile{}
	if raw == nil {
		return meta, files, nil
	}
	b, err := validateBundle(raw)
	if err != nil {
		return meta, nil, err
	}
	for _, f := range bundleFiles(b) {
		omitted := str(f["omitted"])
		size := len(str(f["content"]))
		if omitted != "" {
			size = int(numFloat(f["size"]))
		}
		files[str(f["path"])] = &selectionFile{str(f["sha256"]), omitted != "", omitted, size}
	}
	total := len(files)
	return selectionManifest{true, str(b["sha256"]), &total}, files, nil
}
func compareSelectionBundles(a, b any, only map[string]bool) (selectionBundles, error) {
	left, old, err := inspectSelectionBundle(a)
	if err != nil {
		return selectionBundles{}, err
	}
	right, current, err := inspectSelectionBundle(b)
	if err != nil {
		return selectionBundles{}, err
	}
	result := selectionBundles{left, right, "unchanged", map[string]int{}, []selectionFileChange{}}
	unknown := !left.Known || !right.Known
	if unknown {
		result.State = "unknown"
	}
	union := map[string]bool{}
	for name := range old {
		union[name] = true
	}
	for name := range current {
		union[name] = true
	}
	// Explicitly requested paths may be absent from both recorded manifests.
	for name := range only {
		union[name] = true
	}
	names := make([]string, 0, len(union))
	for n := range union {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		o, n := old[name], current[name]
		state, availability := compareSelectionFile(o, n, unknown)
		if state != "unchanged" && state != "not_listed" && !unknown {
			result.State = "changed"
		}
		if o != nil || n != nil {
			result.Counts[state]++
		}
		if len(only) == 0 || only[name] {
			result.Files = append(result.Files, selectionFileChange{name, state, o, n, availability})
		}
	}
	return result, nil
}
func selectionFileFilter(args []string) (map[string]bool, error) {
	paths := map[string]bool{}
	if len(args)%2 != 0 || len(args) > 64 {
		return nil, errors.New("use --file PATH (up to 32 paths)")
	}
	for i := 0; i < len(args); i += 2 {
		if args[i] != "--file" || !validPath(args[i+1]) {
			return nil, errors.New("invalid bundle file filter")
		}
		paths[args[i+1]] = true
	}
	return paths, nil
}

func compareSelectionFile(o, n *selectionFile, unknown bool) (string, bool) {
	state := "unchanged"
	availability := o != nil && n != nil && o.HashOnly != n.HashOnly
	switch {
	case unknown:
		state = "unknown"
	case o == nil && n == nil:
		state = "not_listed"
	case o == nil:
		state = "added"
	case n == nil:
		state = "removed"
	case o.SHA256 != n.SHA256:
		state = "content_changed"
	case availability || o.Omitted != n.Omitted || o.Size != n.Size:
		state = "metadata_changed"
	}
	return state, availability
}
