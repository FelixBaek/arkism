package crew

import (
	"strings"
	"unicode"
)

// Client preflight rejects declared leakage before invoking an adapter. The
// server independently repeats these checks when recording and approving.
func holdoutTasks(plan, dataset map[string]any) (map[string]string, float64, error) {
	raw, exists := plan["holdout"]
	if !exists || raw == nil {
		return nil, 0, nil
	}
	h := obj(raw)
	if h == nil {
		return nil, 0, cmpErr("invalid_holdout")
	}
	score, minCases, err := holdoutThresholds(h)
	if err != nil {
		return nil, 0, err
	}
	development, err := holdoutDevelopment(h)
	if err != nil {
		return nil, 0, err
	}
	tasks, err := holdoutDeclared(h, development, minCases)
	if err != nil {
		return nil, 0, err
	}
	if err := holdoutCoverage(dataset, tasks, development); err != nil {
		return nil, 0, err
	}
	return tasks, score, nil
}
func holdoutThresholds(h map[string]any) (float64, float64, error) {
	score, ok := num(h["min_category_score"])
	if !ok || score < .9 || score > 1 || !finiteCost(score) {
		return 0, 0, cmpErr("invalid_holdout")
	}
	minCases, ok := num(h["min_category_cases"])
	if !ok || minCases < 1 || minCases > 62 || float64(int(minCases)) != minCases {
		return 0, 0, cmpErr("invalid_holdout")
	}
	return score, minCases, nil
}
func holdoutDevelopment(h map[string]any) (map[string]bool, error) {
	development := map[string]bool{}
	dev, ok := h["development_tasks"].([]any)
	if !ok || len(dev) == 0 || len(dev) > 128 {
		return nil, cmpErr("invalid_holdout")
	}
	for _, v := range dev {
		s, ok := v.(string)
		if !ok || !holdoutHash(s) || development[s] {
			return nil, cmpErr("invalid_holdout")
		}
		development[s] = true
	}
	return development, nil
}
func holdoutDeclared(h map[string]any, development map[string]bool, minCases float64) (map[string]string, error) {
	tasks := map[string]string{}
	counts := map[string]int{}
	declared, ok := h["tasks"].([]any)
	if !ok || len(declared) == 0 || len(declared) > 62 {
		return nil, cmpErr("invalid_holdout")
	}
	for _, v := range declared {
		m := obj(v)
		hash, hok := m["case_sha256"].(string)
		category, cok := m["category"].(string)
		if !hok || !cok || !holdoutHash(hash) || !holdoutCategory(category) || development[hash] || tasks[hash] != "" {
			return nil, cmpErr("invalid_holdout")
		}
		tasks[hash] = category
		counts[category]++
	}
	for _, n := range counts {
		if float64(n) < minCases {
			return nil, cmpErr("invalid_holdout")
		}
	}
	return tasks, nil
}
func holdoutCategory(category string) bool {
	return category != "" && len(category) <= 64 && strings.TrimSpace(category) == category && strings.IndexFunc(category, unicode.IsControl) < 0
}
func holdoutCoverage(dataset map[string]any, tasks map[string]string, development map[string]bool) error {
	seen := map[string]bool{}
	for _, v := range list(dataset["cases"]) {
		m := obj(v)
		hash := digest([]byte(mustCanonical(v)))
		_, declared := tasks[hash]
		if development[hash] || declared != same(m["category"], "quality") || seen[hash] {
			return cmpErr("holdout_leakage_or_coverage")
		}
		seen[hash] = true
	}
	for hash := range tasks {
		if !seen[hash] {
			return cmpErr("holdout_leakage_or_coverage")
		}
	}
	return nil
}
func holdoutHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func holdoutRowsPass(tasks map[string]string, threshold float64, rows []any) bool {
	if tasks == nil {
		return true
	}
	counts, passed := map[string]int{}, map[string]int{}
	for _, v := range rows {
		m := obj(v)
		if m["variant"] != "candidate" || m["category"] != "quality" {
			continue
		}
		category := tasks[mustStr(m["case_sha256"])]
		counts[category]++
		if m["passed"] == true {
			passed[category]++
		}
	}
	for _, category := range tasks {
		if counts[category] == 0 || float64(passed[category])/float64(counts[category])+1e-12 < threshold {
			return false
		}
	}
	return true
}
