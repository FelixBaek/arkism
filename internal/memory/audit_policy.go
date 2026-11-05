package memory

import (
	"context"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"sync"
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
// Optional events (routine reads, previews, searches and observations) follow the space's audit block: enabled
// false drops them, and a non-empty actions list keeps only the actions it
// names (a trailing * matches a prefix). A space without the block, or whose
// block cannot be read, records everything: the default is on, and a failure
// fails toward recording.

// Only verbs that change nothing stored are optional; a verb that writes,
// claims, settles, extracts or reviews anything stays mandatory.
var auditOptionalVerbs = map[string]bool{
	"read": true, "preview": true, "search": true, "retrieve": true, "watch": true, "observe": true, "availability": true,
}

// Raw-content reads stay mandatory although read is optional.
var auditMandatoryReads = []string{"read:session_archive_raw", "read:session_archive_messages", "read:session_archives", "read:artifacts", "read:sources", "read:source_derived"}

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
	start         time.Time
	mcp           bool // the caller sent X-Arkism-Transport: mcp
	route, method string
}

// withAuditRequest binds the request's arrival time, route and the transport
// the caller declared. The declaration is only a claim: auditMeta keeps "mcp"
// for an agent or service principal (the host's tool router) and records every
// other request as "http". "system" is never taken from a request.
func withAuditRequest(ctx context.Context, r *http.Request, route string, start time.Time) context.Context {
	return context.WithValue(ctx, auditRequestInfoKey{}, auditRequestInfo{start: start, mcp: r.Header.Get(auditTransportHeader) == "mcp", route: route, method: r.Method})
}

// auditMeta fills an event's request metadata: transport, route, method and
// the milliseconds from the request's arrival to the event. Work outside a
// request (workers, timers) is transport "system". role is the authenticated
// principal's role; empty reads the role the route check bound to ctx.
func auditMeta(ctx context.Context, role string, m AuditMetadata) *AuditMetadata {
	info, ok := ctx.Value(auditRequestInfoKey{}).(auditRequestInfo)
	if !ok {
		m.Transport = "system"
		return &m
	}
	if actor, bound := ctx.Value(httpActorKey{}).(*httpActor); role == "" && bound {
		role = actor.role
	}
	m.Transport = "http"
	if info.mcp && (role == roleAgent || role == roleService) {
		m.Transport = "mcp"
	}
	m.Route, m.Method = info.route, info.method
	m.DurationMS = time.Since(info.start).Milliseconds()
	if body, ok := ctx.Value(auditBodyKey{}).(*auditBody); ok {
		m.RequestBody = body.text()
	}
	return &m
}

// auditBodyBytes caps how much of a request body an audit event keeps.
const auditBodyBytes = 4 << 10

type auditBodyKey struct{}

// auditBody keeps the first auditBodyBytes of a JSON request body as the
// handler reads it, so events written after decoding carry what was sent.
type auditBody struct {
	mu sync.Mutex
	b  []byte
}

func (a *auditBody) Write(p []byte) (int, error) {
	a.mu.Lock()
	a.b = append(a.b, p[:min(len(p), auditBodyBytes-len(a.b))]...)
	a.mu.Unlock()
	return len(p), nil
}

// text is the kept body with private spans and secret-looking text removed.
func (a *auditBody) text() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return traceText(strings.ToValidUTF8(string(a.b), ""))
}

// captureAuditBody tees a JSON request body for audit events (ARKISM_AUDIT_BODIES).
// Uploads and other media types are never kept.
func captureAuditBody(r *http.Request) *http.Request {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if r.Body == nil || r.Body == http.NoBody || media != "application/json" {
		return r
	}
	body := &auditBody{}
	r = r.WithContext(context.WithValue(r.Context(), auditBodyKey{}, body))
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.TeeReader(r.Body, body), r.Body}
	return r
}
