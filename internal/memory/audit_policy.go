package memory

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Audit policy: which audit events a space may switch off, and the request
// metadata every event carries.
//
// Mandatory events are always written, whatever the space says:
//   - every denial (policy:deny, policy:invalid, policy:error, auth:deny) and
//     every decision:* row; neither goes through audit() at all;
//   - every action whose verb (the part before the first ':') is not listed in
//     auditOptionalVerbs: deletes, erasures, restores, exports, privacy
//     requests and holds, settings and policy changes, writes, creates and
//     updates, handoffs, reviews,
//     transfers, imports, maintenance, and any verb added later;
//   - raw-content reads (auditMandatoryReads), even though read is optional.
//
// Optional events (routine reads, previews, searches, job progress, model
// generation and usage bookkeeping) follow the space's audit block: enabled
// false drops them, and a non-empty actions list keeps only the actions it
// names (a trailing * matches a prefix). A space without the block, or whose
// block cannot be read, records everything: the default is on, and a failure
// fails toward recording.

var auditOptionalVerbs = map[string]bool{
	"read": true, "preview": true, "start": true, "stop": true, "finish": true, "observe": true, "refresh": true,
	"record": true, "claim": true, "settle": true, "reserve": true, "prepare": true, "extract": true, "wiki": true,
	"knowledge": true, "retrieve": true, "revalidate": true, "inspection": true, "flow_child": true, "flow_delivery": true,
	"file_extract": true, "ingest": true, "use": true, "search": true, "watch": true, "judge": true,
	"generate": true, "availability": true, "confirm": true, "set_review_due": true,
	"stale": true, "propose": true,
}

var auditMandatoryReads = []string{"read:session_archive_raw", "read:artifacts", "read:sources"}

const (
	spaceAuditActionsMax = 64
	auditTransportHeader = "X-Arkism-Transport"
)

// SpaceAudit is a space's audit block. It can only narrow optional events.
type SpaceAudit struct {
	Enabled *bool    `json:"enabled,omitempty" bson:"enabled,omitempty"`
	Actions []string `json:"actions,omitempty" bson:"actions,omitempty"`
}

func (a *SpaceAudit) valid() bool {
	if a == nil {
		return true
	}
	if len(a.Actions) > spaceAuditActionsMax {
		return false
	}
	for i, p := range a.Actions {
		if !validRunLabel(p, 128) || containsSecret(p) || slices.Contains(a.Actions[:i], p) {
			return false
		}
	}
	return true
}

// auditMandatory reports whether action is always recorded.
func auditMandatory(action string) bool {
	verb, _, _ := strings.Cut(action, ":")
	return !auditOptionalVerbs[verb] || slices.ContainsFunc(auditMandatoryReads, func(p string) bool {
		return action == p || strings.HasPrefix(action, p+":")
	})
}

// wants reports whether an optional action is recorded under this block.
func (a *SpaceAudit) wants(action string) bool {
	if a == nil {
		return true
	}
	if a.Enabled != nil && !*a.Enabled {
		return false
	}
	return len(a.Actions) == 0 || slices.ContainsFunc(a.Actions, func(p string) bool {
		if prefix, ok := strings.CutSuffix(p, "*"); ok {
			return strings.HasPrefix(action, prefix)
		}
		return p == action
	})
}

// auditRecorded decides one audit() write: mandatory always; optional per the
// space block, read in the caller's transaction. A read failure records.
func auditRecorded(ctx context.Context, tx Tx, scope Scope, action string) bool {
	if auditMandatory(action) {
		return true
	}
	r, err := tx.Get(ctx, colSpaceConfigs, spaceScope(scope), spaceConfigID)
	if err != nil || r.SpaceConfig == nil {
		return true
	}
	return r.SpaceConfig.Audit.wants(action)
}

type auditRequestInfoKey struct{}

// auditRequestInfo is what the HTTP middleware knows about the request an event belongs to.
type auditRequestInfo struct {
	start                    time.Time
	transport, route, method string
}

// withAuditRequest binds the request's arrival time, route and transport. The
// transport is the caller's declared label: "mcp" when the host's tool router
// sent X-Arkism-Transport: mcp, else "http".
func withAuditRequest(ctx context.Context, r *http.Request, route string, start time.Time) context.Context {
	transport := "http"
	if strings.EqualFold(r.Header.Get(auditTransportHeader), "mcp") {
		transport = "mcp"
	}
	return context.WithValue(ctx, auditRequestInfoKey{}, auditRequestInfo{start: start, transport: transport, route: route, method: r.Method})
}

// auditMeta fills an event's request metadata: transport, route, method and
// the milliseconds from the request's arrival to the event. Work outside a
// request (workers, timers) is transport "system".
func auditMeta(ctx context.Context, m AuditMetadata) *AuditMetadata {
	info, ok := ctx.Value(auditRequestInfoKey{}).(auditRequestInfo)
	if !ok {
		m.Transport = "system"
		return &m
	}
	m.Transport, m.Route, m.Method = info.transport, info.route, info.method
	m.DurationMS = time.Since(info.start).Milliseconds()
	return &m
}
