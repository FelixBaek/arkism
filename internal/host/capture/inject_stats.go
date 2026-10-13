package capture

import "github.com/FelixBaek/arkism/internal/host"

// logInjected records how much text a hook added to a session (characters only, never the
// text), so the desktop can show the average injected per session.
func logInjected(hook, sid, text string) {
	if text == "" {
		return
	}
	metric(kv("event", "inject"), kv("hook", hook), kv("session", sid), kv("chars", host.Len(text)))
}

// injectedChars is the number of sessions with an inject metric since an ISO time ("" = all)
// and the characters added to them in total.
func injectedChars(since string) (int, int, error) {
	rows, err := metricRows()
	if err != nil {
		return 0, 0, err
	}
	sessions := map[string]bool{}
	chars := 0
	for _, r := range rows {
		if r["event"] != "inject" || !truthy(r["session"]) || since != "" && str(or(r["ts"], "")) < since {
			continue
		}
		n, ok := host.Int(r["chars"])
		if !ok || n < 0 {
			continue
		}
		sessions[str(r["session"])] = true
		chars += int(n)
	}
	return len(sessions), chars, nil
}

func init() { host.InjectedChars = injectedChars }
