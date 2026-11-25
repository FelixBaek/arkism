package skills

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestSelectionFieldChanges(t *testing.T) {
	cases := []struct {
		field    string
		old, new any
		want     string
	}{
		{"permissions", []any{"read", "edit"}, []any{"edit", "read"}, "unchanged"},
		{"permissions", []any{"read"}, []any{"read", "write"}, "changed"},
		{"writes", false, true, "changed"}, {"reads", false, false, "unchanged"},
		{"tools", nil, []any{}, "unspecified"}, {"input_format", "", "structured record", "one_side_unspecified"},
		{"output_format", "csv", "pdf", "changed"}, {"secret_names", []any{"legacy"}, []any{"new"}, "changed"},
	}
	for _, tc := range cases {
		r := selectionField("requirements", tc.field, map[string]any{tc.field: tc.old}, map[string]any{tc.field: tc.new})
		if r.State != tc.want {
			t.Fatal(r)
		}
		if tc.field == "secret_names" && (r.Selected != 1 || r.Latest != 1) {
			t.Fatal("exposed secret names")
		}
	}
}
func TestSelectionReadOnlyAndFailClosed(t *testing.T) {
	saved := reviewCall
	defer func() { reviewCall = saved }()
	for _, tc := range []string{"ok", "same body changed bundle", "denied", "mismatched identity", "wrong hash"} {
		t.Run(tc, func(t *testing.T) { checkSelectionAccess(t, tc) })
	}
}

func TestSelectionVersionIdentity(t *testing.T) {
	saved := reviewCall
	defer func() { reviewCall = saved }()
	for _, tc := range []identityCase{
		{"correct historical selection", 2, 3, false, false, false, false, true},
		{"newer selected than active", 2, 1, false, false, false, false, true},
		{"same immutable version", 2, 2, true, false, false, false, true},
		{"unexpected selected number", 1, 3, false, false, false, false, false},
		{"missing latest number", 2, nil, false, false, false, false, false},
		{"fraction latest", 2, 1.5, false, false, false, false, false},
		{"zero latest", 2, 0, false, false, false, false, false},
		{"string latest", 2, "3", false, false, false, false, false},
		{"same id conflicting numbers", 2, 3, true, false, false, false, false},
		{"same number conflicting ids", 2, 2, false, false, false, false, false},
		{"same id conflicting valid body hashes", 2, 2, true, true, false, false, false},
		{"same id conflicting bundles", 2, 2, true, false, true, false, false},
		{"detail analysis fingerprint contradicts body", 2, 3, false, false, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) { checkSelectionIdentity(t, tc) })
	}
}

func checkSelectionAccess(t *testing.T, tc string) {

	id := "test/selection"
	n := 0
	reviewCall = func(method, path string, payload any, token string) (map[string]any, error) {
		n++
		return selectionTestRead(t, tc, n, method, path)
	}

	var out, err bytes.Buffer
	code := compareSelectedMain([]string{id + "@2"}, &out, &err)
	if tc == "same body changed bundle" {
		if code != 0 || !strings.Contains(out.String(), "content_changed") || strings.Contains(out.String(), "private-first") || !strings.Contains(out.String(), `"same_body": true`) {
			t.Fatal("same body bundle differences not published correctly")
		}
		return
	}
	if tc == "ok" {
		if code != 0 || n != 3 || !strings.Contains(out.String(), `"same_version": false`) || !strings.Contains(out.String(), `"same_body": true`) {
			t.Fatalf("code=%d output=%s err=%s", code, out.String(), err.String())
		}
	} else if code == 0 || out.Len() != 0 {
		t.Fatal("incomplete or denied data was published")
	}
}

func selectionTestRead(t *testing.T, tc string, n int, method, path string) (map[string]any, error) {
	id := "test/selection"
	old := "skill-" + strings.Repeat("a", 64)
	newer := "skill-" + strings.Repeat("b", 64)
	body := "same"
	hash := digest([]byte(body))
	text := "metadata"
	hashText := digest([]byte(text))
	if n == 1 {
		return selectionTestBatch(t, tc, method, path, id, old, newer, body, hash)
	}
	if method != "GET" || !strings.HasPrefix(path, "/v1/skill_versions/") {
		t.Fatalf("unexpected write %s", path)
	}
	if tc == "denied" {
		return nil, errors.New("denied")
	}
	v := old
	if n == 3 {
		v = newer
	}
	s := id
	if tc == "mismatched identity" {
		s = "other"
	}
	rh := hashText
	if tc == "wrong hash" {
		rh = "invalid"
	}
	return map[string]any{"id": v, "skill_id": s, "content_hash": rh, "text": text, "analysis": map[string]any{"content_hash": hash, "input_format": "text"}}, nil
}
func selectionTestBatch(t *testing.T, tc, method, path, id, old, newer, body, hash string) (map[string]any, error) {

	if method != "POST" || path != "/v1/skills/resolve" {
		t.Fatalf("unexpected write %s", path)
	}
	var items []any
	for i, v := range []string{old, newer} {
		ref := id + "@2"
		if i == 1 {
			ref = id + "@latest-approved"
		}
		items = append(items, map[string]any{"ref": ref, "skill_id": id, "version_id": v, "instructions": body, "content_hash": hash, "resolved_version": i + 2})
	}
	if tc == "same body changed bundle" {
		for j, raw := range items {
			fileBody := []string{"private-first", "private-next"}[j]
			files := []any{map[string]any{"path": "references/work.md", "content": fileBody, "sha256": digest([]byte(fileBody))}}
			raw.(map[string]any)["bundle"] = map[string]any{"sha256": bundleHash(files), "files": files}
		}
	}
	return map[string]any{"items": items}, nil

}

type identityCase struct {
	name                                                      string
	first, second                                             any
	sameID, bodyChanged, bundleChanged, staleMetadata, wantOK bool
}

func checkSelectionIdentity(t *testing.T, tc identityCase) {

	skill := "test/selection"
	details := 0
	reviewCall = func(method, path string, payload any, token string) (map[string]any, error) {
		if method == "GET" {
			details++
		}
		return identityTestRead(tc, method, path)
	}

	var out, errs bytes.Buffer
	result := compareSelectedMain([]string{skill + "@2"}, &out, &errs)
	if (result == 0) != tc.wantOK || (out.Len() > 0) != tc.wantOK {
		t.Fatalf("accepted=%v, want %v; %s", result == 0, tc.wantOK, errs.String())
	}
	if tc.sameID && tc.wantOK && details != 1 {
		t.Fatalf("same immutable record fetched %d times", details)
	}
}

func identityTestRead(tc identityCase, method, path string) (map[string]any, error) {
	skill := "test/selection"
	leftID := "skill-" + strings.Repeat("a", 64)
	rightID := "skill-" + strings.Repeat("b", 64)
	sha := digest([]byte("body"))
	vid := rightID
	if tc.sameID {
		vid = leftID
	}
	if method == "POST" {
		first := map[string]any{"ref": skill + "@2", "skill_id": skill, "resolved_version": tc.first, "version_id": leftID, "instructions": "body", "content_hash": sha}
		second := map[string]any{"ref": skill + "@latest-approved", "skill_id": skill, "resolved_version": tc.second, "version_id": vid, "instructions": "body", "content_hash": sha}
		if tc.bodyChanged {
			second["instructions"] = "altered"
			second["content_hash"] = digest([]byte("altered"))
		}
		if tc.bundleChanged {
			files := []any{map[string]any{"path": "references/info.md", "content": "x", "sha256": digest([]byte("x"))}}
			second["bundle"] = map[string]any{"sha256": bundleHash(files), "files": files}
		}
		return map[string]any{"items": []any{first, second}}, nil
	}

	if strings.Contains(path, leftID) {
		vid = leftID
	}
	meta := map[string]any{"id": vid, "skill_id": skill, "text": "legacy metadata", "content_hash": digest([]byte("legacy metadata"))}
	if tc.staleMetadata {
		meta["analysis"] = map[string]any{"content_hash": digest([]byte("wrong"))}
	}
	return meta, nil

}
