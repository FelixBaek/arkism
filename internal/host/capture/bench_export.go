package capture

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// The drain's extraction step, exported for the reproducible benchmark (bench/locomo).
// It runs the same prompt, schema, model and claim checks as process(); only the spool,
// the cue filter and the user's capture.json note are left to the caller.

// BenchExtraction is one extracted event, ready for POST /v1/capture.
type BenchExtraction struct {
	EventType         string
	Claims            []any
	Generator         host.Obj
	ContextRejections *ClaimContextReceipt `json:"context_rejections,omitempty"`
}

// SetBenchState points the capture state dir (the extractor's working dir) at dir.
func SetBenchState(dir string) { capState = dir }

// CaptureModel is the extraction model (ARKISM_CAPTURE_MODEL, default haiku).
func CaptureModel() string { return capModel }

// ExtractClaims runs the drain's extraction on userText. today is the TODAY line (YYYY-MM-DD),
// related the EXISTING MEMORIES rows (id, text, revision) the drain gets from search, guidance an
// optional user note appended the way capture.json's is, density the space's capture_density as
// the server resolves it (config/effective; "" is durable). The caller feeds user text only, so the
// capture mode stays user. pipeline names the generator. Each line is one turn, so the
// conversation density reads it in windows.
func ExtractClaims(userText, today string, related []map[string]any, guidance, density, pipeline string) (BenchExtraction, error) {
	userText, guidance = redactPrivate(userText), redactPrivate(guidance)
	if err := os.MkdirAll(capState, 0o700); err != nil {
		return BenchExtraction{}, err
	}
	eventType := "turn_completed"
	if rememberRE.MatchString(userText) {
		eventType = "explicit_remember"
	} else if correctionRE.MatchString(userText) {
		eventType = "user_corrected_fact"
	}
	lines := make([]string, len(related))
	for i, r := range related {
		lines[i] = fmt.Sprintf("%s: %s", str(r["id"]), redactPrivate(str(or(r["text"], ""))))
	}
	existing := strings.Join(lines, "\n")
	if existing == "" {
		existing = "(none)"
	}
	system := capturePrompt("", density) + userGuidance(guidance)
	raw, err := extractWindows(density, strings.Split(userText, "\n"), func(text string) (any, error) {
		return privateModel(system, fmt.Sprintf("TODAY: %s\nMEMORY_LANGUAGE: auto\n\nEXISTING MEMORIES:\n%s\n\nUSER MESSAGES:\n%s", today, existing, text),
			capModel, 180*time.Second, claimsSchema, "claims")
	})
	if err != nil {
		return BenchExtraction{}, err
	}
	claims, receipt, err := buildClaimsWithReceipt(raw, userText, related, nil)
	if err != nil {
		return BenchExtraction{}, err
	}
	result := BenchExtraction{EventType: eventType, Claims: claims, Generator: extractionGenerator(pipeline), ContextRejections: receipt}
	if len(claims) == 0 && receipt != nil {
		return result, runtimeErr("capture_context_all_withheld")
	}
	return result, nil
}
