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
	EventType string
	Claims    []any
	Generator host.Obj
}

// SetBenchState points the capture state dir (the extractor's working dir) at dir.
func SetBenchState(dir string) { capState = dir }

// CaptureModel is the extraction model (ARKISM_CAPTURE_MODEL, default haiku).
func CaptureModel() string { return capModel }

// CueMatch reports whether the drain would treat text as a remember-worthy user turn.
func CueMatch(text string) bool { return cueRE.MatchString(text) }

// ExtractClaims runs the drain's extraction on userText. today is the TODAY line (YYYY-MM-DD),
// related the EXISTING MEMORIES rows (id, text, revision) the drain gets from search, guidance an
// optional user note appended the way capture.json's is. pipeline names the generator.
func ExtractClaims(userText, today string, related []map[string]any, guidance, pipeline string) (BenchExtraction, error) {
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
		lines[i] = fmt.Sprintf("%s: %s", str(r["id"]), str(or(r["text"], "")))
	}
	existing := strings.Join(lines, "\n")
	if existing == "" {
		existing = "(none)"
	}
	out, err := llm(extractPrompt+userGuidance(guidance), fmt.Sprintf("TODAY: %s\n\nEXISTING MEMORIES:\n%s\n\nUSER MESSAGES:\n%s", today, existing, userText),
		capModel, 180*time.Second, claimsSchema, "claims")
	if err != nil {
		return BenchExtraction{}, err
	}
	raw, err := asList(out)
	if err != nil {
		return BenchExtraction{}, err
	}
	claims, err := buildClaims(raw, userText, related, nil)
	if err != nil {
		return BenchExtraction{}, err
	}
	return BenchExtraction{EventType: eventType, Claims: claims, Generator: generator(pipeline)}, nil
}
