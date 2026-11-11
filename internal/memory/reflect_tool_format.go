package memory

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// A reserved tool envelope appended to prose is neither an executable tool
// reply nor an answer. Decode only to recognize that format error: the calls
// extracted here are never returned to the tool executor.
func mixedReflectToolEnvelope(text string) bool {
	for offset := strings.IndexByte(text, '{'); offset >= 0; {
		prefix := strings.TrimSpace(text[:offset])
		if prefix != "" && reflectToolEnvelopeSuffix(text[offset:]) {
			return true
		}
		next := strings.IndexByte(text[offset+1:], '{')
		if next < 0 {
			break
		}
		offset += next + 1
	}
	return false
}

func reflectToolEnvelopeSuffix(text string) bool {
	dec := json.NewDecoder(strings.NewReader(text))
	var raw json.RawMessage
	if dec.Decode(&raw) != nil {
		return false
	}
	suffix := strings.TrimSpace(text[dec.InputOffset():])
	if suffix != "" && suffix != "```" {
		return false
	}
	_, ok := parseReflectToolCalls(string(raw))
	return ok
}

func reflectReplyNeedsRepair(text string, isTool bool) bool {
	return !isTool && mixedReflectToolEnvelope(text)
}

func (s *Service) reflectToolCalls(ctx context.Context, token string, run *reflectRun, calls []reflectToolCall) error {
	for _, call := range calls {
		start := time.Now()
		step, err := s.reflectTool(ctx, token, run, call)
		if err != nil {
			return err
		}
		step.MS = time.Since(start).Milliseconds()
		run.trace = append(run.trace, step)
	}
	return nil
}

func reflectPromptMemoryIDs(hits []Hit) []string {
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	return ids
}
