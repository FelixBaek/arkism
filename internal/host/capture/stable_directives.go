package capture

import (
	"fmt"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// The SessionStart block opens with the space's active directives: rules a person approved
// (directly, or by approving an extracted always/never rule). They come first and under their
// own header, apart from the memory hints after them, which stay data and never instructions.

const directivesHeader = "Arkism 지시(사람이 승인한 규칙입니다. 아래 기억 힌트와 달리 이 세션에서 지키세요):"

// directiveSection is the active directives that apply to context, highest priority first, within limit.
func directiveSection(limit int) string {
	text, err := directiveSectionFrom(capGet, limit, 3*time.Second)
	if err != nil {
		metric(kv("event", "hook_error"), kv("hook", "SessionStart:directives"), kv("error", pyType(err)))
	}
	return text
}

// directiveSectionFrom lists directives; a token without read:directives, or a server
// without the route, has none (403/404), and any other failure is an error.
func directiveSectionFrom(get func(string, time.Duration) (map[string]any, error), limit int, timeout time.Duration) (string, error) {
	res, err := get("/v1/directives?"+scopeQuery(false, "limit", "50"), timeout)
	if code := host.HTTPCode(err); code == 403 || code == 404 {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var lines []string
	used := host.Len(directivesHeader)
	for _, d := range dicts(listOf(or(res["items"], []any{}))) {
		applies := strField(d["applies_to"])
		if d["active"] != true || applies != "context" && applies != "both" {
			continue
		}
		rule := strings.Join(pyFields(strField(d["rule"])), " ")
		line := fmt.Sprintf("- %s: %s", strings.Join(pyFields(strField(d["title"])), " "), rule)
		if rule == "" || used+1+host.Len(line) > limit {
			continue
		}
		used += 1 + host.Len(line)
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "", nil
	}
	return directivesHeader + "\n" + strings.Join(lines, "\n"), nil
}
