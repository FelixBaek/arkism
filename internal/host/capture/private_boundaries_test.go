package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestPrivateNestedAndUnfinishedSpans(t *testing.T) {
	for input, want := range map[string]string{
		"visible <private>outer <PRIVATE>inner</private>hidden-tail</PRIVATE> after": "visible [비공개] after",
		"visible <private>outer <private attr=1>inner</private>hidden-tail":          "visible [비공개]",
		"<private>a<private>b</private>c</private>":                                  "",
		"visible </private> after":                                                   "visible </private> after",
		"<privateer>visible</privateer>":                                             "<privateer>visible</privateer>",
	} {
		if got := clean(input); got != want {
			t.Fatal("nested private boundary changed")
		}
	}
}

func TestPrivateNeverEntersRoleTransferAuthorizationOrModel(t *testing.T) {
	for _, role := range []string{"user", "assistant", "tool"} {
		t.Run(role, func(t *testing.T) {
			capFake(t)
			records := []any{map[string]any{"source_id": "source-a", "revision": 1, "role": role, "text": "visible <private>synthetic-private <private>inner</private>hidden-tail</private> after"}}
			previous := map[string]any{"goal": "before <private>previous-private</private> after"}
			prior := []any{host.Obj{{Key: "quote", Value: "before <private>evidence-private</private> after"}}}
			var authorized, sent string
			llm = func(_, content, _ string, _ time.Duration, _, _ string) (any, error) {
				sent = content
				return nil, sessionWait("synthetic_model_pause")
			}
			_, err := sessionPrepare(map[string]any{}, "session", records, 0, 1, 0, previous, prior, "", func(c string) error { authorized = c; return nil })
			if errorCode(err) != "synthetic_model_pause" || sent == "" || sent != authorized {
				t.Fatal("model/authorization path not exercised", err)
			}
			for _, secret := range []string{"synthetic-private", "hidden-tail", "previous-private", "evidence-private"} {
				if strings.Contains(sent, secret) {
					t.Fatal("private span reached transfer/model")
				}
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(sent), &payload); err != nil {
				t.Fatal(err)
			}
			data := payload["data"].(map[string]any)
			rows := data["records"].([]any)
			if autosaveGet(rows[0], "text") != "visible [비공개] after" || autosaveGet(rows[0], "role") != role {
				t.Fatal("allowed role/text changed")
			}
			if !strings.Contains(autosaveGet(records[0], "text").(string), "synthetic-private") {
				t.Fatal("caller records mutated")
			}
		})
	}
}

func TestPrivateBoundaryAllowsUnrelatedComponentNames(t *testing.T) {
	capFake(t)
	called := false
	llm = func(_, content, _ string, _ time.Duration, _, _ string) (any, error) {
		called = true
		if !strings.Contains(content, "PrivateRoute") {
			t.Fatal("ordinary component name was changed")
		}
		return nil, sessionWait("synthetic_model_pause")
	}
	records := []any{map[string]any{"source_id": "source-a", "revision": 1, "role": "user", "text": "Use <PrivateRoute> and <privateer> components."}}
	_, err := sessionPrepare(map[string]any{}, "session", records, 0, 1, 0, nil, nil, "", func(string) error { return nil })
	if !called || errorCode(err) != "synthetic_model_pause" {
		t.Fatal("ordinary component names blocked model transfer", err)
	}
}

func TestPrivateContentCannotCreateRawHashLocators(t *testing.T) {
	if evidencePromptLocator("codex", "server", "session", "visible <private>synthetic-private</private> after") != nil {
		t.Fatal("private prompt produced a raw hash locator")
	}
	context := host.Obj{{Key: "archive_id", Value: "archive"}}
	message := map[string]any{"role": "user"}
	raw, err := json.Marshal(map[string]any{"message": map[string]any{"role": "user", "content": "visible <private>synthetic-private</private>"}})
	if err != nil {
		t.Fatal(err)
	}
	if evidenceRowHint(context, append(raw, '\n'), 0, message, "jsonl") != nil {
		t.Fatal("private record produced a raw hash hint")
	}
	if evidencePromptLocator("codex", "server", "session", "visible allowed prompt") == nil {
		t.Fatal("normal locator lost")
	}
}

func TestPrivateSessionIdentityStopsBeforeCheckpointLookup(t *testing.T) {
	for _, field := range []string{"session_id", "scope"} {
		t.Run(field, func(t *testing.T) {
			capFake(t)
			originalScope := capScope
			t.Cleanup(func() { capScope = originalScope })
			scope := map[string]any{"workspace_id": "workspace"}
			job := map[string]any{"session_id": "session"}
			if field == "scope" {
				scope["workspace_id"] = "<private>synthetic-scope</private>"
			} else {
				job["session_id"] = "<private>synthetic-session</private>"
			}
			capScope = func() map[string]any { return scope }
			before := dumps(job)
			if err := sessionProcess(job, func() error { t.Fatal("private identity persisted"); return nil }, "codex"); errorCode(err) != "session_private_identity" {
				t.Fatal(err)
			}
			if dumps(job) != before {
				t.Fatal("private identity changed queued state")
			}
		})
	}
}

type privateShortReader struct{ io.Reader }

func (r privateShortReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

func TestArchivePrivateMarkersAcrossEncodingsAndReadBoundaries(t *testing.T) {
	for _, text := range []string{
		"before <PRIVATE>after",
		`before \u003cprivate\u003eafter`,
		`before \u003C\u0050\u0072\u0049\u0076\u0041\u0074\u0045\u003eafter`,
		`before \\u003cprivate\u003eafter`,
		`before \u005cu005cu003cprivate\u003eafter`,
		"before <\x00p\x00r\x00i\x00v\x00a\x00t\x00e\x00>\x00after",
		"before \x00<\x00p\x00r\x00i\x00v\x00a\x00t\x00e after",
		`before <private\tattr=1>after`,
		`before <private\nattr=1>after`,
		`before <private`,
		`malformed \u00<private>after`,
	} {
		found, err := scanPrivate(privateShortReader{strings.NewReader(text)})
		if err != nil || !found {
			t.Fatal("private marker crossed an encoding/read boundary", err)
		}
	}
	for _, text := range []string{`normal C:\users\name`, `normal \u003e text`, "normal 한국어", `normal <pri\vate>`, `<PrivateRoute>`, `<privateer>`, `\u003cPrivateRoute\u003e`} {
		found, err := scanPrivate(strings.NewReader(text))
		if err != nil || found {
			t.Fatal("normal archive rejected", err)
		}
	}
}

type privateErrorReader struct{}

func (privateErrorReader) Read([]byte) (int, error) { return 0, errors.New("synthetic read error") }

func TestArchivePrivateInspectionReadFailure(t *testing.T) {
	if _, err := scanPrivate(privateErrorReader{}); err == nil {
		t.Fatal("inspection error disappeared")
	}
	if !privateMarked(filepath.Join(t.TempDir(), "missing")) {
		t.Fatal("uninspectable source allowed")
	}
}

func TestArchiveCollectorExcludesJSONEscapedPrivateBeforeUpload(t *testing.T) {
	home, _ := archiveHome(t)
	raw, err := json.Marshal(map[string]any{"sessionId": "private-json-escaping", "type": "user", "message": map[string]any{"role": "user", "content": "visible <private>synthetic-private-span</private> after"}})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("<private>")) {
		t.Fatal("fixture must use standard JSON escaping")
	}
	put(t, filepath.Join(home, ".claude/projects/p/escaped.jsonl"), string(raw)+"\n")
	client := newFakeArchive(t)
	state := host.Obj{}
	c := newCollector(client, &state, true)
	c.collect()
	if len(client.commits) != 0 || len(client.chunks) != 0 || c.counts.n["private_skipped"] != 1 {
		t.Fatal("marked session reached archive transport")
	}
}
