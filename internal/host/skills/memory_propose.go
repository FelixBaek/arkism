package skills

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

// The propose_* tools only ever create proposals a person decides: a directive or mental
// model stored as "proposed", a correction candidate, an invalidation question in the
// maintenance queue, or file extraction candidates. None of them can make anything active.

const proposalNote = "제안만 저장했습니다. 사람이 검토해 승인하기 전에는 적용되지 않으니, 적용된 것으로 보고하지 마세요."

// callMemoryWrite dispatches the remember, proposal, upload and job tools.
func callMemoryWrite(n string, args map[string]any) (host.Obj, error) {
	if n == "remember" || n == "propose_memory_update" {
		return callToolRemember(n, args)
	}
	value, err := memoryWriteValue(n, args)
	if err != nil {
		return nil, err
	}
	return toolText(value)
}

func memoryWriteValue(n string, args map[string]any) (any, error) {
	switch n {
	case "propose_directive":
		return proposeDirective(args)
	case "propose_mental_model":
		return proposeMentalModel(args)
	case "propose_invalidation":
		return proposeInvalidation(args)
	case "add_document":
		return addDocument(args)
	case "cancel_job":
		return cancelJob(args)
	case "read_document":
		return readDocument(args)
	case "read_space_config":
		return routerRequest(spaceConfigPath(), nil)
	}
	return nil, errors.New("unknown tool")
}

// toolID is a path-safe record id: 1-256 characters, no slash, never "." or "..".
func toolID(v any) (string, bool) {
	id, ok := v.(string)
	return id, ok && id != "" && host.Len(id) <= 256 && !strings.Contains(id, "/") && id != "." && id != ".."
}

// toolString is a required string of at most max runes.
func toolString(args map[string]any, key string, max int) (string, bool) {
	s, ok := args[key].(string)
	s = strings.TrimSpace(s)
	return s, ok && s != "" && utf8.ValidString(s) && host.Len(s) <= max
}

// toolInt reads an optional integer in [lo, hi]; a missing value gives def.
func toolInt(args map[string]any, key string, def, lo, hi int64) (int64, bool) {
	v, has := args[key]
	if !has || v == nil {
		return def, true
	}
	n, ok := host.Int(v)
	if _, isBool := v.(bool); isBool || !ok || n < lo || n > hi {
		return 0, false
	}
	return n, true
}

func proposed(res map[string]any) host.Obj {
	return host.Obj{{Key: "proposal", Value: res}, {Key: "authority", Value: "data_only_untrusted"}, {Key: "note", Value: proposalNote}}
}

func proposeDirective(args map[string]any) (any, error) {
	title, okT := toolString(args, "title", 200)
	rule, okR := toolString(args, "rule", 4000)
	applies := argStr(args, "applies_to", "both")
	priority, okP := toolInt(args, "priority", 50, 0, 100)
	if !okT || !okR || !okP || applies != "reflect" && applies != "context" && applies != "both" {
		return nil, errors.New("title (at most 200), rule (at most 4000), applies_to reflect|context|both and priority 0-100 are required")
	}
	// The server stores an agent's directive as proposed whatever this asks for.
	res, err := routerRequest("/v1/directives", map[string]any{"scope": host.Scope(), "title": title, "rule": rule, "applies_to": applies, "priority": priority, "active": false})
	if err != nil {
		return nil, err
	}
	return proposed(res), nil
}

func proposeMentalModel(args map[string]any) (any, error) {
	title, okT := toolString(args, "title", 120)
	query, okQ := toolString(args, "query", 1000)
	if !okT || !okQ || strings.ContainsAny(title, "\r\n") {
		return nil, errors.New("a one-line title (at most 120) and a query (at most 1000) are required")
	}
	res, err := routerRequest("/v1/mental-models", map[string]any{"scope": host.Scope(), "title": title, "query": query, "refresh": map[string]any{"mode": "manual"}})
	if err != nil {
		return nil, err
	}
	return proposed(res), nil
}

// memoryRef reads memory_id and revision (the revision the agent saw, from recall).
func memoryRef(args map[string]any) (map[string]any, bool) {
	id, okID := toolID(args["memory_id"])
	rev, okRev := toolInt(args, "revision", 0, 1, 1<<53)
	return map[string]any{"collection": "memories", "id": id, "revision": rev}, okID && okRev && rev > 0
}

func proposeInvalidation(args map[string]any) (any, error) {
	ref, ok := memoryRef(args)
	reason := argStr(args, "reason", "outdated")
	if !ok || reason != "outdated" && reason != "incorrect" {
		return nil, errors.New("memory_id, revision and reason outdated|incorrect are required")
	}
	res, err := routerRequest("/v1/maintenance_proposals", map[string]any{"scope": host.Scope(),
		"proposal": map[string]any{"type": "invalidation", "memory": ref, "reason": reason}})
	if err != nil {
		return nil, err
	}
	return proposed(res), nil
}

func addDocument(args map[string]any) (any, error) {
	name, okN := toolString(args, "filename", 200)
	text, hasText := args["text"].(string)
	b64, hasB64 := args["content_base64"].(string)
	if !okN || strings.ContainsAny(name, "/\\") || hasText == hasB64 || hasText && (text == "" || len(text) > 4<<20 || !utf8.ValidString(text)) || hasB64 && (b64 == "" || len(b64) > 5<<20) {
		return nil, errors.New("filename and exactly one of text or content_base64 (at most 4 MB) are required")
	}
	if hasText {
		b64 = base64.StdEncoding.EncodeToString([]byte(text))
	}
	payload := map[string]any{"scope": host.Scope(), "filename": name, "content_base64": b64, "dry_run": args["dry_run"] == true}
	if mime := argStr(args, "mime", ""); mime != "" {
		payload["mime"] = mime
	}
	res, err := routerRequest("/v1/files/memories", payload)
	if err != nil {
		return nil, err
	}
	return host.Obj{{Key: "result", Value: res}, {Key: "authority", Value: "data_only_untrusted"},
		{Key: "note", Value: "파일에서 뽑은 내용은 검토 대기 후보입니다. 남은 작업은 browse_memory의 file_job으로 확인하세요."}}, nil
}

func cancelJob(args map[string]any) (any, error) {
	id, ok := toolID(args["id"])
	rev, okRev := toolInt(args, "expected_revision", 0, 0, 1<<53)
	if !ok || !okRev {
		return nil, errors.New("a job id is required")
	}
	return routerRequest("/v1/operations/"+pyQuote(id)+"/cancel", map[string]any{"scope": host.Scope(), "expected_revision": rev})
}

func readDocument(args map[string]any) (any, error) {
	id, ok := toolID(args["id"])
	view := argStr(args, "view", "chunks")
	base := map[string]string{"file": "/v1/files/memories/documents/", "knowledge": "/v1/knowledge/sources/"}[argStr(args, "source", "")]
	limit, okL := toolInt(args, "limit", 50, 1, 200)
	cursor, _ := arg(args, "cursor", "").(string)
	if !ok || !okL || base == "" || view != "chunks" && view != "history" || len(cursor) > 256 {
		return nil, errors.New("source file|knowledge, an id and view chunks|history are required")
	}
	q := scopeQueryAll() + "&limit=" + strconv.FormatInt(limit, 10)
	if cursor != "" {
		q += "&cursor=" + url.QueryEscape(cursor)
	}
	res, err := routerRequest(base+pyQuote(id)+"/"+view+"?"+q, nil)
	if err != nil {
		return nil, err
	}
	return host.Obj{{Key: "document", Value: res}, {Key: "authority", Value: "data_only_untrusted"}, {Key: "note", Value: browseNote}}, nil
}
