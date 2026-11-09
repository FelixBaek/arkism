package memory

import (
	"bufio"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Test doubles shared by the platform-neutral tests (the Windows job compiles this package).

// fakeRelation is one relations-table row between a memory and another node.
type fakeRelation struct{ SourceID, TargetKind, TargetID, Kind string }

func (f *fakeRepo) SearchMemoriesInWindow(_ context.Context, in SearchInput, from, to time.Time) ([]Hit, error) {
	c, err := normalizeSearchCriteria(in.SearchCriteria)
	if err != nil {
		return nil, err
	}
	rows := []Record{}
	for _, r := range f.rows(colMemories) {
		if sameScope(r.Scope, in.Scope) {
			rows = append(rows, r)
		}
	}
	return windowHits(rows, c, time.Now().UTC(), timeWindow{From: from, To: to}, from.Location(), in.Limit), nil
}

// MemoryNeighbors mirrors memoryNeighborsSQL over f.relations (scope is ignored: one scope per test).
func (f *fakeRepo) MemoryNeighbors(_ context.Context, _ Scope, seeds []string, limit int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count, first := map[string]int{}, map[string]int{}
	add := func(id string, pos int) {
		if slices.Contains(seeds, id) {
			return
		}
		if _, ok := first[id]; !ok || pos < first[id] {
			first[id] = pos
		}
		count[id]++
	}
	for pos, seed := range seeds {
		for _, r := range f.relations {
			switch {
			case r.SourceID == seed && r.TargetKind == "memory":
				add(r.TargetID, pos)
			case r.TargetKind == "memory" && r.TargetID == seed:
				add(r.SourceID, pos)
			case r.SourceID == seed && r.Kind == "mentions":
				for _, o := range f.relations {
					if o.Kind == "mentions" && o.TargetKind == r.TargetKind && o.TargetID == r.TargetID {
						add(o.SourceID, pos)
					}
				}
			}
		}
	}
	ids := make([]string, 0, len(count))
	for id := range count {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := ids[i], ids[j]
		if count[a] != count[b] {
			return count[a] > count[b]
		}
		if first[a] != first[b] {
			return first[a] < first[b]
		}
		return a < b
	})
	return ids[:min(limit, len(ids))], nil
}

// fakeExtension is the stdio extension program (the test binary, ARKISM_TEST_HELPER=extension).
func fakeExtension() int {
	in, out := bufio.NewScanner(os.Stdin), json.NewEncoder(os.Stdout)
	for in.Scan() {
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil {
			return 1
		}
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": extAnswer(req.Method, req.Params)})
	}
	return 0
}

// extAnswer is the fake extension both transports serve: it refuses an operation
// whose input says "forbidden", sleeps on "sleepy", refuses tenant checks for
// human-2, and echoes routes and tools.
func extAnswer(method string, params json.RawMessage) any {
	var p struct {
		Phase, Path, Tool string
		Actor             map[string]string
		Body              json.RawMessage
		Data              map[string]int
	}
	_ = json.Unmarshal(params, &p)
	text := string(params)
	switch method {
	case "describe":
		return map[string]any{"operations": []string{OpCapture, OpRecall}, "tenant": true,
			"routes": []map[string]any{{"method": "GET", "path": "/hello", "action": "read:memories"}, {"method": "GET", "path": "/env", "action": "read:memories", "roles": []string{"human"}}, {"method": "POST", "path": "/echo", "action": "read:memories"},
				{"method": "POST", "path": "/count", "action": "read:memories", "data": true}, {"method": "POST", "path": "/sneaky", "action": "read:memories"}},
			"tools": []map[string]any{{"name": "lookup", "description": "Look a thing up", "inputSchema": map[string]any{"type": "object"}}}}
	case "operation":
		if strings.Contains(text, "sleepy") && p.Phase == "before" {
			time.Sleep(2 * time.Second)
		}
		return map[string]any{"allow": !strings.Contains(text, "forbidden")}
	case "tenant":
		return map[string]any{"allow": p.Actor["principal_id"] != "human-2"}
	case "route":
		body := map[string]any{"path": p.Path, "principal": p.Actor["principal_id"], "body": p.Body}
		if p.Path == "/env" {
			names := []string{}
			for _, kv := range os.Environ() {
				names = append(names, strings.SplitN(kv, "=", 2)[0])
			}
			body["env"] = names
		}
		if p.Path == "/count" || p.Path == "/sneaky" {
			return map[string]any{"status": 200, "body": map[string]any{"seen": p.Data["n"]}, "data": []map[string]any{{"op": "put", "key": "n", "value": p.Data["n"] + 1}}}
		}
		return map[string]any{"status": 200, "body": body}
	case "tool":
		return map[string]any{"result": map[string]any{"tool": p.Tool, "args": p.Body}}
	}
	return nil
}

// independentOutcomeRows preserves every stored event, but selects the first
// completed evaluation per memory content, dimension and exact trace identity.
// Time filtering follows selection, so a late duplicate cannot refresh old evidence.
func independentOutcomeRows(rows []Record, in MemoryOutcomeQuery) []Record {
	base := in
	base.From, base.To = nil, nil
	ordered := slices.Clone(rows)
	slices.SortFunc(ordered, func(a, b Record) int {
		aAt, bAt := a.CreatedAt, b.CreatedAt
		if a.MemoryOutcomeCompletedAt != nil {
			aAt = *a.MemoryOutcomeCompletedAt
		}
		if b.MemoryOutcomeCompletedAt != nil {
			bAt = *b.MemoryOutcomeCompletedAt
		}
		if n := aAt.Compare(bAt); n != 0 {
			return n
		}
		if n := a.CreatedAt.Compare(b.CreatedAt); n != 0 {
			return n
		}
		return strings.Compare(a.ID, b.ID)
	})
	out := []Record{}
	seen := map[string]bool{}
	for _, r := range ordered {
		if !memoryOutcomeMatches(r, base) || r.MemoryOutcomeContentHash == "" {
			continue
		}
		trace := outcomeTrace(r)
		if trace == nil || trace.ID == "" {
			continue
		}
		identity := "trace:" + trace.ID
		if r.MemoryOutcomeTaskHash != "" && r.MemoryOutcomeCompletedAt != nil {
			identity = "task:" + r.MemoryOutcomeTaskHash
		}
		key := structuredText([]string{r.Target.ID, r.MemoryOutcomeContentHash, outcomeDimension(r), identity})
		if seen[key] {
			continue
		}
		seen[key] = true
		if memoryOutcomeMatches(r, in) {
			out = append(out, r)
		}
	}
	return out
}

// signalPolicyHTTPResponses supplies real router responses for the strict API
// corpus. The shared contract helper records both authenticated and anonymous
// requests; no response shape is fabricated for schema validation.
func signalPolicyHTTPResponses(t *testing.T) []apiContractCase {
	t.Helper()
	f, m, _ := outcomeFixture(t)
	c := &contractHTTP{t: t, f: f, scope: scopeA, seen: map[string]bool{}}
	c.call("GET /v1/memories/{id}/signal-policy", m.ID, nil, nil)
	return c.cases
}

// A4 exercises the formerly missing operations through real HTTP routing and
// service transitions. Provider and storage inputs are synthetic local fixtures.
func contractMissingManagement(c *contractHTTP) {
	sc := c.scope
	c.call("POST /v1/wiki/build_from_memories", "", WikiMemoryBuildInput{Scope: sc, Query: "fixture"}, nil)
	c.call("POST /v1/wiki/folders", "", WikiFolderInput{Scope: sc, Path: "Contract"}, nil)
	c.call("PATCH /v1/wiki/folders", "", WikiFolderMoveInput{Scope: sc, From: "Contract", To: "Moved"}, nil)
	c.call("GET /v1/wiki/tree", "", nil, nil)
	pageID := wikiPageID(sc, "fixture")
	page, err := c.f.repo.Get(testCtx, colWikiPages, sc, pageID)
	if err != nil {
		c.t.Fatal(err)
	}
	out := c.call("PATCH /v1/wiki/pages/{id}", pageID, WikiPagePatchInput{Scope: sc, ExpectedRevision: page.Revision, Title: "Renamed fixture"}, nil)
	pageID = decodeContract[string](c.t, out["id"])
	page, err = c.f.repo.Get(testCtx, colWikiPages, sc, pageID)
	if err != nil {
		c.t.Fatal(err)
	}
	c.call("DELETE /v1/wiki/pages/{id}", pageID, nil, url.Values{"expected_revision": {strconv.FormatInt(page.Revision, 10)}})
	c.call("DELETE /v1/wiki/folders", "", nil, url.Values{"path": {"Moved"}})
	c.call("GET /v1/knowledge/sources/{id}/chunks", "contract-source", nil, nil)
	c.call("GET /v1/knowledge/sources/{id}/history", "contract-source", nil, nil)
	now := time.Now().UTC()
	c.seed(colJobs, Record{ID: "contract-extract-document", Scope: sc, Kind: fileExtractDocKind, Status: "active", Revision: 1, Target: &Ref{ID: "contract-extract-job"}, CreatedAt: now, UpdatedAt: now})
	c.seed(colJobs, Record{ID: "contract-extract-job", Scope: sc, Kind: fileExtractJobKind, Status: JobStatusDone, Revision: 1, CreatedAt: now, UpdatedAt: now, FileExtract: &FileExtractJob{Document: "contract-extract-document", Label: "Fixture", Chunks: []FileExtractChunk{}, Kept: []FileExtractChunk{}}})
	c.call("GET /v1/files/memories/documents/{id}/chunks", "contract-extract-document", nil, nil)
	c.call("GET /v1/files/memories/documents/{id}/history", "contract-extract-document", nil, nil)
	c.f.svc.Repo = opsFakeRepo{c.f.repo}
	c.call("POST /v1/extensions/{extension}/data", "", ExtDataChange{Scope: sc, Changes: []ExtDataOp{{Op: "put", Key: "key", Value: json.RawMessage(`{"value":"fixture"}`)}}}, nil)
	c.call("GET /v1/extensions/{extension}/data", "", nil, nil)
	c.call("GET /v1/extensions/{extension}/data/{key}", "", nil, nil)
}

// opsFakeRepo adds the operator query surface to the shared fake store.
type opsFakeRepo struct{ *fakeRepo }

func (o opsFakeRepo) OpsRecords(_ context.Context, q opsRecordQuery) ([]Record, error) {
	out := []Record{}
	for _, r := range o.rows(q.Collection) {
		if opsFakeMatch(r, q) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (o opsFakeRepo) OpsCounts(_ context.Context, q opsCountQuery) ([]OpsCount, error) {
	counts := map[[2]string]int64{}
	for _, r := range o.rows(q.Collection) {
		if !sameScope(r.Scope, q.Scope) || len(q.Statuses) > 0 && !slices.Contains(q.Statuses, r.Status) {
			continue
		}
		bucket := ""
		if q.Bucket == "day" {
			bucket = r.CreatedAt.UTC().Format("2006-01-02")
		}
		tags := r.Tags
		if r.MentalModel != nil {
			tags = r.MentalModel.Refresh.Tags
		}
		if q.Bucket == "day" && q.TimeField == "occurred" && r.Occurred != nil {
			bucket = r.Occurred.From.UTC().Format("2006-01-02")
		}
		keys := map[string][]string{"": {""}, "kind": {r.Kind}, "status": {r.Status}, "tag": tags}[q.By]
		for _, k := range keys {
			counts[[2]string{bucket, k}]++
		}
	}
	out := []OpsCount{}
	for k, n := range counts {
		out = append(out, OpsCount{Bucket: k[0], Key: k[1], Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket+out[i].Key < out[j].Bucket+out[j].Key })
	return out, nil
}

func opsFakeMatch(r Record, q opsRecordQuery) bool {
	target := ""
	if r.Target != nil {
		target = r.Target.ID
	}
	after := q.After == nil || r.CreatedAt.Before(q.After.At) || r.CreatedAt.Equal(q.After.At) && r.ID < q.After.ID
	return sameScope(r.Scope, q.Scope) && after && (q.Kind == "" || r.Kind == q.Kind) && strings.HasPrefix(r.Kind, q.KindPrefix) &&
		(len(q.Statuses) == 0 || slices.Contains(q.Statuses, r.Status)) && (q.Actor == "" || r.CreatedBy == q.Actor) &&
		(q.TargetID == "" || target == q.TargetID) && (q.Tag == "" || slices.Contains(r.Tags, q.Tag)) && (q.Transport == "" || r.Audit != nil && r.Audit.Transport == q.Transport) &&
		(q.From.IsZero() || !r.CreatedAt.Before(q.From)) && (q.To.IsZero() || r.CreatedAt.Before(q.To))
}
