package host

import (
	"net/url"
	"time"
)

// userDigestGet is the API read (fake in tests).
var userDigestGet = Get

// UserDigestContext is the person-approved user summary for the stable
// SessionStart block, already framed by the server as data (data_only_untrusted),
// or "" when there is none. A failure returns "" with the error so a hook can
// go on without it (fail-open). Placement in the hook output is the caller's.
func UserDigestContext(timeout time.Duration) (string, error) {
	return UserDigestContextFrom(userDigestGet, timeout)
}

// UserDigestContextFrom is UserDigestContext read through get (the capture
// hook passes its own API reader).
func UserDigestContextFrom(get func(string, time.Duration) (map[string]any, error), timeout time.Duration) (string, error) {
	q := url.Values{}
	for k, v := range Scope() {
		if s, ok := v.(string); ok && s != "" {
			q.Set(k, s)
		}
	}
	res, err := get("/v1/digests/user/context?"+q.Encode(), timeout)
	if err != nil {
		return "", err
	}
	if res["authority"] != "data_only_untrusted" {
		return "", nil
	}
	text, _ := res["text"].(string)
	return text, nil
}
