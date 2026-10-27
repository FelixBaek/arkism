package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// privatePayload also inspects object keys and JSON-escaped text. Unknown shapes
// fail closed rather than letting a newly added field bypass the model boundary.
func privatePayload(v any) bool {
	raw, err := json.Marshal(v)
	if err != nil {
		return true
	}
	marked, err := scanPrivate(bytes.NewReader(raw))
	return marked || err != nil
}

// Escaped or incomplete marks cannot always be cut into literal spans. Mask
// only that value so other dialogue in the same window can still be captured.
func redactPrivateValue(text string) string {
	text = redactPrivate(text)
	if privatePayload(text) {
		return privateMark
	}
	return text
}

// privateData copies dialogue/state without changing identifiers or object keys.
// A marked key is refused by privatePayload after sanitizing the values.
func privateData(v any) any {
	switch x := v.(type) {
	case string:
		return redactPrivateValue(x)
	case []any:
		out := make([]any, len(x))
		for i, value := range x {
			out[i] = privateData(value)
		}
		return out
	case []string:
		out := make([]string, len(x))
		for i, value := range x {
			out[i] = redactPrivateValue(value)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for key, value := range x {
			out[key] = privateData(value)
		}
		return out
	case host.Obj:
		out := make(host.Obj, len(x))
		for i, entry := range x {
			out[i] = entry
			out[i].Value = privateData(entry.Value)
		}
		return out
	}
	return v
}

// privateModel never changes the request: structured sessions authorize these
// exact bytes before calling it. Callers sanitize decoded data before encoding.
func privateModel(system, user, model string, timeout time.Duration, schema, key string, policyPaths ...string) (any, error) {
	if err := privateCapturePolicy("model_capture_policy_wait", policyPaths...); err != nil {
		return nil, err
	}
	if privatePayload([]string{system, user}) {
		return nil, sessionWait("model_private_content")
	}
	out, err := llm(system, user, model, timeout, schema, key)
	if def, ok := fallbackModel[model]; ok && err != nil { // the picked model could not answer here: the default does
		if policyErr := privateCapturePolicy("model_capture_policy_wait", policyPaths...); policyErr != nil {
			return nil, policyErr // capture was switched off during the first call: nothing more is sent
		}
		out, err = llm(system, user, def, timeout, schema, key)
	}
	if policyErr := privateCapturePolicy("model_capture_policy_wait", policyPaths...); policyErr != nil {
		return nil, policyErr
	}
	if err == nil && privatePayload(out) {
		return nil, sessionWait("model_private_output")
	}
	return out, err
}

func privateCapturePolicy(code string, paths ...string) error {
	settings := readCaptureSettings()
	if captureSkippedWithSettings("", settings) {
		return sessionWait(code)
	}
	for _, path := range paths {
		if captureSkippedWithSettings(path, settings) {
			return sessionWait(code)
		}
	}
	return nil
}

// Only reversible policy waits are free. Private-content defects and unknown
// pending errors retain the existing bounded failure/recovery behavior.
func capturePolicyPending(err error) bool {
	if !errors.Is(err, errSessionPending) {
		return false
	}
	switch errorCode(err) {
	case "capture_policy_wait", "session_capture_policy_wait", "model_capture_policy_wait", "work_capture_policy_wait", "archive_capture_policy_wait":
		return true
	}
	return false
}

var checkpointIdentityKeys = []string{"repository", "branch", "head", "origin", "session_id"}
var evidenceIdentityKeys = []string{"collection", "id", "source_id", "revision", "role", "archive_id", "sha256", "record_key", "chunk_index", "chunk_offset", "message_sha256"}

func privateIdentityFields(value any, keys []string) bool {
	for _, key := range keys {
		if field, has := objGet(value, key); has && privatePayload(field) {
			return true
		}
	}
	return false
}

// These are the checkpoint/session schemas' actual paths and source identifiers.
// Descriptive fields (goal, summary, quote) remain eligible for redaction.
func privateCheckpointIdentity(pkg any) bool {
	if privateIdentityFields(pkg, checkpointIdentityKeys) {
		return true
	}
	for _, key := range []string{"files", "artifacts"} {
		for _, file := range anyList(evidenceField(pkg, key)) {
			if privateIdentityFields(file, []string{"path", "sha256"}) {
				return true
			}
		}
	}
	for _, ref := range anyList(evidenceField(pkg, "source_refs")) {
		if privateIdentityFields(ref, evidenceIdentityKeys) {
			return true
		}
	}
	for _, check := range anyList(evidenceField(pkg, "checks")) {
		if privateIdentityFields(check, []string{"command", "outcome"}) || privateCheckpointIdentity(check) {
			return true
		}
	}
	return false
}

func preserveIdentityFields(masked, original any, keys []string) {
	for _, key := range keys {
		if value, has := objGet(original, key); has {
			switch target := masked.(type) {
			case map[string]any:
				target[key] = value
			case host.Obj:
				target.Set(key, value)
			}
		}
	}
}

func maskCheckpointState(original any) any {
	masked := maskSensitive(original)
	preserveIdentityFields(masked, original, checkpointIdentityKeys)
	for _, key := range []string{"files", "artifacts", "source_refs", "checks"} {
		before, after := anyList(evidenceField(original, key)), anyList(evidenceField(masked, key))
		for i := range after {
			switch key {
			case "files", "artifacts":
				preserveIdentityFields(after[i], before[i], []string{"path", "sha256"})
			case "source_refs":
				preserveIdentityFields(after[i], before[i], evidenceIdentityKeys)
			case "checks":
				after[i] = maskCheckpointState(before[i])
				preserveIdentityFields(after[i], before[i], []string{"command", "outcome"})
			}
		}
	}
	return masked
}

// A prepared request may already have reached its destination before its ACK was
// lost. Do not rewrite it under the same event ID or advance its durable cursor.
func privatePrepared(job any, key string) bool {
	prepared := autosaveGet(job, key)
	return truthy(prepared) && (privatePayload(prepared) || privatePayload(autosaveGet(job, "users")))
}

// Optional prepared checkpoints may be absent on a fresh job. Read only their
// explicit repository metadata; an archive filename is not a project directory.
func privateCheckpointRepository(prepared any, session bool) string {
	if session {
		prepared, _ = objGet(prepared, "checkpoint_request")
	}
	pkg, _ := objGet(prepared, "package")
	repository, _ := objGet(pkg, "repository")
	return str(or(repository, ""))
}
