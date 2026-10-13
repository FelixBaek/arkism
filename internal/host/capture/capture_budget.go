package capture

// The optional, periodic model work (capture reflect, maintain run) spends the user's own Claude
// subscription. When the account the CLI runs as is already near a limit, it waits for a later
// run instead of eating what the user has left. Drain is not optional and does not ask.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// llmBudgetDefault is the usage percent (5-hour or weekly) at which optional model work waits.
const llmBudgetDefault = 85.0

// llmBudgetPct is ARKISM_LLM_BUDGET_PCT (1–100), else llmBudgetDefault.
func llmBudgetPct() float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(host.Env("ARKISM_LLM_BUDGET_PCT", "")), "%")), 64); err == nil && v > 0 && v <= 100 {
		return v
	}
	return llmBudgetDefault
}

// budgetNow is the clock for reading usage; tests replace it.
var budgetNow = time.Now

// budgetAt is an ISO time from the usage cache, or the zero time.
func budgetAt(v any) time.Time {
	s, _ := v.(string)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// budgetQuotaApplies: an all-models window, or one scoped to the model this work runs.
func budgetQuotaApplies(q map[string]any, model string) (label string, span time.Duration, ok bool) {
	window, _ := q["window"].(string)
	switch {
	case window == "5h":
		return "5시간", 5 * time.Hour, true
	case window == "week":
		return "주간", 7 * 24 * time.Hour, true
	case strings.HasPrefix(window, "week-"):
		scope := strings.TrimPrefix(strings.ToLower(str(q["model"])), "claude-")
		name := strings.TrimPrefix(strings.ToLower(model), "claude-")
		if scope != "" && strings.HasPrefix(name, scope) {
			return "주간(" + scope + ")", 7 * 24 * time.Hour, true
		}
	}
	return "", 0, false
}

// LLMBudgetReason is why optional model work for model should wait now, or "" to go ahead.
// It reads the last usage the app recorded for the Claude login the CLI runs as (no probe); no
// reading, a failed one or an expired window means go ahead, as before.
func LLMBudgetReason(model string) string {
	profiles := host.AccountsProfiles("claude", host.Home, host.AgentsState)
	if len(profiles) == 0 {
		return ""
	}
	account, err := host.AccountsAccount(profiles[0], "", host.AgentsState, false)
	if err != nil || account["usage_status"] != "ok" {
		return ""
	}
	limit, now := llmBudgetPct(), budgetNow()
	checked := budgetAt(account["usage_checked_at"])
	quotas, _ := account["quotas"].([]any)
	for _, raw := range quotas {
		q, isMap := raw.(map[string]any)
		if !isMap {
			continue
		}
		label, span, ok := budgetQuotaApplies(q, model)
		left, isNum := num(q["left_pct"])
		if !ok || !isNum {
			continue
		}
		// A reading from a window that has since reset says nothing about now.
		if reset := budgetAt(q["resets_at"]); !reset.IsZero() {
			if !reset.After(now) {
				continue
			}
		} else if checked.IsZero() || now.Sub(checked) > span {
			continue
		}
		if used := 100 - left; used >= limit {
			return fmt.Sprintf("Claude %s 사용량 %.0f%% ≥ %.0f%%: 이번 실행은 건너뜀 (ARKISM_LLM_BUDGET_PCT)", label, used, limit)
		}
	}
	return ""
}
