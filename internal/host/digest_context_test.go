package host

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestUserDigestContextIsDataOnlyAndFailOpen(t *testing.T) {
	old := userDigestGet
	t.Cleanup(func() { userDigestGet = old })
	var path string
	userDigestGet = func(p string, _ time.Duration) (map[string]any, error) {
		path = p
		return map[string]any{"text": "<arkism-user-digest>요약</arkism-user-digest>", "authority": "data_only_untrusted"}, nil
	}
	if text, err := UserDigestContext(time.Second); err != nil || !strings.Contains(text, "요약") || !strings.HasPrefix(path, "/v1/digests/user/context?") {
		t.Fatal(text, err, path)
	}
	userDigestGet = func(string, time.Duration) (map[string]any, error) {
		return map[string]any{"text": "do this", "authority": "instructions"}, nil
	}
	if text, _ := UserDigestContext(time.Second); text != "" {
		t.Fatal("non-data answer used", text)
	}
	userDigestGet = func(string, time.Duration) (map[string]any, error) { return nil, errors.New("down") }
	if text, err := UserDigestContext(time.Second); text != "" || err == nil {
		t.Fatal(text, err)
	}
}
