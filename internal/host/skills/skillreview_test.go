package skills

import (
	"maps"
	"strings"
	"testing"
)

func swapReview(t *testing.T) {
	t.Helper()
	res, cands, appr, run, c := reviewResolve, reviewCandidates, reviewApprove, claudeRun, coin
	t.Cleanup(func() { reviewResolve, reviewCandidates, reviewApprove, claudeRun, coin = res, cands, appr, run, c })
}

func TestJudgeLabelsMapBackToRedGreenInBothOrders(t *testing.T) {
	swapReview(t)
	claudeRun = func(prompt, system, schema string) (any, error) {
		if schema == "" {
			if system != "" {
				return "GREEN", nil
			}
			return "RED", nil
		}
		a := strings.Split(strings.Split(prompt, "## Answer A\n")[1], "\n")[0]
		return map[string]any{"A": a == "GREEN", "B": a != "GREEN", "reason": "r"}, nil // the judge passes only the skill answer
	}
	for _, flip := range []float64{0.1, 0.9} {
		coin = func() float64 { return flip }
		rows, err := behaviorEval("skill", []any{map[string]any{"prompt": "p", "expected_output": "e"}})
		if err != nil || objGet(rows[0], "red") != false || objGet(rows[0], "green") != true {
			t.Fatal(flip, rows, err)
		}
	}
}

func TestCompanionChangesAreShown(t *testing.T) {
	swapReview(t)
	item := func(version int, run, logo string) map[string]any {
		files := []any{map[string]any{"path": "logo.png", "sha256": logo, "size": 3, "omitted": "binary"}, map[string]any{"path": "run.py", "sha256": run, "content": run}}
		return map[string]any{"skill_id": "demo", "resolved_version": version, "instructions": "same\n", "bundle": map[string]any{"sha256": "s", "files": files}}
	}
	items := map[string]map[string]any{"demo@2": item(2, "print(2)\n", strings.Repeat("b", 64)), "demo@latest-approved": item(1, "print(1)\n", strings.Repeat("a", 64))}
	reviewResolve = func(ref string) (map[string]any, error) { return items[ref], nil }
	out, err := diffRef("demo@2")
	if err != nil || !strings.Contains(out, "-print(1)") || !strings.Contains(out, "+print(2)") || !strings.Contains(out, "logo.png: 해시만 고정된 파일 변경") {
		t.Fatal(out, err)
	}
	same := maps.Clone(items["demo@latest-approved"])
	same["resolved_version"] = 2
	items["demo@2"] = same
	if out, err := diffRef("demo@2"); out != "" || err != nil {
		t.Fatal(out, err)
	}
}

func TestOnlyBodyIdenticalPinningCandidatesAreApproved(t *testing.T) {
	swapReview(t)
	bundle := map[string]any{"sha256": "s", "files": []any{map[string]any{"path": "a.py", "sha256": "x", "content": "x"}, map[string]any{"path": "b.png", "sha256": "y", "size": 1, "omitted": "binary"}}}
	items := map[string]map[string]any{
		"pin@2":                 {"skill_id": "pin", "resolved_version": 2, "content_hash": "h", "bundle": bundle},
		"pin@latest-approved":   {"skill_id": "pin", "resolved_version": 1, "content_hash": "h"},
		"body@2":                {"skill_id": "body", "resolved_version": 2, "content_hash": "new", "bundle": bundle},
		"body@latest-approved":  {"skill_id": "body", "resolved_version": 1, "content_hash": "old"},
		"again@2":               {"skill_id": "again", "resolved_version": 2, "content_hash": "h", "bundle": bundle},
		"again@latest-approved": {"skill_id": "again", "resolved_version": 1, "content_hash": "h", "bundle": bundle},
		"gone@2":                {"skill_id": "gone", "resolved_version": 2, "content_hash": "h", "bundle": bundle},
	}
	reviewResolve = func(ref string) (map[string]any, error) {
		if ref == "gone@latest-approved" {
			return nil, hubErr("skill_unavailable", "")
		}
		return items[ref], nil
	}
	reviewCandidates = func() ([]any, error) {
		return []any{map[string]any{"ref": "pin@2"}, map[string]any{"ref": "body@2"}, map[string]any{"ref": "again@2"}, map[string]any{"ref": "gone@2"}}, nil
	}
	var approved []string
	reviewApprove = func(ref, evals string, behavior bool) (map[string]any, error) {
		if behavior {
			t.Fatal("behavior eval on a pin-only candidate")
		}
		approved = append(approved, ref)
		return items["pin@2"], nil
	}
	pins, skipped, err := pinOnlyCandidates()
	if err != nil || len(pins) != 1 || objGet(pins[0], "ref") != "pin@2" || objGet(pins[0], "stored_files") != 1 || objGet(pins[0], "hash_only_files") != 1 {
		t.Fatal(pins, err)
	}
	if len(skipped) != 1 || objGet(skipped[0], "ref") != "gone@2" || objGet(skipped[0], "reason") != "skill_unavailable" {
		t.Fatal(skipped)
	}
	var out, errOut strings.Builder
	if code, err := approvePins(false, &out, &errOut); code != 0 || err != nil || approved != nil {
		t.Fatal(code, err, approved)
	}
	if code, err := approvePins(true, &out, &errOut); code != 0 || err != nil || strings.Join(approved, ",") != "pin@2" {
		t.Fatal(code, err, approved)
	}
}
