package host

// Area C (capture) owns this; see xarea.go for the pattern.
var (
	// InjectedChars returns how many sessions the capture hooks added text to since an ISO
	// time ("" = all), and the characters they added in total (SessionStart + prompt recall).
	InjectedChars = func(since string) (int, int, error) { return 0, 0, errNotPorted }
)
