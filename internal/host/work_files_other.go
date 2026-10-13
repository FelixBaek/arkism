//go:build !linux

package host

// CompareFiles: this platform lacks the descriptor-relative opens that keep a saved
// path inside the checkout, so work still resumes but files are not compared.
func CompareFiles(cwd string, files []any) ([]any, error) {
	results := []any{}
	for _, raw := range files {
		item, _ := raw.(map[string]any)
		p := item["path"]
		if p == nil {
			p = ""
		}
		results = append(results, map[string]any{"path": p, "state": "verification_unsupported"})
	}
	return results, nil
}
