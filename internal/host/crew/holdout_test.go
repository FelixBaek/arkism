package crew

import "testing"

func TestHoldoutPreflightAndCategoryGate(t *testing.T) {
	task := map[string]any{"id": "unseen", "category": "quality", "input": "new task", "expected": "answer", "held_out": true}
	hash := digest([]byte(mustCanonical(task)))
	dataset := map[string]any{"cases": []any{task}}
	plan := map[string]any{"holdout": map[string]any{"development_tasks": []any{digest([]byte("development"))}, "tasks": []any{map[string]any{"case_sha256": hash, "category": "reasoning"}}, "min_category_score": .9, "min_category_cases": 1}}
	tasks, threshold, e := holdoutTasks(plan, dataset)
	if e != nil {
		t.Fatal(e)
	}
	rows := []any{map[string]any{"case_sha256": hash, "category": "quality", "variant": "candidate", "passed": false}}
	if holdoutRowsPass(tasks, threshold, rows) {
		t.Fatal("failed category accepted")
	}
	rows[0].(map[string]any)["passed"] = true
	if !holdoutRowsPass(tasks, threshold, rows) {
		t.Fatal("passing category denied")
	}
	plan["holdout"].(map[string]any)["development_tasks"] = []any{hash}
	if _, _, e = holdoutTasks(plan, dataset); e == nil {
		t.Fatal("development leakage accepted")
	}
}

func TestHoldoutRealProcessComparison(t *testing.T) {
	f := newEvalFixture(t, "adapter-echo")
	quality := list(f.dataset["cases"])[0]
	hash := digest([]byte(mustCanonical(quality)))
	f.plan["holdout"] = map[string]any{"development_tasks": []any{digest([]byte("development-task"))}, "tasks": []any{map[string]any{"case_sha256": hash, "category": "unseen-domain"}}, "min_category_score": .9, "min_category_cases": 1}
	dataset, env, e := f.validate()
	if e != nil {
		t.Fatal(e)
	}
	result, e := evaluate(f.prepared, dataset, env)
	if e != nil || result["passed"] != true {
		t.Fatal("holdout adapter execution", result, e)
	}
	// Bad adapter path proves leakage is rejected before starting any adapter.
	f.env["argv"] = []any{"/nonexistent/holdout-adapter"}
	f.plan["holdout"].(map[string]any)["development_tasks"] = []any{hash}
	if _, e = evaluate(f.prepared, dataset, f.env); code(e) != "invalid_holdout" {
		t.Fatal("leakage did not fail preflight", e)
	}
}
