package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

type comparisonField struct {
	Field    string `json:"field"`
	State    string `json:"state"`
	Selected any    `json:"selected,omitempty"`
	Latest   any    `json:"latest_approved,omitempty"`
}

func selectionField(group, key string, left, right map[string]any) comparisonField {

	a, b := normalizedSelectionValue(group, left[key]), normalizedSelectionValue(group, right[key])
	state := "unchanged"
	switch {
	case a == nil && b == nil:
		state = "unspecified"
	case a == nil || b == nil:
		state = "one_side_unspecified"
	case !sameJSON(a, b):
		state = "changed"
	}
	if state == "unchanged" || state == "unspecified" {
		a, b = nil, nil
	}
	if key == "secret_names" { // Show that names changed without printing credential identifiers.
		count := func(v any) any {
			if l, ok := v.([]string); ok {
				return len(l)
			}
			return nil
		}
		a, b = count(a), count(b)
	}
	return comparisonField{group + "." + key, state, a, b}
}

// Fetch both refs together, then read each returned immutable record under the
// same scope. A moving latest pointer never changes the pair being compared.
func compareSelectedMain(args []string, out, errs io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(errs, "usage: skill-review compare-selected SKILL_ID@N [--file PATH]...")
		return 2
	}
	only, err := selectionFileFilter(args[1:])
	if err != nil {
		fmt.Fprintln(errs, err)
		return 2
	}
	ref := args[0]
	id, requested, ok := splitSelectionRef(ref)
	if !ok {
		fmt.Fprintln(errs, "a numbered skill reference is required")
		return 2
	}
	refs := []string{ref, id + "@latest-approved"}
	batch, err := reviewCall("POST", "/v1/skills/resolve", host.Obj{{Key: "scope", Value: host.Scope()}, {Key: "refs", Value: refs}}, "")
	if err != nil {
		fmt.Fprintln(errs, err)
		return 1
	}
	items := asList(batch["items"])
	if len(items) != 2 {
		fmt.Fprintln(errs, "incomplete skill resolution")
		return 1
	}
	type snapshot struct{ item, analysis map[string]any }
	var pair [2]snapshot
	versions := map[string]map[string]any{}
	records := map[string]map[string]any{}
	var identities []map[string]any
	for i, raw := range items {
		row := asMap(raw)
		if err := validateSelectionRow(row, pair[0].item, id, requested, refs[i]); err != nil {
			fmt.Fprintln(errs, err)
			return 1
		}
		vid, hash := str(row["version_id"]), str(row["content_hash"])
		analysis, loaded := versions[vid]
		if !loaded {
			var v map[string]any
			v, analysis, err = readSelectionVersion(id, vid, hash)
			if err != nil {
				fmt.Fprintln(errs, err)
				return 1
			}
			versions[vid] = analysis
			records[vid] = v
		}
		pair[i] = snapshot{row, analysis}
		identities = append(identities, map[string]any{"ref": row["ref"], "version": row["resolved_version"], "version_id": vid, "sha256": hash, "validation_status": row["validation_status"]})
	}
	var fields []comparisonField
	for _, key := range []string{"problem", "use_when", "avoid_when", "input_format", "output_format", "success_criteria", "failure_types"} {
		fields = append(fields, selectionField("analysis", key, pair[0].analysis, pair[1].analysis))
	}
	for _, key := range []string{"tools", "apis", "permissions", "secret_names", "irreversible_actions", "environments", "dependencies", "reads", "writes", "external_transfer"} {
		fields = append(fields, selectionField("requirements", key, asMap(pair[0].item["requirements"]), asMap(pair[1].item["requirements"])))
	}
	fields = append(fields, selectionField("resolution", "compatibility", pair[0].item, pair[1].item))
	bundles, err := compareSelectionBundles(pair[0].item["bundle"], pair[1].item["bundle"], only)
	if err != nil {
		fmt.Fprintln(errs, "invalid bundle manifest:", err)
		return 1
	}
	result := map[string]any{"bundle_comparison": bundles, "skill_id": id, "selected": identities[0], "latest_approved": identities[1], "same_version": identities[0]["version_id"] == identities[1]["version_id"], "same_body": identities[0]["sha256"] == identities[1]["sha256"], "fields": fields, "assessment": "manual_review", "basis": "분석 요약과 선언된 요구사항을 비교합니다. 동작 호환성 판정은 검토자가 수행합니다."}
	result["measured_comparison"] = selectedMeasurements(records[str(pair[0].item["version_id"])], records[str(pair[1].item["version_id"])])
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fmt.Fprintln(errs, err)
		return 1
	}
	return 0
}

func splitSelectionRef(ref string) (string, string, bool) {
	key, version, ok := strings.Cut(ref, "@")
	n, err := strconv.Atoi(version)
	return key, version, ok && key != "" && err == nil && n > 0 && strconv.Itoa(n) == version
}

func normalizedSelectionValue(group string, v any) any {
	if a, ok := v.([]any); ok {
		if len(a) == 0 {
			return nil
		}
		if group == "requirements" {
			names := make([]string, 0, len(a))
			for _, x := range a {
				t, ok := x.(string)
				if !ok {
					return v
				}
				names = append(names, t)
			}
			slices.Sort(names)
			return slices.Compact(names)
		}
	}
	if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
		return nil
	}
	return v
}

func validateSelectionRow(row, first map[string]any, id, requested, ref string) error {
	if errCode := str(row["error"]); errCode != "" {
		return errors.New(errCode)
	}
	vid, hash := str(row["version_id"]), str(row["content_hash"])
	content, present := row["instructions"].(string)
	if row["ref"] != ref || row["skill_id"] != id || !versionIDRx.MatchString(vid) || !present || digest([]byte(content)) != hash {
		return errors.New("skill reference or body fingerprint mismatch")
	}
	number, validNumber := host.Int(row["resolved_version"])
	if !validNumber || number < 1 || (first == nil && strconv.FormatInt(number, 10) != requested) {
		return errors.New("resolved skill version number mismatch")
	}
	if first != nil {

		firstNumber, _ := host.Int(first["resolved_version"])
		sameID := first["version_id"] == vid
		if sameID != (firstNumber == number) || (sameID && (first["content_hash"] != hash || !sameJSON(first["bundle"], row["bundle"]))) {
			return errors.New("inconsistent immutable skill version")
		}
	}
	return nil
}

func readSelectionVersion(id, vid, hash string) (map[string]any, map[string]any, error) {
	v, err := reviewCall("GET", "/v1/skill_versions/"+vid+reviewQuery(), nil, "")
	if err != nil {
		return nil, nil, err
	}
	if v["id"] != vid || v["skill_id"] != id || v["content_hash"] != digest([]byte(str(v["text"]))) {
		return nil, nil, errors.New("version metadata mismatch")
	}
	analysis := asMap(v["analysis"])
	if analysis == nil {
		var a map[string]any
		if json.Unmarshal([]byte(str(v["text"])), &a) == nil {
			analysis = a
		}
	}
	if recorded := analysis["content_hash"]; recorded != nil && recorded != "" && recorded != hash {
		return nil, nil, errors.New("analysis fingerprint differs from resolved body")
	}
	if analysis["content_hash"] != hash {
		analysis = nil
	}
	return v, analysis, nil
}
