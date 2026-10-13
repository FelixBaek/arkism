package capture

import "github.com/FelixBaek/arkism/internal/host"

// The capture hook and drain reach the evidence and work modules through these
// variables so tests can replace one step at a time.
var (
	evClaudeContext = func(transcript, projects, sid, client string) (any, error) {
		return objAny(evidenceClaudeContext(transcript, projects, sid, client)), nil
	}
	evRowHint = func(context any, raw []byte, start int64, msg any) (any, error) {
		return objAny(evidenceRowHint(context, raw, start, msg, "")), nil
	}
	evPromptLocator = func(origin, device, sid string, prompt any) (any, error) {
		return objAny(evidencePromptLocator(origin, device, sid, prompt)), nil
	}
	selSelected    = func(cwd any) (string, error) { return selectionSelected(cwd), nil }
	delStatus      = deliveryStatus
	hoSuspend      = handoffSuspend
	hoBind         = handoffBind
	hoQueue        = handoffQueue
	hoDrain        = handoffDrain
	pcDrain        = processorDrain
	evLocatePrompt = func(locator map[string]any) (context, hint any, root string, err error) {
		c, h, root, err := evidenceLocatePrompt(locator)
		return objAny(c), objAny(h), root, err
	}
	evPrepareZcode = func(locator map[string]any, cued []any, source string) ([]any, error) {
		return evidencePrepareZcode(locator, cued, source, capPost, capScope())
	}
	evPrepareUsers = func(context any, users []any, root string) ([]any, error) {
		return evidencePrepareUsers(context, users, root, capPost, capScope())
	}
)

// objAny keeps Python's None: a nil host.Obj must not become a non-nil interface.
func objAny(o host.Obj) any {
	if o == nil {
		return nil
	}
	return o
}
